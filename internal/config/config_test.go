package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeConfig writes body to a fresh config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestDurationUnmarshal(t *testing.T) {
	t.Parallel()
	cases := map[string]time.Duration{
		"5s":    5 * time.Second,
		"1m30s": 90 * time.Second,
		// Bare numbers are read as seconds, which is what people write.
		"10":  10 * time.Second,
		"2.5": 2500 * time.Millisecond,
		"":    0,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			var d Duration
			require.NoError(t, d.UnmarshalText([]byte(in)))
			assert.Equal(t, want, d.D())
		})
	}

	var d Duration
	assert.Error(t, d.UnmarshalText([]byte("not a duration")))
}

func TestPathDefaultsToDotConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses %AppData%")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dotConfig := filepath.Join(home, ".config", AppName, "config.toml")

	cases := []struct{ xdg, explicit, want string }{
		{want: dotConfig},
		{xdg: "relative/is/ignored", want: dotConfig},
		{xdg: filepath.Join(home, "x"), want: filepath.Join(home, "x", AppName, "config.toml")},
		{xdg: filepath.Join(home, "x"), explicit: "/explicit.toml", want: "/explicit.toml"},
	}
	for _, tc := range cases {
		t.Setenv("XDG_CONFIG_HOME", tc.xdg)
		t.Setenv("TUNETTY_CONFIG", tc.explicit)
		got, err := Path()
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "XDG_CONFIG_HOME=%q TUNETTY_CONFIG=%q", tc.xdg, tc.explicit)
	}
}

// Out of range values must be clamped rather than breaking playback, while
// a volume of 0 is a legitimate choice.
func TestLoadNormalises(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		volume string
		want   float64
	}{
		"muted":    {"0.0", 0},
		"in range": {"0.4", 0.4},
		"above":    {"5.0", 1},
		"negative": {"-1.0", 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, "[audio]\nvolume = "+tc.volume+"\n[art]\nwidth = 2\n")
			cfg, _, err := Load(path)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, cfg.Audio.Volume, 1e-9)
			assert.Equal(t, 32, cfg.Art.Width)
		})
	}
}

// Environment variables override the file, including when there is no file
// at all, so tunetty can run from TUNETTY_* alone.
func TestLoadEnvironment(t *testing.T) {
	t.Setenv("TUNETTY_SERVER", "https://from-env")
	t.Setenv("TUNETTY_USERNAME", "bob")
	t.Setenv("TUNETTY_PASSWORD", "from-env")

	t.Run("overrides file", func(t *testing.T) {
		path := writeConfig(t, "[server]\nurl = \"from-file\"\npassword_command = \"echo from-file\"\n")
		cfg, _, err := Load(path)
		require.NoError(t, err)
		assert.Equal(t, "https://from-env", cfg.Server.URL)
		assert.Equal(t, "bob", cfg.Server.Username)

		pw, err := cfg.ResolvePassword(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "from-env", pw, "TUNETTY_PASSWORD must beat password_command")
	})

	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.toml")
		cfg, got, err := Load(path)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Equal(t, path, got)
		assert.Equal(t, "opus", cfg.Audio.Format, "defaults must still be returned")
		assert.NoError(t, cfg.Validate())
	})
}

func TestValidate(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		server  Server
		missing []string
	}{
		"empty":                     {Server{}, []string{"server.url", "server.username", "server.password"}},
		"password_command suffices": {Server{URL: "u", Username: "n", PasswordCommand: "echo x"}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := Config{Server: tc.server}.Validate()
			if tc.missing == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.missing {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "config.toml")

	cfg := Default()
	cfg.Server.URL = "https://music.example.com"
	cfg.Audio.MaxBitRate = 192
	require.NoError(t, cfg.Save(path))

	// The file can hold a password, so it must not be world readable.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, _, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, got)
}

func TestResolvePassword(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		server  Server
		want    string
		wantErr bool
	}{
		"literal": {server: Server{Password: "hunter2"}, want: "hunter2"},
		// The command wins, and only its first line is used.
		"command first line": {server: Server{Password: "ignored", PasswordCommand: "printf 'secret\\nnoise\\n'"}, want: "secret"},
		"command fails":      {server: Server{PasswordCommand: "exit 1"}, wantErr: true},
		"command silent":     {server: Server{PasswordCommand: "true"}, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := Config{Server: tc.server}.ResolvePassword(context.Background())
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
