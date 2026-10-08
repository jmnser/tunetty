package art

import "golang.org/x/sys/unix"

// flushInput discards terminal input that has been received but not read.
func flushInput(fd int) { _ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }
