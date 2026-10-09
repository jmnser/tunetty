package mediakeys

import (
	"sync"
	"time"

	"github.com/jmnser/tunetty/internal/audio"
)

// pollInterval bounds how far the system controls lag behind the player.
const pollInterval = 500 * time.Millisecond

// seekTolerance is how far the position may drift from the expected one
// before a poll counts it as a seek.
const seekTolerance = 1500 * time.Millisecond

// watcher polls the engine for what the system controls show. Polling rather
// than reading Engine.Events leaves the UI the only consumer of that stream.
type watcher struct {
	engine *audio.Engine
	kick   chan struct{}
	done   chan struct{}
	wg     sync.WaitGroup
}

// shown is the part of the status whose change the system must be told about.
type shown struct {
	state  audio.State
	index  int
	id     string
	volume float64
}

func newWatcher(engine *audio.Engine) *watcher {
	return &watcher{engine: engine, kick: make(chan struct{}, 1), done: make(chan struct{})}
}

// refresh asks for a poll right away, after a command changed playback.
func (w *watcher) refresh() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// start calls update with the first status and then on every poll. changed
// reports a new track, state or volume; seeked a position that jumped
// instead of advancing.
func (w *watcher) start(update func(st audio.Status, changed, seeked bool)) {
	w.wg.Go(func() {
		tick := time.NewTicker(pollInterval)
		defer tick.Stop()

		var (
			last   shown
			lastAt time.Time
			lastSt audio.Status
		)
		for first := true; ; first = false {
			st := w.engine.Status()
			now := time.Now()
			cur := shown{state: st.State, index: st.Index, volume: st.Volume}
			if st.Track != nil {
				cur.id = st.Track.ID
			}
			changed := first || cur != last
			seeked := false
			if !changed && st.Track != nil {
				want := lastSt.Position
				if st.State == audio.StatePlaying {
					want += now.Sub(lastAt)
				}
				seeked = (st.Position - want).Abs() > seekTolerance
			}
			update(st, changed, seeked)
			last, lastAt, lastSt = cur, now, st

			select {
			case <-w.done:
				return
			case <-tick.C:
			case <-w.kick:
			}
		}
	})
}

func (w *watcher) stop() {
	close(w.done)
	w.wg.Wait()
}
