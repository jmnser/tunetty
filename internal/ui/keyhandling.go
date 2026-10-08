package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jmnser/tunetty/internal/audio"
	"github.com/jmnser/tunetty/internal/subsonic"
)

// handleKey dispatches a key press to the active mode.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeFinder:
		return m.handleFinderKey(msg)
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeHelp:
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		m.mode = modeBrowse
		return m, nil
	}
	return m.handleBrowseKey(msg)
}

// handleBrowseKey handles the main interface. It is split by concern so each
// group of bindings stays readable: the first handler to claim the key wins.
func (m *Model) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	for _, h := range []func(tea.KeyMsg) (tea.Cmd, bool){
		m.navigationKey,
		m.playbackKey,
		m.queueKey,
		m.commandKey,
	} {
		if cmd, handled := h(msg); handled {
			return m, cmd
		}
	}
	return m, nil
}

// navigationKey moves the cursor and switches views.
func (m *Model) navigationKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	l := m.lists[m.view]
	switch {
	case key.Matches(msg, m.keys.Up):
		l.move(-1)
	case key.Matches(msg, m.keys.Down):
		l.move(1)
	case key.Matches(msg, m.keys.PageUp):
		l.move(-l.height)
	case key.Matches(msg, m.keys.PageDown):
		l.move(l.height)
	case key.Matches(msg, m.keys.Home):
		l.moveTo(0)
	case key.Matches(msg, m.keys.End):
		l.moveTo(len(l.items) - 1)
	case key.Matches(msg, m.keys.Tab):
		return m.switchView((m.view + 1) % view(len(viewNames))), true
	case key.Matches(msg, m.keys.ShiftTab):
		return m.switchView((m.view + view(len(viewNames)) - 1) % view(len(viewNames))), true
	case key.Matches(msg, m.keys.Queue):
		return m.switchView(viewQueue), true
	case key.Matches(msg, m.keys.Enter):
		return m.activate(), true
	case key.Matches(msg, m.keys.Back):
		m.goBack()
		return nil, true
	default:
		return nil, false
	}
	return tea.Batch(m.previewCover(), m.moreSongs()), true
}

// playbackKey drives the transport controls.
func (m *Model) playbackKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.PlayPause):
		m.engine.TogglePause()
	case key.Matches(msg, m.keys.Next):
		m.engine.Next()
	case key.Matches(msg, m.keys.Prev):
		m.engine.Prev()
	case key.Matches(msg, m.keys.Stop):
		m.engine.Stop()
	case key.Matches(msg, m.keys.SeekFwd):
		m.seekBy(m.cfg.UI.SeekStep.D())
	case key.Matches(msg, m.keys.SeekBack):
		m.seekBy(-m.cfg.UI.SeekStep.D())
	case key.Matches(msg, m.keys.VolumeUp):
		m.setStatus(pctStatus("volume", m.engine.AdjustVolume(m.cfg.UI.VolumeStep)))
	case key.Matches(msg, m.keys.VolumeDown):
		m.setStatus(pctStatus("volume", m.engine.AdjustVolume(-m.cfg.UI.VolumeStep)))
	case key.Matches(msg, m.keys.Mute):
		if m.engine.ToggleMute() {
			m.setStatus("muted")
		} else {
			m.setStatus("unmuted")
		}
	case key.Matches(msg, m.keys.Repeat):
		m.setStatus("repeat " + m.engine.CycleRepeat().String())
	default:
		return nil, false
	}
	return nil, true
}

// queueKey edits the playback queue.
func (m *Model) queueKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.AddQueue):
		return m.enqueueSelection(false), true
	case key.Matches(msg, m.keys.PlayNext):
		return m.enqueueSelection(true), true
	case key.Matches(msg, m.keys.Shuffle):
		m.shuffleQueue()
	case key.Matches(msg, m.keys.ShufflePlay):
		return m.shufflePlay(), true
	case key.Matches(msg, m.keys.Remove):
		if it, ok := m.lists[viewQueue].selected(); ok && m.view == viewQueue {
			m.engine.RemoveAt(it.QueueIndex)
			m.refreshList()
		}
	case key.Matches(msg, m.keys.ClearQueue):
		m.engine.Clear()
		m.setStatus("queue cleared")
	default:
		return nil, false
	}
	return nil, true
}

