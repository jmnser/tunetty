package subsonic

import (
	"context"
	"crypto/md5" //nolint:gosec // mirrors the scheme under test
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPassword = "hunter2"
	testUser     = "alice"
	okEnvelope   = `{"subsonic-response":{"status":"ok"}}`
)

// newTestServer serves handler and returns a client pointed at it.
func newTestServer(t *testing.T, o Options, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	o.BaseURL, o.Username, o.Password = srv.URL, testUser, testPassword
	c, err := New(o)
	require.NoError(t, err)
	return c
}

// recordQuery answers okEnvelope and stores each request's query in *last.
func recordQuery(last *url.Values) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*last = r.URL.Query()
		_, _ = io.WriteString(w, okEnvelope)
	}
}

// The salted token is the whole point of Subsonic auth: the password itself
// must never appear on the wire unless plain auth is requested.
func TestAuthentication(t *testing.T) {
	t.Parallel()
	for _, plain := range []bool{false, true} {
		t.Run("plain="+strconv.FormatBool(plain), func(t *testing.T) {
			t.Parallel()
			var got url.Values
			c := newTestServer(t, Options{PlainAuth: plain}, recordQuery(&got))

			_, err := c.Ping(context.Background())
			require.NoError(t, err)
			assert.Equal(t, testUser, got.Get("u"))
			assert.Equal(t, "json", got.Get("f"))
			assert.Equal(t, ClientName, got.Get("c"))

			if plain {
				assert.Equal(t, testPassword, got.Get("p"))
				assert.Empty(t, got.Get("t"))
				return
			}
			assert.Empty(t, got.Get("p"), "plaintext password sent despite token auth")
			salt := got.Get("s")
			require.NotEmpty(t, salt)
			sum := md5.Sum([]byte(testPassword + salt)) //nolint:gosec // mirrors the scheme under test
			assert.Equal(t, hex.EncodeToString(sum[:]), got.Get("t"))

			// A fresh salt per request stops a captured token being replayed.
			_, err = c.Ping(context.Background())
			require.NoError(t, err)
			assert.NotEqual(t, salt, got.Get("s"))
		})
	}
}

// Servers report failures, including failed streams, as a JSON envelope rather
// than an HTTP error; it must surface as *APIError and never reach a decoder.
func TestAPIErrorIsSurfaced(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Client) error{
		"call": func(c *Client) error {
			_, err := c.Ping(context.Background())
			return err
		},
		"stream": func(c *Client) error {
			_, _, err := c.Stream(context.Background(), "s1", StreamOptions{})
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newTestServer(t, Options{}, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"subsonic-response":{"status":"failed","error":{"code":40,"message":"Wrong username or password"}}}`)
			})

			var apiErr *APIError
			require.ErrorAs(t, run(c), &apiErr)
			assert.True(t, apiErr.Unauthorized())
		})
	}
}

func TestArtistsFlattensIndexes(t *testing.T) {
	t.Parallel()
	c := newTestServer(t, Options{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"subsonic-response":{"status":"ok","artists":{"index":[
			{"name":"A","artist":[{"id":"1","name":"Air"},{"id":"2","name":"Aphex Twin"}]},
			{"name":"B","artist":[{"id":"3","name":"Bonobo"}]}
		]}}}`)
	})

	artists, err := c.Artists(context.Background())
	require.NoError(t, err)
	require.Len(t, artists, 3)
	assert.Equal(t, "Bonobo", artists[2].Name)
}

func TestStreamPassesTranscodeOptions(t *testing.T) {
	t.Parallel()
	var got url.Values
	c := newTestServer(t, Options{}, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = io.WriteString(w, "OggS")
	})

	rc, ct, err := c.Stream(context.Background(), "s1", StreamOptions{Format: "opus", MaxBitRate: 128, TimeOffset: 42})
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	assert.Equal(t, "audio/ogg", ct)
	assert.Equal(t, "opus", got.Get("format"))
	assert.Equal(t, "128", got.Get("maxBitRate"))
	assert.Equal(t, "42", got.Get("timeOffset"))
}

// A whole track takes far longer to arrive than an API call may, so the API
// timeout must not apply to streams. Uses the default HTTP client on purpose.
func TestStreamOutlivesAPITimeout(t *testing.T) {
	t.Parallel()
	const timeout = 100 * time.Millisecond
	c := newTestServer(t, Options{Timeout: timeout}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/ping.view" {
			time.Sleep(3 * timeout)
			return
		}
		w.Header().Set("Content-Type", "audio/ogg")
		for range 5 {
			_, _ = io.WriteString(w, "chunk")
			w.(http.Flusher).Flush()
			time.Sleep(timeout)
		}
	})

	body, _, err := c.Stream(context.Background(), "s1", StreamOptions{})
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(body)
	require.NoError(t, err, "stream was cut off")
	assert.Equal(t, strings.Repeat("chunk", 5), string(b))

	_, err = c.Ping(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded, "API calls must still time out")
}

// Transport errors embed the request URL; it must not leak the credentials.
func TestTransportErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // connection refused from here on

	for _, plain := range []bool{false, true} {
		c, err := New(Options{BaseURL: base, Username: testUser, Password: testPassword, PlainAuth: plain})
		require.NoError(t, err)

		_, err = c.Ping(context.Background())
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "ping.view")
		assert.NotContains(t, msg, testUser)
		assert.NotContains(t, msg, testPassword)
		for _, k := range []string{"t", "s"} {
			if strings.Contains(msg, k+"=") {
				assert.Contains(t, msg, k+"=REDACTED")
			}
		}
	}
}

func TestStarUsesTheRightParameter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind, key string
		unstar    bool
	}{
		{kind: "album", key: "albumId"},
		{kind: "artist", key: "artistId"},
		{kind: "song", key: "id", unstar: true},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			var got url.Values
			c := newTestServer(t, Options{}, recordQuery(&got))

			op := c.Star
			if tc.unstar {
				op = c.Unstar
			}
			require.NoError(t, op(context.Background(), tc.kind, "x1"))
			assert.Equal(t, "x1", got.Get(tc.key))
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		opts    Options
		wantURL string
	}{
		"adds scheme, trims slash": {Options{BaseURL: "music.example.com/", Username: "a"}, "https://music.example.com"},
		"empty URL":                {Options{Username: "a"}, ""},
		"empty username":           {Options{BaseURL: "http://x"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, err := New(tc.opts)
			if tc.wantURL == "" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantURL, c.BaseURL())
		})
	}
}

func TestDurationFormatting(t *testing.T) {
	t.Parallel()
	cases := map[Duration]string{
		0:    "0:00",
		9:    "0:09",
		75:   "1:15",
		3661: "1:01:01",
		-5:   "0:00",
	}
	for in, want := range cases {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, in.String())
		})
	}
}
