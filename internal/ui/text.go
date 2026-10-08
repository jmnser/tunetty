package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jmnser/tunetty/internal/subsonic"
)

// truncate shortens s to at most w display columns, appending an ellipsis when
// it had to cut. Escape sequences are preserved and never split.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// pad right-pads s to exactly w display columns.
func pad(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

// clock renders a position or length as m:ss or h:mm:ss.
func clock(d time.Duration) string {
	return subsonic.Duration(d / time.Second).String()
}

// progressBar renders a proportional bar w columns wide.
func progressBar(t Theme, frac float64, w int) string {
	if w <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := min(int(frac*float64(w)+0.5), w)
	head := ""
	if filled > 0 && filled < w {
		head = "●"
		filled--
	}
	return t.BarFill.Render(strings.Repeat("━", filled)+head) +
		t.BarEmpty.Render(strings.Repeat("─", w-filled-lipgloss.Width(head)))
}

// meter renders a small block gauge, used for volume and buffer level.
func meter(t Theme, frac float64, w int) string {
	if w <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(w) + 0.5)
	return t.BarFill.Render(strings.Repeat("▰", filled)) +
		t.BarEmpty.Render(strings.Repeat("▱", w-filled))
}

// joinColumns places a left and right block side by side with a gutter,
// padding both to the same height. Every returned line is exactly
// leftW+gutter+rightW columns wide.
func joinColumns(left, right string, leftW, gutter, rightW int) string {
	l := strings.Split(left, "\n")
	r := strings.Split(right, "\n")
	n := max(len(l), len(r))

	sep := strings.Repeat(" ", gutter)
	out := make([]string, n)
	for i := range n {
		var a, b string
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			b = r[i]
		}
		out[i] = padExact(a, leftW) + sep + padExact(b, rightW)
	}
	return strings.Join(out, "\n")
}

// padExact pads or truncates to exactly w columns.
func padExact(s string, w int) string {
	if lipgloss.Width(s) > w {
		return truncate(s, w)
	}
	return pad(s, w)
}
