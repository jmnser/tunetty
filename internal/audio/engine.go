package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Track is one playable item. Open is called by the engine when the track is
// about to be decoded, which with gapless prefetch happens before the previous
// track has finished playing.
type Track struct {
	// ID uniquely identifies the track to the caller.
	ID string
	// Title, Artist and Album are carried through for display only.
	Title  string
	Artist string
	Album  string
	// Duration is the expected length, used for progress display.
	Duration time.Duration
	// Open returns the encoded stream starting at offset into the track.
	// hint carries a MIME type or suffix used to pick a decoder.
	Open func(ctx context.Context, offset time.Duration) (rc io.ReadCloser, hint string, err error)
	// Meta is free-form data the caller can attach.
	Meta any
}

// State is the engine's playback state.
type State int

// Playback states.
const (
	StateStopped State = iota
	StatePaused
	StatePlaying
)

func (s State) String() string {
	switch s {
	case StatePlaying:
		return "playing"
	case StatePaused:
		return "paused"
	default:
		return "stopped"
	}
}

// RepeatMode controls what happens at the end of a track or the queue.
type RepeatMode int

// Repeat modes.
const (
	RepeatOff RepeatMode = iota
	RepeatAll
	RepeatOne
)

func (r RepeatMode) String() string {
	switch r {
	case RepeatAll:
		return "all"
	case RepeatOne:
		return "one"
	default:
		return "off"
	}
}

// EventKind discriminates Event.
type EventKind int

// Event kinds.
const (
	// EventTrackStarted fires when a track becomes *audible*, not when it is
	// opened. With gapless prefetch those two moments are seconds apart, and
	// scrobbling wants the audible one.
	EventTrackStarted EventKind = iota
	// EventTrackFinished fires when a track stops being audible after it
	// counts as played: it reached its end, or it was cut short (skip, stop,
	// queue edit) after half its length or four minutes had been heard.
	// Event.Played carries the position reached.
	EventTrackFinished
	EventQueueFinished
	EventStateChanged
	EventQueueChanged
	EventError
)

// Event is emitted by the engine as playback progresses.
type Event struct {
	Kind  EventKind
	Track *Track
	Index int
	Err   error
	// Played is the position reached, set on EventTrackFinished.
	Played time.Duration
}

// Config tunes the engine.
type Config struct {
	// Backend forces a sink ("oto", "pulse", "command"); empty means auto.
	Backend string
	// BufferDuration is how much decoded audio is kept ahead of the speakers.
	// It doubles as the window available for opening the next track, so it
	// directly determines whether track joins stay gapless on a slow server.
	BufferDuration time.Duration
	// NetworkBuffer is the per stream read-ahead size in bytes.
	NetworkBuffer int

	// openSink overrides backend selection; tests use it to capture output.
	openSink func(f Format, pull PullFunc) (Sink, error)
}

func (c *Config) applyDefaults() {
	if c.BufferDuration <= 0 {
		c.BufferDuration = 5 * time.Second
	}
	if c.BufferDuration < time.Second {
		c.BufferDuration = time.Second
	}
	if c.BufferDuration > 60*time.Second {
		c.BufferDuration = 60 * time.Second
	}
	if c.NetworkBuffer <= 0 {
		c.NetworkBuffer = 1 << 20
	}
}

// entry is one queue slot. Its ID is unique for the engine's lifetime, so
// the same song queued twice is still two distinguishable entries, and queue
// edits never confuse which slot is playing or what follows it.
type entry struct {
	id    uint64
	track Track
}

// seqInfo remembers which queue entry a ring mark belongs to. entry 0 is the
// end-of-queue mark.
type seqInfo struct {
	entry uint64
	track Track
	// seek is set when the mark restarts the same entry at a new position.
	seek bool
}

// Engine plays a queue of tracks gaplessly.
//
// A decode pump goroutine keeps a PCM ring buffer full, resampling everything
// to OutputFormat. The sink is opened once and never torn down between tracks,
// so the pump can splice the next track's samples directly behind the previous
// one's: consecutive tracks join sample-exactly with no device reset, no
// drain, and no silence.
type Engine struct {
	cfg  Config
	sink Sink
	ring *ring

	volume atomic.Uint64 // float64 bits in [0,1]
	muted  atomic.Bool

	events   chan Event
	emitMu   sync.RWMutex
	emitDone bool

	mu      sync.Mutex
	queue   []entry
	lastID  uint64
	audible uint64 // entry ID currently coming out of the speakers, 0 = none
	seq     uint64
	seqMap  map[uint64]seqInfo
	lastSeq uint64 // newest mark already announced
	state   State
	repeat  RepeatMode
	seekReq *seekRequest

	// gen is bumped on every queue or repeat edit, telling the pump to check
	// that what it already buffered still follows the audible entry.
	gen atomic.Uint64
	// net is the read-ahead of the stream being decoded, for Status.
	net atomic.Pointer[readAhead]

	resume  chan struct{}
	closed  chan struct{}
	closing sync.Once
	wg      sync.WaitGroup
}

