//go:build unix

package art

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// queryTerminal asks the terminal what it supports.
//
// It briefly puts the tty into raw mode to read the replies, so it must run
// before the TUI takes over the terminal. Every step degrades to "unknown"
// rather than failing: a terminal that ignores the queries simply falls back
// to environment based detection. With inTmux set, every query is wrapped in
// tmux passthrough so the outer terminal answers instead of tmux.
func queryTerminal(timeout time.Duration, inTmux bool) *queryResult {
	// O_NONBLOCK lets the Go runtime poller drive the read deadline instead of
	// blocking forever on a terminal that never answers.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		return nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil
	}
	defer func() { _ = term.Restore(fd, state) }()
	// Replies that straggle in after the deadline would otherwise reach the
	// TUI as keystrokes. Runs before Restore, while still in raw mode.
	defer flushInput(fd)

	wrap := Capabilities{InTmux: inTmux}.Wrap

	// The kitty query is answered only by terminals implementing the kitty
	// graphics protocol. CSI 16 t reports the cell size. Primary device
	// attributes is answered by every terminal and terminals reply in order,
	// so it is the sentinel: once its reply is in, every earlier one is too.
	var out strings.Builder
	out.WriteString(wrap("\x1b_Gi=4294967295,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"))
	out.WriteString(wrap("\x1b[16t"))
	out.WriteString(wrap("\x1b[c"))
	if _, err := tty.WriteString(out.String()); err != nil {
		return nil
	}

	resp := readReplies(tty, timeout)
	if len(resp) == 0 {
		return nil
	}
	return parseQuery(resp)
}

// readReplies collects terminal responses until the device attributes reply
// arrives or the deadline passes.
func readReplies(tty *os.File, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	_ = tty.SetReadDeadline(deadline)
	defer func() { _ = tty.SetReadDeadline(time.Time{}) }()

	var (
		sb  strings.Builder
		buf = make([]byte, 512)
	)
	for time.Now().Before(deadline) {
		n, err := tty.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
			if daComplete(sb.String()) {
				break
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		break // deadline exceeded, EOF or a real error
	}
	return sb.String()
}

// daComplete reports whether a primary device attributes reply has been fully
// received. That reply is requested last, so its arrival means we have all of
// the answers the terminal intends to send.
func daComplete(s string) bool {
	i := strings.Index(s, "\x1b[?")
	return i >= 0 && strings.IndexByte(s[i:], 'c') >= 0
}
