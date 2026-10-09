# tunetty

Subsonic-compatible terminal music player written in Go.

Gapless playback, a fuzzy finder over your whole library, experimental album
art in the terminal and a single static binary.

## Features

- **Subsonic / OpenSubsonic client**: artists, albums, all songs, playlists,
  starred items, search, scrobbling, favourites.
- **Gapless playback**: consecutive tracks are joined sample-exactly.
- **Fuzzy finder**: local matching merged with server-side search as you type.
- **Pure Go decoding**: Opus, MP3, FLAC, Vorbis and WAV.
- **Media keys**: MPRIS on Linux, Now Playing on macOS.
- **Album art (experimental)**: kitty, iTerm2, sixel or half blocks, also inside
  tmux. Off by default, enable with `--art-work`.

## Install

```sh
go install github.com/jmnser/tunetty/cmd/tunetty@latest
```

Or download a binary from the releases page, or build from source:

```sh
make build      # -> bin/tunetty
make install    # -> $GOBIN/tunetty
```

## Quick start

```sh
tunetty -init            # writes a starter config
$EDITOR ~/.config/tunetty/config.toml
tunetty
```

Or skip the config file and use the environment:

```sh
export TUNETTY_SERVER=https://music.example.com
export TUNETTY_USERNAME=alice
export TUNETTY_PASSWORD=...
tunetty
```

If something doesn't work, `tunetty -doctor` shows what was detected.

## Configuration

`~/.config/tunetty/config.toml` (override with `TUNETTY_CONFIG`). Run
`tunetty -init` to create a starter config.

```toml
[server]
url      = "https://music.example.com"
username = "alice"
# Keep the secret out of this file:
password_command = "pass show music/subsonic"
```

## Keymaps

Press `?` in the app for the full list.
