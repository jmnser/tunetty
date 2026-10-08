package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jmnser/tunetty/internal/remote"
)

// runRemote handles the subcommands that talk to a running instance. It
// reports whether args named one.
func runRemote(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "status":
		// Meant for tmux's status line: print nothing rather than an error
		// when no player is running, so the status line just stays empty.
		s, err := remote.Send(remote.CmdStatus)
		if errors.Is(err, remote.ErrNotRunning) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		if line := remote.Format(s); line != "" {
			fmt.Println(line)
		}
		return true, nil

	case "ctl":
		usage := "usage: tunetty ctl <" + strings.Join(remote.Commands, "|") + ">"
		if len(args) != 2 || !slices.Contains(remote.Commands, args[1]) {
			return true, errors.New(usage)
		}
		_, err := remote.Send(args[1])
		return true, err
	}
	return false, nil
}
