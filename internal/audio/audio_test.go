package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------- ring

func TestRing(t *testing.T) {
	t.Parallel()
	ch := OutputFormat.Channels

	cases := []struct {
		name string
		run  func(t *testing.T, r *ring)
	}{
		{
			// Marks must only become current once the consumer has reached
			// them, otherwise the UI would report the next track while the
			// previous one is still audible from the buffer.
			name: "marks follow the consumer",
			run: func(t *testing.T, r *ring) {
				r.Mark(1, 0)
				mustWrite(t, r, 8)
				r.Mark(2, 0)
				mustWrite(t, r, 8)

				assertCurrent(t, r, 1, 0)
				r.Read(make([]float32, 4))
				assertCurrent(t, r, 1, int64(4/ch))
				r.Read(make([]float32, 4))
				assertCurrent(t, r, 2, 0)
				assert.Equal(t, []segment{{seq: 1, frames: int64(8 / ch), natural: true}}, r.TakeEnded())
			},
		},
		{
			// Regression: Flush used to leave the absolute counters apart, so
			// every later mark was promoted late by the flushed amount.
			name: "flush keeps positions aligned",
			run: func(t *testing.T, r *ring) {
				r.Mark(1, 0)
				mustWrite(t, r, 32)
				r.Read(make([]float32, 8))
				r.Flush(true)
				assert.Equal(t, []segment{{seq: 1, frames: int64(8 / ch)}}, r.TakeEnded())

				r.Mark(2, 0)
				mustWrite(t, r, 8)
				r.Mark(3, 0)
				mustWrite(t, r, 8)
				r.Read(make([]float32, 4))
				assertCurrent(t, r, 2, int64(4/ch))
				r.Read(make([]float32, 4))
				assertCurrent(t, r, 3, 0)
			},
		},
		{
			name: "truncate drops a pending mark and its samples",
			run: func(t *testing.T, r *ring) {
				r.Mark(1, 0)
				mustWrite(t, r, 8)
				r.Mark(2, 0)
				mustWrite(t, r, 8)
				require.True(t, r.Truncate(2))
				assert.Equal(t, 8/ch, r.Buffered())

				r.Mark(3, 0)
				mustWrite(t, r, 8)
				r.Read(make([]float32, 8))
				assertCurrent(t, r, 3, 0)
				assert.False(t, r.Truncate(3), "an audible mark cannot be truncated")
			},
		},
		{
			name: "flush unblocks a writer",
			run: func(t *testing.T, r *ring) {
				errCh := make(chan error, 1)
				go func() {
					_, err := r.Write(make([]float32, 64))
					errCh <- err
				}()
				time.Sleep(20 * time.Millisecond) // let the writer fill the ring and block
				r.Flush(false)
				select {
				case err := <-errCh:
					require.ErrorIs(t, err, errFlushed)
				case <-time.After(time.Second):
					require.FailNow(t, "writer stayed blocked after Flush")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, newRing(32))
		})
	}
}

func mustWrite(t *testing.T, r *ring, n int) {
	t.Helper()
	_, err := r.Write(make([]float32, n))
	require.NoError(t, err)
}

func assertCurrent(t *testing.T, r *ring, seq uint64, frames int64) {
	t.Helper()
	gotSeq, gotFrames, ok := r.Current()
	require.True(t, ok)
	assert.Equal(t, seq, gotSeq)
	assert.Equal(t, frames, gotFrames)
}

// --------------------------------------------------------------- converter

func TestConverter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Format
		src  []float32
		// wantFrames is checked with a tolerance of two frames.
		wantFrames int
		want       []float32
	}{
		{
			name:       "upsamples to the output rate",
			in:         Format{SampleRate: 24000, Channels: 2},
			src:        sine(24000, 2),
			wantFrames: OutputFormat.SampleRate,
		},
		{
			name:       "upmixes mono to stereo",
			in:         Format{SampleRate: 48000, Channels: 1},
			src:        []float32{0.5, -0.25},
			wantFrames: 2,
			want:       []float32{0.5, 0.5, -0.25, -0.25},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := newConverter(tc.in, OutputFormat).convert(nil, tc.src)
			assert.InDelta(t, tc.wantFrames, len(out)/OutputFormat.Channels, 2)
			if tc.want != nil {
				assert.Equal(t, tc.want, out)
			}
			for _, v := range out {
				require.InDelta(t, 0, v, 1.5)
			}
		})
	}
}

