package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jmnser/tunetty/internal/art"
	"github.com/jmnser/tunetty/internal/audio"
)

// View renders the whole frame.
//
// The layout deliberately keeps the cover art on rows that change only when
// the track changes. Terminal graphics are painted into cells, so a redraw of
// any line crossing the image would erase part of it; the progress bar and
// status line therefore live below the artwork rather than beside it.
func (m *Model) View() string {
	if !m.ready {
		return "starting tunetty…"
	}
	// A kitty cover stays on screen until deleted, so every frame without one
	// carries the delete sequence. It measures zero columns and is only sent
	// when the line it prefixes changes.
	clearArt := ""
	if !m.artVisible() {
		clearArt = art.ClearSequence(m.renderer.Protocol())
	}
	if m.mode == modeHelp {
		return clearArt + m.helpView()
	}

	var b strings.Builder
	b.WriteString(clearArt)
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	b.WriteString(m.tabsView())
	b.WriteByte('\n')
	b.WriteString(m.contentView())
	b.WriteByte('\n')
	b.WriteString(m.theme.Border.Render(strings.Repeat("─", m.width)))
	b.WriteByte('\n')
	b.WriteString(m.nowPlayingView())
	b.WriteByte('\n')
	b.WriteString(m.progressView())
	b.WriteByte('\n')
	b.WriteString(m.statusView())
	return b.String()
}

