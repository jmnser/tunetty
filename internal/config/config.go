// Package config loads and persists tunetty's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// AppName is used for config and cache directory names.
const AppName = "tunetty"

// autoValue selects automatic detection for a setting.
const autoValue = "auto"

// Config is the on disk configuration.
type Config struct {
	Server   Server   `toml:"server"`
	Audio    Audio    `toml:"audio"`
	Art      Art      `toml:"art"`
	UI       UI       `toml:"ui"`
	Keybinds Keybinds `toml:"keybinds"`
}

// Server describes the Subsonic endpoint and credentials.
type Server struct {
	// URL is the server root, e.g. https://music.example.com.
	URL string `toml:"url"`
	// Username is the Subsonic account name.
	Username string `toml:"username"`
	// Password is the account password. Prefer PasswordCommand so the secret
	// never has to sit in a plaintext config file.
	Password string `toml:"password"`
	// PasswordCommand is executed to obtain the password; its first line of
	// stdout is used. Example: "pass show music/subsonic".
	PasswordCommand string `toml:"password_command"`
	// PlainAuth sends the password in clear instead of the salted token some
	// servers reject. Only enable over HTTPS.
	PlainAuth bool `toml:"plain_auth"`
	// CAFile is a PEM file with extra certificates to trust, such as a
	// self-signed server certificate or a private CA. Verification stays on.
	CAFile string `toml:"ca_file"`
	// InsecureSkipVerify accepts any server certificate. Prefer CAFile: this
	// leaves the connection, and the credentials in it, open to interception.
	InsecureSkipVerify bool `toml:"insecure_skip_verify"`
	// Timeout bounds individual API calls.
	Timeout Duration `toml:"timeout"`
}

// Audio configures playback.
type Audio struct {
	// Backend forces an output backend: "auto", "oto", "pulse" or "command".
	Backend string `toml:"backend"`
	// Format is the transcode format requested from the server. "opus" keeps
	// bandwidth low and decodes at the engine's native 48 kHz; "raw" asks the
	// server not to transcode at all.
	Format string `toml:"format"`
	// MaxBitRate caps the transcode bitrate in kbps. Zero uses the server default.
	MaxBitRate int `toml:"max_bitrate"`
	// BufferDuration is how much audio is decoded ahead. It also bounds how
	// long opening the next track may take while staying gapless.
	BufferDuration Duration `toml:"buffer"`
	// NetworkBuffer is the per stream read-ahead in bytes.
	NetworkBuffer int `toml:"network_buffer"`
	// Volume is the startup volume in [0,1].
	Volume float64 `toml:"volume"`
	// Scrobble reports plays back to the server.
	Scrobble bool `toml:"scrobble"`
}

// Art configures cover art rendering.
type Art struct {
	// Protocol is "auto", "kitty", "iterm2", "sixel", "blocks" or "none".
	Protocol string `toml:"protocol"`
	// Width and Height are the cover size in terminal cells.
	Width  int `toml:"width"`
	Height int `toml:"height"`
	// Query allows probing the terminal at startup to detect its capabilities.
	Query bool `toml:"query"`
	// TmuxPassthrough enables allow-passthrough for the current tmux pane so
	// graphics escapes reach the outer terminal. It is pane scoped and is
	// dropped when the pane closes.
	TmuxPassthrough bool `toml:"tmux_passthrough"`
	// CacheSize is how many decoded covers are kept in memory.
	CacheSize int `toml:"cache_size"`
}

// UI configures the interface.
type UI struct {
	// Theme selects a colour scheme: "auto", "dark" or "light".
	Theme string `toml:"theme"`
	// Accent is a hex colour overriding the theme's highlight colour.
	Accent string `toml:"accent"`
	// PageSize is how many albums are fetched per library page.
	PageSize int `toml:"page_size"`
	// FuzzyLimit caps how many results the finder shows.
	FuzzyLimit int `toml:"fuzzy_limit"`
	// SeekStep is how far the seek keys jump.
	SeekStep Duration `toml:"seek_step"`
	// VolumeStep is how much the volume keys change the level.
	VolumeStep float64 `toml:"volume_step"`
}

// Keybinds allows overriding the default keys. Each value is a comma separated
// list of key names as bubbletea reports them.
type Keybinds struct {
	PlayPause  string `toml:"play_pause"`
	Next       string `toml:"next"`
	Prev       string `toml:"prev"`
	SeekFwd    string `toml:"seek_forward"`
	SeekBack   string `toml:"seek_back"`
	VolumeUp   string `toml:"volume_up"`
	VolumeDown string `toml:"volume_down"`
	Mute       string `toml:"mute"`
	Search     string `toml:"search"`
	Quit       string `toml:"quit"`
}

// Duration is a time.Duration that unmarshals from a TOML string like "5s".
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*d = 0
		return nil
	}
	// Bare numbers are treated as seconds, which is what people write.
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*d = Duration(time.Duration(v * float64(time.Second)))
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// D converts to a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Server: Server{
			Timeout: Duration(30 * time.Second),
		},
		Audio: Audio{
			Backend:        autoValue,
			Format:         "opus",
			MaxBitRate:     0,
			BufferDuration: Duration(5 * time.Second),
			NetworkBuffer:  1 << 20,
			Volume:         0.7,
			Scrobble:       true,
		},
		Art: Art{
			Protocol:        autoValue,
			Width:           32,
			Height:          16,
			Query:           true,
			TmuxPassthrough: true,
			CacheSize:       32,
		},
		UI: UI{
			Theme:      autoValue,
			PageSize:   200,
			FuzzyLimit: 200,
			SeekStep:   Duration(10 * time.Second),
			VolumeStep: 0.05,
		},
	}
}

