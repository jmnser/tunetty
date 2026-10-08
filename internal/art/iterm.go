package art

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
)

// renderITerm emits an iTerm2 inline image (OSC 1337), which WezTerm, VS Code
// and mintty also implement.
func (r *Renderer) renderITerm(src image.Image, cols, rows int) (Rendered, error) {
	pw, ph := r.pixelSize(cols, rows)
	var buf bytes.Buffer
	if err := png.Encode(&buf, scale(src, pw, ph)); err != nil {
		return Rendered{}, fmt.Errorf("art: encoding png for iterm2: %w", err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())

	// doNotMoveCursor is not universally honoured, so the block wrapper's
	// save/restore pair is what actually keeps the layout stable.
	seq := fmt.Sprintf("\x1b]1337;File=inline=1;size=%d;width=%d;height=%d;preserveAspectRatio=1;doNotMoveCursor=1:%s\a",
		buf.Len(), cols, rows, data)

	return graphicBlock(r.caps.Wrap(seq), cols, rows), nil
}
