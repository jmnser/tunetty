# tunetty

Subsonic-compatible terminal music player written in Go.

Album art in the terminal, gapless playback, and a fuzzy finder over your whole
library. Builds with `CGO_ENABLED=0` on every supported platform — a single
static binary, no ALSA headers, no libopus, no ffmpeg.

## Features

- **Subsonic / OpenSubsonic client** — artists, albums, all songs, playlists,
  starred items, server-side search, scrobbling, favourites.
- **Gapless playback** — consecutive tracks are spliced sample-exactly. The
  output device is opened once and never torn down between tracks.
- **Album art in the terminal** — kitty graphics, iTerm2 inline images, sixel,
  or Unicode half blocks. Works inside tmux via passthrough.
- **Fuzzy finder** — instant local matching over artists, albums and playlists,
  merged with server-side song search as you type.
- **Pure Go decoding** — Opus ([pion/opus]), MP3, FLAC, Vorbis and WAV.
- **No CGO** — verified in CI across linux, darwin and windows on amd64/arm64.

## Install

```sh
go install github.com/jmnser/tunetty/cmd/tunetty@latest
```

Or grab a binary from the [releases page], or build from source:

```sh
make build      # -> bin/tunetty
```

## Quick start

```sh
tunetty -init            # writes a starter config
$EDITOR ~/.config/tunetty/config.toml
tunetty
```

Credentials can come from the environment instead of the file; with all
three set, no config file is needed at all:

```sh
export TUNETTY_SERVER=https://music.example.com
export TUNETTY_USERNAME=alice
export TUNETTY_PASSWORD=...
tunetty
```

If something is not working, `tunetty -doctor` reports exactly what was
detected — terminal graphics protocol, cell geometry, tmux state, and which
audio backend opened.

## Configuration

`~/.config/tunetty/config.toml` on Linux and macOS (`$XDG_CONFIG_HOME/tunetty/`
if set, `%AppData%\tunetty\config.toml` on Windows; override with `TUNETTY_CONFIG`).

```toml
[server]
url      = "https://music.example.com"
username = "alice"
# Keep the secret out of this file:
password_command = "pass show music/subsonic"
# password = "..."          # or set it directly; password_command wins if both are set
# plain_auth = false        # only if your server rejects token auth
timeout = "30s"             # per API call; audio streams are not cut off

[audio]
backend        = "auto"   # auto | oto | pulse | command
format         = "opus"   # transcode format asked of the server; "raw" to disable
max_bitrate    = 0        # kbps cap, 0 = server default
buffer         = "5s"     # decoded audio kept ahead of the speakers
network_buffer = 1048576  # per-stream read-ahead, bytes
volume         = 0.7      # startup volume, 0 to 1
scrobble       = true

[art]
protocol         = "auto" # auto | kitty | iterm2 | sixel | blocks | none
width            = 32     # cover size in terminal cells
height           = 16
query            = true   # probe the terminal at startup
tmux_passthrough = true   # enable allow-passthrough for this tmux pane
cache_size       = 32

[ui]
accent      = ""          # hex colour, e.g. "#89b4fa"
page_size   = 200
fuzzy_limit = 200
seek_step   = "10s"
volume_step = 0.05

[keybinds]                # comma-separated overrides
# play_pause = "space,p"
# quit       = "ctrl+c,Q"
```

Environment overrides: `TUNETTY_CONFIG`, `TUNETTY_SERVER`, `TUNETTY_USERNAME`,
`TUNETTY_PASSWORD`, `TUNETTY_AUDIO_BACKEND`, `TUNETTY_ART_PROTOCOL`,
`TUNETTY_AUDIO_COMMAND`. They take precedence over the file; in particular
`TUNETTY_PASSWORD` replaces both `password` and `password_command`.
`password_command` runs through `/bin/sh -c` (`cmd /C` on Windows).

