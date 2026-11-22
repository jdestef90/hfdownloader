package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mapEnv(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func fixedHome(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func TestLoadFromFileWithEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	content := `{
		"mode": "ssh",
		"routerUrl": "http://router.local:8080/",
		"sshTarget": "justin@router.local",
		"ggufDirectory": "/srv/models/gguf",
		"presetPath": "/srv/llama/presets.ini",
		"restartCommand": "systemctl restart llama-router",
		"helperModel": "qwen-helper",
		"hubToken": "file-token",
		"routerApiKey": "file-key",
		"helperTimeoutMs": 9000
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(LoadOptions{
		ConfigPath: cfgPath,
		LookupEnv: mapEnv(map[string]string{
			"HFDOWNLOADER_HUB_TOKEN":         "env-token",
			"HFDOWNLOADER_HELPER_TIMEOUT_MS": "25000",
		}),
		HomeDir: fixedHome(dir),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Mode != ModeSSH {
		t.Fatalf("expected mode ssh, got %q", cfg.Mode)
	}
	if cfg.RouterURL != "http://router.local:8080" {
		t.Fatalf("expected normalized routerUrl, got %q", cfg.RouterURL)
	}
	if cfg.SSHTarget != "justin@router.local" {
		t.Fatalf("unexpected sshTarget: %q", cfg.SSHTarget)
	}
	if cfg.HubToken != "env-token" {
		t.Fatalf("expected env override for hubToken, got %q", cfg.HubToken)
	}
	if cfg.RouterAPIKey != "file-key" {
		t.Fatalf("expected file routerApiKey, got %q", cfg.RouterAPIKey)
	}
	if cfg.HelperTimeoutMs != 25000 {
		t.Fatalf("expected env helperTimeoutMs 25000, got %d", cfg.HelperTimeoutMs)
	}
}

func TestLoadEnvOnlyLocalModeExpandsHome(t *testing.T) {
	home := t.TempDir()
	cfg, err := Load(LoadOptions{
		LookupEnv: mapEnv(map[string]string{
			"XDG_CONFIG_HOME":             filepath.Join(home, "xdg"),
			"HFDOWNLOADER_MODE":           "local",
			"HFDOWNLOADER_GGUF_DIRECTORY": "~/models/gguf",
			"HFDOWNLOADER_PRESET_PATH":    "~/llama/presets.ini",
			"HFDOWNLOADER_RESTART_COMMAND": "brew services restart llama.cpp",
			"HFDOWNLOADER_HELPER_MODEL":   "helper",
		}),
		HomeDir: fixedHome(home),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Mode != ModeLocal {
		t.Fatalf("expected local mode, got %q", cfg.Mode)
	}
	if cfg.RouterURL != DefaultRouterURL {
		t.Fatalf("expected default routerUrl, got %q", cfg.RouterURL)
	}
	if cfg.GGUFDirectory != filepath.Join(home, "models", "gguf") {
		t.Fatalf("expected expanded ggufDirectory, got %q", cfg.GGUFDirectory)
	}
	if cfg.PresetPath != filepath.Join(home, "llama", "presets.ini") {
		t.Fatalf("expected expanded presetPath, got %q", cfg.PresetPath)
	}
	if cfg.HelperTimeoutMs != DefaultHelperTimeoutMs {
		t.Fatalf("expected default helperTimeoutMs, got %d", cfg.HelperTimeoutMs)
	}
}

func TestLoadModeOverrideDoesNotRequireSSHTarget(t *testing.T) {
	home := t.TempDir()
	cfg, err := Load(LoadOptions{
		ModeOverride: "local",
		LookupEnv: mapEnv(map[string]string{
			"XDG_CONFIG_HOME":              filepath.Join(home, "xdg"),
			"HFDOWNLOADER_MODE":            "ssh",
			"HFDOWNLOADER_GGUF_DIRECTORY":  "/srv/gguf",
			"HFDOWNLOADER_PRESET_PATH":     "/srv/presets.ini",
			"HFDOWNLOADER_RESTART_COMMAND": "systemctl restart llama",
			"HFDOWNLOADER_HELPER_MODEL":    "helper",
		}),
		HomeDir: fixedHome(home),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != ModeLocal {
		t.Fatalf("expected ModeOverride local, got %q", cfg.Mode)
	}
}

func TestLoadMissingRequiredFields(t *testing.T) {
	home := t.TempDir()
	_, err := Load(LoadOptions{
		LookupEnv: mapEnv(map[string]string{
			"XDG_CONFIG_HOME": filepath.Join(home, "xdg"),
		}),
		HomeDir: fixedHome(home),
	})
	if err == nil {
		t.Fatal("expected error for missing required configuration")
	}
	for _, field := range []string{"sshTarget", "ggufDirectory", "presetPath", "restartCommand", "helperModel"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("expected error to mention %q, got %q", field, err.Error())
		}
	}
}

func TestLoadRejectsInvalidModeAndURLAndExplicitMissingFile(t *testing.T) {
	home := t.TempDir()

	_, err := Load(LoadOptions{
		ConfigPath: filepath.Join(home, "does-not-exist.json"),
		LookupEnv:  mapEnv(nil),
		HomeDir:    fixedHome(home),
	})
	if err == nil {
		t.Fatal("expected error when explicit config file is missing")
	}

	_, err = Load(LoadOptions{
		ModeOverride: "ftp",
		LookupEnv:    mapEnv(map[string]string{"XDG_CONFIG_HOME": home}),
		HomeDir:      fixedHome(home),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("expected unknown mode error, got %v", err)
	}

	_, err = Load(LoadOptions{
		LookupEnv: mapEnv(map[string]string{
			"XDG_CONFIG_HOME":              home,
			"HFDOWNLOADER_MODE":            "local",
			"HFDOWNLOADER_ROUTER_URL":      "ftp://router.local",
			"HFDOWNLOADER_GGUF_DIRECTORY":  "/srv/gguf",
			"HFDOWNLOADER_PRESET_PATH":     "/srv/presets.ini",
			"HFDOWNLOADER_RESTART_COMMAND": "restart",
			"HFDOWNLOADER_HELPER_MODEL":    "helper",
		}),
		HomeDir: fixedHome(home),
	})
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("expected invalid routerUrl error, got %v", err)
	}
}