func (m *Model) headerView() string {
	left := m.theme.Header.Render("tunetty")
	if m.version != "" {
		left += m.theme.Dim.Render(" " + m.version)
	}

	right := m.theme.Dim.Render(m.describeServer())
	if m.loading > 0 {
		right = m.theme.Status.Render("loading… ") + right
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return padExact(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) tabsView() string {
	parts := make([]string, 0, len(viewNames))
	for i, name := range viewNames {
		label := " " + name + " "
		if view(i) == m.view {
			parts = append(parts, m.theme.TabActive.Render(label))
			continue
		}
		parts = append(parts, m.theme.TabIdle.Render(label))
	}
	tabs := strings.Join(parts, m.theme.Border.Render("│"))

	// Breadcrumbs sit on the right so drilling into an album is visible.
	crumb := ""
	if c := m.crumbs(); len(c) > 0 {
		crumb = m.theme.Dim.Render(strings.Join(c, " › "))
	}
	gap := m.width - lipgloss.Width(tabs) - lipgloss.Width(crumb)
	if gap < 1 {
		return padExact(tabs, m.width)
	}
	return tabs + strings.Repeat(" ", gap) + crumb
}

// contentView renders the browsing list, or the finder overlay when active.
func (m *Model) contentView() string {
	if m.mode == modeFinder {
		return m.finderView()
	}

	l := m.lists[m.view]
	body := l.render(m.theme, m.mode == modeBrowse)

	if m.mode == modeFilter {
		// The filter prompt replaces the first content row.
		lines := strings.Split(body, "\n")
		prompt := m.theme.Prompt.Render("/") + m.filterQ + m.theme.Dim.Render("▏")
		lines[0] = padExact(prompt, m.width)
		return strings.Join(lines, "\n")
	}
	if len(l.items) == 0 {
		return m.emptyView(l.height)
	}
	return body
}

func (m *Model) emptyView(height int) string {
	msg := "nothing here yet"
	if m.loading > 0 {
		msg = "loading…"
	}
	lines := make([]string, height)
	for i := range lines {
		lines[i] = strings.Repeat(" ", m.width)
	}
	if height > 1 {
		lines[1] = padExact("  "+m.theme.Dim.Render(msg), m.width)
	}
	return strings.Join(lines, "\n")
}

// finderView renders the fuzzy finder overlay.
func (m *Model) finderView() string {
	h := m.contentHeight()
	lines := make([]string, 0, h)

	prompt := m.theme.Prompt.Render("❯ ") + m.finderQ + m.theme.Dim.Render("▏")
	count := m.theme.Dim.Render(fmt.Sprintf("%d matches", len(m.finderHits)))
	gap := m.width - lipgloss.Width(prompt) - lipgloss.Width(count)
	if gap < 1 {
		lines = append(lines, padExact(prompt, m.width))
	} else {
		lines = append(lines, prompt+strings.Repeat(" ", gap)+count)
	}
	lines = append(lines, m.theme.Border.Render(strings.Repeat("─", m.width)))

	rows := h - 2
	// Scroll the hit list so the cursor stays visible.
	start := 0
	if m.finderCur >= rows {
		start = m.finderCur - rows + 1
	}
	for i := start; i < len(m.finderHits) && len(lines) < h; i++ {
		lines = append(lines, m.finderRow(m.finderHits[i], i == m.finderCur))
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", m.width))
	}
	return strings.Join(lines[:h], "\n")
}

func (m *Model) finderRow(hit match, selected bool) string {
	marker := "  "
	if selected {
		marker = m.theme.RowSel.Render("❯ ")
	}
	kind := m.theme.Badge.Render(padExact(hit.Kind.label(), 9))

	avail := m.width - lipgloss.Width(marker) - 9 - 1
	nameW := avail * 3 / 5
	detailW := avail - nameW - 1
	if nameW < 1 || detailW < 1 {
		return padExact(marker+kind, m.width)
	}

	name := highlight(m.theme, hit.Display, hit.indexes, nameW)
	if selected {
		name = m.theme.RowSel.Render(truncate(hit.Display, nameW))
	}
	detail := m.theme.Dim.Render(truncate(hit.Detail, detailW))
	return padExact(marker+kind+padExact(name, nameW)+" "+detail, m.width)
}

// artGutter separates the cover from the metadata beside it.
const artGutter = 2

// artVisible reports whether the frame places the cover.
func (m *Model) artVisible() bool {
	return m.mode != modeHelp && m.artCols > 0 && len(m.artRendered.Lines) > 0 &&
		m.width-m.artCols-artGutter >= 10
}

// nowPlayingView draws the cover art beside the track metadata.
func (m *Model) nowPlayingView() string {
	rows := m.nowPlayingRows()
	st := m.engine.Status()

	meta := m.metadataBlock(st, rows)
	if !m.artVisible() {
		return padBlock(meta, m.width, rows)
	}

	metaW := m.width - m.artCols - artGutter
	artBlock := m.artRendered
	if artBlock.Rows < rows {
		artBlock = art.Blank(m.artCols, rows)
	}
	return joinColumns(artBlock.String(), meta, m.artCols, artGutter, metaW)
}

// metadataBlock renders the text beside the cover. Everything here changes
// only when the track changes, which is what keeps the artwork intact; queue
// position, which changes with queue edits, lives on the progress row.
func (m *Model) metadataBlock(st audio.Status, rows int) string {
	if st.Track == nil {
		lines := []string{
			m.theme.Dim.Render("nothing playing"),
			m.theme.Dim.Render("press enter on a track, or " + firstKey(m.keys.Find.Keys()) + " to search"),
		}
		return strings.Join(lines, "\n")
	}

	lines := []string{
		m.theme.Title.Render(st.Track.Title),
		m.theme.Artist.Render(st.Track.Artist),
		m.theme.Album.Render(st.Track.Album),
	}
	if s, ok := songOf(st.Track); ok {
		detail := []string{}
		if s.Year > 0 {
			detail = append(detail, strconv.Itoa(s.Year))
		}
		if s.Genre != "" {
			detail = append(detail, s.Genre)
		}
		if s.Suffix != "" {
			codec := strings.ToUpper(s.Suffix)
			if m.cfg.Audio.Format != "" && m.cfg.Audio.Format != "raw" {
				codec += " → " + strings.ToUpper(m.cfg.Audio.Format)
			}
			detail = append(detail, codec)
		}
		if s.BitRate > 0 {
			detail = append(detail, strconv.Itoa(s.BitRate)+" kbps")
		}
		if len(detail) > 0 {
			lines = append(lines, "", m.theme.Dim.Render(strings.Join(detail, " · ")))
		}
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return strings.Join(lines, "\n")
}

// progressView is the seek bar row. It updates every tick, so it must not
// share any line with the cover art.
func (m *Model) progressView() string {
	st := m.engine.Status()

	icon := "⏹"
	switch st.State {
	case audio.StatePlaying:
		icon = "▶"
	case audio.StatePaused:
		icon = "⏸"
	}

	total := 0.0
	if st.Track != nil {
		total = st.Track.Duration.Seconds()
	}
	frac := 0.0
	if total > 0 {
		frac = st.Position.Seconds() / total
	}

	left := fmt.Sprintf("%s %s", m.theme.Bar.Render(icon), clock(st.Position))
	right := clock(0)
	if st.Track != nil {
		right = clock(st.Track.Duration)
	}
	if st.Track != nil && st.QueueLen > 0 {
		right += m.theme.Dim.Render(fmt.Sprintf("  %d/%d", st.Index+1, st.QueueLen))
	}

	vol := m.theme.Dim.Render("vol ") + meter(m.theme, st.Volume, 6)
	if st.Muted {
		vol = m.theme.Dim.Render("muted ") + meter(m.theme, 0, 6)
	}
	rep := ""
	if st.Repeat != audio.RepeatOff {
		rep = m.theme.Dim.Render("  ↻ " + st.Repeat.String())
	}

	tail := "  " + right + "   " + vol + rep
	barW := m.width - lipgloss.Width(left) - lipgloss.Width(tail) - 2
	if barW < 4 {
		return padExact(left+tail, m.width)
	}
	return padExact(left+" "+progressBar(m.theme, frac, barW)+tail, m.width)
}

// statusView is the bottom line: transient messages, errors and hints.
func (m *Model) statusView() string {
	switch {
	case m.err != nil:
		return padExact(m.theme.ErrorMsg.Render("✗ "+truncate(m.err.Error(), m.width-2)), m.width)
	case m.status != "":
		return padExact(m.theme.Status.Render(m.status), m.width)
	}

	pos, total := m.lists[m.view].position()
	hint := fmt.Sprintf("%s find · %s filter · %s queue · %s help · %s quit",
		firstKey(m.keys.Find.Keys()),
		firstKey(m.keys.Filter.Keys()),
		firstKey(m.keys.AddQueue.Keys()),
		firstKey(m.keys.Help.Keys()),
		firstKey(m.keys.Quit.Keys()),
	)
	right := ""
	if total > 0 {
		right = fmt.Sprintf("%d/%d", pos, total)
	}
	gap := m.width - lipgloss.Width(hint) - lipgloss.Width(right)
	if gap < 1 {
		return padExact(m.theme.Dim.Render(hint), m.width)
	}
	return m.theme.Dim.Render(hint) + strings.Repeat(" ", gap) + m.theme.Dim.Render(right)
}

// helpView renders the full key reference.
func (m *Model) helpView() string {
	var b strings.Builder
	b.WriteString(m.theme.Header.Render("tunetty — keys"))
	b.WriteString("\n\n")

	for _, sec := range m.keys.HelpSections() {
		b.WriteString(m.theme.Title.Render(sec.Title))
		b.WriteByte('\n')
		for _, e := range sec.Entries {
			b.WriteString("  " + m.theme.Artist.Render(padExact(e.keys, 22)) + m.theme.Dim.Render(e.desc) + "\n")
		}
		b.WriteByte('\n')
	}

	if len(m.tmuxHelp) > 0 {
		b.WriteString(m.theme.Title.Render("tmux"))
		b.WriteByte('\n')
		for _, e := range m.tmuxHelp {
			b.WriteString("  " + m.theme.Artist.Render(padExact(e.keys, 22)) + m.theme.Dim.Render(e.desc) + "\n")
		}
		b.WriteByte('\n')
	}

	b.WriteString(m.theme.Title.Render("Playback"))
	b.WriteByte('\n')
	b.WriteString("  " + m.theme.Dim.Render("output backend: "+m.engine.Backend()) + "\n")
	b.WriteString("  " + m.theme.Dim.Render("stream format:  "+m.cfg.Audio.Format) + "\n")
	b.WriteString("  " + m.theme.Dim.Render("cover protocol: "+string(m.renderer.Protocol())) + "\n")
	if m.caps.InTmux {
		state := "on"
		if !m.caps.PassthroughReady {
			state = "off — run 'tmux set -p allow-passthrough on'"
		}
		b.WriteString("  " + m.theme.Dim.Render("tmux passthrough: "+state) + "\n")
	}
	b.WriteString("\n" + m.theme.Dim.Render("press any key to return"))
	return b.String()
}

// padBlock pads a multi-line block to exactly w columns and h rows.
func padBlock(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range h {
		if i < len(lines) {
			out[i] = padExact(lines[i], w)
			continue
		}
		out[i] = strings.Repeat(" ", w)
	}
	return strings.Join(out, "\n")
}

func firstKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	if keys[0] == " " {
		return "space"
	}
	return keys[0]
}
