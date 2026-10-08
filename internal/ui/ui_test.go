package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jmnser/tunetty/internal/art"
	"github.com/jmnser/tunetty/internal/audio"
	"github.com/jmnser/tunetty/internal/config"
	"github.com/jmnser/tunetty/internal/subsonic"
)

const (
	testArtist  = "Radiohead"
	testAlbum   = "In Rainbows"
	testSong    = "Nude"
	testAlbumID = "al1"
	sample      = "hello"
)

// TestMain points the command audio backend at a harmless program once, so
// tests never touch the sound hardware and can still run in parallel.
func TestMain(m *testing.M) {
	if err := os.Setenv("TUNETTY_AUDIO_COMMAND", "cat"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// newTestModel builds a sized model whose client points at an address that
// never resolves; these tests exercise the UI state, not network calls.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	e, err := audio.NewEngine(audio.Config{Backend: "command", BufferDuration: time.Second})
	if err != nil {
		t.Skipf("no usable test audio backend: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	cl, err := subsonic.New(subsonic.Options{BaseURL: "https://example.invalid", Username: "u", Password: "p"})
	require.NoError(t, err)

	cfg := config.Default()
	cfg.UI.Theme = "dark"
	m := New(Options{
		Client: cl, Engine: e, Config: cfg, Version: "test",
		Caps: art.Capabilities{Protocol: art.ProtocolHalfBlock, CellWidth: 10, CellHeight: 20, TrueColor: true},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.artists = []subsonic.Artist{{ID: "a1", Name: testArtist}}
	m.albums = []subsonic.Album{{ID: testAlbumID, Name: testAlbum, Artist: testArtist}}
	m.reindex()
	m.refreshList()
	return m
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"fits", sample, 5, sample},
		{"cut", sample, 4, "hel…"},
		{"one column", sample, 1, "…"},
		{"zero", sample, 0, ""},
		{"wide runes", "日本語テキスト", 5, "日本…"},
		// Escape sequences take no columns and must survive intact.
		{"styled", "\x1b[1mhello world\x1b[0m", 6, "\x1b[1mhello…\x1b[0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tc.in, tc.width)
			assert.Equal(t, ansi.Strip(tc.want), ansi.Strip(got))
			assert.Equal(t, lipgloss.Width(tc.want), lipgloss.Width(got))
			if strings.Contains(tc.in, "\x1b[1m") {
				assert.True(t, strings.HasPrefix(got, "\x1b[1m"), "escape sequence lost: %q", got)
			}
		})
	}
}

func TestClock(t *testing.T) {
	t.Parallel()
	cases := map[time.Duration]string{
		0:                        "0:00",
		-5 * time.Second:         "0:00",
		59900 * time.Millisecond: "0:59",
		3661 * time.Second:       "1:01:01",
	}
	for in, want := range cases {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, clock(in))
		})
	}
}

// Every widget in a frame must be exactly as wide as it claims, or the layout
// drifts and terminal graphics land in the wrong cells.
func TestWidgetWidths(t *testing.T) {
	t.Parallel()
	theme := NewTheme("dark", "")
	cases := []struct {
		name string
		got  string
		want int
	}{
		{"padExact short", padExact("short", 20), 20},
		{"padExact long", padExact("a much longer string than the target", 8), 8},
		{"padExact styled", padExact(theme.RowSel.Render("styled text"), 4), 4},
		{"padExact wide", padExact("日本語", 5), 5},
		{"progressBar overfull", progressBar(theme, 2, 10), 10},
		{"progressBar half", progressBar(theme, 0.5, 40), 40},
		{"joinColumns", joinColumns("a\nb\nc", "one", 5, 2, 8), 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for line := range strings.SplitSeq(tc.got, "\n") {
				assert.Equal(t, tc.want, lipgloss.Width(line))
			}
		})
	}
}

func TestListScrollKeepsCursorVisible(t *testing.T) {
	t.Parallel()
	l := &itemList{}
	l.setItems(make([]listItem, 100))
	l.setSize(40, 10)

	cases := []struct{ to, cursor, offset int }{
		{50, 50, 43},
		{0, 0, 0},
		{99, 99, 90},
		{1000, 99, 90},
		{-1000, 0, 0},
	}
	for _, tc := range cases {
		l.moveTo(tc.to)
		assert.Equal(t, tc.cursor, l.cursor, "moveTo(%d)", tc.to)
		assert.Equal(t, tc.offset, l.offset, "moveTo(%d)", tc.to)
	}
}