type seekRequest struct {
	entry  uint64 // 0 stops playback
	offset time.Duration
	seek   bool // same entry at a new position
}

// NewEngine opens the audio device and starts the decode pump.
func NewEngine(cfg Config) (*Engine, error) {
	cfg.applyDefaults()

	e := &Engine{
		cfg:    cfg,
		events: make(chan Event, 64),
		resume: make(chan struct{}, 1),
		closed: make(chan struct{}),
		seqMap: map[uint64]seqInfo{},
	}
	e.setVolume(1)
	e.ring = newRing(OutputFormat.FramesFor(cfg.BufferDuration) * OutputFormat.Channels)

	var (
		sink Sink
		err  error
	)
	if cfg.openSink != nil {
		sink, err = cfg.openSink(OutputFormat, e.pull)
	} else {
		sink, err = openSink(OutputFormat, cfg.Backend, e.pull)
	}
	if err != nil {
		return nil, err
	}
	e.sink = sink

	e.wg.Add(2)
	go e.pump()
	go e.watchAudible()
	return e, nil
}

// Backend returns the name of the active output backend.
func (e *Engine) Backend() string { return e.sink.Name() }

// Events returns the engine event stream. Consumers must drain it.
func (e *Engine) Events() <-chan Event { return e.events }

func (e *Engine) emit(evs ...Event) {
	e.emitMu.RLock()
	defer e.emitMu.RUnlock()
	if e.emitDone {
		return
	}
	for _, ev := range evs {
		select {
		case e.events <- ev:
		default: // drop rather than stall the pump on a slow consumer
		}
	}
}

// pull feeds the sink. It runs on the audio thread and must not block.
func (e *Engine) pull(p []float32) int {
	n := e.ring.Read(p)
	if g := e.gain(); g != 1 {
		for i := range n {
			p[i] *= g
		}
	}
	return n
}

func (e *Engine) gain() float32 {
	if e.muted.Load() {
		return 0
	}
	// Human hearing is roughly logarithmic, so the linear slider is squared to
	// make the low end of the range usable.
	v := math.Float64frombits(e.volume.Load())
	return float32(v * v)
}

func (e *Engine) setVolume(v float64) {
	e.volume.Store(math.Float64bits(math.Max(0, math.Min(1, v))))
}

// Volume returns the current volume in [0,1].
func (e *Engine) Volume() float64 { return math.Float64frombits(e.volume.Load()) }

// SetVolume sets the output volume, clamped to [0,1].
func (e *Engine) SetVolume(v float64) {
	e.setVolume(v)
	e.emit(Event{Kind: EventStateChanged})
}

// AdjustVolume changes the volume by delta and returns the new value.
func (e *Engine) AdjustVolume(delta float64) float64 {
	e.setVolume(e.Volume() + delta)
	e.emit(Event{Kind: EventStateChanged})
	return e.Volume()
}

// Muted reports whether output is muted.
func (e *Engine) Muted() bool { return e.muted.Load() }

// ToggleMute flips the mute flag and returns the new value.
func (e *Engine) ToggleMute() bool {
	m := !e.muted.Load()
	e.muted.Store(m)
	e.emit(Event{Kind: EventStateChanged})
	return m
}

// wake nudges the pump.
func (e *Engine) wake() {
	select {
	case e.resume <- struct{}{}:
	default:
	}
}

// queueEdited makes the pump re-check its prefetched successor. A writer
// blocked on a full ring is kicked so the check happens even while paused.
func (e *Engine) queueEdited() {
	e.gen.Add(1)
	e.ring.Kick()
	e.wake()
}

// syncSink aligns the device with the requested state.
func (e *Engine) syncSink(s State) {
	var err error
	if s == StatePlaying {
		err = e.sink.Play()
	} else {
		err = e.sink.Pause()
	}
	if err != nil {
		e.emit(Event{Kind: EventError, Index: -1, Err: fmt.Errorf("audio output: %w", err)})
	}
}

// ---------------------------------------------------------------- queue API

// newEntriesLocked wraps tracks in freshly numbered entries. Caller holds mu.
func (e *Engine) newEntriesLocked(tracks []Track) []entry {
	out := make([]entry, len(tracks))
	for i, t := range tracks {
		e.lastID++
		out[i] = entry{id: e.lastID, track: t}
	}
	return out
}

