package subsonic

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Duration is a track/album length in seconds as reported by the server.
type Duration int

func (d Duration) String() string {
	s := max(int(d), 0)
	if h := s / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// D converts to a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) * time.Second }

// response is the envelope every Subsonic endpoint returns.
type response struct {
	Sub struct {
		Status        string `json:"status"`
		Version       string `json:"version"`
		Type          string `json:"type"`
		ServerVersion string `json:"serverVersion"`
		OpenSubsonic  bool   `json:"openSubsonic"`

		Error *APIError `json:"error"`

		Artists      *artistsRoot  `json:"artists"`
		Artist       *Artist       `json:"artist"`
		Album        *Album        `json:"album"`
		AlbumList2   *albumList2   `json:"albumList2"`
		SearchResult *SearchResult `json:"searchResult3"`
		Starred2     *Starred      `json:"starred2"`
		Playlists    *playlistRoot `json:"playlists"`
		Playlist     *Playlist     `json:"playlist"`
		RandomSongs  *songList     `json:"randomSongs"`
		TopSongs     *songList     `json:"topSongs"`
		SimilarSongs *songList     `json:"similarSongs2"`
		Genres       *genreRoot    `json:"genres"`
		SongsByGenre *songList     `json:"songsByGenre"`
		ScanStatus   *ScanStatus   `json:"scanStatus"`
	} `json:"subsonic-response"`
}

// APIError is a Subsonic protocol level error.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return "subsonic: " + e.Message + " (code " + strconv.Itoa(e.Code) + ")"
}

// Unauthorized reports whether the error is a credentials problem.
func (e *APIError) Unauthorized() bool { return e.Code == 40 || e.Code == 41 || e.Code == 50 }

type artistsRoot struct {
	Index []struct {
		Name   string   `json:"name"`
		Artist []Artist `json:"artist"`
	} `json:"index"`
}

type albumList2 struct {
	Album []Album `json:"album"`
}

type songList struct {
	Song []Song `json:"song"`
}

type playlistRoot struct {
	Playlist []Playlist `json:"playlist"`
}

type genreRoot struct {
	Genre []Genre `json:"genre"`
}

// Artist is an ID3 artist.
type Artist struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CoverArt   string  `json:"coverArt"`
	AlbumCount int     `json:"albumCount"`
	Starred    string  `json:"starred"`
	Album      []Album `json:"album"`
}

// Album is an ID3 album.
type Album struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Artist    string   `json:"artist"`
	ArtistID  string   `json:"artistId"`
	CoverArt  string   `json:"coverArt"`
	SongCount int      `json:"songCount"`
	Duration  Duration `json:"duration"`
	Year      int      `json:"year"`
	Genre     string   `json:"genre"`
	Starred   string   `json:"starred"`
	Song      []Song   `json:"song"`
}

// Song is an ID3 track.
type Song struct {
	ID          string   `json:"id"`
	Parent      string   `json:"parent"`
	Title       string   `json:"title"`
	Album       string   `json:"album"`
	Artist      string   `json:"artist"`
	AlbumID     string   `json:"albumId"`
	ArtistID    string   `json:"artistId"`
	CoverArt    string   `json:"coverArt"`
	Track       int      `json:"track"`
	Disc        int      `json:"discNumber"`
	Year        int      `json:"year"`
	Genre       string   `json:"genre"`
	Size        int64    `json:"size"`
	ContentType string   `json:"contentType"`
	Suffix      string   `json:"suffix"`
	Duration    Duration `json:"duration"`
	BitRate     int      `json:"bitRate"`
	Path        string   `json:"path"`
	Starred     string   `json:"starred"`
}

// IsStarred reports whether the server marked this song as starred.
func (s Song) IsStarred() bool { return s.Starred != "" }

// SearchResult holds the three result buckets of search3.
type SearchResult struct {
	Artist []Artist `json:"artist"`
	Album  []Album  `json:"album"`
	Song   []Song   `json:"song"`
}

// Starred holds starred items from getStarred2.
type Starred struct {
	Artist []Artist `json:"artist"`
	Album  []Album  `json:"album"`
	Song   []Song   `json:"song"`
}

// Playlist is a server side playlist.
type Playlist struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Comment   string   `json:"comment"`
	Owner     string   `json:"owner"`
	SongCount int      `json:"songCount"`
	Duration  Duration `json:"duration"`
	CoverArt  string   `json:"coverArt"`
	Entry     []Song   `json:"entry"`
}

// Genre is a genre bucket.
type Genre struct {
	Name       string `json:"value"`
	SongCount  int    `json:"songCount"`
	AlbumCount int    `json:"albumCount"`
}

// UnmarshalJSON accepts both the string and numeric encodings servers use.
func (g *Genre) UnmarshalJSON(b []byte) error {
	type alias Genre
	var a alias
	if err := json.Unmarshal(b, &a); err == nil {
		*g = Genre(a)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	g.Name = s
	return nil
}

// ScanStatus reports library scan progress.
type ScanStatus struct {
	Scanning bool `json:"scanning"`
	Count    int  `json:"count"`
}
