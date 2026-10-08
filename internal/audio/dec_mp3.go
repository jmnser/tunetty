package audio

import (
	"encoding/binary"
	"io"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// mp3Decoder adapts go-mp3, which emits 16 bit little endian stereo PCM.
type mp3Decoder struct {
	src io.Closer
	dec *mp3.Decoder
	raw []byte
}

func newMP3Decoder(src *wrappedReader) (Decoder, error) {
	dec, err := mp3.NewDecoder(src)
	if err != nil {
		return nil, err
	}
	return &mp3Decoder{src: src, dec: dec, raw: make([]byte, 8192)}, nil
}

func (d *mp3Decoder) Format() Format {
	return Format{SampleRate: d.dec.SampleRate(), Channels: 2}
}

func (d *mp3Decoder) Read(p []float32) (int, error) {
	want := min(len(p)*2, len(d.raw)) // two bytes per sample
	want -= want % 4                  // keep whole stereo frames
	if want == 0 {
		return 0, nil
	}
	n, err := io.ReadFull(d.dec, d.raw[:want])
	if n == 0 {
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return 0, err
	}
	n -= n % 4
	for i := 0; i < n/2; i++ {
		// The conversion reinterprets the bit pattern as a signed sample.
		p[i] = float32(int16(binary.LittleEndian.Uint16(d.raw[i*2:]))) / 32768 //nolint:gosec // intentional PCM reinterpretation
	}
	return n / 2, nil
}

func (d *mp3Decoder) Close() error { return d.src.Close() }
