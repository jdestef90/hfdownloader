package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
	"github.com/jdestef90/hfdownloader/internal/preset"
	"github.com/jdestef90/hfdownloader/internal/router"
	"github.com/jdestef90/hfdownloader/internal/transfer"
)

type fakeHub struct {
	info  hub.RepoInfo
	err   error
	calls []string
}

func (f *fakeHub) ListQuants(_ context.Context, repo, revision string) (hub.RepoInfo, error) {
	f.calls = append(f.calls, repo+"@"+revision)
	if f.err != nil {
		return hub.RepoInfo{}, f.err
	}
	return f.info, nil
}

func (f *fakeHub) ResolveURL(repo, filePath, revision string) string {
	return "https://huggingface.co/" + repo + "/resolve/" + revision + "/" + filePath
}

func (f *fakeHub) AuthHeaders() http.Header {
	return make(http.Header)
}

type fakeRouter struct {
	visible    bool
	waited     []string
	suggestion *router.ContextSuggestion
}

func (f *fakeRouter) SuggestContextWindow(_ context.Context, in router.ContextSuggestionInput) router.ContextSuggestion {
	if f.suggestion != nil {
		return *f.suggestion
	}
	if in.Fallback > 0 {
		v := in.Fallback
		return router.ContextSuggestion{Value: &v, Source: router.SourceHub}
	}
	return router.ContextSuggestion{Source: router.SourceNone}
}

func (f *fakeRouter) WaitForPreset(_ context.Context, alias string, _, _ time.Duration) bool {
	f.waited = append(f.waited, alias)
	return f.visible
}

func sampleConfig() config.Config {
	return config.Config{
		Mode:           config.ModeSSH,
		RouterURL:      "http://127.0.0.1:8080",
		SSHTarget:      "justin@router",
		GGUFDirectory:  "/srv/gguf",
		PresetPath:     "/srv/presets.ini",
		RestartCommand: "systemctl restart llama",
		HelperModel:    "helper",
	}
}

func sampleRepoInfo() hub.RepoInfo {
	return hub.RepoInfo{
		Repo:          "unsloth/Qwen3-GGUF",
		Revision:      "main",
		Architecture:  "qwen3",
		ContextLength: 40960,
		Sidecars: []hub.Sidecar{
			{Path: "mmproj-BF16.gguf", Size: 512 * 1024 * 1024, Kind: hub.SidecarMMProj},
			{Path: "Qwen3-mtp.gguf", Size: 256 * 1024 * 1024, Kind: hub.SidecarMTP},
		},
		Quants: []hub.Quant{
			{
				Label:         "Q4_K_M",
				Size:          4 * 1024 * 1024 * 1024,
				ContextLength: 40960,
				Files: []hub.File{
					{Path: "Qwen3-Q4_K_M-00001-of-00002.gguf", Size: 2 * 1024 * 1024 * 1024, Shard: &hub.Shard{Index: 1, Total: 2}},
					{Path: "Qwen3-Q4_K_M-00002-of-00002.gguf", Size: 2 * 1024 * 1024 * 1024, Shard: &hub.Shard{Index: 2, Total: 2}},
				},
			},
			{
				Label:         "Q8_0",
				Size:          8 * 1024 * 1024 * 1024,
				ContextLength: 40960,
				Files: []hub.File{
					{Path: "Qwen3-Q8_0.gguf", Size: 8 * 1024 * 1024 * 1024},
				},
			},
		},
	}
}

func TestCLIURLAndInteractivePasteProduceIdenticalPicker(t *testing.T) {
	info := sampleRepoInfo()

	cliModel := New(Options{
		Config:     sampleConfig(),
		Hub:        &fakeHub{info: info},
		InitialURL: "https://huggingface.co/unsloth/Qwen3-GGUF",
	})
	if cliModel.screen != screenLoading {
		t.Fatalf("expected screenLoading on initial URL, got %v", cliModel.screen)
	}
	loadedMsg := cliModel.fetchRepoCmd(cliModel.pending)()
	updated, _ := cliModel.Update(loadedMsg)
	cliPicker := updated.(Model)

	pasteModel := New(Options{
		Config: sampleConfig(),
		Hub:    &fakeHub{info: info},
	})
	if pasteModel.screen != screenURLInput {
		t.Fatalf("expected screenURLInput on bare launch, got %v", pasteModel.screen)
	}
	pasteModel.urlInput.SetValue("https://huggingface.co/unsloth/Qwen3-GGUF")
	next, _ := pasteModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	loading := next.(Model)
	if loading.screen != screenLoading {
		t.Fatalf("expected screenLoading after Enter, got %v", loading.screen)
	}
	loadedMsg2 := loading.fetchRepoCmd(loading.pending)()
	updated2, _ := loading.Update(loadedMsg2)
	pastePicker := updated2.(Model)

	if cliPicker.View() != pastePicker.View() {
		t.Fatalf("picker view differed between CLI URL and pasted URL:\nCLI:\n%s\nPASTE:\n%s", cliPicker.View(), pastePicker.View())
	}

	view := cliPicker.View()
	for _, want := range []string{"Q4_K_M", "4.00 GiB", "2 shards", "Q8_0", "8.00 GiB", "mmproj", "mtp"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected picker view to contain %q, got:\n%s", want, view)
		}
	}
}

