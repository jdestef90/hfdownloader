package tui

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
	"github.com/jdestef90/hfdownloader/internal/preset"
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
	visible bool
	waited  []string
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
