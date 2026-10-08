//go:build linux

package audio

import (
	"errors"

	"github.com/jfreymuth/pulse"
)

// PulseAudio (and PipeWire through its pulse compatibility layer) speaks a
// protocol that jfreymuth/pulse implements in pure Go over a unix socket, so
// Linux gets native output without linking ALSA through CGO.
func init() {
	nativeSinks = append(nativeSinks, sinkFactory{name: "pulse", open: openPulseSink})
}

type pulseSink struct {
	client *pulse.Client
	stream *pulse.PlaybackStream
}

func openPulseSink(f Format, pull PullFunc) (Sink, error) {
	if f.Channels != 1 && f.Channels != 2 {
		return nil, errors.New("only mono and stereo are supported")
	}
	client, err := pulse.NewClient(
		pulse.ClientApplicationName("tunetty"),
		pulse.ClientApplicationIconName("audio-headphones"),
	)
	if err != nil {
		return nil, err
	}

	// Returning an error here would tear the stream down, so underruns are
	// padded with silence instead.
	reader := pulse.Float32Reader(func(p []float32) (int, error) {
		n := pull(p)
		for i := n; i < len(p); i++ {
			p[i] = 0
		}
		return len(p), nil
	})

	opts := []pulse.PlaybackOption{
		pulse.PlaybackSampleRate(f.SampleRate),
		pulse.PlaybackLatency(0.05),
		pulse.PlaybackMediaName("tunetty"),
	}
	if f.Channels == 2 {
		opts = append(opts, pulse.PlaybackStereo)
	} else {
		opts = append(opts, pulse.PlaybackMono)
	}

	stream, err := client.NewPlayback(reader, opts...)
	if err != nil {
		client.Close()
		return nil, err
	}
	return &pulseSink{client: client, stream: stream}, nil
}

func (s *pulseSink) Name() string { return "pulse" }

func (s *pulseSink) Play() error {
	// Start only acts on an idle stream (never started, or stopped after a
	// reader error) and Resume only on a paused one, so calling both covers
	// every state and is a no-op while already running.
	s.stream.Start()
	s.stream.Resume()
	return s.stream.Error()
}

func (s *pulseSink) Pause() error {
	s.stream.Pause()
	return s.stream.Error()
}

func (s *pulseSink) Close() error {
	s.stream.Stop()
	s.stream.Close()
	s.client.Close()
	return nil
}