// indexLocked returns the queue position of entry id, or -1. Caller holds mu.
func (e *Engine) indexLocked(id uint64) int {
	if id == 0 {
		return -1
	}
	for i := range e.queue {
		if e.queue[i].id == id {
			return i
		}
	}
	return -1
}

// SetQueue replaces the queue and starts playing at start.
func (e *Engine) SetQueue(tracks []Track, start int) {
	e.mu.Lock()
	evs := e.interruptLocked()
	e.queue = e.newEntriesLocked(tracks)
	if start < 0 || start >= len(e.queue) {
		start = 0
	}
	if len(e.queue) == 0 {
		e.stopLocked()
	} else {
		e.startLocked(e.queue[start].id)
	}
	state := e.state
	e.mu.Unlock()

	e.syncSink(state)
	e.emit(evs...)
	e.emit(Event{Kind: EventQueueChanged})
	e.queueEdited()
}

// Enqueue appends tracks, starting playback if the engine was idle.
func (e *Engine) Enqueue(tracks ...Track) {
	e.insert(-1, tracks)
}

// InsertNext places tracks immediately after the audible track.
func (e *Engine) InsertNext(tracks ...Track) {
	e.insert(0, tracks)
}

// insert adds tracks at the end (where < 0) or behind the audible entry.
func (e *Engine) insert(where int, tracks []Track) {
	e.mu.Lock()
	evs := e.syncAudibleLocked()
	at := len(e.queue)
	if where >= 0 {
		at = min(e.indexLocked(e.audible)+1, len(e.queue))
	}
	e.queue = slices.Insert(e.queue, at, e.newEntriesLocked(tracks)...)
	idle := e.state == StateStopped && len(tracks) > 0
	if idle {
		evs = append(evs, e.interruptLocked()...)
		e.startLocked(e.queue[at].id)
	}
	state := e.state
	e.mu.Unlock()

	if idle {
		e.syncSink(state)
	}
	e.emit(evs...)
	e.emit(Event{Kind: EventQueueChanged})
	e.queueEdited()
}

// RemoveAt drops a queue entry. Removing the audible one moves on to the
// entry that takes its place, keeping the play/pause state.
func (e *Engine) RemoveAt(i int) {
	e.mu.Lock()
	evs := e.syncAudibleLocked()
	if i < 0 || i >= len(e.queue) {
		e.mu.Unlock()
		e.emit(evs...)
		return
	}
	removed := e.queue[i].id
	e.queue = slices.Delete(e.queue, i, i+1)
	flush := false
	if removed == e.audible {
		evs = append(evs, e.replaceAudibleLocked(i)...)
		flush = true
	}
	state := e.state
	e.mu.Unlock()

	if flush {
		e.syncSink(state)
	}
	e.emit(evs...)
	e.emit(Event{Kind: EventQueueChanged})
	e.queueEdited()
}

// replaceAudibleLocked handles the audible entry leaving the queue: playback
// continues with whatever now sits at index i. Caller holds mu.
func (e *Engine) replaceAudibleLocked(i int) []Event {
	switch {
	case e.state == StateStopped:
		e.audible = 0
		return nil
	case len(e.queue) == 0:
		evs := e.interruptLocked()
		e.stopLocked()
		return evs
	default:
		evs := e.interruptLocked()
		e.restartLocked(e.queue[min(i, len(e.queue)-1)].id, 0, false)
		return evs
	}
}

// Clear empties the queue and stops playback.
func (e *Engine) Clear() {
	e.mu.Lock()
	evs := e.interruptLocked()
	e.queue = nil
	e.stopLocked()
	e.mu.Unlock()

	e.syncSink(StateStopped)
	e.emit(evs...)
	e.emit(Event{Kind: EventQueueChanged})
	e.queueEdited()
}

// ReplaceQueue swaps the queue contents without disturbing playback, which is
// what lets the upcoming tracks be reordered or shuffled mid-song. Entries are
// carried over by position first, then by track ID, so as long as the audible
// track is still present the sound continues uninterrupted.
func (e *Engine) ReplaceQueue(tracks []Track) {
	e.mu.Lock()
	evs := e.syncAudibleLocked()
	idx := e.indexLocked(e.audible)
	e.queue = e.matchEntriesLocked(e.queue, tracks)
	flush := false
	if e.audible != 0 && e.indexLocked(e.audible) < 0 {
		evs = append(evs, e.replaceAudibleLocked(max(idx, 0))...)
		flush = true
	}
	state := e.state
	e.mu.Unlock()

	if flush {
		e.syncSink(state)
	}
	e.emit(evs...)
	e.emit(Event{Kind: EventQueueChanged})
	e.queueEdited()
}

