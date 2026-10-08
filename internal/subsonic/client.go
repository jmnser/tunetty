// Package subsonic implements a minimal client for the Subsonic REST API
// (and OpenSubsonic extensions) using only the standard library.
package subsonic

import (
	"context"
	"crypto/md5" //nolint:gosec // mandated by the Subsonic authentication scheme
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the protocol version advertised to the server.
const APIVersion = "1.16.1"

// ClientName identifies this player to the server.
const ClientName = "tunetty"

// DefaultTimeout bounds API calls when Options.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// Options configures a Client.
type Options struct {
	// BaseURL is the server root, e.g. https://music.example.com.
	BaseURL string
	// Username and Password are the Subsonic credentials.
	Username string
	Password string
	// PlainAuth sends the password in clear (p=) instead of the salted token.
	// Only needed by servers that store hashed passwords and reject tokens.
	PlainAuth bool
	// HTTP is the transport to use. Defaults to a client without an overall
	// timeout, so long audio streams are not cut off, whose transport bounds
	// dialling and waiting for response headers.
	HTTP *http.Client
	// Timeout bounds each API call including its body; streams are exempt.
	// Zero means DefaultTimeout.
	Timeout time.Duration
	// UserAgent overrides the request user agent.
	UserAgent string
}

// Client talks to a Subsonic compatible server.
type Client struct {
	base      *url.URL
	user      string
	pass      string
	plain     bool
	http      *http.Client
	timeout   time.Duration
	userAgent string
}

// New builds a Client from Options.
func New(o Options) (*Client, error) {
	raw := strings.TrimSpace(o.BaseURL)
	if raw == "" {
		return nil, errors.New("subsonic: empty server URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("subsonic: bad server URL: %w", err)
	}
	if o.Username == "" {
		return nil, errors.New("subsonic: empty username")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hc := o.HTTP
	if hc == nil {
		hc = defaultHTTPClient(timeout)
	}
	ua := o.UserAgent
	if ua == "" {
		ua = ClientName + "/1"
	}
	return &Client{
		base: u, user: o.Username, pass: o.Password, plain: o.PlainAuth,
		http: hc, timeout: timeout, userAgent: ua,
	}, nil
}

// defaultHTTPClient has no client-wide timeout, which would also cover reading
// the body and so abort any stream longer than it. Connection setup and the
// wait for headers are bounded instead; API calls get a per request deadline.
func defaultHTTPClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = timeout
	tr.ResponseHeaderTimeout = timeout
	return &http.Client{Transport: tr}
}

// BaseURL returns the configured server root.
func (c *Client) BaseURL() string { return c.base.String() }

// Username returns the configured user.
func (c *Client) Username() string { return c.user }

func (c *Client) authParams() url.Values {
	v := url.Values{}
	v.Set("u", c.user)
	v.Set("v", APIVersion)
	v.Set("c", ClientName)
	v.Set("f", "json")
	if c.plain {
		v.Set("p", c.pass)
		return v
	}
	salt := randomSalt()
	sum := md5.Sum([]byte(c.pass + salt)) //nolint:gosec // Subsonic token scheme
	v.Set("t", hex.EncodeToString(sum[:]))
	v.Set("s", salt)
	return v
}

func randomSalt() string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal for auth; fall back to a fixed-length
		// value derived from the current time so the request still forms.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// URL builds a fully authenticated endpoint URL. It is exported so callers can
// hand stream URLs to external processes.
func (c *Client) URL(endpoint string, params url.Values) string {
	v := c.authParams()
	for k, vals := range params {
		for _, val := range vals {
			v.Add(k, val)
		}
	}
	return c.base.String() + "/rest/" + endpoint + "?" + v.Encode()
}

// secretParams are query parameters that carry credentials.
var secretParams = []string{"u", "p", "t", "s"}

// redactError strips credentials from the URL a *url.Error embeds, so the
// error can be shown to the user or logged. Other errors pass through.
func redactError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: redactURL(ue.URL), Err: ue.Err}
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable URL)"
	}
	q := u.Query()
	for _, k := range secretParams {
		if q.Has(k) {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) do(ctx context.Context, endpoint string, params url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(endpoint, params), nil)
	if err != nil {
		return nil, redactError(err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redactError(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("subsonic: %s returned %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// call performs a JSON request and returns the decoded envelope.
func (c *Client) call(ctx context.Context, endpoint string, params url.Values) (*response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.do(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("subsonic: decoding %s: %w", endpoint, err)
	}
	if out.Sub.Status != "ok" {
		if out.Sub.Error != nil {
			return nil, out.Sub.Error
		}
		return nil, fmt.Errorf("subsonic: %s failed with status %q", endpoint, out.Sub.Status)
	}
	return &out, nil
}

// ServerInfo describes the connected server.
type ServerInfo struct {
	Type         string
	Version      string
	OpenSubsonic bool
}

// Ping verifies credentials and returns server information.
func (c *Client) Ping(ctx context.Context) (ServerInfo, error) {
	r, err := c.call(ctx, "ping.view", nil)
	if err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{Type: r.Sub.Type, Version: r.Sub.ServerVersion, OpenSubsonic: r.Sub.OpenSubsonic}, nil
}

// Artists returns every ID3 artist, flattened across index buckets.
func (c *Client) Artists(ctx context.Context) ([]Artist, error) {
	r, err := c.call(ctx, "getArtists.view", nil)
	if err != nil {
		return nil, err
	}
	if r.Sub.Artists == nil {
		return nil, nil
	}
	var out []Artist
	for _, idx := range r.Sub.Artists.Index {
		out = append(out, idx.Artist...)
	}
	return out, nil
}

// Artist returns one artist including its albums.
func (c *Client) Artist(ctx context.Context, id string) (*Artist, error) {
	r, err := c.call(ctx, "getArtist.view", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Sub.Artist == nil {
		return nil, fmt.Errorf("subsonic: artist %s not found", id)
	}
	return r.Sub.Artist, nil
}

// Album returns one album including its songs.
func (c *Client) Album(ctx context.Context, id string) (*Album, error) {
	r, err := c.call(ctx, "getAlbum.view", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Sub.Album == nil {
		return nil, fmt.Errorf("subsonic: album %s not found", id)
	}
	return r.Sub.Album, nil
}

// AlbumListType selects the ordering used by AlbumList.
type AlbumListType string

// Supported album list orderings.
const (
	AlbumsNewest       AlbumListType = "newest"
	AlbumsRecent       AlbumListType = "recent"
	AlbumsFrequent     AlbumListType = "frequent"
	AlbumsRandom       AlbumListType = "random"
	AlbumsStarred      AlbumListType = "starred"
	AlbumsAlphabetical AlbumListType = "alphabeticalByName"
	AlbumsByArtist     AlbumListType = "alphabeticalByArtist"
)

// AlbumList returns albums in the requested order.
func (c *Client) AlbumList(ctx context.Context, t AlbumListType, size, offset int) ([]Album, error) {
	v := url.Values{
		"type":   {string(t)},
		"size":   {strconv.Itoa(size)},
		"offset": {strconv.Itoa(offset)},
	}
	r, err := c.call(ctx, "getAlbumList2.view", v)
	if err != nil {
		return nil, err
	}
	if r.Sub.AlbumList2 == nil {
		return nil, nil
	}
	return r.Sub.AlbumList2.Album, nil
}

// SearchOptions bounds each result bucket of Search.
type SearchOptions struct {
	ArtistCount int
	AlbumCount  int
	SongCount   int
}

// Search runs search3 against the server.
func (c *Client) Search(ctx context.Context, query string, o SearchOptions) (*SearchResult, error) {
	if o.ArtistCount == 0 {
		o.ArtistCount = 20
	}
	if o.AlbumCount == 0 {
		o.AlbumCount = 40
	}
	if o.SongCount == 0 {
		o.SongCount = 100
	}
	v := url.Values{
		"query":       {query},
		"artistCount": {strconv.Itoa(o.ArtistCount)},
		"albumCount":  {strconv.Itoa(o.AlbumCount)},
		"songCount":   {strconv.Itoa(o.SongCount)},
	}
	r, err := c.call(ctx, "search3.view", v)
	if err != nil {
		return nil, err
	}
	if r.Sub.SearchResult == nil {
		return &SearchResult{}, nil
	}
	return r.Sub.SearchResult, nil
}

// Starred returns starred artists, albums and songs.
func (c *Client) Starred(ctx context.Context) (*Starred, error) {
	r, err := c.call(ctx, "getStarred2.view", nil)
	if err != nil {
		return nil, err
	}
	if r.Sub.Starred2 == nil {
		return &Starred{}, nil
	}
	return r.Sub.Starred2, nil
}

// Playlists returns the user's playlists without entries.
func (c *Client) Playlists(ctx context.Context) ([]Playlist, error) {
	r, err := c.call(ctx, "getPlaylists.view", nil)
	if err != nil {
		return nil, err
	}
	if r.Sub.Playlists == nil {
		return nil, nil
	}
	return r.Sub.Playlists.Playlist, nil
}

// Playlist returns one playlist including its entries.
func (c *Client) Playlist(ctx context.Context, id string) (*Playlist, error) {
	r, err := c.call(ctx, "getPlaylist.view", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Sub.Playlist == nil {
		return nil, fmt.Errorf("subsonic: playlist %s not found", id)
	}
	return r.Sub.Playlist, nil
}

// RandomSongs returns up to size random songs.
func (c *Client) RandomSongs(ctx context.Context, size int) ([]Song, error) {
	r, err := c.call(ctx, "getRandomSongs.view", url.Values{"size": {strconv.Itoa(size)}})
	if err != nil {
		return nil, err
	}
	if r.Sub.RandomSongs == nil {
		return nil, nil
	}
	return r.Sub.RandomSongs.Song, nil
}

// Star marks an item as favourite. Exactly one of song, album or artist IDs is used.
func (c *Client) Star(ctx context.Context, kind, id string) error {
	return c.setStar(ctx, "star.view", kind, id)
}

// Unstar removes a favourite.
func (c *Client) Unstar(ctx context.Context, kind, id string) error {
	return c.setStar(ctx, "unstar.view", kind, id)
}

func (c *Client) setStar(ctx context.Context, endpoint, kind, id string) error {
	key := "id"
	switch kind {
	case "album":
		key = "albumId"
	case "artist":
		key = "artistId"
	}
	_, err := c.call(ctx, endpoint, url.Values{key: {id}})
	return err
}

// Scrobble reports playback to the server. submission=false registers a
// "now playing" notification, true records a completed play.
func (c *Client) Scrobble(ctx context.Context, id string, submission bool) error {
	v := url.Values{"id": {id}, "submission": {strconv.FormatBool(submission)}}
	_, err := c.call(ctx, "scrobble.view", v)
	return err
}

// StreamOptions tunes the transcoding requested from the server.
type StreamOptions struct {
	// Format requests a specific container/codec, e.g. "opus" or "raw".
	Format string
	// MaxBitRate caps the transcode bitrate in kbps. Zero means server default.
	MaxBitRate int
	// TimeOffset starts the stream this many seconds in (transcoded formats only).
	TimeOffset int
}

func (o StreamOptions) values(id string) url.Values {
	v := url.Values{"id": {id}}
	if o.Format != "" {
		v.Set("format", o.Format)
	}
	if o.MaxBitRate > 0 {
		v.Set("maxBitRate", strconv.Itoa(o.MaxBitRate))
	}
	if o.TimeOffset > 0 {
		v.Set("timeOffset", strconv.Itoa(o.TimeOffset))
	}
	return v
}

// StreamURL returns an authenticated stream URL for a song.
func (c *Client) StreamURL(id string, o StreamOptions) string {
	return c.URL("stream.view", o.values(id))
}

// Stream opens an audio stream. The caller closes the returned body.
// contentType is the server reported MIME type, which may be empty. Unlike API
// calls no deadline is added: the stream lives as long as ctx.
func (c *Client) Stream(ctx context.Context, id string, o StreamOptions) (body io.ReadCloser, contentType string, err error) {
	resp, err := c.do(ctx, "stream.view", o.values(id))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/xml") {
		// The server answered with an error envelope instead of audio.
		defer func() { _ = resp.Body.Close() }()
		var out response
		if err := json.NewDecoder(resp.Body).Decode(&out); err == nil && out.Sub.Error != nil {
			return nil, "", out.Sub.Error
		}
		return nil, "", fmt.Errorf("subsonic: stream %s returned %s instead of audio", id, ct)
	}
	return resp.Body, ct, nil
}

// CoverArt fetches cover art bytes. size is the requested square edge in
// pixels; zero asks for the original.
func (c *Client) CoverArt(ctx context.Context, id string, size int) ([]byte, error) {
	if id == "" {
		return nil, errors.New("subsonic: empty cover art id")
	}
	v := url.Values{"id": {id}}
	if size > 0 {
		v.Set("size", strconv.Itoa(size))
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.do(ctx, "getCoverArt.view", v)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		var out response
		if err := json.NewDecoder(resp.Body).Decode(&out); err == nil && out.Sub.Error != nil {
			return nil, out.Sub.Error
		}
		return nil, fmt.Errorf("subsonic: cover art %s unavailable", id)
	}
	// Cover art is bounded; 16 MiB is far beyond any sane image.
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}
