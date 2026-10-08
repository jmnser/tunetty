package art

import (
	"image"
	"image/color"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testImage(w, h int) *Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 128, A: 255})
		}
	}
	return &Image{Src: img}
}

func noiseImage(w, h int) *Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 255})
		}
	}
	return &Image{Src: img}
}

// Every protocol must produce a block that occupies exactly the requested
// cells, otherwise the surrounding TUI layout shifts. Graphics escapes have to
// measure zero columns wide for that to hold.
func TestRenderBlockGeometry(t *testing.T) {
	t.Parallel()
	const cols, rows = 24, 12
	caps := Capabilities{CellWidth: 10, CellHeight: 20, TrueColor: true}

	for _, p := range []Protocol{ProtocolKitty, ProtocolITerm, ProtocolSixel, ProtocolHalfBlock} {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			out, err := NewRenderer(p, caps).Render(testImage(200, 200), cols, rows)
			require.NoError(t, err)
			require.Equal(t, rows, out.Rows)
			require.Len(t, out.Lines, rows)
			for i, line := range out.Lines {
				assert.Equal(t, cols, lipgloss.Width(line), "line %d", i)
			}
		})
	}
}

// A square cover in a wide area must stay square rather than stretching.
func TestRenderPreservesAspect(t *testing.T) {
	t.Parallel()
	r := NewRenderer(ProtocolHalfBlock, Capabilities{CellWidth: 10, CellHeight: 20, TrueColor: true})
	cols, rows := r.fit(image.Rect(0, 0, 100, 100), 80, 10)
	// Cells are twice as tall as wide, so a square needs twice the columns.
	assert.Equal(t, 10, rows)
	assert.Equal(t, 20, cols)
}

func TestRenderContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		proto    Protocol
		caps     Capabilities
		img      *Image
		contains []string
		excludes []string
	}{
		{
			name:  "kitty chunks and replaces only its own image",
			proto: ProtocolKitty,
			caps:  Capabilities{CellWidth: 10, CellHeight: 20},
			// Noise defeats PNG compression, forcing more than one chunk.
			img:      noiseImage(400, 400),
			contains: []string{kittyDelete, "a=T,f=100,t=d,", ",m=1;", "\x1b_Gm=1;", "\x1b_Gm=0;"},
			excludes: []string{"d=A"},
		},
		{
			name:     "sixel envelope",
			proto:    ProtocolSixel,
			caps:     Capabilities{CellWidth: 6, CellHeight: 12},
			img:      testImage(60, 60),
			contains: []string{"\x1bP0;1;0q", "#0;2;", "\x1b\\"},
		},
		{
			name:     "blocks without truecolor use 256 colours",
			proto:    ProtocolHalfBlock,
			caps:     Capabilities{CellWidth: 10, CellHeight: 20},
			img:      testImage(40, 40),
			contains: []string{"38;5;"},
			excludes: []string{"38;2;"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := NewRenderer(tc.proto, tc.caps).Render(tc.img, 40, 20)
			require.NoError(t, err)
			s := out.String()
			for _, want := range tc.contains {
				assert.Contains(t, s, want)
			}
			for _, bad := range tc.excludes {
				assert.NotContains(t, s, bad)
			}
		})
	}
}

// Uses t.Setenv, so it cannot run in parallel.
func TestClearSequence(t *testing.T) {
	tests := []struct {
		name  string
		proto Protocol
		tmux  string
		want  string
	}{
		{"kitty direct", ProtocolKitty, "", kittyDelete},
		{"kitty in tmux", ProtocolKitty, "/tmp/tmux-1/default,1,0", Capabilities{InTmux: true}.Wrap(kittyDelete)},
		{"sixel", ProtocolSixel, "", ""},
		{"blocks", ProtocolHalfBlock, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX", tc.tmux)
			assert.Equal(t, tc.want, ClearSequence(tc.proto))
		})
	}
}

func TestTmuxWrap(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "\x1bPtmux;\x1b\x1b_Gtest\x1b\x1b\\\x1b\\", Capabilities{InTmux: true}.Wrap("\x1b_Gtest\x1b\\"))
	assert.Equal(t, "\x1bX", Capabilities{}.Wrap("\x1bX"))
}

func TestParseQuery(t *testing.T) {
	t.Parallel()
	q := parseQuery("\x1b_Gi=4294967295;OK\x1b\\\x1b[6;20;10t\x1b[?62;4;22c")
	require.NotNil(t, q)
	assert.True(t, q.kitty)
	assert.True(t, q.sixel)
	assert.Equal(t, 10, q.cellWidth)
	assert.Equal(t, 20, q.cellHeight)
}

// Uses t.Setenv, so it cannot run in parallel.
func TestProtocolFromEnv(t *testing.T) {
	const termProgram = "TERM_PROGRAM"

	tests := []struct {
		name string
		env  map[string]string
		want Protocol
	}{
		{"kitty window id", map[string]string{"KITTY_WINDOW_ID": "1"}, ProtocolKitty},
		{"iterm2", map[string]string{termProgram: "iTerm.app"}, ProtocolITerm},
		// VS Code images are off by default, so the environment alone must not
		// select a graphics protocol.
		{"vscode", map[string]string{termProgram: "vscode"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{
				"TERM", termProgram, "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR",
				"WEZTERM_EXECUTABLE", "KONSOLE_VERSION", "LC_TERMINAL",
			} {
				t.Setenv(k, tc.env[k])
			}
			got, _ := protocolFromEnv()
			assert.Equal(t, tc.want, got)
		})
	}
}
