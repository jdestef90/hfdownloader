package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdestef90/hfdownloader/internal/config"
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

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 for --help, got %d", code)
	}
	if !strings.Contains(stderr.String(), "Usage: hfdownloader") {
		t.Fatalf("expected usage message, got %q", stderr.String())
	}
}

func TestRunMissingConfigReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing.json")
	code := run([]string{"--config", missing}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1 for missing config, got %d", code)
	}
	if !strings.Contains(stderr.String(), "cannot read config file") {
		t.Fatalf("expected config error message, got %q", stderr.String())
	}
}

func TestBuildTargetSelectsLocalOrSSH(t *testing.T) {
	localTarget := buildTarget(config.Config{Mode: config.ModeLocal}, "")
	if localTarget.Description() != "local" {
		t.Fatalf("expected local target, got %q", localTarget.Description())
	}
	sshTarget := buildTarget(config.Config{Mode: config.ModeSSH, SSHTarget: "justin@router"}, "")
	if sshTarget.Description() != "ssh justin@router" {
		t.Fatalf("expected ssh target, got %q", sshTarget.Description())
	}
}
