package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/jmnser/tunetty/internal/config"
)

// KeyMap is the full set of bindings.
type KeyMap struct {
	Up         key.Binding
	Down       key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
	Home       key.Binding
	End        key.Binding
	Enter      key.Binding
	Back       key.Binding
	Tab        key.Binding
	ShiftTab   key.Binding
	PlayPause  key.Binding
	Next       key.Binding
	Prev       key.Binding
	Stop       key.Binding
	SeekFwd    key.Binding
	SeekBack   key.Binding
	VolumeUp   key.Binding
	VolumeDown key.Binding
	Mute       key.Binding
	Repeat     key.Binding
	Shuffle    key.Binding
	Queue      key.Binding
	AddQueue   key.Binding
	PlayNext   key.Binding
	Remove     key.Binding
	ClearQueue key.Binding
	Star       key.Binding
	Find       key.Binding
	Filter     key.Binding
	Refresh    key.Binding
	Help       key.Binding
	Quit       key.Binding
}

// DefaultKeys returns the stock bindings, with any configured overrides applied.
func DefaultKeys(kb config.Keybinds) KeyMap {
	k := KeyMap{
		Up:         bind([]string{"up", "k"}, "↑/k", "up"),
		Down:       bind([]string{"down", "j"}, "↓/j", "down"),
		PageUp:     bind([]string{"pgup", "ctrl+u"}, "pgup", "page up"),
		PageDown:   bind([]string{"pgdown", "ctrl+d"}, "pgdn", "page down"),
		Home:       bind([]string{"home", "g"}, "g", "top"),
		End:        bind([]string{"end", "G"}, "G", "bottom"),
		Enter:      bind([]string{"enter", "l", "right"}, "enter", "open / play"),
		Back:       bind([]string{"esc", "h", "left", "backspace"}, "esc", "back"),
		Tab:        bind([]string{"tab"}, "tab", "next view"),
		ShiftTab:   bind([]string{"shift+tab"}, "shift+tab", "previous view"),
		PlayPause:  bind([]string{" ", "p"}, "space", "play/pause"),
		Next:       bind([]string{"n", "ctrl+n"}, "n", "next track"),
		Prev:       bind([]string{"b", "ctrl+p"}, "b", "previous track"),
		Stop:       bind([]string{"s"}, "s", "stop"),
		SeekFwd:    bind([]string{"]", "shift+right"}, "]", "seek forward"),
		SeekBack:   bind([]string{"[", "shift+left"}, "[", "seek back"),
		VolumeUp:   bind([]string{"+", "="}, "+", "volume up"),
		VolumeDown: bind([]string{"-", "_"}, "-", "volume down"),
		Mute:       bind([]string{"m"}, "m", "mute"),
		Repeat:     bind([]string{"r"}, "r", "repeat mode"),
		Shuffle:    bind([]string{"z"}, "z", "shuffle queue"),
		Queue:      bind([]string{"q"}, "q", "queue view"),
		AddQueue:   bind([]string{"a"}, "a", "add to queue"),
		PlayNext:   bind([]string{"A"}, "A", "play next"),
		Remove:     bind([]string{"x", "delete"}, "x", "remove"),
		ClearQueue: bind([]string{"X"}, "X", "clear queue"),
		Star:       bind([]string{"*"}, "*", "star"),
		Find:       bind([]string{"f", "ctrl+f"}, "f", "fuzzy find"),
		Filter:     bind([]string{"/"}, "/", "filter list"),
		Refresh:    bind([]string{"R"}, "R", "refresh"),
		Help:       bind([]string{"?"}, "?", "help"),
		Quit:       bind([]string{"ctrl+c", "Q"}, "Q", "quit"),
	}

	override(&k.PlayPause, kb.PlayPause)
	override(&k.Next, kb.Next)
	override(&k.Prev, kb.Prev)
	override(&k.SeekFwd, kb.SeekFwd)
	override(&k.SeekBack, kb.SeekBack)
	override(&k.VolumeUp, kb.VolumeUp)
	override(&k.VolumeDown, kb.VolumeDown)
	override(&k.Mute, kb.Mute)
	override(&k.Find, kb.Search)
	override(&k.Quit, kb.Quit)
	return k
}

func bind(keys []string, help, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
}

// override replaces a binding's keys with a comma separated configured list.
func override(b *key.Binding, spec string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return
	}
	var keys []string
	for k := range strings.SplitSeq(spec, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return
	}
	h := b.Help()
	*b = key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], h.Desc))
}

// helpEntry is one line of the help screen.
type helpEntry struct {
	keys string
	desc string
}

// HelpSections groups bindings for the help view.
func (k KeyMap) HelpSections() []struct {
	Title   string
	Entries []helpEntry
} {
	e := func(b key.Binding) helpEntry {
		h := b.Help()
		return helpEntry{keys: strings.Join(b.Keys(), " / "), desc: h.Desc}
	}
	return []struct {
		Title   string
		Entries []helpEntry
	}{
		{"Navigation", []helpEntry{
			e(k.Up), e(k.Down), e(k.PageUp), e(k.PageDown), e(k.Home), e(k.End),
			e(k.Enter), e(k.Back), e(k.Tab), e(k.ShiftTab),
		}},
		{"Playback", []helpEntry{
			e(k.PlayPause), e(k.Next), e(k.Prev), e(k.Stop),
			e(k.SeekBack), e(k.SeekFwd), e(k.VolumeDown), e(k.VolumeUp),
			e(k.Mute), e(k.Repeat),
		}},
		{"Queue", []helpEntry{
			e(k.AddQueue), e(k.PlayNext), e(k.Remove), e(k.ClearQueue), e(k.Shuffle), e(k.Queue),
		}},
		{"Other", []helpEntry{
			e(k.Find), e(k.Filter), e(k.Star), e(k.Refresh), e(k.Help), e(k.Quit),
		}},
	}
}