// matchEntriesLocked builds entries for tracks, reusing the IDs of old entries
// that hold the same track: at the same position if possible, otherwise the
// first unused one. Caller holds mu.
func (e *Engine) matchEntriesLocked(old []entry, tracks []Track) []entry {
	used := make([]bool, len(old))
	out := make([]entry, len(tracks))
	for i, t := range tracks {
		if i < len(old) && old[i].track.ID == t.ID {
			out[i], used[i] = entry{id: old[i].id, track: t}, true
		}
	}
	for i, t := range tracks {
		if out[i].id != 0 {
			continue
		}
		for j := range old {
			if !used[j] && old[j].track.ID == t.ID {
				out[i], used[j] = entry{id: old[j].id, track: t}, true
				break
			}
		}
		if out[i].id == 0 {
			e.lastID++
			out[i] = entry{id: e.lastID, track: t}
		}
	}
	return out
}

// Queue returns a copy of the current queue.
func (e *Engine) Queue() []Track {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Track, len(e.queue))
	for i := range e.queue {
		out[i] = e.queue[i].track
	}
	return out
}

// restartLocked asks the pump to restart decoding at entry id without
// changing play/pause. Caller holds mu.
func (e *Engine) restartLocked(id uint64, offset time.Duration, seek bool) {
	e.seekReq = &seekRequest{entry: id, offset: offset, seek: seek}
	e.audible = id
}

// startLocked restarts at the head of entry id and marks the engine as
// playing. Caller holds mu.
func (e *Engine) startLocked(id uint64) {
	e.restartLocked(id, 0, false)
	e.state = StatePlaying
}

// seekLocked restarts decoding, leaving a pause in place. Caller holds mu.
func (e *Engine) seekLocked(id uint64, offset time.Duration, seek bool) {
	e.restartLocked(id, offset, seek)
	if e.state == StateStopped {
		e.state = StatePlaying
	}
}

func (e *Engine) stopLocked() {
	e.seekReq = &seekRequest{}
	e.state = StateStopped
	e.audible = 0
}

// interruptLocked drops the buffered audio, ending the audible track early,
// and returns the resulting events. Caller holds mu.
func (e *Engine) interruptLocked() []Event {
	e.ring.Flush(true)
	return e.syncAudibleLocked()
}

// ------------------------------------------------------------- transport API

// Play resumes or starts playback.
func (e *Engine) Play() {
	e.mu.Lock()
	if len(e.queue) == 0 {
		e.mu.Unlock()
		return
	}
	if e.state == StateStopped {
		e.startLocked(e.queue[0].id)
	} else {
		e.state = StatePlaying
	}
	e.mu.Unlock()

	e.syncSink(StatePlaying)
	e.emit(Event{Kind: EventStateChanged})
	e.wake()
}

// Pause halts playback, keeping the decoded buffer intact.
func (e *Engine) Pause() {
	e.mu.Lock()
	if e.state != StatePlaying {
		e.mu.Unlock()
		return
	}
	e.state = StatePaused
	e.mu.Unlock()

	e.syncSink(StatePaused)
	e.emit(Event{Kind: EventStateChanged})
}

// TogglePause switches between playing and paused.
func (e *Engine) TogglePause() {
	if e.State() == StatePlaying {
		e.Pause()
		return
	}
	e.Play()
}

// Stop halts playback and clears the decoded buffer.
func (e *Engine) Stop() {
	e.mu.Lock()
	evs := e.interruptLocked()
	e.stopLocked()
	e.mu.Unlock()

	e.syncSink(StateStopped)
	e.emit(evs...)
	e.emit(Event{Kind: EventStateChanged})
	e.wake()
}

// Next skips to the following track.
func (e *Engine) Next() { e.skip(1) }

// Prev restarts the current track, or moves back if near its start.
func (e *Engine) Prev() {
	if e.Position() > 3*time.Second {
		e.SeekTo(0)
		return
	}
	e.skip(-1)
}

func (e *Engine) skip(delta int) {
	e.mu.Lock()
	evs := e.syncAudibleLocked()
	if len(e.queue) == 0 {
		e.mu.Unlock()
		e.emit(evs...)
		return
	}
	next := max(e.indexLocked(e.audible), 0) + delta
	ended := false
	switch {
	case next >= len(e.queue):
		if e.repeat == RepeatOff {
			ended = true
		} else {
			next = 0
		}
	case next < 0:
		next = 0
	}
	evs = append(evs, e.interruptLocked()...)
	if ended {
		e.stopLocked()
	} else {
		e.seekLocked(e.queue[next].id, 0, false)
	}
	state := e.state
	e.mu.Unlock()

	e.syncSink(state)
	e.emit(evs...)
	if ended {
		e.emit(Event{Kind: EventQueueFinished})
	}
	e.wake()
}

