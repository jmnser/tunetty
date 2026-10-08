//go:build unix && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package art

// flushInput is a no-op where no portable input flush is available.
func flushInput(int) {}
