package art

import (
	"fmt"
	"image"
	"strings"
)

// upperHalf renders two vertically stacked pixels in a single cell: the glyph
// covers the top half (foreground colour) and the cell background shows
// through underneath.
const upperHalf = "▀"

// renderBlocks is the universal fallback. It needs nothing beyond SGR colour
// support, so it works over SSH, in screen, in CI logs and in terminals with
// no graphics protocol at all.
func (r *Renderer) renderBlocks(src image.Image, cols, rows int) Rendered {
	img := scale(src, cols, rows*2)

	lines := make([]string, 0, rows)
	var sb strings.Builder
	for y := range rows {
		sb.Reset()
		var lastTop, lastBottom uint32 = 1 << 24, 1 << 24
		for x := range cols {
			tr, tg, tb := rgb(img, x, y*2)
			br, bg, bb := rgb(img, x, y*2+1)
			top := pack(tr, tg, tb)
			bottom := pack(br, bg, bb)
			// Only re-emit SGR when the colour actually changes; a 40x20 cover
			// otherwise costs several kilobytes per frame.
			if top != lastTop {
				sb.WriteString(r.fg(tr, tg, tb))
				lastTop = top
			}
			if bottom != lastBottom {
				sb.WriteString(r.bg(br, bg, bb))
				lastBottom = bottom
			}
			sb.WriteString(upperHalf)
		}
		sb.WriteString("\x1b[0m")
		lines = append(lines, sb.String())
	}
	return Rendered{Lines: lines, Rows: rows, Cols: cols}
}

func rgb(img *image.RGBA, x, y int) (uint8, uint8, uint8) {
	if !(image.Point{X: x, Y: y}).In(img.Bounds()) {
		return 0, 0, 0
	}
	i := img.PixOffset(x, y)
	return img.Pix[i], img.Pix[i+1], img.Pix[i+2]
}

func pack(r, g, b uint8) uint32 { return uint32(r)<<16 | uint32(g)<<8 | uint32(b) }

func (r *Renderer) fg(cr, cg, cb uint8) string {
	if r.caps.TrueColor {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", cr, cg, cb)
	}
	return fmt.Sprintf("\x1b[38;5;%dm", ansi256(cr, cg, cb))
}

func (r *Renderer) bg(cr, cg, cb uint8) string {
	if r.caps.TrueColor {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", cr, cg, cb)
	}
	return fmt.Sprintf("\x1b[48;5;%dm", ansi256(cr, cg, cb))
}

// ansi256 maps a colour onto the xterm 256 colour palette, choosing between
// the 6x6x6 cube and the grey ramp by whichever is closer.
func ansi256(r, g, b uint8) int {
	cubeIdx := func(v uint8) int { return int((float64(v)/255)*5 + 0.5) }
	ri, gi, bi := cubeIdx(r), cubeIdx(g), cubeIdx(b)
	cubeR, cubeG, cubeB := cubeLevel(ri), cubeLevel(gi), cubeLevel(bi)

	grey := int((float64(r)+float64(g)+float64(b))/3/255*23 + 0.5)
	// grey is in [0,23], so the ramp value stays inside a byte.
	greyV := uint8(grey*10 + 8) //nolint:gosec // bounded by construction

	if dist(r, g, b, cubeR, cubeG, cubeB) <= dist(r, g, b, greyV, greyV, greyV) {
		return 16 + 36*ri + 6*gi + bi
	}
	return 232 + grey
}

// cubeLevel maps a 0..5 cube coordinate to its 8 bit channel value.
func cubeLevel(i int) uint8 {
	if i <= 0 {
		return 0
	}
	if i > 5 {
		i = 5
	}
	return uint8(55 + i*40)
}

func dist(r, g, b, r2, g2, b2 uint8) int {
	dr, dg, db := int(r)-int(r2), int(g)-int(g2), int(b)-int(b2)
	return dr*dr + dg*dg + db*db
}