// commandKey covers the remaining global actions.
func (m *Model) commandKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit, true
	case key.Matches(msg, m.keys.Help):
		m.mode = modeHelp
	case key.Matches(msg, m.keys.Star):
		return m.toggleStar(), true
	case key.Matches(msg, m.keys.Refresh):
		return m.refreshCurrent(), true
	case key.Matches(msg, m.keys.Find):
		m.mode = modeFinder
		m.finderQ = ""
		m.finderCur = 0
		m.index.setRemote(nil)
		m.runFinder()
	case key.Matches(msg, m.keys.Filter):
		m.mode = modeFilter
		m.filterQ = ""
		m.filterBase = append([]listItem(nil), m.lists[m.view].items...)
	default:
		return nil, false
	}
	return nil, true
}

// ----------------------------------------------------------------- finder

func (m *Model) handleFinderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.closeFinder()
		return m, nil
	case tea.KeyEnter:
		cmd := m.openFinderHit()
		m.closeFinder()
		return m, cmd
	case tea.KeyUp, tea.KeyCtrlP:
		m.finderCur = clampTop(m.finderCur-1, max(0, len(m.finderHits)-1))
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.finderCur = clampTop(m.finderCur+1, max(0, len(m.finderHits)-1))
		return m, nil
	case tea.KeyBackspace:
		if m.finderQ != "" {
			r := []rune(m.finderQ)
			m.finderQ = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		m.finderQ = ""
	case tea.KeyRunes, tea.KeySpace:
		// KeySpace carries its rune too.
		m.finderQ += string(msg.Runes)
	default:
		return m, nil
	}

	m.finderSeq++
	m.finderCur = 0
	m.runFinder()

	// Songs are not held locally, so a server search backs the local
	// catalogue once the query is specific enough to be worth a round trip.
	// It is debounced; finderDebounceMsg starts it if nothing was typed since.
	if len([]rune(strings.TrimSpace(m.finderQ))) >= 2 {
		seq := m.finderSeq
		return m, tea.Tick(finderDebounce, func(time.Time) tea.Msg { return finderDebounceMsg{seq: seq} })
	}
	m.index.setRemote(nil)
	return m, nil
}

// closeFinder leaves the finder and discards its server results, including
// any still in flight.
func (m *Model) closeFinder() {
	m.mode = modeBrowse
	m.finderSeq++
	m.index.setRemote(nil)
}

// runFinder recomputes the ranked hit list.
func (m *Model) runFinder() {
	m.finderHits = m.index.search(m.finderQ, m.cfg.UI.FuzzyLimit)
	if m.finderCur >= len(m.finderHits) {
		m.finderCur = max(0, len(m.finderHits)-1)
	}
}

// openFinderHit acts on the highlighted finder result.
func (m *Model) openFinderHit() tea.Cmd {
	if m.finderCur < 0 || m.finderCur >= len(m.finderHits) {
		return nil
	}
	hit := m.finderHits[m.finderCur]

	switch hit.Kind {
	case entryArtist:
		cmd := m.switchView(viewArtists)
		m.loading++
		return tea.Batch(cmd, m.loadArtist(hit.ID, m.beginNav()))
	case entryAlbum:
		cmd := m.switchView(viewAlbums)
		m.loading++
		return tea.Batch(cmd, m.loadAlbum(hit.ID, m.beginNav()))
	case entryPlaylist:
		cmd := m.switchView(viewPlaylists)
		m.loading++
		return tea.Batch(cmd, m.loadPlaylist(hit.ID, m.beginNav()))
	case entrySong:
		tracks := toTracks(m.client, m.cfg.Audio, []subsonic.Song{hit.Song})
		m.engine.SetQueue(tracks, 0)
		m.setStatus("playing " + hit.Display)
		return m.ensureCover(coverIDOf(hit.Song))
	}
	return nil
}

// ----------------------------------------------------------------- filter

