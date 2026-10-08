package art

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// queryResult is what a terminal capability query returned.
type queryResult struct {
	kitty      bool
	sixel      bool
	cellWidth  int
	cellHeight int
}

// Capabilities describes what the terminal can display.
type Capabilities struct {
	// Protocol is the best graphics protocol available.
	Protocol Protocol
	// CellWidth and CellHeight are the pixel dimensions of one character cell.
	// Zero means unknown.
	CellWidth  int
	CellHeight int
	// TrueColor reports 24 bit colour support, used by the half block renderer.
	TrueColor bool
	// InTmux reports whether output goes through tmux, which needs every
	// graphics escape wrapped in a passthrough sequence.
	InTmux bool
	// PassthroughReady reports whether tmux is configured to forward escapes.
	PassthroughReady bool
	// Notes records how detection reached its conclusion, for tunetty --doctor.
	Notes []string
}

// DetectOptions tunes capability detection.
type DetectOptions struct {
	// Query enables interactive terminal queries. Disable for non interactive
	// runs or when the terminal is known to mishandle them.
	Query bool
	// Timeout bounds how long to wait for query responses.
	Timeout time.Duration
	// EnableTmuxPassthrough turns on allow-passthrough for the current tmux
	// pane. It is pane scoped and disappears when the pane closes.
	EnableTmuxPassthrough bool
}

// Detect inspects the environment and, when permitted, queries the terminal to
// determine which graphics protocol to use.
func Detect(o DetectOptions) Capabilities {
	if o.Timeout <= 0 {
		o.Timeout = 250 * time.Millisecond
	}

	caps := Capabilities{Protocol: ProtocolHalfBlock}
	caps.TrueColor = isTrueColor()
	caps.InTmux = os.Getenv("TMUX") != ""

	if caps.InTmux {
		caps.PassthroughReady = tmuxPassthroughEnabled()
		if !caps.PassthroughReady && o.EnableTmuxPassthrough {
			if enableTmuxPassthrough() {
				caps.PassthroughReady = true
				caps.Notes = append(caps.Notes, "enabled tmux allow-passthrough for this pane")
			}
		}
		if !caps.PassthroughReady {
			caps.Notes = append(caps.Notes,
				"tmux allow-passthrough is off; run 'tmux set -p allow-passthrough on' for graphics")
		}
	}

	// Environment based detection first: it is instant and correct for the
	// terminals that advertise themselves.
	if p, why := protocolFromEnv(); p != "" {
		caps.Protocol = p
		caps.Notes = append(caps.Notes, "protocol from environment: "+why)
	}

	// Inside tmux every query goes through passthrough so that only the outer
	// terminal answers: tmux's own device attributes describe tmux, not the
	// terminal that would draw the image. Without passthrough there is nobody
	// trustworthy to ask.
	if o.Query && (!caps.InTmux || caps.PassthroughReady) {
		if q := queryTerminal(o.Timeout, caps.InTmux); q != nil {
			if q.cellWidth > 0 && q.cellHeight > 0 {
				caps.CellWidth, caps.CellHeight = q.cellWidth, q.cellHeight
			}
			switch {
			case q.kitty:
				caps.Protocol = ProtocolKitty
				caps.Notes = append(caps.Notes, "terminal answered the kitty graphics query")
			case q.sixel && !caps.Protocol.Graphical():
				caps.Protocol = ProtocolSixel
				caps.Notes = append(caps.Notes, "terminal advertised sixel in its device attributes")
			}
		}
	}

	if caps.CellWidth == 0 || caps.CellHeight == 0 {
		if w, h, ok := cellSizeFromIoctl(); ok {
			caps.CellWidth, caps.CellHeight = w, h
			caps.Notes = append(caps.Notes, "cell size from TIOCGWINSZ")
		}
	}
	if caps.CellWidth == 0 || caps.CellHeight == 0 {
		// A 1:2 cell is the usual terminal aspect ratio and keeps covers from
		// looking stretched when the real size is unavailable.
		caps.CellWidth, caps.CellHeight = 10, 20
		caps.Notes = append(caps.Notes, "cell size unknown, assuming 10x20 pixels")
	}

	// Graphics through tmux need passthrough; without it the escapes would be
	// swallowed and the pane left blank, so fall back to text rendering.
	if caps.InTmux && !caps.PassthroughReady && caps.Protocol.Graphical() {
		caps.Protocol = ProtocolHalfBlock
		caps.Notes = append(caps.Notes, "falling back to half blocks inside tmux without passthrough")
	}
	return caps
}

