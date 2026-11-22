package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
	"github.com/jdestef90/hfdownloader/internal/preset"
	"github.com/jdestef90/hfdownloader/internal/transfer"
)

// HubService abstracts Hub operations needed by the TUI.
type HubService interface {
	ListQuants(ctx context.Context, repo, revision string) (hub.RepoInfo, error)
	ResolveURL(repo, filePath, revision string) string
	AuthHeaders() http.Header
}

// RouterService abstracts Router HTTP operations needed by the TUI.
type RouterService interface {
	WaitForPreset(ctx context.Context, alias string, timeout, interval time.Duration) bool
}

type screen int

const (
	screenURLInput screen = iota
	screenLoading
	screenPicker
	screenOverwriteConfirm
	screenDownloading
	screenPreparingPreset
	screenPresetEditor
	screenCopyDefaults
	screenPresetOverwriteConfirm
	screenPresetWriting
	screenPresetWritten
)

type repoLoadedMsg struct {
	info hub.RepoInfo
	err  error
}

type downloadProgressMsg struct {
	progress transfer.Progress
}

type downloadFinishedMsg struct {
	result transfer.DownloadResult
	err    error
}

type presetPreparedMsg struct {
	draft          preset.Draft
	existingText   string
	ctxSourceLabel string
	err            error
}

type presetWrittenMsg struct {
	alias   string
	result  preset.WriteResult
	visible bool
	err     error
}

// Options configures a new TUI Model.
type Options struct {
	Config     config.Config
	Hub        HubService
	Target     transfer.Target
	Router     RouterService
	HTTPClient *http.Client
	InitialURL string
}

type downloadSession struct {
	cancel     context.CancelFunc
	progressCh chan transfer.Progress
	doneCh     chan downloadFinishedMsg
}

// Model is the root Bubble Tea model for hfdownloader.
type Model struct {
	cfg        config.Config
	hub        HubService
	target     transfer.Target
	router     RouterService
	httpClient *http.Client

	screen  screen
	width   int
	height  int
	errMsg  string
	status  string
	pending hub.ParsedURL

	urlInput textinput.Model
	spin     spinner.Model
	progBar  progress.Model

	repo           hub.RepoInfo
	cursor         int
	selectedQuant  hub.Quant
	overwritePaths []string

	dlSession  *downloadSession
	dlProgress transfer.Progress
	dlResult   transfer.DownloadResult

	editor          presetEditor
	pendingSaveRaw  string
	pendingSave     preset.Draft
	writtenAlias    string
	writtenResult   preset.WriteResult
	writtenVisible  bool
}

