package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdestef90/hfdownloader/internal/config"
	"github.com/jdestef90/hfdownloader/internal/hub"
	"github.com/jdestef90/hfdownloader/internal/preset"
	"github.com/jdestef90/hfdownloader/internal/router"
	"github.com/jdestef90/hfdownloader/internal/transfer"
	"github.com/jdestef90/hfdownloader/internal/tui"
)

// Demo runs an end-to-end local session against a self-contained fake Hub and Router.
// Pass --scripted for a non-interactive walkthrough, or run without flags for the live Bubble Tea TUI.
func main() {
	scripted := flag.Bool("scripted", false, "run a non-interactive end-to-end walkthrough and print each step")
	flag.Parse()

	workDir, err := os.MkdirTemp("", "hfdownloader-demo-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo setup failed: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(workDir)

	ggufDir := filepath.Join(workDir, "gguf")
	presetPath := filepath.Join(workDir, "presets.ini")
	markerPath := filepath.Join(workDir, "router-restarted")

	initialPreset := "[qwen-helper]\nmodel = /srv/gguf/qwen-helper.gguf\nflash-attn = on\nn-gpu-layers = 99\n"
	if err := os.WriteFile(presetPath, []byte(initialPreset), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "demo setup failed: %v\n", err)
		os.Exit(1)
	}

	q4Data := []byte("demo-qwen3-q4-k-m-gguf-payload")
	q8Data := []byte("demo-qwen3-q8-0-gguf-payload-larger-file")
	mmprojData := []byte("demo-mmproj-bf16-sidecar")
	q4SHA := shaHex(q4Data)
	q8SHA := shaHex(q8Data)
	mmprojSHA := shaHex(mmprojData)

	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/unsloth/Qwen3-Demo-GGUF/tree/main":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "file", "path": "Qwen3-Demo-Q4_K_M.gguf", "size": len(q4Data), "lfs": map[string]any{"oid": q4SHA, "size": len(q4Data)}},
				{"type": "file", "path": "Qwen3-Demo-Q8_0.gguf", "size": len(q8Data), "lfs": map[string]any{"oid": q8SHA, "size": len(q8Data)}},
				{"type": "file", "path": "mmproj-BF16.gguf", "size": len(mmprojData), "lfs": map[string]any{"oid": mmprojSHA, "size": len(mmprojData)}},
			})
		case "/api/models/unsloth/Qwen3-Demo-GGUF":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gguf": map[string]any{"context_length": 40960, "architecture": "qwen3"},
			})
		case "/unsloth/Qwen3-Demo-GGUF/resolve/main/Qwen3-Demo-Q4_K_M.gguf":
			_, _ = w.Write(q4Data)
		case "/unsloth/Qwen3-Demo-GGUF/resolve/main/Qwen3-Demo-Q8_0.gguf":
			_, _ = w.Write(q8Data)
		case "/unsloth/Qwen3-Demo-GGUF/resolve/main/mmproj-BF16.gguf":
			_, _ = w.Write(mmprojData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hubSrv.Close()

	var restarted atomic.Bool
	routerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "32768"}}},
			})
		case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
			if _, err := os.Stat(markerPath); err == nil {
				restarted.Store(true)
			}
			var models []map[string]any
			if restarted.Load() {
				data, _ := os.ReadFile(presetPath)
				for _, name := range preset.SectionNames(string(data)) {
					models = append(models, map[string]any{"id": name})
				}
			} else {
				models = append(models, map[string]any{"id": "qwen-helper"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
		default:
			http.NotFound(w, r)
		}
	}))
	defer routerSrv.Close()

	restartCmd := fmt.Sprintf("touch %s && echo 'Router restarted cleanly (no model loaded)'", transfer.ShellQuote(markerPath))
	cfg := config.Config{
		Mode:            config.ModeLocal,
		RouterURL:       routerSrv.URL,
		GGUFDirectory:   ggufDir,
		PresetPath:      presetPath,
		RestartCommand:  restartCmd,
		HelperModel:     "qwen-helper",
		HelperTimeoutMs: 5000,
	}

	hubClient := hub.NewClient(hub.ClientOptions{BaseURL: hubSrv.URL, HTTPClient: hubSrv.Client()})
	routerClient := router.NewClient(router.ClientOptions{BaseURL: routerSrv.URL, HTTPClient: routerSrv.Client()})
	target := transfer.NewLocalTarget()

	if *scripted {
		if err := runScripted(cfg, hubClient, routerClient, target); err != nil {
			fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	model := tui.New(tui.Options{
		Config:     cfg,
		Hub:        hubClient,
		Target:     target,
		Router:     routerClient,
		HTTPClient: hubSrv.Client(),
		InitialURL: "https://huggingface.co/unsloth/Qwen3-Demo-GGUF",
	})
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "demo TUI failed: %v\n", err)
		os.Exit(1)
	}
}

