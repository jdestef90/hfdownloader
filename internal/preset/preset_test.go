package preset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdestef90/hfdownloader/internal/transfer"
)

func intPtr(v int) *int { return &v }

func TestCreateDraftRenderAndApplyRawRoundTrip(t *testing.T) {
	existing := `# Router Presets
[existing-model]
model = /srv/gguf/old.gguf
ctx-size = 8192
flash-attn = on
n-gpu-layers = 99
`
	draft, err := CreateDraft(CreateDraftOptions{
		Repo:               "unsloth/Qwen3-30B-GGUF",
		QuantLabel:         "Q4_K_M",
		ModelPath:          "/srv/gguf/Qwen3-30B-Q4_K_M.gguf",
		ContextWindow:      intPtr(32768),
		MMProjPath:         "/srv/gguf/mmproj-BF16.gguf",
		MTPPath:            "/srv/gguf/Qwen3-mtp.gguf",
		ExistingPresetText: existing,
		DefaultsAlias:      "existing-model",
	})
	if err != nil {
		t.Fatalf("unexpected CreateDraft error: %v", err)
	}
	if draft.Alias != "Qwen3-30B-Q4_K_M" {
		t.Fatalf("unexpected alias %q", draft.Alias)
	}

	raw, err := Render(draft)
	if err != nil {
		t.Fatalf("unexpected Render error: %v", err)
	}
	for _, want := range []string{
		"[Qwen3-30B-Q4_K_M]",
		"flash-attn = on",
		"n-gpu-layers = 99",
		"model = /srv/gguf/Qwen3-30B-Q4_K_M.gguf",
		"ctx-size = 32768",
		"mmproj = /srv/gguf/mmproj-BF16.gguf",
		"model-draft = /srv/gguf/Qwen3-mtp.gguf",
		"spec-type = draft-mtp",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("expected rendered INI to contain %q, got:\n%s", want, raw)
		}
	}

	// Edit raw text and sync back into structured Draft.
	editedRaw := strings.Replace(raw, "ctx-size = 32768", "ctx-size = 65536", 1)
	editedRaw = strings.Replace(editedRaw, "[Qwen3-30B-Q4_K_M]", "[qwen3-custom]", 1)

	synced, err := ApplyRaw(editedRaw)
	if err != nil {
		t.Fatalf("unexpected ApplyRaw error: %v", err)
	}
	if synced.Alias != "qwen3-custom" {
		t.Fatalf("expected synced alias qwen3-custom, got %q", synced.Alias)
	}
	if synced.ContextWindow == nil || *synced.ContextWindow != 65536 {
		t.Fatalf("expected synced ContextWindow 65536, got %v", synced.ContextWindow)
	}
	if synced.MMProjPath != "/srv/gguf/mmproj-BF16.gguf" || synced.MTPPath != "/srv/gguf/Qwen3-mtp.gguf" {
		t.Fatalf("expected synced sidecar paths, got mmproj=%q mtp=%q", synced.MMProjPath, synced.MTPPath)
	}
}

func TestWriteCreatesTimestampedBackupAndRequiresOverwriteConfirmation(t *testing.T) {
	dir := t.TempDir()
	presetFile := filepath.Join(dir, "presets.ini")
	initial := `# Existing header comment
[alpha]
model = /srv/gguf/alpha.gguf
ctx-size = 8192
`
	if err := os.WriteFile(presetFile, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	target := transfer.NewLocalTarget()
	fixedNow := func() time.Time {
		return time.Date(2026, 11, 21, 20, 45, 0, 0, time.UTC)
	}

	draft := Draft{
		Alias:         "alpha",
		ModelPath:     "/srv/gguf/alpha-v2.gguf",
		ContextWindow: intPtr(16384),
	}

	// First write without OverwriteAlias fails with AliasExistsError and leaves file untouched.
	_, err := Write(context.Background(), WriteOptions{
		Target:         target,
		PresetPath:     presetFile,
		Draft:          draft,
		OverwriteAlias: false,
		Now:            fixedNow,
	})
	var existsErr *AliasExistsError
	if !errors.As(err, &existsErr) {
		t.Fatalf("expected AliasExistsError, got %v", err)
	}

	// Confirmed overwrite creates timestamped backup and updates section cleanly.
	res, err := Write(context.Background(), WriteOptions{
		Target:         target,
		PresetPath:     presetFile,
		Draft:          draft,
		OverwriteAlias: true,
		Now:            fixedNow,
	})
	if err != nil {
		t.Fatalf("unexpected Write error: %v", err)
	}
	wantBackup := presetFile + ".bak-2026-11-21T20-45-00.000Z"
	if res.BackupPath != wantBackup {
		t.Fatalf("expected backup path %q, got %q", wantBackup, res.BackupPath)
	}

	backupData, err := os.ReadFile(wantBackup)
	if err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	if string(backupData) != initial {
		t.Fatalf("backup content mismatch:\ngot:\n%s\nwant:\n%s", string(backupData), initial)
	}

	newData, err := os.ReadFile(presetFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(newData), "# Existing header comment") || !strings.Contains(string(newData), "alpha-v2.gguf") {
		t.Fatalf("unexpected updated Preset file:\n%s", string(newData))
	}
}