// PlayIndex jumps to a specific queue position and plays it.
func (e *Engine) PlayIndex(i int) {
	e.mu.Lock()
	if i < 0 || i >= len(e.queue) {
		e.mu.Unlock()
		return
	}
	evs := e.interruptLocked()
	e.startLocked(e.queue[i].id)
	e.mu.Unlock()

	e.syncSink(StatePlaying)
	e.emit(evs...)
	e.emit(Event{Kind: EventStateChanged})
	e.wake()
}

// SeekTo jumps to an absolute offset within the audible track.
//
// Seeking is performed server side (the Subsonic timeOffset parameter), so it
// requires the server to transcode the stream; on a raw passthrough stream the
// server may ignore the offset. The offset is rounded to whole seconds, the
// resolution of timeOffset, so the reported position matches the stream.
func (e *Engine) SeekTo(offset time.Duration) {
	offset = max(offset.Round(time.Second), 0)
	e.mu.Lock()
	evs := e.syncAudibleLocked()
	if e.indexLocked(e.audible) < 0 {
		e.mu.Unlock()
		e.emit(evs...)
		return
	}
	e.ring.Flush(false)
	e.seekLocked(e.audible, offset, true)
	state := e.state
	e.mu.Unlock()

	e.syncSink(state)
	e.emit(evs...)
	e.wake()
}

// SeekBy moves relative to the current position.
func (e *Engine) SeekBy(delta time.Duration) {
	e.SeekTo(max(e.Position()+delta, 0))
}

// SetRepeat changes the repeat mode.
func (e *Engine) SetRepeat(m RepeatMode) {
	e.mu.Lock()
	e.repeat = m
	e.mu.Unlock()
	e.emit(Event{Kind: EventStateChanged})
	e.queueEdited()
}

// CycleRepeat advances off -> all -> one -> off and returns the new mode.
func (e *Engine) CycleRepeat() RepeatMode {
	e.mu.Lock()
	e.repeat = (e.repeat + 1) % 3
	m := e.repeat
	e.mu.Unlock()
	e.emit(Event{Kind: EventStateChanged})
	e.queueEdited()
	return m
}

// Repeat returns the repeat mode.
func (e *Engine) Repeat() RepeatMode {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.repeat
}

// State returns the current playback state.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// ------------------------------------------------------------------ status

// Status is a consistent snapshot of engine state for the UI.
type Status struct {
	State    State
	Repeat   RepeatMode
	Index    int
	Track    *Track
	Position time.Duration
	Volume   float64
	Muted    bool
	QueueLen int
	// Buffered is the fraction of the PCM ring that is filled, 0..1.
	Buffered float64
	// NetBytes is how much encoded audio is waiting in the network read-ahead.
	NetBytes int64
}

