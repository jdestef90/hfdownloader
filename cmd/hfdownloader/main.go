package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
	"github.com/jdestef90/hfdownloader/internal/transfer"
	"github.com/jdestef90/hfdownloader/internal/tui"
)

const version = "0.1.0"

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hfdownloader", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		showVersion bool
		configPath  string
		mode        string
	)
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.StringVar(&configPath, "config", "", "path to config JSON file")
	fs.StringVar(&mode, "mode", "", "override mode (ssh or local)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: hfdownloader [flags] [hub-url]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if showVersion {
		fmt.Fprintf(stdout, "hfdownloader %s\n", version)
		return 0
	}

	cfg, err := config.Load(config.LoadOptions{
		ConfigPath:   configPath,
		ModeOverride: mode,
	})
	if err != nil {
		fmt.Fprintf(stderr, "hfdownloader: %v\n", err)
		return 1
	}

	var initialURL string
	if fs.NArg() > 0 {
		initialURL = fs.Arg(0)
	}

	hubClient := hub.NewClient(hub.ClientOptions{
		BaseURL: os.Getenv("HFDOWNLOADER_HUB_URL"),
		Token:   cfg.HubToken,
	})

	target := buildTarget(cfg, os.Getenv("HFDOWNLOADER_SSH_BIN"))

	model := tui.New(tui.Options{
		Config:     cfg,
		Hub:        hubClient,
		Target:     target,
		InitialURL: initialURL,
	})

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "hfdownloader: %v\n", err)
		return 1
	}
	return 0
}

func buildTarget(cfg config.Config, sshBin string) transfer.Target {
	if cfg.Mode == config.ModeLocal {
		return transfer.NewLocalTarget()
	}
	return transfer.NewSSHTarget(cfg.SSHTarget, sshBin)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