## Keys

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | move |
| `enter` | open / play |
| `esc`, `h` | back |
| `tab` / `shift+tab` | switch view |
| `space` | play / pause |
| `n` / `b` | next / previous track |
| `[` / `]` | seek back / forward |
| `-` / `+` | volume |
| `m` | mute |
| `r` | cycle repeat off → all → one |
| `a` / `A` | queue / play next |
| `x` / `X` | remove from queue / clear queue |
| `z` | shuffle upcoming tracks |
| `S` | shuffle play the current list (on Songs: the whole library) |
| `*` | star |
| `f` | fuzzy find |
| `/` | filter the current list |
| `?` | help |
| `Q` | quit |

Press `?` in the app for the full list, including the active audio backend and
cover art protocol.

## How it works

### Gapless playback

A decode pump goroutine keeps a PCM ring buffer full while the audio callback
drains it. Everything is resampled to a fixed 48 kHz stereo mixing format, so
the output device is opened once at startup and never reopened.

When a track ends, the pump opens the next one and writes its samples directly
behind the previous track's — no drain, no device reset, no silence. The ring
holds several seconds of audio, which is the window available for opening the
next stream, so the join stays gapless even on a slow server. Track boundaries
travel through the ring as markers, so the UI and scrobbler report the track
that is *audible* rather than the one being decoded ahead of it.

`internal/audio` has a test that queues two tracks and asserts the captured
output is continuous across the boundary, sample for sample.

### Audio output without CGO

| Platform | Backend | Mechanism |
| --- | --- | --- |
| macOS, Windows | `oto` | CoreAudio / WASAPI through purego |
| Linux | `pulse` | PulseAudio / PipeWire protocol over a unix socket, pure Go |
| anywhere | `command` | pipes float32 PCM to `pw-cat`, `paplay`, `ffplay`, `aplay` or `sox` |

Backends are tried in that order and the first that opens wins. Set
`TUNETTY_AUDIO_COMMAND` to pipe into any player that accepts raw float32
little-endian PCM on stdin.

Linux deliberately does not use oto: its ALSA backend needs CGO. The PulseAudio
wire protocol is implemented in pure Go and PipeWire's compatibility layer
speaks it, which covers essentially every modern desktop.

### Album art

The protocol is detected from the environment, then confirmed by querying the
terminal (a kitty graphics query, `CSI 16 t` for cell geometry, and primary
device attributes for sixel). Cell pixel geometry also comes from `TIOCGWINSZ`
so covers keep their aspect ratio.

Inside tmux every graphics escape is wrapped in a passthrough envelope. tmux
only forwards those when `allow-passthrough` is on, so tunetty enables it for
the **current pane only** (`tmux set -p`), leaving your session and global
config untouched. Disable that with `tmux_passthrough = false`; without
passthrough it falls back to half blocks automatically.

The cover is rendered into a reserved block of cells whose escape sequence
measures zero display columns, so the surrounding layout is unaffected. The
now-playing band is laid out so the artwork shares its rows only with metadata
that changes when the track does — the progress bar, which redraws every tick,
lives on its own row below. That is what stops repaints from eroding the image.

## Development

```sh
make test        # go test ./...
make lint        # golangci-lint run ./...
make snapshot    # goreleaser release --snapshot --clean
make version     # svu current  (requires svu)
```

CI enforces the no-CGO constraint with a cross-compile matrix over
linux/darwin/windows × amd64/arm64.

Releases are tagged with [svu] and built by [GoReleaser]; `make release-patch`
(or `-minor` / `-major`) creates the annotated tag and prints the push command.

While the project is on 0.x it stays there. `.svu.yml` sets `v0: true`, so a
breaking-change commit makes `svu next` answer v0.2.0 rather than v1.0.0 —
under semver 0.x promises no stability, so breaking changes are a minor bump.
`svu major` ignores that setting, so `make release-major` refuses to leave 0.x
unless you say so explicitly:

```sh
make release-major ALLOW_V1=1
```

## Licence

MIT

[pion/opus]: https://github.com/pion/opus
[releases page]: https://github.com/jmnser/tunetty/releases
[svu]: https://github.com/caarlos0/svu
[GoReleaser]: https://goreleaser.com
