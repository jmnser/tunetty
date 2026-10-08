package audio

import (
	"io"
	"sync"
)

// readAhead pumps an upstream reader into a bounded buffer on its own
// goroutine. Decoders therefore never make a network round trip while the
// decode pump is trying to keep the audio ring full.
type readAhead struct {
	mu      sync.Mutex
	cond    sync.Cond
	buf     []byte
	r, w, n int
	err     error
	closed  bool

	src  io.ReadCloser
	once sync.Once
}

func newReadAhead(src io.ReadCloser, size int) *readAhead {
	if size < 64<<10 {
		size = 64 << 10
	}
	ra := &readAhead{buf: make([]byte, size), src: src}
	ra.cond.L = &ra.mu
	go ra.pump()
	return ra
}

func (a *readAhead) pump() {
	chunk := make([]byte, 32<<10)
	for {
		n, err := a.src.Read(chunk)
		if n > 0 {
			if !a.deposit(chunk[:n]) {
				return
			}
		}
		if err != nil {
			a.mu.Lock()
			if a.err == nil {
				a.err = err
			}
			a.cond.Broadcast()
			a.mu.Unlock()
			return
		}
	}
}

// deposit blocks until p fits in the buffer. It reports false once closed.
func (a *readAhead) deposit(p []byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(p) > 0 {
		for a.n == len(a.buf) && !a.closed {
			a.cond.Wait()
		}
		if a.closed {
			return false
		}
		free := len(a.buf) - a.n
		k := min(len(p), free)
		for i := range k {
			a.buf[a.w] = p[i]
			a.w = (a.w + 1) % len(a.buf)
		}
		a.n += k
		p = p[k:]
		a.cond.Broadcast()
	}
	return true
}

func (a *readAhead) Read(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.n == 0 {
		if a.closed {
			return 0, io.ErrClosedPipe
		}
		if a.err != nil {
			return 0, a.err
		}
		a.cond.Wait()
	}
	n := min(a.n, len(p))
	for i := range n {
		p[i] = a.buf[a.r]
		a.r = (a.r + 1) % len(a.buf)
	}
	a.n -= n
	a.cond.Broadcast()
	return n, nil
}

// Buffered returns the number of bytes waiting to be decoded.
func (a *readAhead) Buffered() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func (a *readAhead) Close() error {
	a.mu.Lock()
	a.closed = true
	a.cond.Broadcast()
	a.mu.Unlock()
	var err error
	a.once.Do(func() { err = a.src.Close() })
	return err
}