func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeBrowse
		m.lists[m.view].setItems(m.filterBase)
		m.filterQ = ""
		return m, nil
	case tea.KeyEnter:
		m.mode = modeBrowse
		return m, nil
	case tea.KeyBackspace:
		if m.filterQ != "" {
			r := []rune(m.filterQ)
			m.filterQ = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		m.filterQ = ""
	case tea.KeyUp:
		m.lists[m.view].move(-1)
		return m, nil
	case tea.KeyDown:
		m.lists[m.view].move(1)
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		m.filterQ += string(msg.Runes)
	default:
		return m, nil
	}
	m.applyFilter()
	return m, nil
}

// applyFilter narrows the current list to fuzzy matches of the filter query.
func (m *Model) applyFilter() {
	l := m.lists[m.view]
	q := strings.TrimSpace(m.filterQ)
	if q == "" {
		l.setItems(m.filterBase)
		l.moveTo(0)
		return
	}
	src := itemSource(m.filterBase)
	results := fuzzyFind(q, src)
	out := make([]listItem, 0, len(results))
	for _, i := range results {
		out = append(out, m.filterBase[i])
	}
	l.setItems(out)
	l.moveTo(0)
}

// ------------------------------------------------------------- navigation

// switchView changes the active tab, loading its data on first visit.
func (m *Model) switchView(v view) tea.Cmd {
	m.view = v
	m.level = levelRoot
	m.crumbArtist, m.crumbAlbum, m.crumbList = nil, nil, nil
	m.beginNav()
	m.refreshList()

	switch v {
	case viewStarred:
		if m.starred == nil {
			m.loading++
			return m.loadStarred()
		}
	case viewSongs:
		if m.songs == nil && !m.songsBusy {
			return m.requestSongPage(0)
		}
	case viewAlbums:
		if len(m.albums) == 0 {
			m.loading++
			return m.loadAlbums(subsonic.AlbumsNewest, m.cfg.UI.PageSize)
		}
	case viewPlaylists:
		if len(m.playlists) == 0 {
			m.loading++
			return m.loadPlaylists()
		}
	case viewArtists:
		if len(m.artists) == 0 {
			m.loading++
			return m.loadArtists()
		}
	}
	return m.previewCover()
}

// activate opens the selected row: drill down, or play it.
func (m *Model) activate() tea.Cmd {
	it, ok := m.lists[m.view].selected()
	if !ok {
		return nil
	}

	switch data := it.Data.(type) {
	case subsonic.Artist:
		m.loading++
		return m.loadArtist(data.ID, m.beginNav())

	case subsonic.Album:
		m.loading++
		return m.loadAlbum(data.ID, m.beginNav())

	case subsonic.Playlist:
		m.loading++
		return m.loadPlaylist(data.ID, m.beginNav())

	case subsonic.Song:
		if m.view == viewQueue {
			m.engine.PlayIndex(it.QueueIndex)
			return nil
		}
		// Playing a track queues its whole context, which is what makes the
		// gapless engine useful: the rest of the album follows seamlessly.
		songs := m.currentSongs()
		start := indexOfSong(songs, data.ID)
		m.engine.SetQueue(toTracks(m.client, m.cfg.Audio, songs), start)
		return m.ensureCover(coverIDOf(data))
	}
	return nil
}

// goBack pops one browse level.
func (m *Model) goBack() {
	switch m.level {
	case levelTracks:
		m.crumbAlbum, m.crumbList = nil, nil
		m.level = levelRoot
		if m.crumbArtist != nil {
			m.level = levelAlbums
		}
	case levelAlbums:
		m.level = levelRoot
		m.crumbArtist = nil
	default:
		return
	}
	m.beginNav()
	m.refreshList()
}

