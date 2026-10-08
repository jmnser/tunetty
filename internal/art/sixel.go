package art

import (
	"image"
	"strconv"
	"strings"
)

// sixelColors is the palette size. 256 is the ceiling most sixel terminals
// (xterm, foot, contour, Windows Terminal) guarantee.
const sixelColors = 256

// renderSixel encodes the cover as a sixel image. Sixel has no notion of
// cells, so the image is scaled to the exact pixel size of the target area.
func (r *Renderer) renderSixel(src image.Image, cols, rows int) Rendered {
	pw, ph := r.pixelSize(cols, rows)
	img := scale(src, pw, ph)

	pal, idx := quantize(img, sixelColors)

	var b strings.Builder
	b.Grow(pw * ph / 4)

	// P1=0 (default aspect), P2=1 (leave untouched pixels transparent),
	// P3=0, then raster attributes carrying the true pixel size.
	b.WriteString("\x1bP0;1;0q")
	b.WriteString(`"1;1;`)
	b.WriteString(strconv.Itoa(pw))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(ph))

	for i, c := range pal {
		b.WriteByte('#')
		b.WriteString(strconv.Itoa(i))
		b.WriteString(";2;")
		b.WriteString(strconv.Itoa(int(c[0]) * 100 / 255))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(int(c[1]) * 100 / 255))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(int(c[2]) * 100 / 255))
	}

	// Sixels encode six vertical pixels per character, so the image is walked
	// in horizontal bands of six rows, once per colour present in the band.
	used := make([]bool, len(pal))
	line := make([]byte, pw)
	for top := 0; top < ph; top += 6 {
		for i := range used {
			used[i] = false
		}
		height := 6
		if top+height > ph {
			height = ph - top
		}
		for y := top; y < top+height; y++ {
			row := idx[y*pw : (y+1)*pw]
			for _, c := range row {
				used[c] = true
			}
		}

		first := true
		for c := range used {
			if !used[c] {
				continue
			}
			if !first {
				b.WriteByte('$') // return to the start of the band
			}
			first = false
			b.WriteByte('#')
			b.WriteString(strconv.Itoa(c))

			for x := range pw {
				var bits byte
				for dy := 0; dy < height; dy++ {
					if idx[(top+dy)*pw+x] == uint8(c) {
						bits |= 1 << dy
					}
				}
				line[x] = 0x3F + bits
			}
			writeRLE(&b, line)
		}
		b.WriteByte('-') // next band
	}
	b.WriteString("\x1b\\")

	return graphicBlock(r.caps.Wrap(b.String()), cols, rows)
}

// writeRLE emits sixel data using run length compression, which shrinks the
// large flat areas typical of album art by an order of magnitude.
func writeRLE(b *strings.Builder, line []byte) {
	for i := 0; i < len(line); {
		j := i + 1
		for j < len(line) && line[j] == line[i] {
			j++
		}
		run := j - i
		// The !<count> form costs four characters, so it only pays off past
		// a run of three.
		if run > 3 {
			b.WriteByte('!')
			b.WriteString(strconv.Itoa(run))
			b.WriteByte(line[i])
		} else {
			for range run {
				b.WriteByte(line[i])
			}
		}
		i = j
	}
}

// ------------------------------------------------------------- quantisation

type rgb8 [3]uint8

// quantize reduces img to at most n colours using median cut, returning the
// palette and one palette index per pixel in row major order.
func quantize(img *image.RGBA, n int) ([]rgb8, []uint8) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	pixels := make([]rgb8, 0, w*h)
	for y := range h {
		for x := range w {
			i := img.PixOffset(x, y)
			pixels = append(pixels, rgb8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]})
		}
	}

	pal := medianCut(pixels, n)
	if len(pal) == 0 {
		pal = []rgb8{{0, 0, 0}}
	}

	// Nearest colour lookups dominate the cost, so results are memoised on the
	// top five bits of each channel: a 32k entry table that a cover fills only
	// sparsely but hits constantly.
	const cacheBits = 5
	cache := make([]int16, 1<<(cacheBits*3))
	for i := range cache {
		cache[i] = -1
	}

	idx := make([]uint8, len(pixels))
	for i, p := range pixels {
		key := int(p[0]>>3)<<10 | int(p[1]>>3)<<5 | int(p[2]>>3)
		if v := cache[key]; v >= 0 {
			idx[i] = uint8(v) //nolint:gosec // palette indexes are bounded by sixelColors
			continue
		}
		best, bestD := 0, 1<<30
		for j, c := range pal {
			if d := dist(p[0], p[1], p[2], c[0], c[1], c[2]); d < bestD {
				best, bestD = j, d
			}
		}
		cache[key] = int16(best)
		idx[i] = uint8(best)
	}
	return pal, idx
}

// bucket is a set of pixels awaiting subdivision.
type bucket struct {
	pixels []rgb8
	axis   int
	spread int
}

// medianCut repeatedly splits the widest colour bucket until n buckets exist,
// then averages each one.
func medianCut(pixels []rgb8, n int) []rgb8 {
	if len(pixels) == 0 || n < 1 {
		return nil
	}
	buckets := []bucket{newBucket(pixels)}
	for len(buckets) < n {
		// Split whichever bucket spans the widest range of any channel.
		target, widest := -1, 0
		for i, b := range buckets {
			if len(b.pixels) > 1 && b.spread > widest {
				target, widest = i, b.spread
			}
		}
		if target < 0 {
			break
		}
		b := buckets[target]
		axis := b.axis
		sortByAxis(b.pixels, axis)
		mid := len(b.pixels) / 2
		buckets[target] = newBucket(b.pixels[:mid])
		buckets = append(buckets, newBucket(b.pixels[mid:]))
	}

	pal := make([]rgb8, 0, len(buckets))
	for _, b := range buckets {
		if len(b.pixels) == 0 {
			continue
		}
		var sr, sg, sb int
		for _, p := range b.pixels {
			sr += int(p[0])
			sg += int(p[1])
			sb += int(p[2])
		}
		count := len(b.pixels)
		// Each channel sum divided by its count is a mean of bytes, so the
		// result is always back inside a byte.
		pal = append(pal, rgb8{
			uint8(sr / count), //nolint:gosec // mean of byte values
			uint8(sg / count), //nolint:gosec // mean of byte values
			uint8(sb / count), //nolint:gosec // mean of byte values
		})
	}
	return pal
}

func newBucket(pixels []rgb8) bucket {
	b := bucket{pixels: pixels}
	if len(pixels) == 0 {
		return b
	}
	var lo, hi rgb8
	lo = rgb8{255, 255, 255}
	for _, p := range pixels {
		for c := range 3 {
			if p[c] < lo[c] {
				lo[c] = p[c]
			}
			if p[c] > hi[c] {
				hi[c] = p[c]
			}
		}
	}
	for c := range 3 {
		if s := int(hi[c]) - int(lo[c]); s > b.spread {
			b.spread, b.axis = s, c
		}
	}
	return b
}

// sortByAxis orders pixels by one channel using a counting sort, which is
// linear in the pixel count and avoids comparison sort overhead entirely.
func sortByAxis(pixels []rgb8, axis int) {
	var counts [256]int
	for _, p := range pixels {
		counts[p[axis]]++
	}
	offset := 0
	var starts [256]int
	for v := range 256 {
		starts[v] = offset
		offset += counts[v]
	}
	out := make([]rgb8, len(pixels))
	for _, p := range pixels {
		v := p[axis]
		out[starts[v]] = p
		starts[v]++
	}
	copy(pixels, out)
}