// Status returns the current playback snapshot.
func (e *Engine) Status() Status {
	buffered := e.ring.Buffered()
	var netBytes int64
	if ra := e.net.Load(); ra != nil {
		netBytes = int64(ra.Buffered())
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	st := Status{
		State:    e.state,
		Repeat:   e.repeat,
		Index:    -1,
		Volume:   math.Float64frombits(e.volume.Load()),
		Muted:    e.muted.Load(),
		QueueLen: len(e.queue),
		NetBytes: netBytes,
	}
	if n := e.ring.Capacity(); n > 0 {
		st.Buffered = float64(buffered*OutputFormat.Channels) / float64(n)
	}

	// The ring is fresher than e.audible, which the watcher updates on a tick.
	audible := e.audible
	seq, frames, ok := e.ring.Current()
	info, known := e.seqMap[seq]
	ok = ok && known && info.entry != 0
	if ok {
		audible = info.entry
	}
	if idx := e.indexLocked(audible); idx >= 0 {
		st.Index = idx
		t := e.queue[idx].track
		st.Track = &t
		if ok {
			st.Position = OutputFormat.DurationOfFrames(frames)
		}
	}
	return st
}

// Position returns the playback position within the audible track.
func (e *Engine) Position() time.Duration { return e.Status().Position }

// Close stops playback and releases the device.
func (e *Engine) Close() error {
	var err error
	e.closing.Do(func() {
		close(e.closed)
		e.ring.Close()
		e.wake()
		e.wg.Wait()
		err = e.sink.Close()
		e.emitMu.Lock()
		e.emitDone = true
		close(e.events)
		e.emitMu.Unlock()
	})
	return err
}

// -------------------------------------------------------- audible watcher

// sinkHealth is implemented by sinks that can fail after they were opened.
type sinkHealth interface {
	Err() error
}

// watchAudible converts ring marks into track transition events. Doing this
// here rather than in the pump means the events line up with what the listener
// actually hears, which is what scrobbling and the now-playing view need.
func (e *Engine) watchAudible() {
	defer e.wg.Done()

	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()

	health, _ := e.sink.(sinkHealth)
	sinkFailed := false
	for {
		select {
		case <-e.closed:
			return
		case <-tick.C:
		}
		e.mu.Lock()
		evs := e.syncAudibleLocked()
		e.mu.Unlock()
		e.emit(evs...)

		if health != nil && !sinkFailed {
			if err := health.Err(); err != nil {
				sinkFailed = true
				e.Pause()
				e.emit(Event{Kind: EventError, Index: -1, Err: fmt.Errorf("audio output: %w", err)})
			}
		}
	}
}

// syncAudibleLocked turns what the ring reports about the audible position
// into events and updates e.audible and, at the end of the queue, e.state.
// Caller holds mu.
func (e *Engine) syncAudibleLocked() []Event {
	var evs []Event
	for _, s := range e.ring.TakeEnded() {
		info, ok := e.seqMap[s.seq]
		if !ok || info.entry == 0 {
			continue
		}
		if s.seq != e.lastSeq {
			// Audible for less than a watcher tick: announce it late rather
			// than never.
			e.lastSeq = s.seq
			if !info.seek {
				evs = append(evs, e.trackEventLocked(EventTrackStarted, info))
			}
		}
		played := OutputFormat.DurationOfFrames(s.frames)
		if s.natural || countsAsPlayed(info.track.Duration, played) {
			ev := e.trackEventLocked(EventTrackFinished, info)
			ev.Played = played
			evs = append(evs, ev)
		}
	}

	seq, _, ok := e.ring.Current()
	if !ok || seq == e.lastSeq {
		return evs
	}
	e.lastSeq = seq
	info, known := e.seqMap[seq]
	switch {
	case !known:
	case info.entry == 0:
		// The end-of-queue mark is audible: the last track has played out.
		if e.state != StateStopped {
			e.state = StateStopped
			evs = append(evs, Event{Kind: EventQueueFinished, Index: -1})
		}
	default:
		e.audible = info.entry
		if !info.seek {
			evs = append(evs, e.trackEventLocked(EventTrackStarted, info))
		}
	}
	return evs
}

func (e *Engine) trackEventLocked(kind EventKind, info seqInfo) Event {
	t := info.track
	return Event{Kind: kind, Track: &t, Index: e.indexLocked(info.entry)}
}

// countsAsPlayed applies the common scrobbling rule to a track cut short: at
// least half of it, or four minutes, must have been heard.
func countsAsPlayed(length, played time.Duration) bool {
	threshold := 4 * time.Minute
	if length > 0 {
		threshold = min(length/2, threshold)
	}
	return played >= threshold
}

// -------------------------------------------------------------- decode pump

// source is a track opened for decoding.
type source struct {
	track  Track
	entry  uint64
	seq    uint64
	dec    Decoder
	conv   *converter
	net    *readAhead
	cancel context.CancelFunc
	err    error // terminal decode error (io.EOF at the end), set once seen
}

func (s *source) close() {
	if s.dec != nil {
		_ = s.dec.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
}

// drop closes s and forgets its read-ahead.
func (e *Engine) drop(s *source) {
	if s == nil {
		return
	}
	e.net.CompareAndSwap(s.net, nil)
	s.close()
}

// pump is the single goroutine that decodes, converts and fills the ring.
func (e *Engine) pump() {
	defer e.wg.Done()

	var cur *source
	defer func() { e.drop(cur) }()

	in := make([]float32, 4096)
	out := make([]float32, 0, 16384)
	var pending []float32 // converted samples not yet in the ring
	checked := e.gen.Load()
	recheck := false

	for {
		select {
		case <-e.closed:
			return
		default:
		}

		if req := e.takeSeek(); req != nil {
			e.drop(cur)
			cur, pending = nil, nil
			e.ring.Flush(false)
			checked = e.gen.Load()
			if req.entry == 0 {
				e.waitForWork()
				continue
			}
			cur = e.begin(req)
			continue
		}

		if g := e.gen.Load(); g != checked || recheck {
			checked = g
			var changed bool
			cur, changed, recheck = e.revalidate(cur)
			if changed {
				pending = nil
			}
		}

		if cur == nil {
			e.waitForWork()
			continue
		}

		if len(pending) > 0 {
			n, err := e.ring.Write(pending)
			pending = pending[n:]
			switch {
			case err == nil:
			case errors.Is(err, errRingClosed):
				return
			case errors.Is(err, errFlushed):
				pending = nil
				continue
			default:
				continue // kicked: re-check the queue before writing on
			}
		}

		if cur.err != nil {
			if !errors.Is(cur.err, io.EOF) {
				e.emit(Event{
					Kind: EventError, Track: &cur.track, Index: e.indexOf(cur.entry),
					Err: fmt.Errorf("decoding %q: %w", cur.track.Title, cur.err),
				})
			}
			prev := cur.entry
			e.drop(cur)
			cur = e.spliceAfter(prev, 0)
			if cur == nil {
				// The entry may have left the queue meanwhile; let the
				// re-check sort out what follows.
				recheck = true
			}
			continue
		}

		n, err := cur.dec.Read(in)
		if n > 0 {
			out = cur.conv.convert(out[:0], in[:n])
			pending = out
		}
		if err != nil {
			cur.err = err
		}
	}
}

// begin opens the target of a restart request, moving on past it if it fails.
func (e *Engine) begin(req *seekRequest) *source {
	s, err := e.openEntry(req.entry, req.offset, req.seek)
	if err == nil {
		offset := int64(req.offset.Seconds() * float64(OutputFormat.SampleRate))
		if e.markIfCurrent(s, offset) {
			return s
		}
		return nil
	}
	if errors.Is(err, errNoTrack) {
		return nil
	}
	e.emitOpenError(req.entry, err)
	if !e.backoff(1) {
		return nil
	}
	return e.spliceAfter(req.entry, 1)
}

// spliceAfter opens the successor of entry prev and marks it directly behind
// what the ring already holds. Nothing is drained and the sink is never
// touched, which is what keeps the join gapless: the ring still holds several
// seconds of audio while the next stream opens.
//
// An entry that fails to open is skipped after a growing backoff, so a single
// unplayable file cannot wedge the queue; once every entry has failed in a
// row the queue is ended. failures counts failures already seen in this run.
// It returns nil at the end of the queue, when a restart request supersedes
// the splice, or when prev has left the queue.
func (e *Engine) spliceAfter(prev uint64, failures int) *source {
	for {
		e.mu.Lock()
		if e.seekReq != nil {
			e.mu.Unlock()
			return nil
		}
		next, st := e.successorLocked(prev, failures > 0)
		if st == succMissing {
			e.mu.Unlock()
			return nil
		}
		gaveUp := st == succFound && failures > 0 && failures >= len(e.queue)
		if st == succEnd || gaveUp {
			e.markEndLocked()
			e.mu.Unlock()
			if gaveUp {
				e.emit(Event{Kind: EventError, Index: -1, Err: errAllFailed})
			}
			return nil
		}
		e.mu.Unlock()

		s, err := e.openEntry(next, 0, false)
		if err == nil {
			if e.markIfCurrent(s, 0) {
				return s
			}
			return nil
		}
		if !errors.Is(err, errNoTrack) {
			failures++
			e.emitOpenError(next, err)
			if !e.backoff(failures) {
				return nil
			}
		}
		prev = next
	}
}

// markIfCurrent marks s in the ring unless a restart request arrived while it
// was being opened, in which case s is dropped.
func (e *Engine) markIfCurrent(s *source, offsetFrames int64) bool {
	e.mu.Lock()
	superseded := e.seekReq != nil
	if !superseded {
		e.ring.Mark(s.seq, offsetFrames)
		e.net.Store(s.net)
	}
	e.mu.Unlock()
	if superseded {
		e.drop(s)
	}
	return !superseded
}

// markEndLocked queues the end-of-queue mark. Caller holds mu.
func (e *Engine) markEndLocked() {
	e.seq++
	e.seqMap[e.seq] = seqInfo{}
	e.ring.Mark(e.seq, 0)
}

// revalidate checks that every mark buffered behind the audible one is still
// the successor of the mark before it. After a queue or repeat edit that is no
// longer guaranteed; the first stale mark and everything behind it are cut
// from the ring and the right successor is spliced in instead. It returns the
// source to keep decoding, whether it changed, and whether to check again.
func (e *Engine) revalidate(cur *source) (*source, bool, bool) {
	e.mu.Lock()
	if e.seekReq != nil {
		e.mu.Unlock()
		return cur, false, false
	}
	anchor, ok, pending := e.ring.Chain()
	if !ok {
		e.mu.Unlock()
		return cur, false, false
	}
	prev := e.seqMap[anchor].entry
	var stale uint64
	for _, s := range pending {
		want, st := e.successorLocked(prev, false)
		got := e.seqMap[s].entry
		if st == succMissing {
			break // cannot judge; the restart that removed prev handles it
		}
		if (st == succEnd) != (got == 0) || (st == succFound && got != want) {
			stale = s
			break
		}
		prev = got
	}
	// Nothing being decoded behind a real entry: the splice was abandoned
	// because its entry left the queue.
	idleTail := stale == 0 && cur == nil && prev != 0
	if stale != 0 && !e.ring.Truncate(stale) {
		// The stale mark became audible meanwhile; look again.
		e.mu.Unlock()
		return cur, false, true
	}
	e.mu.Unlock()

	switch {
	case stale != 0:
		// cur always decodes the newest mark, so it was cut as well.
		e.drop(cur)
		return e.spliceAfter(prev, 0), true, false
	case idleTail:
		return e.spliceAfter(prev, 0), true, false
	}
	return cur, false, false
}

// successor outcomes.
const (
	succFound = iota
	succEnd
	succMissing
)

// successorLocked resolves the entry that follows prev, honouring repeat
// mode. After a failed open, repeat-one moves on rather than retrying the
// same entry. Caller holds mu.
func (e *Engine) successorLocked(prev uint64, afterFailure bool) (uint64, int) {
	idx := e.indexLocked(prev)
	if idx < 0 {
		return 0, succMissing
	}
	next := idx + 1
	switch {
	case e.repeat == RepeatOne && !afterFailure:
		next = idx
	case next >= len(e.queue):
		if e.repeat == RepeatOff {
			return 0, succEnd
		}
		next = 0
	}
	return e.queue[next].id, succFound
}

var (
	errNoTrack   = errors.New("audio: no such queue entry")
	errAllFailed = errors.New("audio: no track in the queue could be opened")
)

// Backoff between failed opens, doubling per failure.
const (
	openRetryBase = 250 * time.Millisecond
	openRetryMax  = 5 * time.Second
)

// backoff waits before the next open attempt. A user action cuts it short so
// its request is seen promptly. It reports false once the engine closes.
func (e *Engine) backoff(failures int) bool {
	d := openRetryMax
	if failures <= 5 {
		d = min(openRetryBase<<(failures-1), openRetryMax)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-e.resume:
	case <-e.closed:
		return false
	}
	return true
}

func (e *Engine) emitOpenError(id uint64, err error) {
	e.emit(Event{Kind: EventError, Index: e.indexOf(id), Err: err})
}

func (e *Engine) indexOf(id uint64) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.indexLocked(id)
}

// takeSeek consumes a pending restart request.
func (e *Engine) takeSeek() *seekRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.seekReq
	e.seekReq = nil
	return r
}

