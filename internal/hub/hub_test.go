package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseHubURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantRepo string
		wantRev  string
		wantErr  bool
	}{
		{
			name:     "standard https url",
			input:    "https://huggingface.co/unsloth/Qwen3-0.6B-GGUF",
			wantRepo: "unsloth/Qwen3-0.6B-GGUF",
			wantRev:  "main",
		},
		{
			name:     "tree revision url",
			input:    "https://www.huggingface.co/org/model/tree/v2.1",
			wantRepo: "org/model",
			wantRev:  "v2.1",
		},
		{
			name:     "blob file url",
			input:    "https://huggingface.co/org/model/blob/dev/model-Q4_K_M.gguf",
			wantRepo: "org/model",
			wantRev:  "dev",
		},
		{
			name:     "org repo shorthand",
			input:    "unsloth/Qwen3-0.6B-GGUF",
			wantRepo: "unsloth/Qwen3-0.6B-GGUF",
			wantRev:  "main",
		},
		{
			name:    "empty string",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "non-huggingface host",
			input:   "https://example.com/org/model",
			wantErr: true,
		},
		{
			name:    "missing repo name",
			input:   "https://huggingface.co/onlyorg",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHubURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %+v", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if got.Repo != tc.wantRepo || got.Revision != tc.wantRev {
				t.Fatalf("got %+v, want repo=%q rev=%q", got, tc.wantRepo, tc.wantRev)
			}
		})
	}
}

func TestListQuantsGroupsShardsDetectsSidecarsAndPaginates(t *testing.T) {
	sha1 := strings.Repeat("a", 64)
	sha2 := strings.Repeat("b", 64)
	sha3 := strings.Repeat("c", 64)
	shaMM := strings.Repeat("d", 64)
	shaMTP := strings.Repeat("e", 64)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/models/unsloth/Qwen3-GGUF/tree/main" && r.URL.Query().Get("page") == "":
			w.Header().Set("Link", `<`+srv.URL+`/api/models/unsloth/Qwen3-GGUF/tree/main?recursive=true&page=2>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "file", "path": "README.md", "size": 100},
				{
					"type": "file",
					"path": "Q4_K_M/Qwen3-Q4_K_M-00002-of-00002.gguf",
					"size": 2000,
					"lfs":  map[string]any{"oid": sha2, "size": 2000},
				},
				{
					"type": "file",
					"path": "Q4_K_M/Qwen3-Q4_K_M-00001-of-00002.gguf",
					"size": 3000,
					"lfs":  map[string]any{"oid": sha1, "size": 3000},
				},
				{
					"type": "file",
					"path": "mmproj-BF16.gguf",
					"size": 500,
					"lfs":  map[string]any{"oid": shaMM, "size": 500},
				},
			})
		case r.URL.Path == "/api/models/unsloth/Qwen3-GGUF/tree/main" && r.URL.Query().Get("page") == "2":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"type": "file",
					"path": "Qwen3-UD-IQ3_XXS.gguf",
					"size": 1200,
					"lfs":  map[string]any{"oid": sha3, "size": 1200},
				},
				{
					"type": "file",
					"path": "Qwen3-mtp-Q8_0.gguf",
					"size": 300,
					"lfs":  map[string]any{"oid": shaMTP, "size": 300},
				},
			})
		case r.URL.Path == "/api/models/unsloth/Qwen3-GGUF":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gguf": map[string]any{
					"context_length": 40960,
					"architecture":   "qwen3",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
	info, err := client.ListQuants(context.Background(), "unsloth/Qwen3-GGUF", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.ContextLength != 40960 || info.Architecture != "qwen3" {
		t.Fatalf("unexpected repo metadata: context=%d arch=%q", info.ContextLength, info.Architecture)
	}
	if len(info.Sidecars) != 2 {
		t.Fatalf("expected 2 Sidecars, got %d: %+v", len(info.Sidecars), info.Sidecars)
	}
	if info.Sidecars[0].Kind != SidecarMMProj || info.Sidecars[1].Kind != SidecarMTP {
		t.Fatalf("unexpected Sidecar kinds: %+v", info.Sidecars)
	}
	if len(info.Quants) != 2 {
		t.Fatalf("expected 2 Quants, got %d: %+v", len(info.Quants), info.Quants)
	}

	// Sorted by size ascending: UD-IQ3_XXS (1200) first, sharded Q4_K_M (5000) second.
	if info.Quants[0].Label != "UD-IQ3_XXS" || info.Quants[0].Size != 1200 {
		t.Fatalf("unexpected first Quant: %+v", info.Quants[0])
	}
	q2 := info.Quants[1]
	if q2.Label != "Q4_K_M" || q2.Size != 5000 || len(q2.Files) != 2 {
		t.Fatalf("unexpected sharded Quant: %+v", q2)
	}
	if q2.Files[0].Shard == nil || q2.Files[0].Shard.Index != 1 || q2.Files[1].Shard.Index != 2 {
		t.Fatalf("shards not sorted in order: %+v", q2.Files)
	}
	if len(q2.Sidecars) != 2 {
		t.Fatalf("expected Sidecars attached to Quant, got %+v", q2.Sidecars)
	}
}

func TestListQuantsGatedRepoRequiresToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-hub-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/tree/") {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "file", "path": "gated-Q8_0.gguf", "size": 4096},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	unauth := NewClient(ClientOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
	_, err := unauth.ListQuants(context.Background(), "org/gated", "main")
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError without token, got %v", err)
	}

	auth := NewClient(ClientOptions{BaseURL: srv.URL, Token: "secret-hub-token", HTTPClient: srv.Client()})
	info, err := auth.ListQuants(context.Background(), "org/gated", "main")
	if err != nil {
		t.Fatalf("expected gated repo to succeed with token, got %v", err)
	}
	if len(info.Quants) != 1 || info.Quants[0].Label != "Q8_0" {
		t.Fatalf("unexpected Quants: %+v", info.Quants)
	}
}
