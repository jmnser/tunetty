package ui

import "github.com/charmbracelet/lipgloss"

// Theme holds every style the interface uses.
type Theme struct {
	Accent    lipgloss.Color
	Subtle    lipgloss.Color
	Faint     lipgloss.Color
	Error     lipgloss.Color
	Base      lipgloss.Style
	Header    lipgloss.Style
	TabActive lipgloss.Style
	TabIdle   lipgloss.Style
	Title     lipgloss.Style
	Artist    lipgloss.Style
	Album     lipgloss.Style
	Row       lipgloss.Style
	RowSel    lipgloss.Style
	RowPlay   lipgloss.Style
	Dim       lipgloss.Style
	Bar       lipgloss.Style
	BarFill   lipgloss.Style
	BarEmpty  lipgloss.Style
	Status    lipgloss.Style
	ErrorMsg  lipgloss.Style
	Prompt    lipgloss.Style
	Match     lipgloss.Style
	Badge     lipgloss.Style
	Border    lipgloss.Style
}

// palette is the set of base colours a theme is built from.
type palette struct{ accent, subtle, faint, err lipgloss.Color }

var (
	darkPalette  = palette{accent: "#c792ea", subtle: "#8f8f9d", faint: "#5c5c6b", err: "#ff5f87"}
	lightPalette = palette{accent: "#8839ef", subtle: "#5c5f77", faint: "#9ca0b0", err: "#d20f39"}
)

// NewTheme builds a theme. name is "dark", "light" or "auto", which asks the
// terminal for its background colour. accent overrides the highlight colour
// when it is a non-empty hex value.
func NewTheme(name, accent string) Theme {
	p := darkPalette
	switch name {
	case "light":
		p = lightPalette
	case "dark":
	default:
		if !lipgloss.HasDarkBackground() {
			p = lightPalette
		}
	}
	acc := p.accent
	if accent != "" {
		acc = lipgloss.Color(accent)
	}
	subtle, faint, errc := p.subtle, p.faint, p.err

	return Theme{
		Accent: acc,
		Subtle: subtle,
		Faint:  faint,
		Error:  errc,

		Base:      lipgloss.NewStyle(),
		Header:    lipgloss.NewStyle().Bold(true).Foreground(acc),
		TabActive: lipgloss.NewStyle().Bold(true).Foreground(acc).Underline(true),
		TabIdle:   lipgloss.NewStyle().Foreground(subtle),
		Title:     lipgloss.NewStyle().Bold(true),
		Artist:    lipgloss.NewStyle().Foreground(acc),
		Album:     lipgloss.NewStyle().Foreground(subtle),
		Row:       lipgloss.NewStyle(),
		RowSel:    lipgloss.NewStyle().Bold(true).Foreground(acc),
		RowPlay:   lipgloss.NewStyle().Foreground(acc),
		Dim:       lipgloss.NewStyle().Foreground(faint),
		Bar:       lipgloss.NewStyle().Foreground(acc),
		BarFill:   lipgloss.NewStyle().Foreground(acc),
		BarEmpty:  lipgloss.NewStyle().Foreground(faint),
		Status:    lipgloss.NewStyle().Foreground(subtle),
		ErrorMsg:  lipgloss.NewStyle().Foreground(errc),
		Prompt:    lipgloss.NewStyle().Bold(true).Foreground(acc),
		Match:     lipgloss.NewStyle().Bold(true).Foreground(acc),
		Badge:     lipgloss.NewStyle().Foreground(faint),
		Border:    lipgloss.NewStyle().Foreground(faint),
	}
}
