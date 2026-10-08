package ui

import (
	"strconv"
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/jmnser/tunetty/internal/subsonic"
)

// entryKind distinguishes what a finder hit refers to.
type entryKind int

// Finder entry kinds.
const (
	entryArtist entryKind = iota
	entryAlbum
	entrySong
	entryPlaylist
)

func (k entryKind) label() string {
	switch k {
	case entryArtist:
		return "artist"
	case entryAlbum:
		return "album"
	case entrySong:
		return "song"
	default:
		return "playlist"
	}
}

// entry is one searchable item.
type entry struct {
	Kind entryKind
	ID   string
	// Text is what the fuzzy matcher scores against.
	Text string
	// Display and Detail are what the user sees.
	Display string
	Detail  string

	Artist   subsonic.Artist
	Album    subsonic.Album
	Song     subsonic.Song
	Playlist subsonic.Playlist
}

// index is the searchable catalogue. Artists, albums and playlists are held
// locally so typing is instant; songs are merged in from the server, which
// avoids downloading an entire library up front.
type index struct {
	local  []entry
	remote []entry
}

// setLocal replaces the locally held catalogue.
func (ix *index) setLocal(artists []subsonic.Artist, albums []subsonic.Album, playlists []subsonic.Playlist) {
	ix.local = catalogueEntries(artists, albums, playlists)
}

func (e entry) key() string { return e.Kind.label() + ":" + e.ID }

// catalogueEntries builds finder entries for artists, albums and playlists.
func catalogueEntries(artists []subsonic.Artist, albums []subsonic.Album, playlists []subsonic.Playlist) []entry {
	out := make([]entry, 0, len(artists)+len(albums)+len(playlists))
	for _, a := range artists {
		out = append(out, entry{
			Kind: entryArtist, ID: a.ID, Text: a.Name,
			Display: a.Name, Detail: plural(a.AlbumCount, "album"), Artist: a,
		})
	}
	for _, al := range albums {
		out = append(out, entry{
			Kind: entryAlbum, ID: al.ID, Text: al.Name + " " + al.Artist,
			Display: al.Name, Detail: al.Artist, Album: al,
		})
	}
	for _, p := range playlists {
		out = append(out, entry{
			Kind: entryPlaylist, ID: p.ID, Text: p.Name,
			Display: p.Name, Detail: plural(p.SongCount, "track"), Playlist: p,
		})
	}
	return out
}

// setRemote replaces the server-supplied hits for the current query. Artists
// and albums already in the local catalogue are skipped so they are not
// listed twice.
func (ix *index) setRemote(res *subsonic.SearchResult) {
	if res == nil {
		ix.remote = nil
		return
	}
	known := make(map[string]struct{}, len(ix.local))
	for _, e := range ix.local {
		known[e.key()] = struct{}{}
	}
	out := make([]entry, 0, len(res.Artist)+len(res.Album)+len(res.Song))
	for _, e := range catalogueEntries(res.Artist, res.Album, nil) {
		if _, dup := known[e.key()]; !dup {
			out = append(out, e)
		}
	}
	for _, s := range res.Song {
		detail := s.Artist
		if s.Album != "" {
			detail += " · " + s.Album
		}
		out = append(out, entry{
			Kind: entrySong, ID: s.ID, Text: displayTitle(s) + " " + s.Artist + " " + s.Album,
			Display: displayTitle(s), Detail: detail, Song: s,
		})
	}
	ix.remote = out
}

// match is a scored finder result.
type match struct {
	entry
	score   int
	indexes []int
}

// search ranks the catalogue against query, best first; ties keep catalogue
// order, local before remote. An empty query lists the catalogue unranked so
// the finder is useful before typing anything.
func (ix *index) search(query string, limit int) []match {
	all := make([]entry, 0, len(ix.local)+len(ix.remote))
	all = append(all, ix.local...)
	all = append(all, ix.remote...)

	query = strings.TrimSpace(query)
	if query == "" {
		out := make([]match, 0, min(limit, len(all)))
		for i := 0; i < len(all) && i < limit; i++ {
			out = append(out, match{entry: all[i]})
		}
		return out
	}

	src := entrySource(all)
	results := fuzzy.FindFrom(query, src)

	out := make([]match, 0, min(limit, len(results)))
	for i, r := range results {
		if i >= limit {
			break
		}
		out = append(out, match{entry: all[r.Index], score: r.Score, indexes: r.MatchedIndexes})
	}
	return out
}

// entrySource adapts entries to the fuzzy matcher's interface.
type entrySource []entry

func (s entrySource) String(i int) string { return s[i].Text }
func (s entrySource) Len() int            { return len(s) }

// highlight renders a matched string with the matching characters emphasised.
// The matcher reports byte offsets into entry.Text, whose prefix is the
// display string, so offsets past the display simply do not match anything.
func highlight(t Theme, s string, indexes []int, width int) string {
	s = truncate(s, width)
	if len(indexes) == 0 {
		return s
	}
	set := make(map[int]struct{}, len(indexes))
	for _, i := range indexes {
		set[i] = struct{}{}
	}
	var b strings.Builder
	for i, r := range s { // i is a byte offset, matching the fuzzy matcher
		if _, ok := set[i]; ok {
			b.WriteString(t.Match.Render(string(r)))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
