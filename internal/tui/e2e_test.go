package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
	"github.com/jdestef90/hfdownloader/internal/preset"
	"github.com/jdestef90/hfdownloader/internal/router"
	"github.com/jdestef90/hfdownloader/internal/transfer"
)

func TestEndToEndHubURLToConfirmedRestart(t *testing.T) {
	quantBytes := []byte("gguf-quant-weights-q4-k-m")
	mmprojBytes := []byte("gguf-mmproj-weights")
	quantSum := sha256.Sum256(quantBytes)
	mmprojSum := sha256.Sum256(mmprojBytes)
	quantSHA := hex.EncodeToString(quantSum[:])
	mmprojSHA := hex.EncodeToString(mmprojSum[:])

	// Fake Hugging Face Hub server.
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/unsloth/Qwen3-GGUF/tree/main":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"type": "file",
					"path": "Qwen3-Q4_K_M.gguf",
					"size": len(quantBytes),
					"lfs":  map[string]any{"oid": quantSHA, "size": len(quantBytes)},
				},
				{
					"type": "file",
					"path": "mmproj-BF16.gguf",
					"size": len(mmprojBytes),
					"lfs":  map[string]any{"oid": mmprojSHA, "size": len(mmprojBytes)},
				},
			})
		case "/api/models/unsloth/Qwen3-GGUF":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gguf": map[string]any{"context_length": 40960, "architecture": "qwen3"},
			})
		case "/unsloth/Qwen3-GGUF/resolve/main/Qwen3-Q4_K_M.gguf":
			_, _ = w.Write(quantBytes)
		case "/unsloth/Qwen3-GGUF/resolve/main/mmproj-BF16.gguf":
			_, _ = w.Write(mmprojBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hubSrv.Close()

	workDir := t.TempDir()
	ggufDir := filepath.Join(workDir, "gguf")
	presetPath := filepath.Join(workDir, "presets.ini")
	restartMarker := filepath.Join(workDir, "restarted.txt")

	initialINI := "[helper-model]\nmodel = /srv/gguf/helper.gguf\nflash-attn = on\n"
	if err := os.WriteFile(presetPath, []byte(initialINI), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake Router HTTP server: serves Helper Model chat-completions and reads Presets after restart.
	var modelLoadRequests atomic.Int32
	routerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "helper-model" {
				modelLoadRequests.Add(1)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "32768"}},
				},
			})
		case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
			// Only report new Presets once the restart marker exists (simulating Router reload).
			var models []map[string]any
			if _, err := os.Stat(restartMarker); err == nil {
				data, _ := os.ReadFile(presetPath)
				for _, name := range preset.SectionNames(string(data)) {
					models = append(models, map[string]any{"id": name})
				}
			} else {
				models = append(models, map[string]any{"id": "helper-model"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
		default:
			modelLoadRequests.Add(1)
			http.NotFound(w, r)
		}
	}))
	defer routerSrv.Close()

	// Fake SSH binary executing commands locally so we prove Preset edits & restart share the same SSH target.
	fakeSSH := filepath.Join(workDir, "fake-ssh")
	if err := os.WriteFile(fakeSSH, []byte("#!/bin/sh\nshift\nexec sh -c \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	restartCmd := "printf 'reloaded-ok' > " + transfer.ShellQuote(restartMarker) + " && echo router-restarted"
	cfg := config.Config{
		Mode:            config.ModeSSH,
		RouterURL:       routerSrv.URL,
		SSHTarget:       "justin@router",
		GGUFDirectory:   ggufDir,
		PresetPath:      presetPath,
		RestartCommand:  restartCmd,
		HelperModel:     "helper-model",
		HelperTimeoutMs: 5000,
	}

	m := New(Options{
		Config:     cfg,
		Hub:        hub.NewClient(hub.ClientOptions{BaseURL: hubSrv.URL, HTTPClient: hubSrv.Client()}),
		Target:     transfer.NewSSHTarget(cfg.SSHTarget, fakeSSH),
		Router:     router.NewClient(router.ClientOptions{BaseURL: routerSrv.URL, HTTPClient: routerSrv.Client()}),
		HTTPClient: hubSrv.Client(),
		InitialURL: "https://huggingface.co/unsloth/Qwen3-GGUF",
	})

	// 1. Load Hub repository.
	loaded := m.fetchRepoCmd(m.pending)()
	updated, _ := m.Update(loaded)
	picker := updated.(Model)
	if picker.screen != screenPicker {
		t.Fatalf("expected screenPicker, got %v (err=%q)", picker.screen, picker.errMsg)
	}

	// 2. Check the mmproj Sidecar and start downloading Quant + Sidecar.
	updated, _ = picker.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyTab})
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	downloading := updated.(Model)
	if downloading.screen != screenDownloading {
		t.Fatalf("expected screenDownloading, got %v", downloading.screen)
	}

	// 3. Wait for download + SHA-256 verification.
	dlDone := <-downloading.dlSession.doneCh
	updated, _ = downloading.Update(dlDone)
	prep := updated.(Model)
	if prep.screen != screenPreparingPreset {
		t.Fatalf("expected screenPreparingPreset, got %v (err=%q)", prep.screen, prep.errMsg)
	}

	// 4. Prepare Preset draft with Helper Model suggestion (32768).
	prepMsg := prep.preparePresetCmd()()
	updated, _ = prep.Update(prepMsg)
	ed := updated.(Model)
	if ed.screen != screenPresetEditor {
		t.Fatalf("expected screenPresetEditor, got %v (err=%q)", ed.screen, ed.errMsg)
	}
	if ed.editor.ctxInput.Value() != "32768" {
		t.Fatalf("expected Helper Model Context Window 32768, got %q", ed.editor.ctxInput.Value())
	}

	// 5. Write Preset (creates backup, checks Router, displays restart confirmation preview).
	d, raw, err := ed.editor.buildForSave()
	if err != nil {
		t.Fatal(err)
	}
	writeMsg := ed.writePresetCmd(d, raw, false)()
	updated, _ = ed.Update(writeMsg)
	confirm := updated.(Model)
	if confirm.screen != screenPresetWritten {
		t.Fatalf("expected screenPresetWritten, got %v (err=%q)", confirm.screen, confirm.errMsg)
	}
	// Restart command MUST be displayed and MUST NOT have run before confirmation.
	if !strings.Contains(confirm.View(), restartCmd) {
		t.Fatalf("expected restart command preview in view, got:\n%s", confirm.View())
	}
	if _, err := os.Stat(restartMarker); !os.IsNotExist(err) {
		t.Fatal("restart command ran before user confirmation")
	}

	// 6. Confirm restart with 'y' and execute over the same SSH target.
	updated, _ = confirm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	restarting := updated.(Model)
	if restarting.screen != screenRestarting {
		t.Fatalf("expected screenRestarting, got %v", restarting.screen)
	}
	restartMsg := restarting.runRestartCmd()()
	updated, _ = restarting.Update(restartMsg)
	done := updated.(Model)
	if done.screen != screenDone {
		t.Fatalf("expected screenDone, got %v", done.screen)
	}
	if done.restartErr != "" || !done.writtenVisible {
		t.Fatalf("expected restart success and Router visibility, err=%q visible=%v", done.restartErr, done.writtenVisible)
	}
	if !strings.Contains(done.View(), "router-restarted") {
		t.Fatalf("expected restart stdout in final view, got:\n%s", done.View())
	}

	// 7. Verify marker written by restart command and zero non-helper model load calls.
	markerData, err := os.ReadFile(restartMarker)
	if err != nil || string(markerData) != "reloaded-ok" {
		t.Fatalf("expected restart marker file, got data=%q err=%v", string(markerData), err)
	}
	if modelLoadRequests.Load() != 0 {
		t.Fatalf("expected zero model load requests on Router, got %d", modelLoadRequests.Load())
	}
}