// protocolFromEnv identifies terminals that can be recognised without a query.
func protocolFromEnv() (Protocol, string) {
	env := os.Getenv
	termProgram := env("TERM_PROGRAM")
	termVar := env("TERM")

	switch {
	case env("KITTY_WINDOW_ID") != "", strings.Contains(termVar, "kitty"):
		return ProtocolKitty, "kitty"
	case termProgram == "ghostty", env("GHOSTTY_RESOURCES_DIR") != "":
		return ProtocolKitty, "ghostty"
	case termProgram == "WezTerm", env("WEZTERM_EXECUTABLE") != "":
		// WezTerm implements both; kitty's protocol handles resizing better.
		return ProtocolKitty, "wezterm"
	case env("KONSOLE_VERSION") != "":
		return ProtocolKitty, "konsole"
	case termProgram == "iTerm.app", env("LC_TERMINAL") == "iTerm2":
		return ProtocolITerm, "iterm2"
	// VS Code is deliberately absent: its image support is off unless
	// terminal.integrated.enableImages is set, which the environment does not
	// reveal. When enabled it advertises sixel in its device attributes, so
	// the query below picks it up; otherwise half blocks are the safe choice.
	case termProgram == "mintty":
		return ProtocolITerm, "mintty"
	case strings.Contains(termVar, "foot"), strings.Contains(termVar, "mlterm"), strings.Contains(termVar, "yaft"):
		return ProtocolSixel, termVar
	case strings.Contains(termVar, "contour"):
		return ProtocolSixel, "contour"
	}
	return "", ""
}

func isTrueColor() bool {
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" {
		return true
	}
	t := os.Getenv("TERM")
	return strings.Contains(t, "truecolor") || strings.Contains(t, "direct")
}

// ---------------------------------------------------------------- tmux glue

func tmuxPassthroughEnabled() bool {
	out, err := runTmux("show", "-Apv", "allow-passthrough")
	if err != nil {
		return false
	}
	v := strings.TrimSpace(out)
	return v == "on" || v == "all"
}

func enableTmuxPassthrough() bool {
	// -p scopes the option to this pane only, so the user's session and global
	// tmux configuration are left untouched.
	_, err := runTmux("set", "-p", "allow-passthrough", "on")
	return err == nil
}

func runTmux(args ...string) (string, error) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, args...) //nolint:gosec // fixed argument list, binary resolved from PATH
	out, err := cmd.Output()
	return string(out), err
}

// Wrap prepares an escape sequence for the current terminal, adding the tmux
// passthrough envelope when needed.
func (c Capabilities) Wrap(seq string) string {
	if !c.InTmux || seq == "" {
		return seq
	}
	// tmux passthrough: DCS tmux; <payload with every ESC doubled> ST
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

func parseQuery(s string) *queryResult {
	q := &queryResult{}

	// Kitty answers a graphics query with APC _G...;OK ST.
	if i := strings.Index(s, "\x1b_G"); i >= 0 {
		end := strings.Index(s[i:], "\x1b\\")
		if end < 0 {
			end = len(s) - i
		}
		if strings.Contains(s[i:i+end], ";OK") {
			q.kitty = true
		}
	}

	// CSI 6 ; height ; width t reports the cell size in pixels.
	if i := strings.Index(s, "\x1b[6;"); i >= 0 {
		if j := strings.IndexByte(s[i:], 't'); j > 0 {
			parts := strings.Split(s[i+4:i+j], ";")
			if len(parts) == 2 {
				h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
				w, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err1 == nil && err2 == nil && w > 0 && h > 0 && w < 100 && h < 200 {
					q.cellWidth, q.cellHeight = w, h
				}
			}
		}
	}

	// Primary device attributes: attribute 4 means sixel support.
	if i := strings.Index(s, "\x1b[?"); i >= 0 {
		if j := strings.IndexByte(s[i:], 'c'); j > 0 {
			for f := range strings.SplitSeq(s[i+3:i+j], ";") {
				if strings.TrimSpace(f) == "4" {
					q.sixel = true
				}
			}
		}
	}
	return q
}
