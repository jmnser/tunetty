package audio

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSink stands in for the audio thread so the test pulls samples by hand.
type testSink struct{ pull PullFunc }

func (s *testSink) Name() string { return "test" }
func (s *testSink) Play() error  { return nil }
func (s *testSink) Pause() error { return nil }
func (s *testSink) Close() error { return nil }

// TestOpusPlayback plays a 1s 440 Hz Opus file (testdata/sine.opus, made with
// ffmpeg -f lavfi -i sine=frequency=440:sample_rate=48000:duration=1 -ac 2
// -c:a libopus -b:a 32k) through the engine and checks
// that a full second of audible signal comes out before the engine stops.
func TestOpusPlayback(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/sine.opus")
	require.NoError(t, err)

	sink := &testSink{}
	e, err := NewEngine(Config{
		BufferDuration: time.Second,
		openSink: func(_ Format, pull PullFunc) (Sink, error) {
			sink.pull = pull
			return sink, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	e.SetQueue([]Track{{
		ID: "sine",
		Open: func(context.Context, time.Duration) (io.ReadCloser, string, error) {
			return io.NopCloser(bytes.NewReader(data)), "audio/ogg; codecs=opus", nil
		},
	}}, 0)

	var samples []float32
	buf := make([]float32, 512)
	deadline := time.Now().Add(10 * time.Second)
	for e.State() != StateStopped {
		require.True(t, time.Now().Before(deadline), "engine never stopped")
		n := sink.pull(buf)
		if n == 0 {
			time.Sleep(time.Millisecond)
		}
		samples = append(samples, buf[:n]...)
	}

	var sum float64
	for _, v := range samples {
		sum += float64(v) * float64(v)
	}
	// ffmpeg's astats measures the file at -24.1 dBFS RMS. The engine plays
	// at full volume, so the level must come out unchanged, and Ogg pre-skip
	// plus end trimming make the frame count exact.
	const wantRMS = 0.0622
	assert.Equal(t, OutputFormat.SampleRate, len(samples)/OutputFormat.Channels, "frames played")
	assert.InDelta(t, wantRMS, math.Sqrt(sum/float64(len(samples))), 0.01, "signal level")
}