// sine returns one second of a 440 Hz tone.
func sine(rate, channels int) []float32 {
	out := make([]float32, rate*channels)
	for i := range out {
		out[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i/channels) / float64(rate)))
	}
	return out
}

// ---------------------------------------------------------------- decoders

func TestSniff(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		head []byte
		want codec
		ok   bool
	}{
		{"opus", append([]byte("OggS\x00\x02"), []byte("........OpusHead")...), codecOpus, true},
		{"vorbis", append([]byte("OggS\x00\x02"), []byte("\x01vorbis")...), codecVorbis, true},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), codecFLAC, true},
		{"wav", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), codecWAV, true},
		{"mp3 id3", []byte("ID3\x04\x00"), codecMP3, true},
		{"mp3 sync", []byte{0xFF, 0xFB, 0x90, 0x00}, codecMP3, true},
		{"garbage", []byte("not audio at all"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := sniff(tc.head)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCodecFromHint(t *testing.T) {
	t.Parallel()

	cases := map[string]codec{
		"audio/ogg; codecs=opus": codecOpus,
		"audio/mpeg":             codecMP3,
		"audio/flac":             codecFLAC,
		"opus":                   codecOpus,
		"mp3":                    codecMP3,
	}
	for hint, want := range cases {
		t.Run(hint, func(t *testing.T) {
			t.Parallel()
			got, ok := codecFromHint(hint)
			assert.True(t, ok)
			assert.Equal(t, want, got)
		})
	}
}

// ------------------------------------------------------------------ engine

// testSink stands in for the audio thread: tests pull samples by hand, so
// what is audible is fully under their control.
type testSink struct {
	t    *testing.T
	pull PullFunc
}

func (s *testSink) Name() string { return "test" }
func (s *testSink) Play() error  { return nil }
func (s *testSink) Pause() error { return nil }
func (s *testSink) Close() error { return nil }

// pullFrames consumes exactly n frames, waiting for the pump as needed.
func (s *testSink) pullFrames(n int) []float32 {
	s.t.Helper()
	out := make([]float32, 0, n*OutputFormat.Channels)
	buf := make([]float32, 512)
	deadline := time.Now().Add(10 * time.Second)
	for len(out) < cap(out) {
		require.True(s.t, time.Now().Before(deadline), "pulled only %d of %d samples", len(out), cap(out))
		got := s.pull(buf[:min(len(buf), cap(out)-len(out))])
		if got == 0 {
			time.Sleep(time.Millisecond)
		}
		out = append(out, buf[:got]...)
	}
	return out
}

// drainUntilStopped consumes everything the engine plays until it stops.
func (s *testSink) drainUntilStopped(e *Engine) []float32 {
	s.t.Helper()
	var out []float32
	buf := make([]float32, 512)
	deadline := time.Now().Add(10 * time.Second)
	for e.State() != StateStopped {
		require.True(s.t, time.Now().Before(deadline), "engine never stopped")
		got := s.pull(buf)
		if got == 0 {
			time.Sleep(time.Millisecond)
		}
		out = append(out, buf[:got]...)
	}
	return out
}

func newTestEngine(t *testing.T) (*Engine, *testSink) {
	t.Helper()
	sink := &testSink{t: t}
	e, err := NewEngine(Config{
		BufferDuration: time.Second,
		openSink: func(_ Format, pull PullFunc) (Sink, error) {
			sink.pull = pull
			return sink, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e, sink
}

// makeWAV builds a 16 bit stereo WAV carrying a constant sample value, which
// makes discontinuities at a track join trivial to spot.
func makeWAV(frames int, value float32) []byte {
	var b bytes.Buffer
	dataLen := frames * 2 * 2
	put := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }

	b.WriteString("RIFF")
	put(uint32(36 + dataLen))
	b.WriteString("WAVEfmt ")
	put(uint32(16))
	put(uint16(1)) // PCM
	put(uint16(2)) // stereo
	put(uint32(48000))
	put(uint32(48000 * 4))
	put(uint16(4))
	put(uint16(16))
	b.WriteString("data")
	put(uint32(dataLen))
	s := int16(value * 32767)
	for range frames * 2 {
		put(s)
	}
	return b.Bytes()
}

func wavTrack(id string, value float32, frames int) Track {
	data := makeWAV(frames, value)
	return Track{
		ID:       id,
		Title:    id,
		Duration: time.Duration(frames) * time.Second / 48000,
		Open: func(context.Context, time.Duration) (io.ReadCloser, string, error) {
			return io.NopCloser(bytes.NewReader(data)), "audio/wav", nil
		},
	}
}

func badTrack(id string, opens *atomic.Int32) Track {
	return Track{
		ID:    id,
		Title: id,
		Open: func(context.Context, time.Duration) (io.ReadCloser, string, error) {
			opens.Add(1)
			return nil, "", io.ErrUnexpectedEOF
		},
	}
}

// runs collapses samples into the sequence of distinct levels played.
func runs(samples []float32) []float32 {
	var out []float32
	for _, v := range samples {
		if len(out) == 0 || math.Abs(float64(out[len(out)-1]-v)) > 0.01 {
			out = append(out, v)
		}
	}
	return out
}

func assertLevels(t *testing.T, want []float32, samples []float32) {
	t.Helper()
	got := runs(samples)
	require.Len(t, got, len(want), "levels played: %v", got)
	for i := range want {
		assert.InDelta(t, want[i], got[i], 0.01, "level %d of %v", i, got)
	}
}

// chainIDs lists the track IDs buffered in the ring, audible first; "" is
// the end-of-queue mark.
func chainIDs(e *Engine) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur, ok, pending := e.ring.Chain()
	if !ok {
		return nil
	}
	var out []string
	for _, s := range append([]uint64{cur}, pending...) {
		out = append(out, e.seqMap[s].track.ID)
	}
	return out
}

func waitChain(t *testing.T, e *Engine, want ...string) {
	t.Helper()
	require.Eventually(t, func() bool { return assert.ObjectsAreEqual(want, chainIDs(e)) },
		5*time.Second, time.Millisecond, "chain = %v, want %v", chainIDs(e), want)
}

// TestEngineGaplessJoin is the core guarantee: two queued tracks must come out
// of the sink as one continuous stream, with no silence and no dropped samples
// at the boundary.
func TestEngineGaplessJoin(t *testing.T) {
	t.Parallel()
	e, sink := newTestEngine(t)

	const frames = 24000
	e.SetQueue([]Track{wavTrack("a", 0.5, frames), wavTrack("b", -0.5, frames)}, 0)

	got := sink.pullFrames(2 * frames)
	boundary := frames * OutputFormat.Channels
	for i, v := range got {
		want := float32(0.5)
		if i >= boundary {
			want = -0.5
		}
		require.InDelta(t, want, v, 0.01, "sample %d (boundary at %d): the join is not gapless", i, boundary)
	}
}

// Queue edits made after the successor was prefetched must replace the stale
// audio in the ring, still joining gaplessly.
func TestEngineQueueEditsReachPrefetchedAudio(t *testing.T) {
	t.Parallel()

	const frames = 4800
	cases := []struct {
		name  string
		edit  func(e *Engine)
		chain []string
		want  []float32
	}{
		{"remove next", func(e *Engine) { e.RemoveAt(1) }, []string{"a", "c", ""}, []float32{0.5, 0.25}},
		{
			"insert next", func(e *Engine) { e.InsertNext(wavTrack("d", -0.25, frames)) },
			[]string{"a", "d", "b", "c", ""},
			[]float32{0.5, -0.25, -0.5, 0.25},
		},
		{
			"enqueue after the end", func(e *Engine) { e.Enqueue(wavTrack("d", -0.25, frames)) },
			[]string{"a", "b", "c", "d", ""},
			[]float32{0.5, -0.5, 0.25, -0.25},
		},
		{"shuffle upcoming", func(e *Engine) {
			q := e.Queue()
			e.ReplaceQueue([]Track{q[0], q[2], q[1]})
		}, []string{"a", "c", "b", ""}, []float32{0.5, 0.25, -0.5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, sink := newTestEngine(t)
			e.SetQueue([]Track{
				wavTrack("a", 0.5, frames),
				wavTrack("b", -0.5, frames),
				wavTrack("c", 0.25, frames),
			}, 0)
			waitChain(t, e, "a", "b", "c", "")

			tc.edit(e)
			waitChain(t, e, tc.chain...)
			assertLevels(t, tc.want, sink.drainUntilStopped(e))
		})
	}
}

// The same song queued twice is two entries: the second copy must be reported
// as playing and must be followed by what follows it, not by the first copy's
// successor.
func TestEngineDuplicateEntries(t *testing.T) {
	t.Parallel()
	e, sink := newTestEngine(t)

	first, second := wavTrack("a", 0.5, 4800), wavTrack("a", 0.25, 4800)
	e.SetQueue([]Track{first, wavTrack("b", -0.5, 4800), second}, 2)

	require.Eventually(t, func() bool { return e.Status().Track != nil }, 5*time.Second, time.Millisecond)
	assert.Equal(t, 2, e.Status().Index)
	assertLevels(t, []float32{0.25}, sink.drainUntilStopped(e))
}

// Removing the audible track must keep the play/pause state.
func TestEngineRemoveAudibleKeepsState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		setup func(e *Engine)
		want  State
	}{
		{"playing", func(*Engine) {}, StatePlaying},
		{"paused", (*Engine).Pause, StatePaused},
		{"stopped", (*Engine).Stop, StateStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, _ := newTestEngine(t)
			e.SetQueue([]Track{wavTrack("a", 0.5, 4800), wavTrack("b", 0.25, 4800)}, 0)
			tc.setup(e)
			e.RemoveAt(0)
			assert.Equal(t, tc.want, e.State())
			assert.Equal(t, []string{"b"}, ids(e.Queue()))
		})
	}
}

