package ui

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ctlCommand finds the `tunetty ctl <command>` a tmux binding runs.
var ctlCommand = regexp.MustCompile(`tunetty ctl ([a-z-]+)`)

// tmuxBindings returns the tmux key bindings that drive tunetty, read from
// the running tmux server so changes to tmux.conf show up. It is empty
// outside tmux or when no binding mentions tunetty.
func tmuxBindings() []helpEntry {
	if os.Getenv("TMUX") == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-keys").Output()
	if err != nil {
		return nil
	}

	var entries []helpEntry
	popup := false
	for line := range strings.Lines(string(out)) {
		if !strings.Contains(line, "tunetty") {
			continue
		}
		f := strings.Fields(line)
		i := indexOf(f, "-T")
		if i < 0 || i+2 >= len(f) {
			continue
		}
		table, key := f[i+1], f[i+2]
		switch table {
		case "root":
		case "prefix":
			key = "prefix + " + key
		default:
			key = table + ": " + key
		}
		desc := ""
		switch {
		case strings.Contains(line, "display-popup"):
			desc, popup = "open / close the tunetty popup", true
		case ctlCommand.MatchString(line):
			desc = strings.ReplaceAll(ctlCommand.FindStringSubmatch(line)[1], "-", " ")
		default:
			continue
		}
		entries = append(entries, helpEntry{keys: key, desc: desc})
	}
	if popup {
		entries = append(entries, helpEntry{keys: "prefix + d", desc: "hide the popup, keep playing"})
	}
	return entries
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
