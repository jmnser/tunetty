package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// listItem is one row in a browsing list.
type listItem struct {
	// Primary is the main label, Secondary is shown right aligned.
	Primary   string
	Secondary string
	// Prefix is drawn before the label, used for track numbers and markers.
	Prefix string
	// Playing marks the row as the currently audible track.
	Playing bool
	// Starred draws a favourite marker.
	Starred bool
	// Data carries the underlying model object.
	Data any
	// QueueIndex is the engine queue position of a row in the queue view,
	// which differs from the row index while the list is filtered.
	QueueIndex int
}

// itemList is a lightweight virtualised list. Only the visible window is rendered,
// so a library with tens of thousands of tracks costs the same as a short one.
type itemList struct {
	items  []listItem
	cursor int
	offset int
	height int
	width  int
}

func (l *itemList) setItems(items []listItem) {
	l.items = items
	if l.cursor >= len(items) {
		l.cursor = max(0, len(items)-1)
	}
	l.clampOffset()
}

func (l *itemList) setSize(w, h int) {
	l.width, l.height = w, h
	l.clampOffset()
}

func (l *itemList) selected() (listItem, bool) {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		return listItem{}, false
	}
	return l.items[l.cursor], true
}

func (l *itemList) move(delta int) {
	if len(l.items) == 0 {
		return
	}
	l.cursor = clampTop(l.cursor+delta, len(l.items)-1)
	l.clampOffset()
}

func (l *itemList) moveTo(i int) {
	if len(l.items) == 0 {
		return
	}
	l.cursor = clampTop(i, len(l.items)-1)
	l.clampOffset()
}

// clampOffset keeps the cursor inside the visible window, with a two row
// scroll margin so the next entries are always visible.
func (l *itemList) clampOffset() {
	if l.height <= 0 {
		l.offset = 0
		return
	}
	const margin = 2
	maxOffset := max(0, len(l.items)-l.height)

	if l.cursor-margin < l.offset {
		l.offset = l.cursor - margin
	}
	if l.cursor+margin >= l.offset+l.height {
		l.offset = l.cursor + margin - l.height + 1
	}
	l.offset = clampTop(l.offset, maxOffset)
}

// render draws the visible window, padded to exactly height lines.
func (l *itemList) render(t Theme, focused bool) string {
	if l.height <= 0 || l.width <= 0 {
		return ""
	}
	lines := make([]string, 0, l.height)

	end := min(l.offset+l.height, len(l.items))
	for i := l.offset; i < end; i++ {
		lines = append(lines, l.renderRow(t, i, focused))
	}
	blank := strings.Repeat(" ", l.width)
	for len(lines) < l.height {
		lines = append(lines, blank)
	}
	return strings.Join(lines, "\n")
}

func (l *itemList) renderRow(t Theme, i int, focused bool) string {
	it := l.items[i]
	sel := i == l.cursor

	marker := "  "
	switch {
	case sel && focused:
		marker = t.RowSel.Render("❯ ")
	case it.Playing:
		marker = t.RowPlay.Render("♪ ")
	}

	star := ""
	if it.Starred {
		star = lipgloss.NewStyle().Foreground(t.Accent).Render("★ ")
	}

	prefix := ""
	if it.Prefix != "" {
		prefix = t.Dim.Render(it.Prefix) + " "
	}

	right := ""
	if it.Secondary != "" {
		right = t.Dim.Render(it.Secondary)
	}

	// Reserve room for the marker, the right column and a gap, then truncate
	// the label to whatever is left.
	rightW := lipgloss.Width(right)
	fixed := lipgloss.Width(marker) + lipgloss.Width(prefix) + lipgloss.Width(star)
	avail := max(l.width-fixed-rightW-1, 1)
	label := truncate(it.Primary, avail)

	style := t.Row
	switch {
	case sel && focused:
		style = t.RowSel
	case it.Playing:
		style = t.RowPlay
	}

	left := marker + prefix + star + style.Render(label)
	gap := max(l.width-lipgloss.Width(left)-rightW, 0)
	return left + strings.Repeat(" ", gap) + right
}

// position returns the 1-based cursor row and the row count.
func (l *itemList) position() (int, int) { return l.cursor + 1, len(l.items) }

// clamp bounds v to the inclusive range [lo, hi].
func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// clampTop bounds v to [0, hi], the common case in list navigation.
func clampTop(v, hi int) int { return clamp(v, 0, hi) }
