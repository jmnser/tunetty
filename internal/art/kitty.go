package art

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
)

// kittyChunk is the payload limit per escape sequence mandated by the kitty
// graphics protocol.
const kittyChunk = 4096

// kittyImageID and kittyPlacementID identify tunetty's single cover image.
// Deleting by this id leaves images of other programs, such as those in other
// tmux panes sharing the same terminal, untouched.
const (
	kittyImageID     = 0x74756e // "tun"
	kittyPlacementID = 1
)

// kittyDelete removes tunetty's cover and frees its image data (d=I).
var kittyDelete = fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", kittyImageID)

// ClearSequence returns the escape that removes tunetty's cover image from the
// screen, or "" when the protocol needs no explicit removal. Text based
// protocols disappear when overdrawn; sixel and iTerm2 images are plain cell
// content, but kitty images live on a separate layer until deleted.
func ClearSequence(p Protocol) string {
	if p != ProtocolKitty {
		return ""
	}
	return Capabilities{InTmux: os.Getenv("TMUX") != ""}.Wrap(kittyDelete)
}

// renderKitty transmits the cover as PNG and places it in a cols x rows cell
// area. The image always uses the same id, so the previous cover can be
// deleted instead of accumulating in the terminal's image store.
func (r *Renderer) renderKitty(src image.Image, cols, rows int) (Rendered, error) {
	pw, ph := r.pixelSize(cols, rows)
	var buf bytes.Buffer
	if err := png.Encode(&buf, scale(src, pw, ph)); err != nil {
		return Rendered{}, fmt.Errorf("art: encoding png for kitty: %w", err)
	}

	payload := base64.StdEncoding.EncodeToString(buf.Bytes())

	var seq strings.Builder
	// Drop the previous cover so covers do not stack up.
	seq.WriteString(r.caps.Wrap(kittyDelete))

	// f=100 is PNG, t=d embeds the data directly, C=1 keeps the cursor where
	// it is so the surrounding TUI layout is unaffected, q=2 suppresses the
	// terminal's acknowledgement so it never leaks into the input stream.
	first := true
	for len(payload) > 0 {
		n := min(kittyChunk, len(payload))
		chunk := payload[:n]
		payload = payload[n:]

		more := 0
		if len(payload) > 0 {
			more = 1
		}

		var ctrl string
		if first {
			ctrl = fmt.Sprintf("a=T,f=100,t=d,i=%d,p=%d,c=%d,r=%d,C=1,q=2,m=%d",
				kittyImageID, kittyPlacementID, cols, rows, more)
			first = false
		} else {
			ctrl = fmt.Sprintf("m=%d", more)
		}
		seq.WriteString(r.caps.Wrap("\x1b_G" + ctrl + ";" + chunk + "\x1b\\"))
	}

	return graphicBlock(seq.String(), cols, rows), nil
}

// graphicBlock packages a graphics escape as a Rows x Cols text block.
//
// The escape is emitted on the first line wrapped in save/restore cursor, so
// it measures zero display columns and the block still lays out as a plain
// rectangle of spaces for the TUI framework.
func graphicBlock(seq string, cols, rows int) Rendered {
	pad := strings.Repeat(" ", cols)
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = pad
	}
	if rows > 0 {
		lines[0] = "\x1b7" + seq + "\x1b8" + pad
	}
	return Rendered{Lines: lines, Rows: rows, Cols: cols}
}
