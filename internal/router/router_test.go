package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jdestef90/hfdownloader/internal/hub"
)

func TestListAndWaitForPresetVisibility(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer router-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		n := calls.Add(1)
		models := []map[string]any{{"id": "helper-model"}}
		if n >= 2 {
			models = append(models, map[string]any{"id": "Qwen3-Q4_K_M", "context_length": 32768})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{
		BaseURL:    srv.URL,
		APIKey:     "router-key",
		HTTPClient: srv.Client(),
	})

	visible := client.WaitForPreset(context.Background(), "Qwen3-Q4_K_M", 500*time.Millisecond, 20*time.Millisecond)
	if !visible {
		t.Fatal("expected Preset to become visible on Router")
	}
	if calls.Load() < 2 {
		t.Fatalf("expected polling retry before visibility, calls=%d", calls.Load())
	}
}

func TestSuggestContextWindowUsesHelperModelAndParsesResponse(t *testing.T) {
	var capturedModel string
	var capturedPrompt string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		capturedModel = body.Model
		if len(body.Messages) >= 2 {
			capturedPrompt = body.Messages[1].Content
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "32768"}},
			},
		})
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
	s := client.SuggestContextWindow(context.Background(), ContextSuggestionInput{
		Repo:        "unsloth/Qwen3-GGUF",
		HelperModel: "qwen-helper",
		Fallback:    40960,
		Timeout:     2 * time.Second,
		Quant: hub.Quant{
			Label:         "Q4_K_M",
			Architecture:  "qwen3",
			ContextLength: 40960,
			Files:         []hub.File{{Path: "Qwen3-Q4_K_M.gguf", Size: 4096}},
		},
	})

	if capturedModel != "qwen-helper" {
		t.Fatalf("expected helper model qwen-helper, got %q", capturedModel)
	}
	if !strings.Contains(capturedPrompt, "unsloth/Qwen3-GGUF") || !strings.Contains(capturedPrompt, "Q4_K_M") {
		t.Fatalf("expected Hub metadata in prompt, got:\n%s", capturedPrompt)
	}
	if s.Source != SourceHelper || s.Value == nil || *s.Value != 32768 {
		t.Fatalf("expected helper suggestion 32768, got %+v", s)
	}
}

func TestSuggestContextWindowFallsBackOnTimeoutAndUnusableOutput(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()

		client := NewClient(ClientOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
		s := client.SuggestContextWindow(context.Background(), ContextSuggestionInput{
			Repo:        "org/repo",
			HelperModel: "helper",
			Fallback:    65536,
			Timeout:     40 * time.Millisecond,
			Quant:       hub.Quant{Label: "Q4_K_M", ContextLength: 65536},
		})
		if s.Source != SourceHub || s.Value == nil || *s.Value != 65536 {
			t.Fatalf("expected Hub fallback 65536 on timeout, got %+v", s)
		}
	})

	t.Run("unusable output", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "I am not sure, maybe large?"}},
				},
			})
		}))
		defer srv.Close()

		client := NewClient(ClientOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
		s := client.SuggestContextWindow(context.Background(), ContextSuggestionInput{
			Repo:        "org/repo",
			HelperModel: "helper",
			Fallback:    32768,
			Timeout:     time.Second,
			Quant:       hub.Quant{Label: "Q4_K_M", ContextLength: 32768},
		})
		if s.Source != SourceHub || s.Value == nil || *s.Value != 32768 {
			t.Fatalf("expected Hub fallback 32768 on unusable output, got %+v", s)
		}
	})
}
