package tui

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
)

// HubBrowser abstracts Hub Quant discovery so the TUI is straightforward to test.
type HubBrowser interface {
	ListQuants(ctx context.Context, repo, revision string) (hub.RepoInfo, error)
}

type screen int

const (
	screenURLInput screen = iota
	screenLoading
	screenPicker
)

type repoLoadedMsg struct {
	info hub.RepoInfo
	err  error
}

// Options configures a new TUI Model.
type Options struct {
	Config     config.Config
	Hub        HubBrowser
	InitialURL string
}

// Model is the root Bubble Tea model for hfdownloader.
type Model struct {
	cfg     config.Config
	hub     HubBrowser
	screen  screen
	width   int
	height  int
	errMsg  string
	status  string
	pending hub.ParsedURL

	urlInput textinput.Model
	spin     spinner.Model

	repo   hub.RepoInfo
	cursor int
}

// New creates the initial Bubble Tea model. If InitialURL is non-empty,
// Init immediately parses and loads it using the same path as the paste field.
func New(opts Options) Model {
	ti := textinput.New()
	ti.Placeholder = "https://huggingface.co/org/repo"
	ti.CharLimit = 512
	ti.Width = 64
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	m := Model{
		cfg:      opts.Config,
		hub:      opts.Hub,
		screen:   screenURLInput,
		urlInput: ti,
		spin:     sp,
	}

	if strings.TrimSpace(opts.InitialURL) != "" {
		m.urlInput.SetValue(strings.TrimSpace(opts.InitialURL))
		parsed, err := hub.ParseHubURL(opts.InitialURL)
		if err != nil {
			m.errMsg = err.Error()
		} else {
			m.pending = parsed
			m.screen = screenLoading
		}
	}

	return m
}

func (m Model) Init() tea.Cmd {
	if m.screen == screenLoading {
		return tea.Batch(m.spin.Tick, m.fetchRepoCmd(m.pending))
	}
	return textinput.Blink
}

func (m Model) fetchRepoCmd(parsed hub.ParsedURL) tea.Cmd {
	browser := m.hub
	return func() tea.Msg {
		info, err := browser.ListQuants(context.Background(), parsed.Repo, parsed.Revision)
		return repoLoadedMsg{info: info, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if msg.Width > 12 {
			w := msg.Width - 10
			if w > 80 {
				w = 80
			}
			m.urlInput.Width = w
		}
		return m, nil

	case spinner.TickMsg:
		if m.screen == screenLoading {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
		return m, nil

	case repoLoadedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.screen = screenURLInput
			m.urlInput.Focus()
			return m, textinput.Blink
		}
		if len(msg.info.Quants) == 0 {
			m.errMsg = fmt.Sprintf("No GGUF Quants found in %s (%s).", msg.info.Repo, msg.info.Revision)
			m.screen = screenURLInput
			m.urlInput.Focus()
			return m, textinput.Blink
		}
		m.errMsg = ""
		m.status = ""
		m.repo = msg.info
		m.cursor = 0
		m.screen = screenPicker
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.screen {
		case screenURLInput:
			return m.updateURLInput(msg)
		case screenLoading:
			if msg.String() == "q" || msg.String() == "esc" {
				return m, tea.Quit
			}
			return m, nil
		case screenPicker:
			return m.updatePicker(msg)
		}
	}

	return m, nil
}

func (m Model) updateURLInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, tea.Quit
	case "enter":
		parsed, err := hub.ParseHubURL(m.urlInput.Value())
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.errMsg = ""
		m.pending = parsed
		m.screen = screenLoading
		return m, tea.Batch(m.spin.Tick, m.fetchRepoCmd(parsed))
	}

	var cmd tea.Cmd
	m.urlInput, cmd = m.urlInput.Update(msg)
	return m, cmd
}

