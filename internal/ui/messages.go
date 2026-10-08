package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jmnser/tunetty/internal/art"
	"github.com/jmnser/tunetty/internal/audio"
	"github.com/jmnser/tunetty/internal/subsonic"
)

// Messages carrying loaded data back into the update loop.
type (
	artistsMsg   struct{ artists []subsonic.Artist }
	albumsMsg    struct{ albums []subsonic.Album }
	playlistsMsg struct{ playlists []subsonic.Playlist }
	starredMsg   struct{ starred *subsonic.Starred }
	// Drill-down results carry the navigation sequence they were requested
	// under; see Model.navSeq.
	artistMsg struct {
		artist *subsonic.Artist
		seq    int
	}
	albumMsg struct {
		album *subsonic.Album
		seq   int
	}
	playlistMsg struct {
		playlist *subsonic.Playlist
		seq      int
	}
	// finderDebounceMsg fires once typing in the finder has paused.
	finderDebounceMsg struct{ seq int }
	// searchMsg carries server results for the finder edit numbered seq.
	searchMsg struct {
		seq    int
		result *subsonic.SearchResult
	}
	// starMsg confirms a successful star or unstar.
	starMsg struct {
		kind, id string
		on       bool
	}
	coverMsg struct {
		id  string
		img *art.Image
		err error
	}
	// engineMsg forwards an audio engine event into the UI.
	engineMsg struct{ event audio.Event }
	// tickMsg drives the progress display.
	tickMsg time.Time
	// errMsg reports a failure to the status line. counted marks failures of
	// requests that incremented Model.loading.
	errMsg struct {
		err     error
		counted bool
	}
	// statusMsg shows a transient message.
	statusMsg struct{ text string }
	// enqueueMsg carries a collection fetched in the background so it can be
	// added to the queue on the UI goroutine.
	enqueueMsg struct {
		songs []subsonic.Song
		label string
		next  bool
	}
)

// finderDebounce is how long the finder waits after a keystroke before
// asking the server, so typing a word costs one request instead of one each.
const finderDebounce = 200 * time.Millisecond

// tick schedules the next progress refresh. Twice a second keeps the seek bar
// smooth without redrawing the whole frame constantly.
func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// listenEngine forwards one engine event per command invocation, which is how
// a channel is bridged into bubbletea's message loop.
func listenEngine(events <-chan audio.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return nil
		}
		return engineMsg{event: ev}
	}
}

// ---------------------------------------------------------------- loaders

func (m *Model) loadArtists() tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		artists, err := cl.Artists(ctx)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return artistsMsg{artists}
	}
}

func (m *Model) loadAlbums(kind subsonic.AlbumListType, size int) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		albums, err := cl.AlbumList(ctx, kind, size, 0)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return albumsMsg{albums}
	}
}

func (m *Model) loadArtist(id string, seq int) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		a, err := cl.Artist(ctx, id)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return artistMsg{artist: a, seq: seq}
	}
}

func (m *Model) loadAlbum(id string, seq int) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		a, err := cl.Album(ctx, id)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return albumMsg{album: a, seq: seq}
	}
}

func (m *Model) loadPlaylists() tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		p, err := cl.Playlists(ctx)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return playlistsMsg{p}
	}
}

func (m *Model) loadPlaylist(id string, seq int) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		p, err := cl.Playlist(ctx, id)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return playlistMsg{playlist: p, seq: seq}
	}
}

func (m *Model) loadStarred() tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		s, err := cl.Starred(ctx)
		if err != nil {
			return errMsg{err: err, counted: true}
		}
		return starredMsg{s}
	}
}

// searchRemote queries the server for the finder input. Artists and albums
// are requested too, because the local index only holds the first page of
// albums.
func (m *Model) searchRemote(query string, seq int) tea.Cmd {
	cl := m.client
	limit := m.cfg.UI.FuzzyLimit
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		res, err := cl.Search(ctx, query, subsonic.SearchOptions{
			ArtistCount: 20, AlbumCount: 40, SongCount: limit,
		})
		if err != nil {
			return errMsg{err: err}
		}
		return searchMsg{seq: seq, result: res}
	}
}

// loadCover fetches artwork sized for the display area.
func (m *Model) loadCover(id string, px int) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		img, err := fetchCover(ctx, cl, id, px)
		return coverMsg{id: id, img: img, err: err}
	}
}

// scrobble reports a play to the server.
func (m *Model) scrobble(id string, submission bool) tea.Cmd {
	if !m.cfg.Audio.Scrobble || id == "" {
		return nil
	}
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		// A failed scrobble must never interrupt playback, so the error is
		// deliberately dropped rather than surfaced.
		_ = cl.Scrobble(ctx, id, submission)
		return nil
	}
}

// setStar toggles a favourite on the server.
func (m *Model) setStar(kind, id string, on bool) tea.Cmd {
	cl := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.apiTimeout)
		defer cancel()
		var err error
		if on {
			err = cl.Star(ctx, kind, id)
		} else {
			err = cl.Unstar(ctx, kind, id)
		}
		if err != nil {
			return errMsg{err: err}
		}
		return starMsg{kind: kind, id: id, on: on}
	}
}
