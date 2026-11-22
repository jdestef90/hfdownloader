package main

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hfdownloader ") {
		t.Fatalf("expected version output, got %q", stdout.String())
	}
}

func TestAppModelQuitsOnQ(t *testing.T) {
	m := appModel{hubURL: "https://huggingface.co/unsloth/Qwen3-0.6B-GGUF"}
	if !strings.Contains(m.View(), m.hubURL) {
		t.Fatalf("expected view to contain Hub URL, got %q", m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected tea.Quit command on q")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}
}
