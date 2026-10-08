// Package remote lets other processes query and control a running tunetty
// over a local unix socket. It backs `tunetty status` and `tunetty ctl`,
// which tmux calls from its status line and key bindings.
package remote

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmnser/tunetty/internal/audio"
)

// Commands understood by the server. Each request is one line holding the
// command name; each response is one JSON line.
const (
	CmdStatus     = "status"
	CmdPlayPause  = "play-pause"
	CmdPlay       = "play"
	CmdPause      = "pause"
	CmdStop       = "stop"
	CmdNext       = "next"
	CmdPrev       = "prev"
	CmdSeekFwd    = "seek-forward"
	CmdSeekBack   = "seek-back"
	CmdVolumeUp   = "volume-up"
	CmdVolumeDown = "volume-down"
	CmdMute       = "mute"
)

// Commands lists the control commands, in the order `tunetty ctl` shows them.
var Commands = []string{
	CmdPlayPause, CmdPlay, CmdPause, CmdStop, CmdNext, CmdPrev,
	CmdSeekFwd, CmdSeekBack, CmdVolumeUp, CmdVolumeDown, CmdMute,
}

// ErrNotRunning reports that no tunetty instance is listening.
var ErrNotRunning = errors.New("tunetty is not running")

// Status is the playback snapshot sent to clients.
type Status struct {
	State    string        `json:"state"`
	Title    string        `json:"title,omitempty"`
	Artist   string        `json:"artist,omitempty"`
	Album    string        `json:"album,omitempty"`
	Position time.Duration `json:"position"`
	Duration time.Duration `json:"duration"`
	Volume   float64       `json:"volume"`
	Muted    bool          `json:"muted"`
}

type response struct {
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// SocketPath is where the running instance listens. It lives in
// $XDG_RUNTIME_DIR when set, otherwise in a per-user directory under the
// system temp dir.
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "tunetty.sock")
	}
	return filepath.Join(os.TempDir(), "tunetty-"+strconv.Itoa(os.Getuid()), "tunetty.sock")
}

// Steps are the increments the seek and volume commands use.
type Steps struct {
	Seek   time.Duration
	Volume float64
}

// Server answers requests for one engine.
type Server struct {
	ln     net.Listener
	path   string
	engine *audio.Engine
	steps  Steps
}

// Listen opens the control socket. When another instance already listens it
// returns ErrInUse; a socket left behind by a crashed instance is replaced.
func Listen(engine *audio.Engine, steps Steps) (*Server, error) {
	path := SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("remote: %w", err)
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = conn.Close()
		return nil, ErrInUse
	}
	_ = os.Remove(path) // stale socket from an instance that did not exit cleanly
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("remote: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("remote: %w", err)
	}
	s := &Server{ln: ln, path: path, engine: engine, steps: steps}
	go s.serve()
	return s, nil
}

// ErrInUse reports that another instance owns the control socket.
var ErrInUse = errors.New("remote: another tunetty instance is running")

// Close stops listening and removes the socket.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.path)
	return err
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // closed
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	resp := s.run(strings.TrimSpace(line))
	_ = json.NewEncoder(conn).Encode(resp)
}

func (s *Server) run(cmd string) response {
	e := s.engine
	switch cmd {
	case CmdStatus:
	case CmdPlayPause:
		e.TogglePause()
	case CmdPlay:
		e.Play()
	case CmdPause:
		e.Pause()
	case CmdStop:
		e.Stop()
	case CmdNext:
		e.Next()
	case CmdPrev:
		e.Prev()
	case CmdSeekFwd:
		e.SeekBy(s.steps.Seek)
	case CmdSeekBack:
		e.SeekBy(-s.steps.Seek)
	case CmdVolumeUp:
		e.AdjustVolume(s.steps.Volume)
	case CmdVolumeDown:
		e.AdjustVolume(-s.steps.Volume)
	case CmdMute:
		e.ToggleMute()
	default:
		return response{Error: fmt.Sprintf("unknown command %q", cmd)}
	}
	return response{Status: snapshot(e)}
}

func snapshot(e *audio.Engine) *Status {
	st := e.Status()
	out := &Status{State: st.State.String(), Position: st.Position, Volume: st.Volume, Muted: st.Muted}
	if t := st.Track; t != nil {
		out.Title, out.Artist, out.Album, out.Duration = t.Title, t.Artist, t.Album, t.Duration
	}
	return out
}

// Send runs cmd against the running instance and returns its status after
// the command took effect.
func Send(cmd string) (*Status, error) {
	conn, err := net.DialTimeout("unix", SocketPath(), time.Second)
	if err != nil {
		return nil, ErrNotRunning
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintln(conn, cmd); err != nil {
		return nil, fmt.Errorf("remote: %w", err)
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("remote: %w", err)
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Status, nil
}

// Format renders a status for a status line, e.g. "▶ Artist – Title 1:23/4:05".
// A bar width above zero adds a progress bar of that many cells before the
// times. A stopped player renders as an empty string so the status line
// stays clean.
func Format(s *Status, bar int) string {
	if s == nil || s.State == audio.StateStopped.String() || s.Title == "" {
		return ""
	}
	icon := "▶"
	if s.State == audio.StatePaused.String() {
		icon = "⏸"
	}
	name := s.Title
	if s.Artist != "" {
		name = s.Artist + " – " + s.Title
	}
	// The server's track length can be a little shorter than the audio, so
	// the position is capped to keep "3:47/3:43" off the status line.
	pos := s.Position
	if s.Duration > 0 {
		pos = min(pos, s.Duration)
	}
	times := clock(pos) + "/" + clock(s.Duration)
	if bar > 0 {
		times = progress(pos, s.Duration, bar) + " " + times
	}
	return icon + " " + name + " " + times
}

// progress draws a bar of width cells filled by pos/total.
func progress(pos, total time.Duration, width int) string {
	filled := 0
	if total > 0 {
		filled = min(width, int(int64(width)*int64(pos)/int64(total)))
	}
	return strings.Repeat("━", filled) + strings.Repeat("─", width-filled)
}

func clock(d time.Duration) string {
	sec := int(d.Seconds())
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
