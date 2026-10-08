package audio

import (
	"errors"
	"sync"
)

// Errors returned by ring writes.
var (
	// errRingClosed is returned after Close.
	errRingClosed = errors.New("audio: ring closed")
	// errFlushed tells a producer its data was discarded by a concurrent Flush.
	errFlushed = errors.New("audio: ring flushed")
	// errKicked tells a blocked producer to look at its surroundings before
	// writing the rest of its data.
	errKicked = errors.New("audio: ring writer kicked")
)

// mark records the sample position at which a new queue item becomes audible.
// Marks travel through the ring alongside the PCM so the UI can report the
// track that is actually coming out of the speakers rather than the one being
// decoded ahead of it.
type mark struct {
	pos    int64 // absolute write position (in samples) where the item starts
	seq    uint64
	offset int64 // frames already skipped in the source (seek start position)
}

// segment is a finished stretch of audible output for one mark.
type segment struct {
	seq uint64
	// frames is the position within the source the listener reached.
	frames int64
	// natural is true when the item played to its end, false when a Flush
	// cut it short.
	natural bool
}

// maxSegments bounds the segment log should nobody drain it.
const maxSegments = 64

// ring is a blocking single producer / single consumer sample FIFO.
//
// The decode pump is free to block on network I/O while filling it; the audio
// callback drains it without ever blocking, emitting silence on underrun.
type ring struct {
	mu       sync.Mutex
	notFull  sync.Cond
	notEmpty sync.Cond

	buf  []float32
	r, w int
	n    int // samples currently stored

	closed  bool
	flushed uint64 // bumped on Flush so writers can abandon stale data
	kicks   uint64 // bumped on Kick to unblock a waiting writer

	written int64 // absolute samples written
	read    int64 // absolute samples read

	marks  []mark
	cur    mark
	hasCur bool
	ended  []segment
}

func newRing(capacity int) *ring {
	r := &ring{buf: make([]float32, capacity)}
	r.notFull.L = &r.mu
	r.notEmpty.L = &r.mu
	return r
}

// Write blocks until every sample of p is stored, and returns how many were.
// It stops early with errFlushed after a Flush, errKicked after a Kick and
// errRingClosed after Close.
func (b *ring) Write(p []float32) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	gen, kicks := b.flushed, b.kicks
	total := 0
	for len(p) > 0 {
		for b.n == len(b.buf) && !b.closed && b.flushed == gen && b.kicks == kicks {
			b.notFull.Wait()
		}
		switch {
		case b.closed:
			return total, errRingClosed
		case b.flushed != gen:
			return total, errFlushed
		case b.kicks != kicks:
			return total, errKicked
		}
		free := len(b.buf) - b.n
		chunk := p
		if len(chunk) > free {
			chunk = chunk[:free]
		}
		for _, v := range chunk {
			b.buf[b.w] = v
			b.w = (b.w + 1) % len(b.buf)
		}
		b.n += len(chunk)
		b.written += int64(len(chunk))
		total += len(chunk)
		p = p[len(chunk):]
		b.notEmpty.Signal()
	}
	return total, nil
}

// Kick makes a writer blocked on a full ring return errKicked.
func (b *ring) Kick() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.kicks++
	b.notFull.Broadcast()
}

// Mark records that the next sample written starts a new queue item.
func (b *ring) Mark(seq uint64, offsetFrames int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := mark{pos: b.written, seq: seq, offset: offsetFrames}
	if !b.hasCur && b.n == 0 {
		// Nothing buffered: the item is audible immediately.
		b.cur, b.hasCur = m, true
		return
	}
	b.marks = append(b.marks, m)
	b.advanceMarks()
}

// Read copies at most len(p) samples out, never blocking. Missing samples are
// left untouched by the ring and reported through n so the caller can pad.
func (b *ring) Read(p []float32) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(b.n, len(p))
	for i := range n {
		p[i] = b.buf[b.r]
		b.r = (b.r + 1) % len(b.buf)
	}
	b.n -= n
	b.read += int64(n)
	b.advanceMarks()
	if n > 0 {
		b.notFull.Signal()
	}
	return n
}

// advanceMarks promotes every mark the consumer has now passed, logging the
// item each one replaces as naturally finished. Caller holds mu.
func (b *ring) advanceMarks() {
	for len(b.marks) > 0 && b.marks[0].pos <= b.read {
		m := b.marks[0]
		if b.hasCur {
			b.logSegment(segment{seq: b.cur.seq, frames: b.cur.offset + b.framesSince(b.cur.pos, m.pos), natural: true})
		}
		b.cur, b.hasCur = m, true
		b.marks = b.marks[1:]
	}
}

func (b *ring) framesSince(from, to int64) int64 {
	return max(to-from, 0) / int64(OutputFormat.Channels)
}

// logSegment appends to the bounded segment log. Caller holds mu.
func (b *ring) logSegment(s segment) {
	if len(b.ended) >= maxSegments {
		b.ended = b.ended[1:]
	}
	b.ended = append(b.ended, s)
}

// TakeEnded returns and clears the log of finished segments, oldest first.
func (b *ring) TakeEnded() []segment {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.ended
	b.ended = nil
	return s
}

// Current returns the queue item currently audible and how many frames of it
// have been played.
func (b *ring) Current() (seq uint64, frames int64, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.hasCur {
		return 0, 0, false
	}
	return b.cur.seq, b.cur.offset + b.framesSince(b.cur.pos, b.read), true
}

// Chain returns the audible mark and the marks pending behind it, in order.
func (b *ring) Chain() (cur uint64, ok bool, pending []uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending = make([]uint64, len(b.marks))
	for i, m := range b.marks {
		pending[i] = m.seq
	}
	return b.cur.seq, b.hasCur, pending
}

// Truncate drops the pending mark seq and every sample written from it on,
// so a stale prefetched item can be replaced without disturbing what plays
// before it. It reports false once the mark has become audible.
func (b *ring) Truncate(seq uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, m := range b.marks {
		if m.seq != seq {
			continue
		}
		drop := int(b.written - m.pos)
		b.n -= drop
		b.w = (b.w - drop%len(b.buf) + len(b.buf)) % len(b.buf)
		b.written = m.pos
		b.marks = b.marks[:i]
		b.notFull.Broadcast()
		return true
	}
	return false
}

// Capacity returns the ring size in samples.
func (b *ring) Capacity() int { return len(b.buf) }

// Buffered returns the number of buffered frames.
func (b *ring) Buffered() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n / OutputFormat.Channels
}

// Flush drops all buffered audio and wakes a blocked writer so it can bail out.
// With logEnd the item audible until now is logged as cut short; a seek within
// the same item passes false.
func (b *ring) Flush(logEnd bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if logEnd && b.hasCur {
		b.logSegment(segment{seq: b.cur.seq, frames: b.cur.offset + b.framesSince(b.cur.pos, b.read)})
	}
	b.r, b.w, b.n = 0, 0, 0
	// Mark positions are measured against the absolute counters, so they
	// must agree again once the buffer is empty.
	b.written = b.read
	b.marks = nil
	b.hasCur = false
	b.flushed++
	b.notFull.Broadcast()
	b.notEmpty.Broadcast()
}

// Close unblocks all waiters permanently.
func (b *ring) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.notFull.Broadcast()
	b.notEmpty.Broadcast()
}
