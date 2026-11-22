package tui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
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
				Label: "Q4_K_M",
				Size:  4 * 1024 * 1024 * 1024,
				Files: []hub.File{
					{Path: "Qwen3-Q4_K_M-00001-of-00002.gguf", Size: 2 * 1024 * 1024 * 1024, Shard: &hub.Shard{Index: 1, Total: 2}},
					{Path: "Qwen3-Q4_K_M-00002-of-00002.gguf", Size: 2 * 1024 * 1024 * 1024, Shard: &hub.Shard{Index: 2, Total: 2}},
				},
			},
			{
				Label: "Q8_0",
				Size:  8 * 1024 * 1024 * 1024,
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

	// OverwriteRequiredError routes to overwrite confirmation screen.
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

	// Declining overwrite returns to picker.
	updated, _ = owModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	back := updated.(Model)
	if back.screen != screenPicker {
		t.Fatalf("expected return to picker on n, got %v", back.screen)
	}

	// ChecksumError blocks completion and shows error on picker.
	back.screen = screenDownloading
	updated, _ = back.Update(downloadFinishedMsg{
		err: &transfer.ChecksumError{Path: "/srv/gguf/Qwen3-Q8_0.gguf", Expected: "aaa", Actual: "bbb"},
	})
	errModel := updated.(Model)
	if errModel.screen != screenPicker {
		t.Fatalf("expected checksum error to return to picker (blocking Preset creation), got %v", errModel.screen)
	}
	if !strings.Contains(errModel.View(), "SHA-256 mismatch") {
		t.Fatalf("expected SHA-256 mismatch message in view, got:\n%s", errModel.View())
	}
}
