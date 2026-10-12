// Command tunetty is a Subsonic compatible terminal music player.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jmnser/tunetty/internal/art"
	"github.com/jmnser/tunetty/internal/audio"
	"github.com/jmnser/tunetty/internal/config"
	"github.com/jmnser/tunetty/internal/mediakeys"
	"github.com/jmnser/tunetty/internal/subsonic"
	"github.com/jmnser/tunetty/internal/ui"
)

// Build information, injected by the linker.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	runMain(func() int {
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, "tunetty: "+err.Error())
			return 1
		}
		return 0
	})
}

type flags struct {
	config      string
	server      string
	user        string
	backend     string
	artProto    string
	artWork     bool
	showVersion bool
	doctor      bool
	writeConfig bool
}

func parseFlags() flags {
	var f flags
	flag.StringVar(&f.config, "config", "", "path to config file")
	flag.StringVar(&f.server, "server", "", "Subsonic server URL (overrides config)")
	flag.StringVar(&f.user, "user", "", "username (overrides config)")
	backends := strings.Join(audio.AvailableBackends(), ", ")
	flag.StringVar(&f.backend, "audio-backend", "", "audio backend: auto, "+backends)
	flag.BoolVar(&f.artWork, "art-work", false, "enable cover art (experimental)")
	flag.StringVar(&f.artProto, "art", "", "cover art protocol with -art-work: auto, kitty, iterm2, sixel, blocks")
	flag.BoolVar(&f.showVersion, "version", false, "print version and exit")
	flag.BoolVar(&f.doctor, "doctor", false, "report terminal and audio capabilities, then exit")
	flag.BoolVar(&f.writeConfig, "init", false, "write a starter config file and exit")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprint(out, "tunetty — Subsonic terminal music player\n\nusage: tunetty [flags]\n\nflags:\n")
		flag.PrintDefaults()
		_, _ = fmt.Fprint(out, "\nenvironment:\n"+
			"  TUNETTY_CONFIG          config file path\n"+
			"  TUNETTY_SERVER          server URL\n"+
			"  TUNETTY_USERNAME        username\n"+
			"  TUNETTY_PASSWORD        password\n"+
			"  TUNETTY_AUDIO_BACKEND   audio backend override\n"+
			"  TUNETTY_ART_PROTOCOL    cover art protocol override\n"+
			"  TUNETTY_AUDIO_COMMAND   external player for the command backend\n")
	}
	flag.Parse()
	return f
}

func run() error {
	f := parseFlags()

	if f.showVersion {
		fmt.Printf("tunetty %s (%s, built %s)\n", version, commit, date)
		return nil
	}

	cfg, path, err := config.Load(f.config)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}
	missingConfig := errors.Is(err, config.ErrNotFound)

	// Command line flags win over both the file and the environment.
	if f.server != "" {
		cfg.Server.URL = f.server
	}
	if f.user != "" {
		cfg.Server.Username = f.user
	}
	if f.backend != "" {
		cfg.Audio.Backend = f.backend
	}
	if f.artProto != "" {
		cfg.Art.Protocol = f.artProto
	}
	// Cover art is experimental and off unless asked for. Disabling it also
	// skips the terminal probe and the tmux passthrough change at startup.
	if !f.artWork {
		cfg.Art.Protocol = string(art.ProtocolNone)
		cfg.Art.TmuxPassthrough = false
	}

	if f.writeConfig {
		return writeStarterConfig(cfg, path)
	}
	if f.doctor {
		return doctor(cfg, path, missingConfig)
	}

	// The environment alone may configure everything, so a missing file only
	// matters when the result is incomplete.
	if err := cfg.Validate(); err != nil {
		if missingConfig {
			return fmt.Errorf("no configuration at %s\n\nrun 'tunetty -init' to create one, "+
				"or set TUNETTY_SERVER, TUNETTY_USERNAME and TUNETTY_PASSWORD", path)
		}
		return err
	}

	return start(cfg)
}

