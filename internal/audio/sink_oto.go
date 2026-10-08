//go:build darwin

package audio

import (
	"time"

	"github.com/ebitengine/oto/v3"
)

// oto drives CoreAudio on macOS through purego, so it links without CGO. On Linux oto needs ALSA via CGO, which is why that
// platform uses the PulseAudio backend instead.
func init() {
	nativeSinks = append(nativeSinks, sinkFactory{name: "oto", open: openOtoSink})
}

type otoSink struct {
	ctx    *oto.Context
	player *oto.Player
}

func openOtoSink(f Format, pull PullFunc) (Sink, error) {
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   f.SampleRate,
		ChannelCount: f.Channels,
		Format:       oto.FormatFloat32LE,
		BufferSize:   40 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	<-ready
	p := ctx.NewPlayer(&pullReader{pull: pull})
	p.SetBufferSize(f.Channels * 4 * f.SampleRate / 20) // ~50 ms
	return &otoSink{ctx: ctx, player: p}, nil
}

func (s *otoSink) Name() string { return "oto" }

func (s *otoSink) Play() error {
	s.player.Play()
	return nil
}

func (s *otoSink) Pause() error {
	s.player.Pause()
	return nil
}

func (s *otoSink) Close() error {
	// The oto context is process global and cannot be reopened, so only the
	// player is torn down here.
	return s.player.Close()
}
