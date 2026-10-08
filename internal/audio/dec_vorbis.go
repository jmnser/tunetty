package audio

import (
	"io"

	"github.com/jfreymuth/oggvorbis"
)

// vorbisDecoder adapts oggvorbis, which already emits interleaved float32.
type vorbisDecoder struct {
	src io.Closer
	r   *oggvorbis.Reader
}

func newVorbisDecoder(src *wrappedReader) (Decoder, error) {
	r, err := oggvorbis.NewReader(src)
	if err != nil {
		return nil, err
	}
	return &vorbisDecoder{src: src, r: r}, nil
}

func (d *vorbisDecoder) Format() Format {
	return Format{SampleRate: d.r.SampleRate(), Channels: d.r.Channels()}
}

func (d *vorbisDecoder) Read(p []float32) (int, error) {
	// oggvorbis returns whole frames, so trim to a channel multiple.
	ch := d.r.Channels()
	if ch > 1 && len(p) >= ch {
		p = p[:len(p)-len(p)%ch]
	}
	return d.r.Read(p)
}

func (d *vorbisDecoder) Close() error { return d.src.Close() }