func start(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	password, err := cfg.ResolvePassword(ctx)
	if err != nil {
		return err
	}

	client, err := subsonic.New(subsonic.Options{
		BaseURL:   cfg.Server.URL,
		Username:  cfg.Server.Username,
		Password:  password,
		PlainAuth: cfg.Server.PlainAuth,
		UserAgent: "tunetty/" + version,
		Timeout:   cfg.Server.Timeout.D(),

		CAFile:             cfg.Server.CAFile,
		InsecureSkipVerify: cfg.Server.InsecureSkipVerify,
	})
	if err != nil {
		return err
	}

	// Verify credentials before taking over the terminal, so authentication
	// problems are reported as plain text rather than inside the TUI.
	pingCtx, cancel := context.WithTimeout(ctx, cfg.Server.Timeout.D())
	info, err := client.Ping(pingCtx)
	cancel()
	if err != nil {
		var apiErr *subsonic.APIError
		if errors.As(err, &apiErr) && apiErr.Unauthorized() {
			return fmt.Errorf("login failed for %s at %s: %s", cfg.Server.Username, cfg.Server.URL, apiErr.Message)
		}
		return fmt.Errorf("connecting to %s: %w", cfg.Server.URL, err)
	}

	// Terminal capabilities must be probed before bubbletea owns the tty.
	caps := art.Detect(art.DetectOptions{
		Query:                 cfg.Art.Query && art.Protocol(cfg.Art.Protocol) == art.ProtocolAuto,
		Timeout:               300 * time.Millisecond,
		EnableTmuxPassthrough: cfg.Art.TmuxPassthrough,
	})

	engine, err := audio.NewEngine(audio.Config{
		Backend:        cfg.Audio.Backend,
		BufferDuration: cfg.Audio.BufferDuration.D(),
		NetworkBuffer:  cfg.Audio.NetworkBuffer,
	})
	if err != nil {
		return fmt.Errorf("opening audio output: %w", err)
	}
	defer func() { _ = engine.Close() }()

	// Media keys are a convenience: without them the player works the same.
	if stopKeys, err := mediakeys.Start(engine); err == nil {
		defer stopKeys()
	}

	model := ui.New(ui.Options{
		Client:     client,
		Engine:     engine,
		Config:     cfg,
		Caps:       caps,
		Version:    version,
		ServerInfo: info,
	})

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	return nil
}

// doctor prints what tunetty detected about the environment. It is the first
// thing to reach for when cover art or sound is not working.
func doctor(cfg config.Config, path string, missing bool) error {
	fmt.Printf("tunetty %s (%s)\n\n", version, commit)

	fmt.Println("config")
	fmt.Printf("  path              %s\n", path)
	if missing {
		fmt.Println("  status            not found (run 'tunetty -init')")
	} else {
		fmt.Println("  status            loaded")
	}
	fmt.Printf("  server            %s\n", orNone(cfg.Server.URL))
	fmt.Printf("  username          %s\n", orNone(cfg.Server.Username))
	if err := cfg.Validate(); err != nil {
		fmt.Printf("  problem           %s\n", err)
	}

	fmt.Println("\nterminal")
	caps := art.Detect(art.DetectOptions{
		Query:                 cfg.Art.Query,
		Timeout:               400 * time.Millisecond,
		EnableTmuxPassthrough: false,
	})
	fmt.Printf("  TERM              %s\n", orNone(os.Getenv("TERM")))
	fmt.Printf("  TERM_PROGRAM      %s\n", orNone(os.Getenv("TERM_PROGRAM")))
	fmt.Printf("  graphics          %s\n", caps.Protocol)
	if art.Protocol(cfg.Art.Protocol) == art.ProtocolNone {
		fmt.Println("  cover art         off (experimental, enable with --art-work)")
	}
	fmt.Printf("  cell size         %dx%d px\n", caps.CellWidth, caps.CellHeight)
	fmt.Printf("  truecolor         %v\n", caps.TrueColor)
	fmt.Printf("  tmux              %v\n", caps.InTmux)
	if caps.InTmux {
		fmt.Printf("  passthrough       %v\n", caps.PassthroughReady)
	}
	for _, n := range caps.Notes {
		fmt.Printf("  · %s\n", n)
	}

	fmt.Println("\naudio")
	fmt.Printf("  compiled backends %s\n", strings.Join(audio.AvailableBackends(), ", "))
	fmt.Printf("  configured        %s\n", cfg.Audio.Backend)
	fmt.Printf("  stream format     %s\n", cfg.Audio.Format)

	engine, err := audio.NewEngine(audio.Config{
		Backend:        cfg.Audio.Backend,
		BufferDuration: cfg.Audio.BufferDuration.D(),
		NetworkBuffer:  cfg.Audio.NetworkBuffer,
	})
	if err != nil {
		fmt.Printf("  output           FAILED: %v\n", err)
		return nil
	}
	fmt.Printf("  active backend    %s\n", engine.Backend())
	_ = engine.Close()
	return nil
}

func writeStarterConfig(cfg config.Config, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing config at %s", path)
	}
	if cfg.Server.URL == "" {
		cfg.Server.URL = "https://music.example.com"
	}
	if cfg.Server.Username == "" {
		cfg.Server.Username = "your-username"
	}
	// A password from TUNETTY_PASSWORD must not end up in the file in
	// plain text.
	cfg.Server.Password = ""
	if err := cfg.Save(path); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nEdit it to set server.url, server.username and either\n"+
		"server.password or server.password_command, then run tunetty.\n", path)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