func runScripted(cfg config.Config, hubClient *hub.Client, routerClient *router.Client, target transfer.Target) error {
	ctx := context.Background()
	parsed, err := hub.ParseHubURL("https://huggingface.co/unsloth/Qwen3-Demo-GGUF")
	if err != nil {
		return err
	}
	info, err := hubClient.ListQuants(ctx, parsed.Repo, parsed.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("1. Browsed %s: found %d Quants and %d Sidecar(s)\n", info.Repo, len(info.Quants), len(info.Sidecars))

	quant := info.Quants[0]
	sidecar := info.Sidecars[0]
	dlRes, err := transfer.DownloadQuant(ctx, transfer.DownloadOptions{
		Files: []transfer.DownloadFile{
			{Path: quant.Files[0].Path, Size: quant.Files[0].Size, SHA256: quant.Files[0].SHA256, URL: hubClient.ResolveURL(info.Repo, quant.Files[0].Path, info.Revision)},
			{Path: sidecar.Path, Size: sidecar.Size, SHA256: sidecar.SHA256, URL: hubClient.ResolveURL(info.Repo, sidecar.Path, info.Revision)},
		},
		Target:    target,
		Directory: cfg.GGUFDirectory,
	})
	if err != nil {
		return err
	}
	fmt.Printf("2. Downloaded & SHA-256 verified %d files (%s) into %s\n", len(dlRes.Files), hub.FormatBytes(dlRes.Bytes), cfg.GGUFDirectory)

	suggestion := routerClient.SuggestContextWindow(ctx, router.ContextSuggestionInput{
		Repo:        info.Repo,
		Quant:       quant,
		HelperModel: cfg.HelperModel,
		Fallback:    quant.ContextLength,
		Timeout:     time.Duration(cfg.HelperTimeoutMs) * time.Millisecond,
	})
	fmt.Printf("3. Helper Model suggested Context Window: %d (source=%s)\n", *suggestion.Value, suggestion.Source)

	existing, _, _ := target.ReadText(ctx, cfg.PresetPath)
	draft, err := preset.CreateDraft(preset.CreateDraftOptions{
		Repo:               info.Repo,
		QuantLabel:         quant.Label,
		ModelPath:          dlRes.Files[0].Path,
		ContextWindow:      suggestion.Value,
		MMProjPath:         dlRes.Files[1].Path,
		ExistingPresetText: existing,
		DefaultsAlias:      "qwen-helper",
	})
	if err != nil {
		return err
	}
	writeRes, err := preset.Write(ctx, preset.WriteOptions{
		Target:     target,
		PresetPath: cfg.PresetPath,
		Draft:      draft,
	})
	if err != nil {
		return err
	}
	fmt.Printf("4. Wrote Preset [%s] (backup: %s)\n", draft.Alias, filepath.Base(writeRes.BackupPath))

	cmdRes, err := target.Run(ctx, cfg.RestartCommand)
	if err != nil {
		return err
	}
	visible := routerClient.WaitForPreset(ctx, draft.Alias, 2*time.Second, 50*time.Millisecond)
	fmt.Printf("5. Confirmed restart (exit=%d): %s   Router visible=%v\n", cmdRes.ExitCode, cmdRes.Stdout, visible)
	return nil
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