func TestInvalidURLShowsErrorInPasteField(t *testing.T) {
	m := New(Options{
		Config: sampleConfig(),
		Hub:    &fakeHub{},
	})
	m.urlInput.SetValue("https://example.com/not-hf/repo")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if got.screen != screenURLInput {
		t.Fatalf("expected to stay on URL input screen, got %v", got.screen)
	}
	if !strings.Contains(got.View(), "huggingface.co") {
		t.Fatalf("expected huggingface.co validation error, got:\n%s", got.View())
	}
}

func TestDownloadOverwriteConfirmationAndChecksumErrorHandling(t *testing.T) {
	m := New(Options{
		Config: sampleConfig(),
		Hub:    &fakeHub{info: sampleRepoInfo()},
	})
	m.repo = sampleRepoInfo()
	m.screen = screenDownloading

	updated, _ := m.Update(downloadFinishedMsg{
		err: &transfer.OverwriteRequiredError{Paths: []string{"/srv/gguf/Qwen3-Q8_0.gguf"}},
	})
	owModel := updated.(Model)
	if owModel.screen != screenOverwriteConfirm {
		t.Fatalf("expected screenOverwriteConfirm, got %v", owModel.screen)
	}
	if !strings.Contains(owModel.View(), "/srv/gguf/Qwen3-Q8_0.gguf") {
		t.Fatalf("expected overwrite path in view, got:\n%s", owModel.View())
	}

	updated, _ = owModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	back := updated.(Model)
	if back.screen != screenPicker {
		t.Fatalf("expected return to picker on n, got %v", back.screen)
	}

	back.screen = screenDownloading
	updated, _ = back.Update(downloadFinishedMsg{
		err: &transfer.ChecksumError{Path: "/srv/gguf/Qwen3-Q8_0.gguf", Expected: "aaa", Actual: "bbb"},
	})
	errModel := updated.(Model)
	if errModel.screen != screenPicker {
		t.Fatalf("expected checksum error to return to picker, got %v", errModel.screen)
	}
	if !strings.Contains(errModel.View(), "SHA-256 mismatch") {
		t.Fatalf("expected SHA-256 mismatch message in view, got:\n%s", errModel.View())
	}
}

