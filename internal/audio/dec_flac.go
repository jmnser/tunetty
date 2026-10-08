package audio

import (
	"errors"
	"fmt"
	"io"

	"github.com/mewkiz/flac"
)

// flacDecoder streams FLAC frames and normalises them to float32.
type flacDecoder struct {
	src     io.Closer
	stream  *flac.Stream
	format  Format
	scale   float32
	pending []float32
	done    bool
}

func newFLACDecoder(src *wrappedReader) (Decoder, error) {
	st, err := flac.New(src)
	if err != nil {
		return nil, err
	}
	if st.Info == nil || st.Info.SampleRate == 0 || st.Info.NChannels == 0 {
		return nil, errors.New("missing or invalid StreamInfo")
	}
	bits := st.Info.BitsPerSample
	if bits < 4 || bits > 32 {
		return nil, fmt.Errorf("unsupported bit depth %d", bits)
	}
	return &flacDecoder{
		src:    src,
		stream: st,
		format: Format{SampleRate: int(st.Info.SampleRate), Channels: int(st.Info.NChannels)},
		scale:  1 / float32(int64(1)<<(bits-1)),
	}, nil
}

func (d *flacDecoder) Format() Format { return d.format }

func (d *flacDecoder) Read(p []float32) (int, error) {
	for len(d.pending) == 0 {
		if d.done {
			return 0, io.EOF
		}
		if err := d.fill(); err != nil {
			return 0, err
		}
	}
	// Hand out whole frames only; the converter maps channels per frame.
	ch := d.format.Channels
	if len(p) < ch {
		return 0, io.ErrShortBuffer
	}
	n := copy(p[:len(p)-len(p)%ch], d.pending)
	d.pending = d.pending[n:]
	return n, nil
}

func (d *flacDecoder) fill() error {
	for {
		fr, err := d.stream.ParseNext()
		if err != nil {
			d.done = true
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return io.EOF
			}
			return err
		}
		if len(fr.Subframes) == 0 {
			continue
		}
		ch := len(fr.Subframes)
		frames := fr.Subframes[0].NSamples
		if frames == 0 {
			continue
		}
		out := d.pending[:0]
		if cap(out) < frames*ch {
			out = make([]float32, 0, frames*ch)
		}
		for i := range frames {
			for c := range ch {
				sub := fr.Subframes[c]
				var v int32
				if i < len(sub.Samples) {
					v = sub.Samples[i]
				}
				out = append(out, float32(v)*d.scale)
			}
		}
		d.pending = out
		return nil
	}
}

func (d *flacDecoder) Close() error { return d.src.Close() }
