package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PresetInfo represents a Preset entry returned by the Router's /v1/models endpoint.
type PresetInfo struct {
	ID            string
	Preset        string
	ContextLength int
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
