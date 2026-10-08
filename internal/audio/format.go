// Package audio implements a pure Go (CGO free) playback engine: streaming
// decoders, resampling, a gapless queue and per platform output sinks.
package audio

import "time"

// Format describes an interleaved PCM stream.
type Format struct {
	SampleRate int
	Channels   int
}

// Valid reports whether the format is usable.
func (f Format) Valid() bool { return f.SampleRate > 0 && f.Channels > 0 && f.Channels <= 8 }

// FramesFor returns the number of frames covering d.
func (f Format) FramesFor(d time.Duration) int {
	return int(d.Seconds() * float64(f.SampleRate))
}

// DurationOfFrames returns the wall time covered by n frames.
func (f Format) DurationOfFrames(n int64) time.Duration {
	if f.SampleRate <= 0 {
		return 0
	}
	return time.Duration(float64(n) / float64(f.SampleRate) * float64(time.Second))
}

// OutputFormat is the fixed engine mixing format. Everything is resampled to
// it so tracks of differing rates can be spliced without reopening the sink,
// which is what makes gapless playback possible.
var OutputFormat = Format{SampleRate: 48000, Channels: 2}
