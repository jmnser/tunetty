package ui

import (
	"context"
	"io"
	"time"

	"github.com/jmnser/tunetty/internal/audio"
	"github.com/jmnser/tunetty/internal/config"
	"github.com/jmnser/tunetty/internal/subsonic"
)

// trackMeta is attached to every engine track so the UI can render rich
// information without looking anything up again.
type trackMeta struct {
	Song subsonic.Song
}

// toTracks converts Subsonic songs into engine tracks. The Open closure is
// what the engine calls when it is ready to decode, which for the next track
// happens while the current one is still playing.
func toTracks(cl *subsonic.Client, cfg config.Audio, songs []subsonic.Song) []audio.Track {
	out := make([]audio.Track, 0, len(songs))
	for _, s := range songs {
		out = append(out, toTrack(cl, cfg, s))
	}
	return out
}

func toTrack(cl *subsonic.Client, cfg config.Audio, s subsonic.Song) audio.Track {
	song := s
	return audio.Track{
		ID:       song.ID,
		Title:    displayTitle(song),
		Artist:   song.Artist,
		Album:    song.Album,
		Duration: song.Duration.D(),
		Meta:     trackMeta{Song: song},
		Open: func(ctx context.Context, offset time.Duration) (io.ReadCloser, string, error) {
			opts := subsonic.StreamOptions{
				Format:     cfg.Format,
				MaxBitRate: cfg.MaxBitRate,
				TimeOffset: int(offset / time.Second),
			}
			rc, contentType, err := cl.Stream(ctx, song.ID, opts)
			if err != nil {
				return nil, "", err
			}
			hint := contentType
			if hint == "" {
				// Fall back to what we asked for, then to the stored suffix.
				hint = cfg.Format
				if hint == "" || hint == "raw" {
					hint = song.Suffix
				}
			}
			return rc, hint, nil
		},
	}
}

// seekBy moves playback relative to the current position. The target is
// rounded to whole seconds because the Subsonic timeOffset parameter has no
// finer resolution; seeking to the exact second the server starts at keeps
// the displayed position in step with what is heard.
func (m *Model) seekBy(delta time.Duration) {
	m.engine.SeekTo(max(m.engine.Position()+delta, 0).Round(time.Second))
}

func displayTitle(s subsonic.Song) string {
	if s.Title != "" {
		return s.Title
	}
	return s.Path
}

// songOf extracts the Subsonic song from an engine track, if present.
func songOf(t *audio.Track) (subsonic.Song, bool) {
	if t == nil {
		return subsonic.Song{}, false
	}
	m, ok := t.Meta.(trackMeta)
	return m.Song, ok
}
