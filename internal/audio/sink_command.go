package audio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// The command sink is the universal fallback: it pipes raw float32 PCM into
// whatever system player is on PATH. It keeps tunetty working on setups the
// native backends do not cover (bare ALSA, remote sound daemons, WSL) without
// pulling in a CGO dependency.
type commandSink struct {
	name string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	paused  bool
	closed  bool
	err     error // why the player went away, if it did
	stopped chan struct{}
}

// commandCandidate is an external player invocation template.
type commandCandidate struct {
	bin  string
	args func(f Format) []string
}

// commandCandidates is ordered by fidelity of float32 support then ubiquity.
func commandCandidates() []commandCandidate {
	return []commandCandidate{
		{"pw-cat", func(f Format) []string {
			return []string{
				"--playback", "--format", "f32", "--rate", strconv.Itoa(f.SampleRate),
				"--channels", strconv.Itoa(f.Channels), "--media-role", "Music", "-",
			}
		}},
		{"paplay", func(f Format) []string {
			return []string{
				"--raw", "--format=float32le", "--rate=" + strconv.Itoa(f.SampleRate),
				"--channels=" + strconv.Itoa(f.Channels), "--stream-name=tunetty",
			}
		}},
		{"ffplay", func(f Format) []string {
			return []string{
				"-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit",
				"-f", "f32le", "-ar", strconv.Itoa(f.SampleRate), "-ac", strconv.Itoa(f.Channels), "-i", "pipe:0",
			}
		}},
		{"aplay", func(f Format) []string {
			return []string{
				"-q", "-t", "raw", "-f", "FLOAT_LE", "-r", strconv.Itoa(f.SampleRate),
				"-c", strconv.Itoa(f.Channels), "-",
			}
		}},
		{"sox", func(f Format) []string {
			return []string{
				"-q", "-t", "f32", "-r", strconv.Itoa(f.SampleRate),
				"-c", strconv.Itoa(f.Channels), "-", "-d",
			}
		}},
	}
}

func openCommandSink(f Format, pull PullFunc) (Sink, error) {
	var cand *commandCandidate

	// TUNETTY_AUDIO_COMMAND lets users point at any player that accepts raw
	// float32 little endian PCM on stdin.
	if custom := strings.TrimSpace(os.Getenv("TUNETTY_AUDIO_COMMAND")); custom != "" {
		fields := strings.Fields(custom)
		bin, err := exec.LookPath(fields[0])
		if err != nil {
			return nil, fmt.Errorf("TUNETTY_AUDIO_COMMAND: %w", err)
		}
		rest := fields[1:]
		cand = &commandCandidate{bin: bin, args: func(Format) []string { return rest }}
	} else {
		for _, c := range commandCandidates() {
			if path, err := exec.LookPath(c.bin); err == nil {
				found := c
				found.bin = path
				cand = &found
				break
			}
		}
	}
	if cand == nil {
		return nil, errors.New("no external player found (tried pw-cat, paplay, ffplay, aplay, sox)")
	}

	cmd := exec.Command(cand.bin, cand.args(f)...) //nolint:gosec // binary resolved from a fixed allow list or explicit user config
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", cand.bin, err)
	}

	s := &commandSink{
		name:    "command:" + baseName(cand.bin),
		cmd:     cmd,
		stdin:   stdin,
		paused:  true,
		stopped: make(chan struct{}),
	}
	go s.feed(f, pull)
	return s, nil
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// feed writes PCM continuously. Pausing writes silence rather than stalling
// the pipe, which keeps external players from underrunning or exiting.
func (s *commandSink) feed(f Format, pull PullFunc) {
	defer close(s.stopped)
	reader := &pullReader{pull: func(p []float32) int {
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if paused {
			return 0
		}
		return pull(p)
	}}
	chunkBytes := f.Channels * 4 * f.SampleRate / 50 // 20 ms
	buf := make([]byte, chunkBytes)
	for {
		s.mu.Lock()
		closed, w := s.closed, s.stdin
		s.mu.Unlock()
		if closed {
			return
		}
		n, _ := reader.Read(buf) // pullReader never fails
		if _, err := w.Write(buf[:n]); err != nil {
			s.fail(err)
			return
		}
	}
}

// fail records that the player stopped accepting audio. A write error after
// Close is the expected shutdown, not a failure.
func (s *commandSink) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.err = fmt.Errorf("%s stopped accepting audio: %w", s.name, err)
	// Whatever is left of the player is useless; Close reaps it.
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// Err reports why the external player went away, or nil while it runs.
func (s *commandSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *commandSink) Name() string { return s.name }

func (s *commandSink) Play() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
	return s.err
}

func (s *commandSink) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	return s.err
}

func (s *commandSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stdin := s.stdin
	cmd := s.cmd
	s.mu.Unlock()

	_ = stdin.Close()
	<-s.stopped
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	return nil
}
