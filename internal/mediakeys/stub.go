//go:build !linux && !darwin

package mediakeys

import "github.com/jmnser/tunetty/internal/audio"

// Start does nothing: this platform has no media key integration yet.
func Start(*audio.Engine) (stop func(), err error) {
	return func() {}, nil
}
