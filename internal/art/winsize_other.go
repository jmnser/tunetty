//go:build !unix

package art

// Windows consoles do not report a pixel geometry, so callers fall back to the
// assumed cell aspect ratio.
func cellSizeFromIoctl() (int, int, bool) { return 0, 0, false }