// A track that fails to open must be skipped rather than wedging the queue,
// and a queue where nothing opens must give up after one round.
func TestEngineOpenFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		good      bool
		wantOpens int32
		want      []float32
	}{
		{"skips an unopenable track", true, 1, []float32{0.5}},
		{"gives up after a full cycle", false, 2, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, sink := newTestEngine(t)
			var opens atomic.Int32
			second := badTrack("bad2", &opens)
			if tc.good {
				second = wavTrack("good", 0.5, 4800)
			}
			if !tc.good {
				e.SetRepeat(RepeatAll)
			}
			e.SetQueue([]Track{badTrack("bad", &opens), second}, 0)

			if tc.good {
				assertLevels(t, tc.want, sink.pullFrames(4800))
			} else {
				sink.drainUntilStopped(e)
				time.Sleep(300 * time.Millisecond) // no further attempts once given up
			}
			assert.Equal(t, tc.wantOpens, opens.Load())
		})
	}
}

// Finished events must follow what is audible rather than the decoder, and
// count tracks cut short only once half of them was heard.
func TestEngineFinishedEventsFollowTheListener(t *testing.T) {
	t.Parallel()
	e, sink := newTestEngine(t)

	const frames = 9600 // 200 ms
	e.SetQueue([]Track{
		wavTrack("a", 0.5, frames),
		wavTrack("b", -0.5, frames),
		wavTrack("c", 0.25, frames),
	}, 0)

	var (
		mu  sync.Mutex
		got []string
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range e.Events() {
			name := map[EventKind]string{
				EventTrackStarted: "start", EventTrackFinished: "finish", EventQueueFinished: "end",
			}[ev.Kind]
			if name == "" {
				continue
			}
			if ev.Track != nil {
				name += " " + ev.Track.ID
			}
			mu.Lock()
			got = append(got, name)
			mu.Unlock()
			if ev.Kind == EventQueueFinished {
				return
			}
		}
	}()

	waitChain(t, e, "a", "b", "c", "")
	sink.pullFrames(frames / 4)
	time.Sleep(300 * time.Millisecond) // a was decoded long ago; it must not count yet
	e.Next()                           // a cut short at a quarter: no finish

	sink.pullFrames(frames * 3 / 4)
	e.Next() // b cut short at three quarters: counts

	sink.drainUntilStopped(e)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no queue finished event")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"start a", "start b", "finish b", "start c", "finish c", "end"}, got)
}

func ids(tracks []Track) []string {
	out := make([]string, len(tracks))
	for i, t := range tracks {
		out[i] = t.ID
	}
	return out
}
