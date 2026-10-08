package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ResolvePassword returns the account password, running password_command when
// one is configured so the secret can live in a password manager rather than
// in a plaintext file. TUNETTY_PASSWORD takes precedence because Load clears
// the command when it is set.
func (c Config) ResolvePassword(ctx context.Context) (string, error) {
	if cmdline := strings.TrimSpace(c.Server.PasswordCommand); cmdline != "" {
		return runPasswordCommand(ctx, cmdline)
	}
	return c.Server.Password, nil
}

// runPasswordCommand executes cmdline through the system shell (/bin/sh, or
// cmd on Windows) and returns its first line of output.
func runPasswordCommand(ctx context.Context, cmdline string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// A shell is used deliberately: password managers are normally invoked as
	// a pipeline, and this value comes from the user's own config file.
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == goosWindows {
		shell, flag = "cmd", "/C"
	}
	cmd := exec.CommandContext(ctx, shell, flag, cmdline) //nolint:gosec // user supplied by design
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("config: password_command failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("config: password_command failed: %w", err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("config: password_command produced no output")
	}
	return line, nil
}
