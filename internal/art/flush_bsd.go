//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package art

import "golang.org/x/sys/unix"

// fread selects the input queue for TIOCFLUSH; x/sys does not export it.
const fread = 0x1

// flushInput discards terminal input that has been received but not read.
func flushInput(fd int) { _ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread) }
