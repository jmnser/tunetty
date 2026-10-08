package audio

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/pion/opus"
	"github.com/pion/opus/pkg/oggreader"
)

// opusFrameSamples is the largest number of samples a single Opus packet can
// produce per channel: 120 ms at 48 kHz.
const opusFrameSamples = 5760

// opusDecoder decodes Ogg encapsulated Opus using the pure Go pion decoder.
type opusDecoder struct {
	src      io.Closer
	ogg      *oggreader.OggReader
	dec      opus.Decoder
	channels int

	scratch []float32 // decode buffer for one packet
	pending []float32 // decoded samples not yet handed to the caller

	// preSkip is the number of priming frames the encoder asks us to discard.
	preSkip int
	// decoded counts frames decoded so far, pre-skip included, which is the
	// unit Ogg granule positions use.
	decoded uint64
	done    bool
}

func newOpusDecoder(src *wrappedReader) (Decoder, error) {
	ogg, header, err := oggreader.NewWith(src)
	if err != nil {
		return nil, err
	}
	ch := int(header.Channels)
	if ch < 1 || ch > 2 {
		// The pion decoder emits mono or stereo; anything else is out of scope.
		return nil, fmt.Errorf("unsupported channel count %d", ch)
	}
	dec, err := opus.NewDecoderWithOutput(OutputFormat.SampleRate, ch)
	if err != nil {
		return nil, err
	}
	return &opusDecoder{
		src:      src,
		ogg:      ogg,
		dec:      dec,
		channels: ch,
		scratch:  make([]float32, opusFrameSamples*ch),
		preSkip:  int(header.PreSkip),
	}, nil
}

// Format reports 48 kHz because Opus always decodes at the Opus API rate we
// configured, which is already the engine output rate.
func (d *opusDecoder) Format() Format {
	return Format{SampleRate: OutputFormat.SampleRate, Channels: d.channels}
}

func (d *opusDecoder) Read(p []float32) (int, error) {
	for len(d.pending) == 0 {
		if d.done {
			return 0, io.EOF
		}
		if err := d.fill(); err != nil {
			return 0, err
		}
	}
	// Hand out whole frames only; the converter maps channels per frame.
	if len(p) < d.channels {
		return 0, io.ErrShortBuffer
	}
	n := copy(p[:len(p)-len(p)%d.channels], d.pending)
	d.pending = d.pending[n:]
	return n, nil
}

// fill decodes the next Opus packet into pending, honouring the pre-skip at
// the start and the granule position end trim (RFC 7845 section 4) at the
// end, which together make track joins sample-exact.
func (d *opusDecoder) fill() error {
	for {
		payload, page, err := d.ogg.ParseNextPacket()
		if err != nil {
			d.done = true
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return io.EOF
			}
			return err
		}
		if len(payload) == 0 {
			continue
		}
		// DecodeToFloat32 returns samples *per channel* and writes
		// framesPerChannel*channels interleaved values.
		frames, err := d.dec.DecodeToFloat32(payload, d.scratch)
		if err != nil {
			// A single corrupt packet should not end the track; skip it.
			continue
		}
		before := d.decoded
		d.decoded += uint64(frames) //nolint:gosec // frame counts are never negative
		// A page's granule position counts the frames (pre-skip included) up
		// to the last packet completed on it. Only the final page may hold
		// fewer frames than its packets decode to; the surplus is padding.
		if g := page.GranulePosition; g != math.MaxUint64 && g >= before && g < d.decoded {
			frames = int(g - before) //nolint:gosec // bounded by frames
		}
		out := d.scratch[:frames*d.channels]
		if d.preSkip > 0 {
			skip := d.preSkip * d.channels
			if skip >= len(out) {
				d.preSkip -= len(out) / d.channels
				continue
			}
			out = out[skip:]
			d.preSkip = 0
		}
		if len(out) == 0 {
			continue
		}
		d.pending = append(d.pending[:0], out...)
		return nil
	}
}

func (d *opusDecoder) Close() error { return d.src.Close() }
