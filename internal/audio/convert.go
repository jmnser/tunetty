package audio

// converter turns a decoder's native PCM into the fixed engine output format:
// channel mapping followed by rate conversion.
//
// Resampling uses Catmull-Rom interpolation, which is cheap enough to stay far
// from the audio deadline while avoiding the audible dullness of linear
// interpolation. The common case (Opus at 48 kHz stereo) bypasses it entirely.
type converter struct {
	in  Format
	out Format

	ratio float64 // input frames consumed per output frame
	pos   float64 // fractional read position within hist

	// hist holds the four input frames the interpolator needs, interleaved in
	// output channel order.
	hist   []float32
	primed bool
	mapped []float32
}

const interpTaps = 4

func newConverter(in, out Format) *converter {
	c := &converter{in: in, out: out, ratio: float64(in.SampleRate) / float64(out.SampleRate)}
	c.hist = make([]float32, interpTaps*out.Channels)
	return c
}

// passthrough reports whether the input needs no conversion at all.
func (c *converter) passthrough() bool {
	return c.in.SampleRate == c.out.SampleRate && c.in.Channels == c.out.Channels
}

// mapChannels rewrites interleaved input frames into output channel order.
// Mono is duplicated, multichannel is downmixed to the first two channels
// plus centre, which keeps stereo imaging sane for 5.1 sources.
func (c *converter) mapChannels(src []float32) []float32 {
	ic, oc := c.in.Channels, c.out.Channels
	if ic == oc {
		return src
	}
	frames := len(src) / ic
	if cap(c.mapped) < frames*oc {
		c.mapped = make([]float32, frames*oc)
	}
	dst := c.mapped[:frames*oc]
	for i := range frames {
		f := src[i*ic : (i+1)*ic]
		var l, r float32
		switch {
		case ic == 1:
			l, r = f[0], f[0]
		case ic >= 3:
			// L, R, C, ... — fold centre in at -3 dB.
			l = f[0] + 0.7071*f[2]
			r = f[1] + 0.7071*f[2]
		default:
			l, r = f[0], f[1]
		}
		o := dst[i*oc : (i+1)*oc]
		o[0] = l
		if oc > 1 {
			o[1] = r
		}
		for ch := 2; ch < oc; ch++ {
			o[ch] = 0
		}
	}
	return dst
}

// convert appends the converted form of src to dst and returns it.
func (c *converter) convert(dst, src []float32) []float32 {
	if len(src) == 0 {
		return dst
	}
	if c.passthrough() {
		return append(dst, src...)
	}
	src = c.mapChannels(src)
	if c.in.SampleRate == c.out.SampleRate {
		return append(dst, src...)
	}
	return c.resample(dst, src)
}

func (c *converter) resample(dst, src []float32) []float32 {
	ch := c.out.Channels
	frames := len(src) / ch
	if frames == 0 {
		return dst
	}
	if !c.primed {
		// Seed the history with the first frame so playback starts at the very
		// beginning instead of ramping out of silence.
		for t := range interpTaps {
			copy(c.hist[t*ch:(t+1)*ch], src[:ch])
		}
		c.primed = true
	}

	for i := range frames {
		c.pushFrame(src[i*ch : (i+1)*ch])
		// hist now holds frames [n-3 … n]; emit every output sample whose
		// position falls in the [n-2, n-1] interval covered by the kernel.
		for c.pos < 1 {
			for k := range ch {
				dst = append(dst, catmullRom(
					c.hist[0*ch+k], c.hist[1*ch+k], c.hist[2*ch+k], c.hist[3*ch+k], float32(c.pos)))
			}
			c.pos += c.ratio
		}
		c.pos--
	}
	return dst
}

// pushFrame shifts one frame into the interpolation history.
func (c *converter) pushFrame(f []float32) {
	ch := c.out.Channels
	copy(c.hist, c.hist[ch:])
	copy(c.hist[(interpTaps-1)*ch:], f)
}

// catmullRom interpolates between p1 and p2 at t in [0,1).
func catmullRom(p0, p1, p2, p3, t float32) float32 {
	t2 := t * t
	t3 := t2 * t
	return 0.5 * ((2 * p1) +
		(-p0+p2)*t +
		(2*p0-5*p1+4*p2-p3)*t2 +
		(-p0+3*p1-3*p2+p3)*t3)
}
