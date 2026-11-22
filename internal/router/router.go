package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jdestef90/hfdownloader/internal/hub"
)

// PresetInfo represents a Preset entry returned by the Router's /v1/models endpoint.
type PresetInfo struct {
	ID            string
	Preset        string
	ContextLength int
}

// SuggestionSource identifies where a Context Window prefill came from.
type SuggestionSource string

const (
	SourceHelper SuggestionSource = "helper"
	SourceHub    SuggestionSource = "hub"
	SourceNone   SuggestionSource = "none"
)

// ContextSuggestion holds an editable Context Window prefill and its provenance.
type ContextSuggestion struct {
	Value  *int
	Source SuggestionSource
}

// ContextSuggestionInput supplies Quant Hub metadata for the Helper Model prompt.
type ContextSuggestionInput struct {
	Repo        string
	Quant       hub.Quant
	HelperModel string
	Fallback    int
	Timeout     time.Duration
}

// ClientOptions configures a llama.cpp Router HTTP client.
type ClientOptions struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Client talks directly to the Router over HTTP.
type Client struct {
	BaseURL    string
	APIKey     string
	httpClient *http.Client
}

// NewClient creates a Router HTTP client.
func NewClient(opts ClientOptions) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		BaseURL:    strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		APIKey:     strings.TrimSpace(opts.APIKey),
		httpClient: httpClient,
	}
}

var (
	thousandsRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*k\b`)
	digitsRe    = regexp.MustCompile(`\d+`)
)

func parseSuggestedContext(content string) (int, bool) {
	normalized := strings.ReplaceAll(content, ",", "")
	if m := thousandsRe.FindStringSubmatch(normalized); len(m) == 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			v := int(math.Round(f * 1024))
			if v > 0 && v <= 10_000_000 {
				return v, true
			}
		}
	}
	for _, token := range digitsRe.FindAllString(normalized, -1) {
		v, err := strconv.Atoi(token)
		if err == nil && v > 0 && v <= 10_000_000 {
			return v, true
		}
	}
	return 0, false
}

func buildContextPrompt(in ContextSuggestionInput) string {
	fileDescs := make([]string, 0, len(in.Quant.Files))
	for _, f := range in.Quant.Files {
		fileDescs = append(fileDescs, fmt.Sprintf("%s (%d bytes)", f.Path, f.Size))
	}
	arch := in.Quant.Architecture
	if arch == "" {
		arch = "unknown"
	}
	ctxLimit := "unknown"
	if in.Quant.ContextLength > 0 {
		ctxLimit = fmt.Sprintf("%d tokens", in.Quant.ContextLength)
	}
	return strings.Join([]string{
		"Choose a safe llama.cpp Context Window for this GGUF Quant.",
		"Repository: " + in.Repo,
		"Quant: " + in.Quant.Label,
		"Architecture: " + arch,
		"Published Hub context limit: " + ctxLimit,
		"Files: " + strings.Join(fileDescs, ", "),
		"Reply with one integer number of tokens and no other text.",
	}, "\n")
}

// SuggestContextWindow asks the configured Helper Model via /v1/chat/completions for a
// Context Window suggestion, falling back to the published Hub context limit on timeout or unusable output.
func (c *Client) SuggestContextWindow(ctx context.Context, in ContextSuggestionInput) ContextSuggestion {
	fallback := ContextSuggestion{Source: SourceNone}
	if in.Fallback > 0 {
		v := in.Fallback
		fallback = ContextSuggestion{Value: &v, Source: SourceHub}
	}
	if strings.TrimSpace(in.HelperModel) == "" {
		return fallback
	}

	timeout := in.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqBody := map[string]any{
		"model": in.HelperModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You size Context Windows for llama.cpp Presets. Reply with a single integer number of tokens.",
			},
			{
				"role":    "user",
				"content": buildContextPrompt(in),
			},
		},
		"temperature": 0,
		"max_tokens":  32,
		"stream":      false,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fallback
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.BaseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return fallback
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallback
	}

	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fallback
	}
	if len(payload.Choices) == 0 {
		return fallback
	}
	content := payload.Choices[0].Message.Content
	if content == "" {
		content = payload.Choices[0].Text
	}
	if value, ok := parseSuggestedContext(content); ok {
		return ContextSuggestion{Value: &value, Source: SourceHelper}
	}
	return fallback
}

// ListPresets fetches /v1/models from the Router without loading any model.
func (c *Client) ListPresets(ctx context.Context) ([]PresetInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Router at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Router returned HTTP %d while listing Presets", resp.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Preset        string `json:"preset"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid Router /v1/models response: %w", err)
	}

	out := make([]PresetInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID == "" {
			continue
		}
		out = append(out, PresetInfo{
			ID:            item.ID,
			Preset:        item.Preset,
			ContextLength: item.ContextLength,
		})
	}
	return out, nil
}

// IsPresetVisible checks whether a Preset alias is listed on the Router.
func (c *Client) IsPresetVisible(ctx context.Context, alias string) (bool, error) {
	presets, err := c.ListPresets(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range presets {
		if p.ID == alias {
			return true, nil
		}
	}
	return false, nil
}

// WaitForPreset polls /v1/models until the alias appears or timeout elapses.
func (c *Client) WaitForPreset(ctx context.Context, alias string, timeout, interval time.Duration) bool {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		if visible, err := c.IsPresetVisible(ctx, alias); err == nil && visible {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}

func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hfdownloader/0.1")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}