func TestIndexSearch(t *testing.T) {
	t.Parallel()
	ix := &index{}
	ix.setLocal(
		[]subsonic.Artist{{ID: "a1", Name: testArtist}},
		[]subsonic.Album{{ID: testAlbumID, Name: testAlbum, Artist: testArtist}},
		[]subsonic.Playlist{{ID: "p1", Name: "Late night"}},
	)
	// Server results add songs and albums beyond the local page, while
	// entries already held locally are not listed twice.
	ix.setRemote(&subsonic.SearchResult{
		Album: []subsonic.Album{
			{ID: testAlbumID, Name: testAlbum, Artist: testArtist},
			{ID: "al9", Name: "Amnesiac", Artist: testArtist},
		},
		Song: []subsonic.Song{{ID: "s1", Title: testSong, Artist: testArtist, Album: testAlbum}},
	})

	cases := []struct {
		query string
		ids   []string
	}{
		{"rainbows", []string{testAlbumID, "s1"}}, // the local album only once
		{"amnesiac", []string{"al9"}},
		{"nude", []string{"s1"}},
		{"zzzzzzzz", nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			var ids []string
			for _, h := range ix.search(tc.query, 10) {
				ids = append(ids, h.ID)
			}
			assert.Equal(t, tc.ids, ids)
		})
	}
}

func TestCoverCache(t *testing.T) {
	t.Parallel()
	c := newCoverCache(2)
	c.put("a", &art.Image{})
	c.put("b", &art.Image{})
	_, _ = c.get("a") // makes "b" the eviction candidate
	c.put("c", &art.Image{})
	c.markMissing("d")

	for id, cached := range map[string]bool{"a": true, "b": false, "c": true} {
		_, ok := c.get(id)
		assert.Equal(t, cached, ok, id)
	}
	assert.True(t, c.missing("d"))
	assert.False(t, c.missing("a"))
}

// The assembled frame must be exactly as tall as the terminal and never wider,
// otherwise bubbletea scrolls the alt screen and the cover art escapes land in
// the wrong cells.
func TestViewFillsTheTerminal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		w, h int
		mode mode
	}{
		{80, 24, modeBrowse},
		{200, 60, modeBrowse},
		{50, 14, modeBrowse}, // too small for artwork: the band must collapse
		{100, 30, modeFinder},
		{100, 30, modeFilter},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d mode %d", tc.w, tc.h, tc.mode), func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t)
			m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
			m.mode = tc.mode
			lines := strings.Split(m.View(), "\n")
			assert.Len(t, lines, tc.h)
			for i, l := range lines {
				assert.LessOrEqual(t, lipgloss.Width(l), tc.w, "line %d", i)
			}
		})
	}
}

func TestSpaceIsTypedOnce(t *testing.T) {
	t.Parallel()
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	for _, md := range []mode{modeFinder, modeFilter} {
		t.Run(fmt.Sprintf("mode %d", md), func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t)
			m.mode = md
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
			m.Update(space)
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
			assert.Equal(t, "a b", m.finderQ+m.filterQ)
		})
	}
}

func TestStaleResponsesAreDropped(t *testing.T) {
	t.Parallel()
	artist := &subsonic.Artist{ID: "a1", Name: testArtist}

	m := newTestModel(t)
	stale := m.beginNav()
	m.switchView(viewArtists) // navigating invalidates the pending request
	m.Update(artistMsg{artist: artist, seq: stale})
	assert.Equal(t, levelRoot, m.level)
	assert.Empty(t, m.crumbs())

	m.Update(artistMsg{artist: artist, seq: m.beginNav()})
	assert.Equal(t, levelAlbums, m.level)
	assert.Equal(t, []string{testArtist}, m.crumbs())

	// A server search answering a closed finder must not resurface later.
	m.mode = modeFinder
	seq := m.finderSeq
	m.closeFinder()
	m.Update(searchMsg{seq: seq, result: &subsonic.SearchResult{Song: []subsonic.Song{{ID: "s1"}}}})
	assert.Empty(t, m.index.remote)
}

func TestErrorsOnlyFinishCountedLoads(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	m.loading = 1
	m.Update(errMsg{err: errors.New("search failed")})
	assert.Equal(t, 1, m.loading)
	m.Update(errMsg{err: errors.New("load failed"), counted: true})
	assert.Equal(t, 0, m.loading)
}

func TestStarToggleUpdatesRows(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	m.Update(starMsg{kind: "artist", id: "a1", on: true})
	it, ok := m.lists[viewArtists].selected()
	require.True(t, ok)
	assert.True(t, it.Starred)
}

func TestQueueRemoveUsesQueuePosition(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	titles := []string{"one", "two", "three"}
	songs := make([]subsonic.Song, 0, len(titles))
	for _, title := range titles {
		songs = append(songs, subsonic.Song{ID: title, Title: title})
	}
	m.engine.ReplaceQueue(toTracks(m.client, m.cfg.Audio, songs))
	m.switchView(viewQueue)

	// Filtering leaves "three" as the first row.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("three")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	q := m.engine.Queue()
	left := make([]string, 0, len(q))
	for _, tr := range q {
		left = append(left, tr.ID)
	}
	assert.Equal(t, []string{"one", "two"}, left)
}

func TestHiddenArtForgetsStaleCover(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	m.covers.put("old", &art.Image{})
	m.ensureCover("old")
	m.artCols = 0 // terminal too small for artwork
	assert.Nil(t, m.ensureCover("new"))
	assert.Equal(t, "new", m.currentCID)
	assert.Nil(t, m.currentArt)
}
