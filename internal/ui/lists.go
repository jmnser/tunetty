package ui

import (
	"fmt"
	"strconv"

	"github.com/sahilm/fuzzy"

	"github.com/jmnser/tunetty/internal/subsonic"
)

// refreshList rebuilds the rows of the active view from cached data.
func (m *Model) refreshList() {
	l := m.lists[m.view]
	l.setItems(m.buildItems())
	if m.mode == modeFilter {
		m.filterBase = append([]listItem(nil), l.items...)
		m.applyFilter()
	}
}

func (m *Model) buildItems() []listItem {
	switch m.view {
	case viewArtists:
		switch m.level {
		case levelAlbums:
			if m.crumbArtist != nil {
				return append([]listItem{allSongsItem(*m.crumbArtist)}, m.albumItems(m.crumbArtist.Album)...)
			}
		case levelTracks:
			if m.crumbAlbum != nil {
				return m.songItems(m.crumbAlbum.Song, true)
			}
			if m.crumbArtist != nil {
				return m.songItems(m.artistSongs, false)
			}
		}
		return m.artistItems(m.artists)

	case viewAlbums:
		if m.level == levelTracks && m.crumbAlbum != nil {
			return m.songItems(m.crumbAlbum.Song, true)
		}
		return m.albumItems(m.albums)

	case viewSongs:
		return m.songItems(m.songs, false)

	case viewPlaylists:
		if m.level == levelTracks && m.crumbList != nil {
			return m.songItems(m.crumbList.Entry, false)
		}
		return playlistItems(m.playlists)

	case viewStarred:
		if m.level == levelTracks && m.crumbAlbum != nil {
			return m.songItems(m.crumbAlbum.Song, true)
		}
		if m.starred == nil {
			return nil
		}
		items := make([]listItem, 0, len(m.starred.Album)+len(m.starred.Song))
		items = append(items, m.albumItems(m.starred.Album)...)
		items = append(items, m.songItems(m.starred.Song, false)...)
		return items

	case viewQueue:
		return m.queueItems()
	}
	return nil
}

func (m *Model) artistItems(artists []subsonic.Artist) []listItem {
	out := make([]listItem, 0, len(artists))
	for _, a := range artists {
		out = append(out, listItem{
			Primary:   a.Name,
			Secondary: plural(a.AlbumCount, "album"),
			Starred:   m.isStarred("artist", a.ID, a.Starred != ""),
			Data:      a,
		})
	}
	return out
}

// allSongsLabel names the entry that opens every song of an artist.
const allSongsLabel = "All songs"

// allSongs is the row data of an artist's "All songs" entry.
type allSongs struct{ artist subsonic.Artist }

// allSongsItem is the first row of an artist's album list.
func allSongsItem(a subsonic.Artist) listItem {
	n := 0
	for _, al := range a.Album {
		n += al.SongCount
	}
	return listItem{
		Primary:   allSongsLabel,
		Secondary: plural(n, "track"),
		Data:      allSongs{artist: a},
	}
}

func (m *Model) albumItems(albums []subsonic.Album) []listItem {
	out := make([]listItem, 0, len(albums))
	for _, a := range albums {
		secondary := a.Artist
		if a.Year > 0 {
			secondary = fmt.Sprintf("%s · %d", a.Artist, a.Year)
		}
		out = append(out, listItem{
			Primary:   a.Name,
			Secondary: secondary,
			Starred:   m.isStarred("album", a.ID, a.Starred != ""),
			Data:      a,
		})
	}
	return out
}

func playlistItems(playlists []subsonic.Playlist) []listItem {
	out := make([]listItem, 0, len(playlists))
	for _, p := range playlists {
		out = append(out, listItem{
			Primary:   p.Name,
			Secondary: fmt.Sprintf("%s · %s", plural(p.SongCount, "track"), p.Duration),
			Data:      p,
		})
	}
	return out
}

// songItems renders track rows. withTrackNo numbers them, which suits an
// album listing but not a starred or search result list.
func (m *Model) songItems(songs []subsonic.Song, withTrackNo bool) []listItem {
	playingID := ""
	if st := m.engine.Status(); st.Track != nil {
		playingID = st.Track.ID
	}

	out := make([]listItem, 0, len(songs))
	for _, s := range songs {
		prefix := ""
		if withTrackNo && s.Track > 0 {
			prefix = fmt.Sprintf("%2d.", s.Track)
		}
		primary := displayTitle(s)
		secondary := s.Duration.String()
		if !withTrackNo && s.Artist != "" {
			secondary = s.Artist + "  " + secondary
		}
		out = append(out, listItem{
			Prefix:    prefix,
			Primary:   primary,
			Secondary: secondary,
			Starred:   m.isStarred("song", s.ID, s.IsStarred()),
			Playing:   s.ID == playingID,
			Data:      s,
		})
	}
	return out
}

// queueItems renders the playback queue, marking the audible entry.
func (m *Model) queueItems() []listItem {
	q := m.engine.Queue()
	st := m.engine.Status()

	out := make([]listItem, 0, len(q))
	for i, t := range q {
		s, _ := songOf(&q[i])
		secondary := t.Artist
		if t.Album != "" {
			secondary += " · " + t.Album
		}
		secondary += "  " + clock(t.Duration)
		out = append(out, listItem{
			Prefix:     strconv.Itoa(i+1) + ".",
			Primary:    t.Title,
			Secondary:  secondary,
			Playing:    i == st.Index,
			Starred:    m.isStarred("song", s.ID, s.IsStarred()),
			Data:       s,
			QueueIndex: i,
		})
	}
	return out
}

// reindex rebuilds the fuzzy finder's local catalogue.
func (m *Model) reindex() {
	m.index.setLocal(m.artists, m.albums, m.playlists)
}

// fuzzyFind returns the source indexes matching pattern, best first.
func fuzzyFind(pattern string, src fuzzy.Source) []int {
	results := fuzzy.FindFrom(pattern, src)
	out := make([]int, 0, len(results))
	for _, r := range results {
		out = append(out, r.Index)
	}
	return out
}