// Path returns the configuration file location, honouring TUNETTY_CONFIG and
// the XDG base directory specification.
func Path() (string, error) {
	if p := os.Getenv("TUNETTY_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := configDir()
	if err != nil {
		return "", fmt.Errorf("config: locating config dir: %w", err)
	}
	return filepath.Join(dir, AppName, "config.toml"), nil
}

// configDir returns $XDG_CONFIG_HOME or ~/.config. Unlike os.UserConfigDir it
// does not use ~/Library/Application Support on macOS, where terminal tools
// are expected under ~/.config too.
func configDir() (string, error) {
	// The spec says relative values are invalid and must be ignored.
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// CacheDir returns the directory used for cover art caching.
func CacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("config: locating cache dir: %w", err)
	}
	return filepath.Join(dir, AppName), nil
}

// ErrNotFound reports a missing configuration file.
var ErrNotFound = errors.New("config: no configuration file")

// Load reads the configuration from path, or from the default location when
// path is empty. A missing file yields ErrNotFound wrapped with the path, but
// the returned Config still has the environment applied, so callers can run
// from TUNETTY_* variables alone when Validate passes.
func Load(path string) (Config, string, error) {
	cfg := Default()

	var err error
	if path == "" {
		path, err = Path()
		if err != nil {
			return cfg, "", err
		}
	}

	b, err := os.ReadFile(path) //nolint:gosec // the path is user supplied by design
	switch {
	case os.IsNotExist(err):
		// The environment alone may be enough to run, so it is still applied.
		err = fmt.Errorf("%w at %s", ErrNotFound, path)
	case err != nil:
		return cfg, path, fmt.Errorf("config: reading %s: %w", path, err)
	default:
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return cfg, path, fmt.Errorf("config: parsing %s: %w", path, err)
		}
	}

	applyEnv(&cfg)
	cfg.normalise()
	return cfg, path, err
}

// applyEnv lets environment variables override the file, which is how the
// credentials are usually supplied in scripted or containerised setups.
func applyEnv(c *Config) {
	if v := os.Getenv("TUNETTY_SERVER"); v != "" {
		c.Server.URL = v
	}
	if v := os.Getenv("TUNETTY_USERNAME"); v != "" {
		c.Server.Username = v
	}
	if v := os.Getenv("TUNETTY_PASSWORD"); v != "" {
		// The command would otherwise win in ResolvePassword.
		c.Server.Password = v
		c.Server.PasswordCommand = ""
	}
	if v := os.Getenv("TUNETTY_CA_FILE"); v != "" {
		c.Server.CAFile = v
	}
	if v, err := strconv.ParseBool(os.Getenv("TUNETTY_INSECURE_SKIP_VERIFY")); err == nil {
		c.Server.InsecureSkipVerify = v
	}
	if v := os.Getenv("TUNETTY_AUDIO_BACKEND"); v != "" {
		c.Audio.Backend = v
	}
	if v := os.Getenv("TUNETTY_ART_PROTOCOL"); v != "" {
		c.Art.Protocol = v
	}
}

// normalise clamps values that would otherwise break the running player.
func (c *Config) normalise() {
	// TOML has no shell, so a leading ~ is expanded here.
	if rest, ok := strings.CutPrefix(c.Server.CAFile, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			c.Server.CAFile = filepath.Join(home, rest)
		}
	}
	if c.Audio.Backend == "" {
		c.Audio.Backend = autoValue
	}
	if c.Audio.Format == "" {
		c.Audio.Format = "opus"
	}
	c.Audio.Volume = min(max(c.Audio.Volume, 0), 1)
	if c.Audio.BufferDuration <= 0 {
		c.Audio.BufferDuration = Duration(5 * time.Second)
	}
	if c.Audio.NetworkBuffer < 64<<10 {
		c.Audio.NetworkBuffer = 1 << 20
	}
	if c.Art.Protocol == "" {
		c.Art.Protocol = autoValue
	}
	if c.Art.Width < 4 {
		c.Art.Width = 32
	}
	if c.Art.Height < 2 {
		c.Art.Height = 16
	}
	if c.Art.CacheSize < 1 {
		c.Art.CacheSize = 32
	}
	if c.UI.PageSize < 10 {
		c.UI.PageSize = 200
	}
	if c.UI.FuzzyLimit < 10 {
		c.UI.FuzzyLimit = 200
	}
	if c.UI.SeekStep <= 0 {
		c.UI.SeekStep = Duration(10 * time.Second)
	}
	if c.UI.VolumeStep <= 0 || c.UI.VolumeStep > 0.5 {
		c.UI.VolumeStep = 0.05
	}
	if c.Server.Timeout <= 0 {
		c.Server.Timeout = Duration(30 * time.Second)
	}
}

// Validate reports whether the configuration is usable for connecting.
func (c Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.Server.URL) == "" {
		missing = append(missing, "server.url")
	}
	if strings.TrimSpace(c.Server.Username) == "" {
		missing = append(missing, "server.username")
	}
	if c.Server.Password == "" && c.Server.PasswordCommand == "" {
		missing = append(missing, "server.password or server.password_command")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// Save writes the configuration to path, creating parent directories.
func (c Config) Save(path string) error {
	if path == "" {
		var err error
		path, err = Path()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the destination is chosen by the user
	if err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(header); err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return fmt.Errorf("config: encoding %s: %w", path, err)
	}
	return nil
}

const header = `# tunetty configuration
#
# Credentials can also come from the environment:
#   TUNETTY_SERVER, TUNETTY_USERNAME, TUNETTY_PASSWORD
# or from a command, which keeps the password out of this file:
#   password_command = "pass show music/subsonic"

`
