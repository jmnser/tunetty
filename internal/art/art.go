// Package art renders album cover art in a terminal, using the richest
// graphics protocol the terminal supports and degrading to Unicode half blocks
// everywhere else. All rendering is pure Go.
package art

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // cover art is occasionally a GIF
	_ "image/jpeg" // the common case
	_ "image/png"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // some servers store covers as WebP
)

// Protocol identifies a terminal graphics protocol.
type Protocol string

// Supported protocols. ProtocolAuto asks Detect to choose.
const (
	ProtocolAuto      Protocol = "auto"
	ProtocolKitty     Protocol = "kitty"
	ProtocolITerm     Protocol = "iterm2"
	ProtocolSixel     Protocol = "sixel"
	ProtocolHalfBlock Protocol = "blocks"
	ProtocolNone      Protocol = "none"
)

// Graphical reports whether the protocol draws real pixels rather than text.
func (p Protocol) Graphical() bool {
	switch p {
	case ProtocolKitty, ProtocolITerm, ProtocolSixel:
		return true
	default:
		return false
	}
}

// Renderer turns an image into terminal output sized to a cell grid.
type Renderer struct {
	proto Protocol
	caps  Capabilities
}

// NewRenderer builds a renderer for the given protocol and terminal
// capabilities. Pass ProtocolAuto to use the detected protocol.
func NewRenderer(p Protocol, caps Capabilities) *Renderer {
	if p == "" || p == ProtocolAuto {
		p = caps.Protocol
	}
	return &Renderer{proto: p, caps: caps}
}

// Protocol returns the protocol actually in use.
func (r *Renderer) Protocol() Protocol { return r.proto }

// Capabilities returns the terminal capabilities the renderer was built with.
func (r *Renderer) Capabilities() Capabilities { return r.caps }

// Image is a decoded cover ready for rendering.
type Image struct {
	Src image.Image
}

// Decode parses PNG, JPEG, GIF or WebP cover art bytes.
func Decode(b []byte) (*Image, error) {
	if len(b) == 0 {
		return nil, errors.New("art: empty image data")
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("art: decoding cover: %w", err)
	}
	return &Image{Src: img}, nil
}

// Rendered is terminal output for one image placement.
type Rendered struct {
	// Lines are exactly Rows strings, each occupying Cols display columns.
	// Graphics protocols hide their escape sequence inside the first line,
	// where it measures zero columns wide, so the block still lays out as a
	// plain Rows x Cols rectangle.
	Lines []string
	Rows  int
	Cols  int
}

// String joins the rendered lines.
func (r Rendered) String() string { return strings.Join(r.Lines, "\n") }

// Blank returns an empty placeholder block of the same size.
func Blank(cols, rows int) Rendered {
	if cols < 0 {
		cols = 0
	}
	if rows < 0 {
		rows = 0
	}
	lines := make([]string, rows)
	pad := strings.Repeat(" ", cols)
	for i := range lines {
		lines[i] = pad
	}
	return Rendered{Lines: lines, Rows: rows, Cols: cols}
}

// Render draws img into a cols x rows cell area, preserving aspect ratio and
// centring the result.
func (r *Renderer) Render(img *Image, cols, rows int) (Rendered, error) {
	if img == nil || img.Src == nil {
		return Blank(cols, rows), nil
	}
	if cols <= 0 || rows <= 0 || r.proto == ProtocolNone {
		return Blank(cols, rows), nil
	}

	fitCols, fitRows := r.fit(img.Src.Bounds(), cols, rows)
	if fitCols <= 0 || fitRows <= 0 {
		return Blank(cols, rows), nil
	}

	var (
		body Rendered
		err  error
	)
	switch r.proto {
	case ProtocolKitty:
		body, err = r.renderKitty(img.Src, fitCols, fitRows)
	case ProtocolITerm:
		body, err = r.renderITerm(img.Src, fitCols, fitRows)
	case ProtocolSixel:
		body = r.renderSixel(img.Src, fitCols, fitRows)
	default:
		body = r.renderBlocks(img.Src, fitCols, fitRows)
	}
	if err != nil {
		// Only the PNG based protocols can fail, and a failure there should
		// never blank the UI: half blocks always work.
		return center(r.renderBlocks(img.Src, fitCols, fitRows), cols, rows), nil
	}
	return center(body, cols, rows), nil
}

// fit computes the largest cell rectangle with the image's aspect ratio that
// fits in cols x rows, accounting for the terminal's non-square cells.
func (r *Renderer) fit(b image.Rectangle, cols, rows int) (int, int) {
	iw, ih := b.Dx(), b.Dy()
	if iw <= 0 || ih <= 0 {
		return 0, 0
	}
	// An area of cols x rows cells covers cols*cw by rows*ch screen pixels, so
	// preserving the aspect ratio is the same computation for every protocol.
	// The half block renderer's two-pixels-per-cell trick is an internal
	// detail of how it fills those same screen pixels.
	cw, ch := r.caps.CellWidth, r.caps.CellHeight
	if cw <= 0 || ch <= 0 {
		cw, ch = 1, 2
	}

	// Width in cells if height is the binding constraint, and vice versa.
	byRows := int(float64(rows) * float64(ch) / float64(cw) * float64(iw) / float64(ih))
	if byRows <= cols {
		if byRows < 1 {
			byRows = 1
		}
		return byRows, rows
	}
	byCols := max(int(float64(cols)*float64(cw)/float64(ch)*float64(ih)/float64(iw)), 1)
	return cols, byCols
}

// center pads a rendered block out to cols x rows.
func center(in Rendered, cols, rows int) Rendered {
	if in.Cols >= cols && in.Rows >= rows {
		return in
	}
	left := max((cols-in.Cols)/2, 0)
	top := max((rows-in.Rows)/2, 0)
	lpad := strings.Repeat(" ", left)
	rpad := strings.Repeat(" ", max(0, cols-in.Cols-left))
	blank := strings.Repeat(" ", cols)

	out := make([]string, 0, rows)
	for range top {
		out = append(out, blank)
	}
	for _, l := range in.Lines {
		out = append(out, lpad+l+rpad)
	}
	for len(out) < rows {
		out = append(out, blank)
	}
	return Rendered{Lines: out[:rows], Rows: rows, Cols: cols}
}

// scale resizes src to exactly w x h pixels with a high quality kernel.
func scale(src image.Image, w, h int) *image.RGBA {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// pixelSize returns the pixel dimensions covered by a cell rectangle.
func (r *Renderer) pixelSize(cols, rows int) (int, int) {
	cw, ch := r.caps.CellWidth, r.caps.CellHeight
	if cw <= 0 {
		cw = 10
	}
	if ch <= 0 {
		ch = 20
	}
	return cols * cw, rows * ch
}