func (m Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.status = ""
		}
	case "down", "j":
		if m.cursor < len(m.repo.Quants)-1 {
			m.cursor++
			m.status = ""
		}
	case "u":
		m.screen = screenURLInput
		m.errMsg = ""
		m.status = ""
		m.urlInput.Focus()
		return m, textinput.Blink
	case "enter":
		if m.cursor >= 0 && m.cursor < len(m.repo.Quants) {
			q := m.repo.Quants[m.cursor]
			m.status = fmt.Sprintf("Selected Quant %s (%s, %d file(s)) — read-only browse mode.", q.Label, hub.FormatBytes(q.Size), len(q.Files))
		}
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	modeBadge := string(m.cfg.Mode)
	if m.cfg.Mode == config.ModeSSH && m.cfg.SSHTarget != "" {
		modeBadge = "ssh: " + m.cfg.SSHTarget
	}
	b.WriteString(styleHeader.Render("HF Downloader"))
	b.WriteString("  ")
	b.WriteString(styleBadge.Render(modeBadge))
	b.WriteString("  ")
	b.WriteString(styleMuted.Render(m.cfg.RouterURL))
	b.WriteString("\n\n")

	switch m.screen {
	case screenURLInput:
		b.WriteString(styleSection.Render("Enter Hugging Face Hub URL"))
		b.WriteString("\n\n")
		b.WriteString(styleBox.Render(m.urlInput.View()))
		b.WriteString("\n")
		if m.errMsg != "" {
			b.WriteString("\n")
			b.WriteString(styleError.Render("Error: " + m.errMsg))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("Enter: browse Quants  •  Esc / Ctrl+C: quit"))
		b.WriteString("\n")

	case screenLoading:
		b.WriteString(fmt.Sprintf("%s Fetching Quants from %s (%s)...\n\n", m.spin.View(), m.pending.Repo, m.pending.Revision))
		b.WriteString(styleMuted.Render("Press q or Ctrl+C to quit."))
		b.WriteString("\n")

	case screenPicker:
		b.WriteString(styleSection.Render(fmt.Sprintf("%s (%s)", m.repo.Repo, m.repo.Revision)))
		var meta []string
		if m.repo.Architecture != "" {
			meta = append(meta, "arch: "+m.repo.Architecture)
		}
		if m.repo.ContextLength > 0 {
			meta = append(meta, fmt.Sprintf("Hub Context Window: %d", m.repo.ContextLength))
		}
		if len(meta) > 0 {
			b.WriteString("  ")
			b.WriteString(styleMuted.Render(strings.Join(meta, " • ")))
		}
		b.WriteString("\n\n")

		for i, q := range m.repo.Quants {
			cursor := "  "
			lineStyle := lipglossStylePlain
			if i == m.cursor {
				cursor = styleSelected.Render("▸ ")
				lineStyle = styleSelected
			}

			shardBadge := ""
			if len(q.Files) > 1 {
				shardBadge = styleBadge.Render(fmt.Sprintf("%d shards", len(q.Files))) + " "
			}
			firstFile := ""
			if len(q.Files) > 0 {
				firstFile = path.Base(q.Files[0].Path)
			}

			row := fmt.Sprintf("%-14s %10s  %s%s",
				q.Label,
				hub.FormatBytes(q.Size),
				shardBadge,
				styleMuted.Render(firstFile),
			)
			b.WriteString(cursor)
			if i == m.cursor {
				b.WriteString(lineStyle.Render(fmt.Sprintf("%-14s %10s", q.Label, hub.FormatBytes(q.Size))))
				b.WriteString("  ")
				b.WriteString(shardBadge)
				b.WriteString(styleMuted.Render(firstFile))
			} else {
				b.WriteString(row)
			}
			b.WriteString("\n")
		}

		if len(m.repo.Sidecars) > 0 {
			b.WriteString("\n")
			b.WriteString(styleWarning.Render(fmt.Sprintf("Detected Sidecars (%d):", len(m.repo.Sidecars))))
			b.WriteString("\n")
			for _, sc := range m.repo.Sidecars {
				b.WriteString(fmt.Sprintf("  [%s] %s (%s)\n", sc.Kind, path.Base(sc.Path), hub.FormatBytes(sc.Size)))
			}
		}

		if m.status != "" {
			b.WriteString("\n")
			b.WriteString(styleSuccess.Render(m.status))
			b.WriteString("\n")
		}

		b.WriteString("\n")
		b.WriteString(styleMuted.Render("↑/↓ or j/k: move  •  Enter: select  •  u: change URL  •  q: quit"))
		b.WriteString("\n")
	}

	return b.String()
}

var lipglossStylePlain = styleMuted