// waitForWork blocks until something changes or the engine closes.
func (e *Engine) waitForWork() {
	select {
	case <-e.resume:
	case <-e.closed:
	}
}

// openEntry opens queue entry id, starting at offset.
func (e *Engine) openEntry(id uint64, offset time.Duration, seek bool) (*source, error) {
	e.mu.Lock()
	idx := e.indexLocked(id)
	if idx < 0 {
		e.mu.Unlock()
		return nil, errNoTrack
	}
	t := e.queue[idx].track
	e.seq++
	seq := e.seq
	e.seqMap[seq] = seqInfo{entry: id, track: t, seek: seek}
	e.pruneSeqLocked(seq)
	e.mu.Unlock()

	if t.Open == nil {
		return nil, fmt.Errorf("%w: track %q has no opener", ErrUnsupportedFormat, t.ID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan struct{})
	go func() {
		select {
		case <-e.closed:
			cancel()
		case <-stop:
		}
	}()
	release := func() {
		close(stop)
		cancel()
	}

	rc, hint, err := t.Open(ctx, offset)
	if err != nil {
		release()
		return nil, fmt.Errorf("opening %q: %w", t.Title, err)
	}

	net := newReadAhead(rc, e.cfg.NetworkBuffer)
	dec, err := Open(&closerFunc{Reader: net, closeFn: net.Close}, hint)
	if err != nil {
		release()
		_ = net.Close()
		return nil, fmt.Errorf("decoding %q: %w", t.Title, err)
	}

	return &source{
		track:  t,
		entry:  id,
		seq:    seq,
		dec:    dec,
		conv:   newConverter(dec.Format(), OutputFormat),
		net:    net,
		cancel: release,
	}, nil
}

// pruneSeqLocked keeps the mark map from growing over a long session.
func (e *Engine) pruneSeqLocked(current uint64) {
	if len(e.seqMap) <= 256 {
		return
	}
	for k := range e.seqMap {
		if k+128 < current {
			delete(e.seqMap, k)
		}
	}
}

// closerFunc attaches a custom Close to a Reader.
type closerFunc struct {
	io.Reader
	closeFn func() error
}

func (c *closerFunc) Close() error { return c.closeFn() }
