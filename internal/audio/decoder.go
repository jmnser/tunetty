package audio

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Decoder streams interleaved float32 PCM in the range [-1, 1].
type Decoder interface {
	// Format reports the native sample rate and channel count.
	Format() Format
	// Read fills p with interleaved samples and returns how many it wrote.
	// It returns io.EOF once the stream is exhausted.
	Read(p []float32) (int, error)
	// Close releases the decoder and the underlying stream.
	Close() error
}

// ErrUnsupportedFormat is returned when no decoder recognises the stream.
var ErrUnsupportedFormat = errors.New("audio: unsupported stream format")

// codec identifies a container/codec combination.
type codec string

const (
	codecOpus   codec = "opus"
	codecVorbis codec = "vorbis"
	codecMP3    codec = "mp3"
	codecFLAC   codec = "flac"
	codecWAV    codec = "wav"
)

// sniffSize is the number of leading bytes inspected to identify a stream.
// It must cover an ID3v2 header plus the first Ogg page so Opus and Vorbis
// can be told apart.
const sniffSize = 8192

// Open identifies the stream and returns a decoder for it. hint is an optional
// MIME type or file suffix used only when magic byte sniffing is inconclusive.
// The returned decoder takes ownership of rc.
func Open(rc io.ReadCloser, hint string) (Decoder, error) {
	br := bufio.NewReaderSize(rc, sniffSize)
	head, err := br.Peek(sniffSize)
	if err != nil && len(head) == 0 {
		_ = rc.Close()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: empty stream", ErrUnsupportedFormat)
		}
		return nil, err
	}

	src := &wrappedReader{Reader: br, closer: rc}
	c, ok := sniff(head)
	if !ok {
		if c, ok = codecFromHint(hint); !ok {
			_ = rc.Close()
			return nil, fmt.Errorf("%w (hint %q)", ErrUnsupportedFormat, hint)
		}
	}

	var dec Decoder
	switch c {
	case codecOpus:
		dec, err = newOpusDecoder(src)
	case codecVorbis:
		dec, err = newVorbisDecoder(src)
	case codecMP3:
		dec, err = newMP3Decoder(src)
	case codecFLAC:
		dec, err = newFLACDecoder(src)
	case codecWAV:
		dec, err = newWAVDecoder(src)
	default:
		err = ErrUnsupportedFormat
	}
	if err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("audio: opening %s stream: %w", c, err)
	}
	return dec, nil
}

// wrappedReader pairs a buffered reader with the closer of its source.
type wrappedReader struct {
	io.Reader
	closer io.Closer
}

func (w *wrappedReader) Close() error { return w.closer.Close() }

// sniff identifies a codec from the leading bytes of a stream.
func sniff(head []byte) (codec, bool) {
	switch {
	case bytes.HasPrefix(head, []byte("OggS")):
		// Distinguish Opus from Vorbis by the codec identification header that
		// follows the first page header (27 bytes + segment table).
		if bytes.Contains(head[:min(len(head), 512)], []byte("OpusHead")) {
			return codecOpus, true
		}
		if bytes.Contains(head[:min(len(head), 512)], []byte("vorbis")) {
			return codecVorbis, true
		}
		return "", false
	case bytes.HasPrefix(head, []byte("fLaC")):
		return codecFLAC, true
	case bytes.HasPrefix(head, []byte("RIFF")) && len(head) >= 12 && bytes.Equal(head[8:12], []byte("WAVE")):
		return codecWAV, true
	case bytes.HasPrefix(head, []byte("ID3")):
		return codecMP3, true
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return codecMP3, true
	}
	return "", false
}

// codecFromHint maps a MIME type or filename suffix to a codec.
//
// The codec keyword is looked for in the whole hint, because servers commonly
// answer "audio/ogg; codecs=opus" where the distinguishing information lives
// in the parameters rather than the media type.
func codecFromHint(hint string) (codec, bool) {
	full := strings.ToLower(strings.TrimSpace(hint))
	base := full
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch {
	case strings.Contains(full, "opus"):
		return codecOpus, true
	case strings.Contains(full, "vorbis"), strings.HasSuffix(base, "ogg"), strings.HasSuffix(base, "oga"):
		return codecVorbis, true
	case strings.Contains(base, "mpeg"), strings.Contains(base, "mp3"):
		return codecMP3, true
	case strings.Contains(base, "flac"):
		return codecFLAC, true
	case strings.Contains(base, "wav"), strings.Contains(base, "x-pcm"):
		return codecWAV, true
	}
	return "", false
}
