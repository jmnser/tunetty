//go:build unix

package art

import (
	"os"

	"golang.org/x/sys/unix"
)

// cellSizeFromIoctl derives the pixel size of a character cell from the
// kernel's window size, which most terminal emulators keep up to date.
func cellSizeFromIoctl() (int, int, bool) {
	for _, f := range candidateTTYs() {
		ws, err := unix.IoctlGetWinsize(int(f.fd), unix.TIOCGWINSZ)
		if f.close != nil {
			f.close()
		}
		if err != nil || ws == nil {
			continue
		}
		if ws.Xpixel == 0 || ws.Ypixel == 0 || ws.Col == 0 || ws.Row == 0 {
			continue
		}
		return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row), true
	}
	return 0, 0, false
}

type ttyRef struct {
	fd    uintptr
	close func()
}

func candidateTTYs() []ttyRef {
	refs := []ttyRef{{fd: os.Stdout.Fd()}, {fd: os.Stderr.Fd()}}
	if f, err := os.Open("/dev/tty"); err == nil {
		refs = append(refs, ttyRef{fd: f.Fd(), close: func() { _ = f.Close() }})
	}
	return refs
}