// New creates the initial Bubble Tea model.
func New(opts Options) Model {
	ti := textinput.New()
	ti.Placeholder = "https://huggingface.co/org/repo"
	ti.CharLimit = 512
	ti.Width = 64
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	pb := progress.New(progress.WithDefaultGradient())
	pb.Width = 48

	m := Model{
		cfg:        opts.Config,
		hub:        opts.Hub,
		target:     opts.Target,
		router:     opts.Router,
		httpClient: opts.HTTPClient,
		screen:     screenURLInput,
		urlInput:   ti,
		spin:       sp,
		progBar:    pb,
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
	svc := m.hub
	return func() tea.Msg {
		info, err := svc.ListQuants(context.Background(), parsed.Repo, parsed.Revision)
		return repoLoadedMsg{info: info, err: err}
	}
}

func waitDownloadEvent(sess *downloadSession) tea.Cmd {
	return func() tea.Msg {
		select {
		case p, ok := <-sess.progressCh:
			if ok {
				return downloadProgressMsg{progress: p}
			}
			return <-sess.doneCh
		case done := <-sess.doneCh:
			return done
		}
	}
}

func (m *Model) startDownload(quant hub.Quant, overwrite bool) tea.Cmd {
	m.selectedQuant = quant
	m.errMsg = ""
	m.status = ""
	m.overwritePaths = nil

	files := make([]transfer.DownloadFile, 0, len(quant.Files))
	for _, f := range quant.Files {
		files = append(files, transfer.DownloadFile{
			Path:   f.Path,
			Size:   f.Size,
			SHA256: f.SHA256,
			URL:    m.hub.ResolveURL(m.repo.Repo, f.Path, m.repo.Revision),
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess := &downloadSession{
		cancel:     cancel,
		progressCh: make(chan transfer.Progress, 16),
		doneCh:     make(chan downloadFinishedMsg, 1),
	}
	m.dlSession = sess
	m.dlProgress = transfer.Progress{
		File:       path.Base(files[0].Path),
		Total:      quant.Size,
		TotalFiles: len(files),
		ETASeconds: math.Inf(1),
	}
	m.screen = screenDownloading

	opts := transfer.DownloadOptions{
		Files:      files,
		Target:     m.target,
		Directory:  m.cfg.GGUFDirectory,
		Overwrite:  overwrite,
		HTTPClient: m.httpClient,
		Headers:    m.hub.AuthHeaders(),
		OnProgress: func(p transfer.Progress) {
			select {
			case sess.progressCh <- p:
			default:
				select {
				case <-sess.progressCh:
				default:
				}
				select {
				case sess.progressCh <- p:
				default:
				}
			}
		},
	}

	go func() {
		res, err := transfer.DownloadQuant(ctx, opts)
		close(sess.progressCh)
		sess.doneCh <- downloadFinishedMsg{result: res, err: err}
	}()

	return tea.Batch(m.spin.Tick, waitDownloadEvent(sess))
}

func (m Model) preparePresetCmd() tea.Cmd {
	target := m.target
	presetPath := m.cfg.PresetPath
	repo := m.repo.Repo
	quant := m.selectedQuant
	dlRes := m.dlResult

	return func() tea.Msg {
		var existingText string
		if target != nil {
			txt, _, err := target.ReadText(context.Background(), presetPath)
			if err != nil {
				return presetPreparedMsg{err: err}
			}
			existingText = txt
		}

		modelPath := ""
		if len(dlRes.Files) > 0 {
			modelPath = dlRes.Files[0].Path
		}

		var ctxWin *int
		ctxLabel := ""
		if quant.ContextLength > 0 {
			v := quant.ContextLength
			ctxWin = &v
			ctxLabel = "Hub metadata"
		}

		draft, err := preset.CreateDraft(preset.CreateDraftOptions{
			Repo:               repo,
			QuantLabel:         quant.Label,
			ModelPath:          modelPath,
			ContextWindow:      ctxWin,
			ExistingPresetText: existingText,
		})
		if err != nil {
			return presetPreparedMsg{err: err}
		}

		return presetPreparedMsg{
			draft:          draft,
			existingText:   existingText,
			ctxSourceLabel: ctxLabel,
		}
	}
}

func (m Model) writePresetCmd(d preset.Draft, rawText string, overwriteAlias bool) tea.Cmd {
	target := m.target
	rtr := m.router
	presetPath := m.cfg.PresetPath

	return func() tea.Msg {
		res, err := preset.Write(context.Background(), preset.WriteOptions{
			Target:         target,
			PresetPath:     presetPath,
			Draft:          d,
			RawText:        rawText,
			OverwriteAlias: overwriteAlias,
		})
		if err != nil {
			return presetWrittenMsg{alias: d.Alias, err: err}
		}

		visible := false
		if rtr != nil {
			visible = rtr.WaitForPreset(context.Background(), d.Alias, 3*time.Second, 250*time.Millisecond)
		}
		return presetWrittenMsg{
			alias:   d.Alias,
			result:  res,
			visible: visible,
		}
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
			pbW := msg.Width - 12
			if pbW > 64 {
				pbW = 64
			}
			if pbW < 20 {
				pbW = 20
			}
			m.progBar.Width = pbW
			m.editor.setWidth(msg.Width)
		}
		return m, nil

	case spinner.TickMsg:
		if m.screen == screenLoading || m.screen == screenDownloading || m.screen == screenPreparingPreset || m.screen == screenPresetWriting {
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

	case downloadProgressMsg:
		m.dlProgress = msg.progress
		if m.dlSession != nil {
			return m, waitDownloadEvent(m.dlSession)
		}
		return m, nil

	case downloadFinishedMsg:
		if m.dlSession != nil {
			m.dlSession.cancel()
			m.dlSession = nil
		}
		if msg.err != nil {
			var owErr *transfer.OverwriteRequiredError
			if errors.As(msg.err, &owErr) {
				m.overwritePaths = owErr.Paths
				m.screen = screenOverwriteConfirm
				return m, nil
			}
			if errors.Is(msg.err, context.Canceled) {
				m.status = "Download cancelled; partial files removed."
				m.errMsg = ""
				m.screen = screenPicker
				return m, nil
			}
			m.errMsg = msg.err.Error()
			m.screen = screenPicker
			return m, nil
		}
		m.dlResult = msg.result
		m.errMsg = ""
		m.status = fmt.Sprintf("Downloaded and verified %d file(s) (%s).", len(msg.result.Files), hub.FormatBytes(msg.result.Bytes))
		m.screen = screenPreparingPreset
		return m, tea.Batch(m.spin.Tick, m.preparePresetCmd())

	case presetPreparedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.screen = screenPicker
			return m, nil
		}
		m.editor = newPresetEditor(msg.draft, msg.existingText, msg.ctxSourceLabel)
		if m.width > 12 {
			m.editor.setWidth(m.width)
		}
		m.errMsg = ""
		m.screen = screenPresetEditor
		return m, textinput.Blink

	case presetWrittenMsg:
		if msg.err != nil {
			var existsErr *preset.AliasExistsError
			if errors.As(msg.err, &existsErr) {
				m.errMsg = ""
				m.screen = screenPresetOverwriteConfirm
				return m, nil
			}
			m.errMsg = msg.err.Error()
			m.screen = screenPresetEditor
			return m, nil
		}
		m.writtenAlias = msg.alias
		m.writtenResult = msg.result
		m.writtenVisible = msg.visible
		m.errMsg = ""
		m.screen = screenPresetWritten
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if m.dlSession != nil {
				m.dlSession.cancel()
			}
			return m, tea.Quit
		}
		switch m.screen {
		case screenURLInput:
			return m.updateURLInput(msg)
		case screenLoading, screenPreparingPreset, screenPresetWriting:
			if msg.String() == "q" || msg.String() == "esc" {
				return m, tea.Quit
			}
			return m, nil
		case screenPicker:
			return m.updatePicker(msg)
		case screenOverwriteConfirm:
			return m.updateOverwriteConfirm(msg)
		case screenDownloading:
			if msg.String() == "esc" || msg.String() == "c" {
				if m.dlSession != nil {
					m.dlSession.cancel()
				}
				return m, nil
			}
			return m, nil
		case screenPresetEditor:
			return m.updatePresetEditor(msg)
		case screenCopyDefaults:
			return m.updateCopyDefaults(msg)
		case screenPresetOverwriteConfirm:
			return m.updatePresetOverwriteConfirm(msg)
		case screenPresetWritten:
			switch msg.String() {
			case "q", "esc", "enter":
				return m, tea.Quit
			case "b":
				m.screen = screenPicker
				return m, nil
			}
			return m, nil
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
			m.errMsg = ""
		}
	case "down", "j":
		if m.cursor < len(m.repo.Quants)-1 {
			m.cursor++
			m.status = ""
			m.errMsg = ""
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
			if m.target == nil {
				m.status = fmt.Sprintf("Selected Quant %s (%s, %d file(s)) — read-only browse mode.", q.Label, hub.FormatBytes(q.Size), len(q.Files))
				return m, nil
			}
			cmd := m.startDownload(q, false)
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) updateOverwriteConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch strings.ToLower(msg.String()) {
	case "y":
		cmd := m.startDownload(m.selectedQuant, true)
		return m, cmd
	case "n", "esc":
		m.overwritePaths = nil
		m.status = "Overwrite cancelled."
		m.screen = screenPicker
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) updatePresetEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.errMsg = ""
		m.screen = screenPicker
		return m, nil
	case "ctrl+r", "f2":
		if err := m.editor.toggleRawMode(); err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.errMsg = ""
		return m, nil
	case "ctrl+d":
		if len(m.editor.existingNames) == 0 {
			m.errMsg = "No existing Presets in " + m.cfg.PresetPath + " to copy defaults from."
			return m, nil
		}
		m.errMsg = ""
		m.editor.defaultsCursor = 0
		m.screen = screenCopyDefaults
		return m, nil
	case "ctrl+s":
		d, raw, err := m.editor.buildForSave()
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.pendingSave = d
		m.pendingSaveRaw = raw
		m.errMsg = ""
		m.screen = screenPresetWriting
		return m, tea.Batch(m.spin.Tick, m.writePresetCmd(d, raw, false))
	}

	if !m.editor.rawMode {
		switch msg.String() {
		case "tab", "down":
			m.editor.nextField()
			return m, nil
		case "shift+tab", "up":
			m.editor.prevField()
			return m, nil
		case "enter":
			if m.editor.activeField < fieldCount-1 {
				m.editor.nextField()
				return m, nil
			}
			d, raw, err := m.editor.buildForSave()
			if err != nil {
				m.errMsg = err.Error()
				return m, nil
			}
			m.pendingSave = d
			m.pendingSaveRaw = raw
			m.errMsg = ""
			m.screen = screenPresetWriting
			return m, tea.Batch(m.spin.Tick, m.writePresetCmd(d, raw, false))
		}
	}

	cmd := m.editor.updateInput(msg)
	return m, cmd
}

func (m Model) updateCopyDefaults(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenPresetEditor
		return m, nil
	case "up", "k":
		if m.editor.defaultsCursor > 0 {
			m.editor.defaultsCursor--
		}
	case "down", "j":
		if m.editor.defaultsCursor < len(m.editor.existingNames)-1 {
			m.editor.defaultsCursor++
		}
	case "enter":
		if m.editor.defaultsCursor >= 0 && m.editor.defaultsCursor < len(m.editor.existingNames) {
			name := m.editor.existingNames[m.editor.defaultsCursor]
			if err := m.editor.applyCopiedDefaults(name); err != nil {
				m.errMsg = err.Error()
			} else {
				m.errMsg = ""
				m.status = fmt.Sprintf("Copied defaults from Preset [%s].", name)
			}
			m.screen = screenPresetEditor
			return m, nil
		}
	}
	return m, nil
}

func (m Model) updatePresetOverwriteConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch strings.ToLower(msg.String()) {
	case "y":
		m.screen = screenPresetWriting
		return m, tea.Batch(m.spin.Tick, m.writePresetCmd(m.pendingSave, m.pendingSaveRaw, true))
	case "n", "esc":
		m.screen = screenPresetEditor
		m.status = "Preset overwrite cancelled."
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func formatETA(seconds float64) string {
	if math.IsInf(seconds, 0) || math.IsNaN(seconds) || seconds < 0 {
		return "--"
	}
	d := time.Duration(math.Round(seconds)) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %02ds", mins, secs)
}

func (m Model) View() string {
	var b strings.Builder

	modeBadge := string(m.cfg.Mode)
	if m.target != nil {
		modeBadge = m.target.Description()
	} else if m.cfg.Mode == config.ModeSSH && m.cfg.SSHTarget != "" {
		modeBadge = "ssh " + m.cfg.SSHTarget
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
			if i == m.cursor {
				cursor = styleSelected.Render("▸ ")
			}

			shardBadge := ""
			if len(q.Files) > 1 {
				shardBadge = styleBadge.Render(fmt.Sprintf("%d shards", len(q.Files))) + " "
			}
			firstFile := ""
			if len(q.Files) > 0 {
				firstFile = path.Base(q.Files[0].Path)
			}

			b.WriteString(cursor)
			if i == m.cursor {
				b.WriteString(styleSelected.Render(fmt.Sprintf("%-14s %10s", q.Label, hub.FormatBytes(q.Size))))
			} else {
				b.WriteString(fmt.Sprintf("%-14s %10s", q.Label, hub.FormatBytes(q.Size)))
			}
			b.WriteString("  ")
			b.WriteString(shardBadge)
			b.WriteString(styleMuted.Render(firstFile))
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

		if m.errMsg != "" {
			b.WriteString("\n")
			b.WriteString(styleError.Render("Error: " + m.errMsg))
			b.WriteString("\n")
		}
		if m.status != "" {
			b.WriteString("\n")
			b.WriteString(styleSuccess.Render(m.status))
			b.WriteString("\n")
		}

		b.WriteString("\n")
		b.WriteString(styleMuted.Render("↑/↓ or j/k: move  •  Enter: download Quant  •  u: change URL  •  q: quit"))
		b.WriteString("\n")

	case screenOverwriteConfirm:
		b.WriteString(styleWarning.Render("Target file(s) already exist in the GGUF Directory:"))
		b.WriteString("\n\n")
		for _, p := range m.overwritePaths {
			b.WriteString("  • " + p + "\n")
		}
		b.WriteString("\n")
		b.WriteString("Overwrite existing file(s)? ")
		b.WriteString(styleSelected.Render("[y] Overwrite"))
		b.WriteString("  ")
		b.WriteString(styleMuted.Render("[n / Esc] Cancel"))
		b.WriteString("\n")

	case screenDownloading:
		b.WriteString(styleSection.Render(fmt.Sprintf("Downloading %s into %s", m.selectedQuant.Label, m.cfg.GGUFDirectory)))
		b.WriteString("\n\n")
		pct := 0.0
		if m.dlProgress.Total > 0 {
			pct = float64(m.dlProgress.Transferred) / float64(m.dlProgress.Total)
			if pct > 1 {
				pct = 1
			}
		}
		b.WriteString(m.progBar.ViewAs(pct))
		b.WriteString("\n\n")

		fileNum := m.dlProgress.CompletedFiles + 1
		if fileNum > m.dlProgress.TotalFiles && m.dlProgress.TotalFiles > 0 {
			fileNum = m.dlProgress.TotalFiles
		}
		b.WriteString(fmt.Sprintf("File %d/%d: %s\n", fileNum, m.dlProgress.TotalFiles, m.dlProgress.File))
		if m.dlProgress.Verifying {
			b.WriteString(fmt.Sprintf("%s Verifying SHA-256 checksum...\n", m.spin.View()))
		} else {
			b.WriteString(fmt.Sprintf("%s / %s  •  %s/s  •  ETA %s\n",
				hub.FormatBytes(m.dlProgress.Transferred),
				hub.FormatBytes(m.dlProgress.Total),
				hub.FormatBytes(int64(m.dlProgress.SpeedBps)),
				formatETA(m.dlProgress.ETASeconds),
			))
		}
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("Esc / c: cancel download and remove partial files"))
		b.WriteString("\n")

	case screenPreparingPreset:
		b.WriteString(fmt.Sprintf("%s Preparing Preset draft for %s...\n", m.spin.View(), m.selectedQuant.Label))

	case screenPresetEditor:
		viewLabel := "Structured View"
		if m.editor.rawMode {
			viewLabel = "Raw INI View"
		}
		b.WriteString(styleSection.Render("Preset Draft"))
		b.WriteString("  ")
		b.WriteString(styleBadge.Render(viewLabel))
		b.WriteString("  ")
		b.WriteString(styleMuted.Render(m.cfg.PresetPath))
		b.WriteString("\n\n")

		if !m.editor.rawMode {
			b.WriteString(renderField("Alias", m.editor.aliasInput.View(), m.editor.activeField == fieldAlias))
			b.WriteString(renderField("Model path", m.editor.modelInput.View(), m.editor.activeField == fieldModelPath))
			ctxLabel := "Context Window"
			if m.editor.ctxSourceLabel != "" {
				ctxLabel += " (" + m.editor.ctxSourceLabel + ")"
			}
			b.WriteString(renderField(ctxLabel, m.editor.ctxInput.View(), m.editor.activeField == fieldContextWindow))
			if len(m.editor.draft.Defaults) > 0 {
				src := "copied defaults"
				if m.editor.copiedFrom != "" {
					src = "copied from [" + m.editor.copiedFrom + "]"
				}
				b.WriteString("\n")
				b.WriteString(styleMuted.Render(fmt.Sprintf("Inherited settings (%s):", src)))
				b.WriteString("\n")
				for _, e := range m.editor.draft.Defaults {
					b.WriteString(fmt.Sprintf("  %s = %s\n", e.Key, e.Value))
				}
			}
		} else {
			b.WriteString(styleBox.Render(m.editor.rawArea.View()))
			b.WriteString("\n")
		}

		if m.errMsg != "" {
			b.WriteString("\n")
			b.WriteString(styleError.Render("Error: " + m.errMsg))
			b.WriteString("\n")
		}
		if m.status != "" {
			b.WriteString("\n")
			b.WriteString(styleSuccess.Render(m.status))
			b.WriteString("\n")
		}

		b.WriteString("\n")
		if m.editor.rawMode {
			b.WriteString(styleMuted.Render("Ctrl+R / F2: structured view  •  Ctrl+D: copy defaults  •  Ctrl+S: write Preset  •  Esc: back"))
		} else {
			b.WriteString(styleMuted.Render("Tab/↑/↓: field  •  Ctrl+R / F2: raw INI view  •  Ctrl+D: copy defaults  •  Ctrl+S: write Preset  •  Esc: back"))
		}
		b.WriteString("\n")

	case screenCopyDefaults:
		b.WriteString(styleSection.Render("Copy Defaults from Existing Preset"))
		b.WriteString("\n\n")
		for i, name := range m.editor.existingNames {
			if i == m.editor.defaultsCursor {
				b.WriteString(styleSelected.Render("▸ [" + name + "]"))
			} else {
				b.WriteString("  [" + name + "]")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("↑/↓: move  •  Enter: copy defaults  •  Esc: cancel"))
		b.WriteString("\n")

	case screenPresetOverwriteConfirm:
		b.WriteString(styleWarning.Render(fmt.Sprintf("Preset [%s] already exists in %s.", m.pendingSave.Alias, m.cfg.PresetPath)))
		b.WriteString("\n\n")
		b.WriteString("Overwrite this Preset (a timestamped backup will be created first)? ")
		b.WriteString(styleSelected.Render("[y] Overwrite"))
		b.WriteString("  ")
		b.WriteString(styleMuted.Render("[n / Esc] Cancel"))
		b.WriteString("\n")

	case screenPresetWriting:
		b.WriteString(fmt.Sprintf("%s Writing Preset [%s] and checking Router visibility...\n", m.spin.View(), m.pendingSave.Alias))

	case screenPresetWritten:
		b.WriteString(styleSuccess.Render(fmt.Sprintf("Preset [%s] written to %s", m.writtenAlias, m.cfg.PresetPath)))
		b.WriteString("\n\n")
		if m.writtenResult.BackupPath != "" {
			b.WriteString(fmt.Sprintf("Backup created: %s\n", m.writtenResult.BackupPath))
		}
		if m.writtenVisible {
			b.WriteString(styleSuccess.Render("Router status: Preset is visible on the Router (no model loaded)."))
		} else {
			b.WriteString(styleWarning.Render("Router status: Preset not yet visible on the Router — restart the Router to reload Presets (no model loaded)."))
		}
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("b: back to Quant picker  •  Enter / q: quit"))
		b.WriteString("\n")
	}

	return b.String()
}

func renderField(label, inputView string, active bool) string {
	prefix := "  "
	lbl := styleMuted.Render(label + ":")
	if active {
		prefix = styleSelected.Render("▸ ")
		lbl = styleSelected.Render(label + ":")
	}
	return fmt.Sprintf("%s%s\n  %s\n", prefix, lbl, inputView)
}