func TestPresetEditorStructuredRawSyncCopyDefaultsAndWrite(t *testing.T) {
	dir := t.TempDir()
	presetPath := filepath.Join(dir, "presets.ini")
	existing := "[base-preset]\nmodel = /srv/gguf/old.gguf\nflash-attn = on\nn-gpu-layers = 99\n"
	if err := os.WriteFile(presetPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := sampleConfig()
	cfg.Mode = config.ModeLocal
	cfg.PresetPath = presetPath

	rtr := &fakeRouter{visible: true}
	m := New(Options{
		Config: cfg,
		Hub:    &fakeHub{info: sampleRepoInfo()},
		Target: transfer.NewLocalTarget(),
		Router: rtr,
	})
	m.repo = sampleRepoInfo()
	m.selectedQuant = m.repo.Quants[0]
	m.dlResult = transfer.DownloadResult{
		Files: []transfer.DownloadedFile{{Path: "/srv/gguf/Qwen3-Q4_K_M-00001-of-00002.gguf", Size: 100}},
		Bytes: 100,
	}

	// Prepare Preset draft after download.
	prepMsg := m.preparePresetCmd()()
	updated, _ := m.Update(prepMsg)
	ed := updated.(Model)
	if ed.screen != screenPresetEditor {
		t.Fatalf("expected screenPresetEditor, got %v", ed.screen)
	}
	if ed.editor.aliasInput.Value() != "Qwen3-Q4_K_M" {
		t.Fatalf("expected prefilled alias Qwen3-Q4_K_M, got %q", ed.editor.aliasInput.Value())
	}

	// Copy defaults from [base-preset].
	updated, _ = ed.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	cp := updated.(Model)
	if cp.screen != screenCopyDefaults {
		t.Fatalf("expected screenCopyDefaults, got %v", cp.screen)
	}
	updated, _ = cp.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ed = updated.(Model)
	if ed.screen != screenPresetEditor || len(ed.editor.draft.Defaults) != 2 {
		t.Fatalf("expected 2 copied defaults, got screen=%v defaults=%+v", ed.screen, ed.editor.draft.Defaults)
	}

	// Switch to raw INI view, edit ctx-size, switch back to structured view, verify sync.
	updated, _ = ed.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	ed = updated.(Model)
	if !ed.editor.rawMode {
		t.Fatal("expected rawMode=true after Ctrl+R")
	}
	ed.editor.rawArea.SetValue(strings.Replace(ed.editor.rawArea.Value(), "ctx-size = 40960", "ctx-size = 32768", 1))
	updated, _ = ed.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	ed = updated.(Model)
	if ed.editor.rawMode || ed.editor.ctxInput.Value() != "32768" {
		t.Fatalf("expected synced structured ctx-size 32768, got rawMode=%v val=%q", ed.editor.rawMode, ed.editor.ctxInput.Value())
	}

	// Save Preset and verify backup + Router visibility.
	d, raw, err := ed.editor.buildForSave()
	if err != nil {
		t.Fatal(err)
	}
	writeMsg := ed.writePresetCmd(d, raw, false)()
	updated, _ = ed.Update(writeMsg)
	done := updated.(Model)
	if done.screen != screenPresetWritten {
		t.Fatalf("expected screenPresetWritten, got %v (err=%q)", done.screen, done.errMsg)
	}
	if !done.writtenVisible || done.writtenResult.BackupPath == "" {
		t.Fatalf("expected visible Preset with backup, got visible=%v backup=%q", done.writtenVisible, done.writtenResult.BackupPath)
	}

	// Attempting to overwrite same alias without confirmation routes to screenPresetOverwriteConfirm.
	writeAgain := done.writePresetCmd(preset.Draft{Alias: "Qwen3-Q4_K_M", ModelPath: "/srv/gguf/x.gguf"}, "", false)()
	updated, _ = done.Update(writeAgain)
	ow := updated.(Model)
	if ow.screen != screenPresetOverwriteConfirm {
		t.Fatalf("expected screenPresetOverwriteConfirm on duplicate alias, got %v", ow.screen)
	}
}

func TestHelperModelContextSuggestionAppearsEditableInDraft(t *testing.T) {
	dir := t.TempDir()
	presetPath := filepath.Join(dir, "presets.ini")
	cfg := sampleConfig()
	cfg.Mode = config.ModeLocal
	cfg.PresetPath = presetPath

	suggested := 24576
	rtr := &fakeRouter{
		visible:    true,
		suggestion: &router.ContextSuggestion{Value: &suggested, Source: router.SourceHelper},
	}

	m := New(Options{
		Config: cfg,
		Hub:    &fakeHub{info: sampleRepoInfo()},
		Target: transfer.NewLocalTarget(),
		Router: rtr,
	})
	m.repo = sampleRepoInfo()
	m.selectedQuant = m.repo.Quants[0]
	m.dlResult = transfer.DownloadResult{
		Files: []transfer.DownloadedFile{{Path: "/srv/gguf/Qwen3-Q4_K_M-00001-of-00002.gguf", Size: 100}},
	}

	prepMsg := m.preparePresetCmd()()
	updated, _ := m.Update(prepMsg)
	ed := updated.(Model)
	if ed.screen != screenPresetEditor {
		t.Fatalf("expected Preset editor (not silently written), got screen %v", ed.screen)
	}
	if ed.editor.ctxInput.Value() != "24576" {
		t.Fatalf("expected editable prefill 24576, got %q", ed.editor.ctxInput.Value())
	}
	if !strings.Contains(ed.View(), "Helper Model") {
		t.Fatalf("expected Helper Model provenance in view, got:\n%s", ed.View())
	}
	// Preset file must not exist yet (never written silently).
	if _, err := os.Stat(presetPath); !os.IsNotExist(err) {
		t.Fatal("expected Preset file not to be written silently")
	}
}

func TestSidecarCheckboxesTransferAndWireIntoPreset(t *testing.T) {
	mainBytes := []byte("main-quant-gguf")
	mmprojBytes := []byte("mmproj-sidecar-gguf")
	mtpBytes := []byte("mtp-sidecar-gguf")

	mux := http.NewServeMux()
	mux.HandleFunc("/org/repo/resolve/main/Qwen3-Q8_0.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mainBytes)
	})
	mux.HandleFunc("/org/repo/resolve/main/mmproj-BF16.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mmprojBytes)
	})
	mux.HandleFunc("/org/repo/resolve/main/Qwen3-mtp.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mtpBytes)
	})

	// Serve via local hub client backed by an in-memory RoundTripper.
	dir := t.TempDir()
	ggufDir := filepath.Join(dir, "gguf")
	presetPath := filepath.Join(dir, "presets.ini")

	info := hub.RepoInfo{
		Repo:     "org/repo",
		Revision: "main",
		Sidecars: []hub.Sidecar{
			{Path: "mmproj-BF16.gguf", Size: int64(len(mmprojBytes)), Kind: hub.SidecarMMProj},
			{Path: "Qwen3-mtp.gguf", Size: int64(len(mtpBytes)), Kind: hub.SidecarMTP},
		},
		Quants: []hub.Quant{
			{
				Label:         "Q8_0",
				Size:          int64(len(mainBytes)),
				ContextLength: 32768,
				Files:         []hub.File{{Path: "Qwen3-Q8_0.gguf", Size: int64(len(mainBytes))}},
			},
		},
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	hubSvc := &localTestHub{info: info, baseURL: srv.URL}

	cfg := sampleConfig()
	cfg.Mode = config.ModeLocal
	cfg.GGUFDirectory = ggufDir
	cfg.PresetPath = presetPath

	m := New(Options{
		Config:     cfg,
		Hub:        hubSvc,
		Target:     transfer.NewLocalTarget(),
		Router:     &fakeRouter{visible: true},
		HTTPClient: srv.Client(),
	})
	updated, _ := m.Update(repoLoadedMsg{info: info})
	picker := updated.(Model)

	if !strings.Contains(picker.View(), "[ ] [mmproj] mmproj-BF16.gguf") || !strings.Contains(picker.View(), "[ ] [mtp] Qwen3-mtp.gguf") {
		t.Fatalf("expected optional Sidecar checkboxes in picker view, got:\n%s", picker.View())
	}

	// Tab to Sidecars, check mmproj, move down, check mtp, Tab back to Quants.
	updated, _ = picker.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyTab})
	picker = updated.(Model)

	if !strings.Contains(picker.View(), "[x]") {
		t.Fatalf("expected checked Sidecar boxes in picker view, got:\n%s", picker.View())
	}

	// Press Enter on Quant to download main Quant + both checked Sidecars.
	updated, _ = picker.Update(tea.KeyMsg{Type: tea.KeyEnter})
	downloading := updated.(Model)
	if downloading.screen != screenDownloading {
		t.Fatalf("expected screenDownloading, got %v", downloading.screen)
	}
	doneMsg := <-downloading.dlSession.doneCh
	updated, _ = downloading.Update(doneMsg)
	prep := updated.(Model)
	if prep.screen != screenPreparingPreset {
		t.Fatalf("expected screenPreparingPreset after download, got %v (err=%q)", prep.screen, prep.errMsg)
	}

	// Verify all three files landed in GGUF Directory.
	for _, name := range []string{"Qwen3-Q8_0.gguf", "mmproj-BF16.gguf", "Qwen3-mtp.gguf"} {
		if _, err := os.Stat(filepath.Join(ggufDir, name)); err != nil {
			t.Fatalf("expected %s to be transferred into GGUF Directory: %v", name, err)
		}
	}

	// Prepare Preset and verify Sidecar references in rendered & written Preset.
	prepMsg := prep.preparePresetCmd()()
	updated, _ = prep.Update(prepMsg)
	ed := updated.(Model)
	d, raw, err := ed.editor.buildForSave()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mmproj = " + filepath.Join(ggufDir, "mmproj-BF16.gguf"),
		"model-draft = " + filepath.Join(ggufDir, "Qwen3-mtp.gguf"),
		"spec-type = draft-mtp",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("expected Preset INI to contain %q, got:\n%s", want, raw)
		}
	}
	writeMsg := ed.writePresetCmd(d, raw, false)()
	updated, _ = ed.Update(writeMsg)
	if updated.(Model).screen != screenPresetWritten {
		t.Fatalf("expected screenPresetWritten, got %v", updated.(Model).screen)
	}
}

type localTestHub struct {
	info    hub.RepoInfo
	baseURL string
}

func (h *localTestHub) ListQuants(_ context.Context, _, _ string) (hub.RepoInfo, error) {
	return h.info, nil
}
func (h *localTestHub) ResolveURL(repo, filePath, revision string) string {
	return h.baseURL + "/" + repo + "/resolve/" + revision + "/" + filePath
}
func (h *localTestHub) AuthHeaders() http.Header { return make(http.Header) }


