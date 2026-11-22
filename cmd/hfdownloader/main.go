package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const version = "0.1.0-dev"

type appModel struct {
	hubURL string
}

func (m appModel) Init() tea.Cmd { return nil }

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m appModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Render("HF Downloader")
	body := "Go + Bubble Tea scaffold ready for implementation."
	if m.hubURL != "" {
		body = fmt.Sprintf("Hub URL: %s", m.hubURL)
	}
	help := lipgloss.NewStyle().Faint(true).Render("Press q to quit.")
	return fmt.Sprintf("%s\n\n%s\n\n%s\n", title, body, help)
}

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

	var hubURL string
	if fs.NArg() > 0 {
		hubURL = fs.Arg(0)
	}
	_ = configPath
	_ = mode

	p := tea.NewProgram(appModel{hubURL: hubURL}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "hfdownloader: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