// refreshCurrent reloads the data behind the current view.
func (m *Model) refreshCurrent() tea.Cmd {
	m.loading++
	switch {
	case m.level == levelTracks && m.crumbAlbum != nil:
		return m.loadAlbum(m.crumbAlbum.ID, m.beginNav())
	case m.level == levelTracks && m.crumbList != nil:
		return m.loadPlaylist(m.crumbList.ID, m.beginNav())
	case m.level == levelAlbums && m.crumbArtist != nil:
		return m.loadArtist(m.crumbArtist.ID, m.beginNav())
	}
	switch m.view {
	case viewArtists:
		return m.loadArtists()
	case viewAlbums:
		return m.loadAlbums(subsonic.AlbumsNewest, m.cfg.UI.PageSize)
	case viewPlaylists:
		return m.loadPlaylists()
	case viewSongs:
		m.loading--
		m.songs, m.songsDone = nil, false
		m.refreshList()
		return m.requestSongPage(0)
	case viewStarred:
		return m.loadStarred()
	}
	m.loading--
	return nil
}

// previewCover loads artwork for the highlighted album so browsing shows the
// cover before anything is played.
func (m *Model) previewCover() tea.Cmd {
	if m.engine.State() != audio.StateStopped {
		return nil // the playing track owns the cover slot
	}
	it, ok := m.lists[m.view].selected()
	if !ok {
		return nil
	}
	switch d := it.Data.(type) {
	case subsonic.Album:
		return m.ensureCover(d.CoverArt)
	case subsonic.Song:
		return m.ensureCover(coverIDOf(d))
	case subsonic.Artist:
		return m.ensureCover(d.CoverArt)
	}
	return nil
}

// ---------------------------------------------------------------- actions

// enqueueSelection appends or inserts the selection into the queue.
func (m *Model) enqueueSelection(next bool) tea.Cmd {
	it, ok := m.lists[m.view].selected()
	if !ok {
		return nil
	}

	switch data := it.Data.(type) {
	case subsonic.Song:
		tracks := toTracks(m.client, m.cfg.Audio, []subsonic.Song{data})
		if next {
			m.engine.InsertNext(tracks...)
			m.setStatus("playing next: " + displayTitle(data))
		} else {
			m.engine.Enqueue(tracks...)
			m.setStatus("queued " + displayTitle(data))
		}
		return nil

	case subsonic.Album:
		// The album's tracks are not loaded until it is opened, so queueing
		// from the album list fetches them first.
		return m.fetchAndQueue(data.Name, next, func(ctx context.Context) ([]subsonic.Song, error) {
			al, err := m.client.Album(ctx, data.ID)
			if err != nil {
				return nil, err
			}
			return al.Song, nil
		})

	case subsonic.Playlist:
		return m.fetchAndQueue(data.Name, next, func(ctx context.Context) ([]subsonic.Song, error) {
			pl, err := m.client.Playlist(ctx, data.ID)
			if err != nil {
				return nil, err
			}
			return pl.Entry, nil
		})
	}
	return nil
}

// fetchAndQueue loads a collection in the background and queues it.
func (m *Model) fetchAndQueue(label string, next bool, load func(context.Context) ([]subsonic.Song, error)) tea.Cmd {
	timeout := m.apiTimeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		songs, err := load(ctx)
		if err != nil {
			return errMsg{err: err}
		}
		return enqueueMsg{songs: songs, label: label, next: next}
	}
}

// toggleStar stars or unstars the highlighted item, flipping the state the
// row displays.
func (m *Model) toggleStar() tea.Cmd {
	it, ok := m.lists[m.view].selected()
	if !ok {
		return nil
	}
	switch d := it.Data.(type) {
	case subsonic.Song:
		return m.setStar("song", d.ID, !it.Starred)
	case subsonic.Album:
		return m.setStar("album", d.ID, !it.Starred)
	case subsonic.Artist:
		return m.setStar("artist", d.ID, !it.Starred)
	}
	return nil
}

func indexOfSong(songs []subsonic.Song, id string) int {
	for i, s := range songs {
		if s.ID == id {
			return i
		}
	}
	return 0
}

func pctStatus(label string, v float64) string {
	return label + " " + strconv.Itoa(int(v*100+0.5)) + "%"
}

// itemSource adapts list rows to the fuzzy matcher.
type itemSource []listItem

func (s itemSource) String(i int) string {
	if s[i].Secondary != "" {
		return s[i].Primary + " " + s[i].Secondary
	}
	return s[i].Primary
}
func (s itemSource) Len() int { return len(s) }
