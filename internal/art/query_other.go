//go:build !unix

package art

import "time"

// Windows consoles have no /dev/tty to query, so detection relies entirely on
// the environment.
func queryTerminal(time.Duration, bool) *queryResult { return nil }
