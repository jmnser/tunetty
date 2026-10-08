package audio

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
)

// PullFunc is called by a sink to obtain the next interleaved float32 samples.
// It must never block: short reads are padded with silence by the sink.
type PullFunc func(p []float32) int

// Sink is a platform audio output.
type Sink interface {
	// Name identifies the backend for display purposes.
	Name() string
	// Play starts or resumes consuming samples.
	Play() error
	// Pause stops consuming without discarding the sink.
	Pause() error
	// Close releases the device.
	Close() error
}

// ErrNoSink is returned when no backend is available on this system.
var ErrNoSink = errors.New("audio: no usable output backend")

// sinkFactory opens a backend. Factories are registered per platform.
type sinkFactory struct {
	name string
	open func(f Format, pull PullFunc) (Sink, error)
}

// nativeSinks is populated by the per platform files via build tags.
var nativeSinks []sinkFactory

// openSink tries every backend in order, preferring the platform native one.
// backend selects a specific factory by name; "" or "auto" tries all.
func openSink(f Format, backend string, pull PullFunc) (Sink, error) {
	factories := append(append([]sinkFactory{}, nativeSinks...), sinkFactory{name: "command", open: openCommandSink})

	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend != "" && backend != "auto" {
		for _, fac := range factories {
			if fac.name == backend {
				return fac.open(f, pull)
			}
		}
		return nil, errors.New("audio: unknown backend " + backend)
	}

	var errs []error
	for _, fac := range factories {
		s, err := fac.open(f, pull)
		if err == nil {
			return s, nil
		}
		errs = append(errs, errors.New(fac.name+": "+err.Error()))
	}
	return nil, errors.Join(append([]error{ErrNoSink}, errs...)...)
}

// AvailableBackends lists the backend names compiled into this binary.
func AvailableBackends() []string {
	out := make([]string, 0, len(nativeSinks)+1)
	for _, f := range nativeSinks {
		out = append(out, f.name)
	}
	return append(out, "command")
}

// pullReader adapts a PullFunc to an io.Reader emitting little endian float32,
// which is what the oto backend and external players consume.
type pullReader struct {
	pull PullFunc
	scr  []float32
}

func (r *pullReader) Read(p []byte) (int, error) {
	n := len(p) / 4
	if n == 0 {
		return 0, nil
	}
	if cap(r.scr) < n {
		r.scr = make([]float32, n)
	}
	buf := r.scr[:n]
	got := r.pull(buf)
	for i := got; i < n; i++ {
		buf[i] = 0 // pad underruns with silence rather than stuttering
	}
	for i, v := range buf {
		binary.LittleEndian.PutUint32(p[i*4:], math.Float32bits(v))
	}
	return n * 4, nil
}

var _ io.Reader = (*pullReader)(nil)
