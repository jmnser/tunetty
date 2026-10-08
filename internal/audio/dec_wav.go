package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// wavDecoder handles the uncompressed RIFF/WAVE variants a Subsonic server may
// hand back when asked for format=wav or format=raw.
type wavDecoder struct {
	src    io.Closer
	r      io.Reader
	format Format
	bits   int
	float  bool
	raw    []byte
}

const (
	wavFormatPCM       = 1
	wavFormatFloat     = 3
	wavFormatExtension = 0xFFFE
)

func newWAVDecoder(src *wrappedReader) (Decoder, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(src, hdr[:]); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE stream")
	}

	d := &wavDecoder{src: src, r: src, raw: make([]byte, 16384)}
	for {
		var ch [8]byte
		if _, err := io.ReadFull(src, ch[:]); err != nil {
			return nil, err
		}
		id := string(ch[0:4])
		size := binary.LittleEndian.Uint32(ch[4:8])
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("short fmt chunk (%d bytes)", size)
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(src, body); err != nil {
				return nil, err
			}
			tag := binary.LittleEndian.Uint16(body[0:2])
			d.format.Channels = int(binary.LittleEndian.Uint16(body[2:4]))
			d.format.SampleRate = int(binary.LittleEndian.Uint32(body[4:8]))
			d.bits = int(binary.LittleEndian.Uint16(body[14:16]))
			if tag == wavFormatExtension && size >= 40 {
				tag = binary.LittleEndian.Uint16(body[24:26])
			}
			switch tag {
			case wavFormatPCM:
			case wavFormatFloat:
				d.float = true
			default:
				return nil, fmt.Errorf("unsupported WAVE format tag %d", tag)
			}
			if size%2 == 1 {
				_, _ = io.CopyN(io.Discard, src, 1)
			}
		case "data":
			if !d.format.Valid() {
				return nil, errors.New("data chunk before fmt chunk")
			}
			switch {
			case d.float && d.bits == 32:
			case !d.float && (d.bits == 8 || d.bits == 16 || d.bits == 24 || d.bits == 32):
			default:
				return nil, fmt.Errorf("unsupported sample width %d (float=%v)", d.bits, d.float)
			}
			// Servers often stream WAVE with an unknown length, so trust the
			// connection rather than the declared chunk size.
			return d, nil
		default:
			skip := int64(size) + int64(size%2)
			if _, err := io.CopyN(io.Discard, src, skip); err != nil {
				return nil, err
			}
		}
	}
}

func (d *wavDecoder) Format() Format { return d.format }

func (d *wavDecoder) Read(p []float32) (int, error) {
	width := d.bits / 8
	want := min(len(p)*width, len(d.raw))
	frame := width * d.format.Channels
	want -= want % frame
	if want == 0 {
		return 0, nil
	}
	n, err := io.ReadFull(d.r, d.raw[:want])
	if n < frame {
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return 0, err
	}
	n -= n % frame
	count := n / width
	for i := range count {
		p[i] = d.sample(d.raw[i*width : (i+1)*width])
	}
	return count, nil
}

func (d *wavDecoder) sample(b []byte) float32 {
	switch {
	case d.float:
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	case d.bits == 8:
		return (float32(b[0]) - 128) / 128
	case d.bits == 16:
		return float32(int16(binary.LittleEndian.Uint16(b))) / 32768 //nolint:gosec // intentional PCM reinterpretation
	case d.bits == 24:
		v := int32(b[0]) | int32(b[1])<<8 | int32(int8(b[2]))<<16 //nolint:gosec // sign extends the top byte of a 24 bit sample
		return float32(v) / 8388608
	default:
		return float32(int32(binary.LittleEndian.Uint32(b))) / 2147483648 //nolint:gosec // intentional PCM reinterpretation
	}
}

func (d *wavDecoder) Close() error { return d.src.Close() }
