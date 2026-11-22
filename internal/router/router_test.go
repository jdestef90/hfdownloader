package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
