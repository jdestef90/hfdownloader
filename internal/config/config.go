package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Mode selects how hfdownloader accesses the Router host.
type Mode string

const (
	ModeSSH   Mode = "ssh"
	ModeLocal Mode = "local"
)

const (
	DefaultRouterURL       = "http://127.0.0.1:8080"
	DefaultHelperTimeoutMs = 15000
)

// Config holds the runtime configuration for hfdownloader.
type Config struct {
	Mode            Mode
	RouterURL       string
	SSHTarget       string
	GGUFDirectory   string
	PresetPath      string
	RestartCommand  string
	HelperModel     string
	HubToken        string
	RouterAPIKey    string
	HelperTimeoutMs int
	ConfigPath      string
}

// ConfigError represents an invalid or incomplete configuration.
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return e.Message
}

// LoadOptions customizes how configuration is loaded (primarily for CLI overrides and testing).
type LoadOptions struct {
	ConfigPath   string
	ModeOverride string
	LookupEnv    func(string) (string, bool)
	HomeDir      func() (string, error)
}

type fileConfig struct {
	Mode            *string `json:"mode"`
	RouterURL       *string `json:"routerUrl"`
	SSHTarget       *string `json:"sshTarget"`
	GGUFDirectory   *string `json:"ggufDirectory"`
	PresetPath      *string `json:"presetPath"`
	RestartCommand  *string `json:"restartCommand"`
	HelperModel     *string `json:"helperModel"`
	HubToken        *string `json:"hubToken"`
	RouterAPIKey    *string `json:"routerApiKey"`
	HelperTimeoutMs *int    `json:"helperTimeoutMs"`
}

// DefaultConfigPath resolves $XDG_CONFIG_HOME/hfdownloader/config.json
// or falls back to ~/.config/hfdownloader/config.json.
func DefaultConfigPath(lookupEnv func(string) (string, bool), homeDir func() (string, error)) (string, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if xdg, ok := lookupEnv("XDG_CONFIG_HOME"); ok && strings.TrimSpace(xdg) != "" {
		return filepath.Join(strings.TrimSpace(xdg), "hfdownloader", "config.json"), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", &ConfigError{Message: fmt.Sprintf("cannot determine home directory: %v", err)}
	}
	return filepath.Join(home, ".config", "hfdownloader", "config.json"), nil
}

// Load reads configuration from JSON file and applies environment variable and CLI overrides.
func Load(opts LoadOptions) (Config, error) {
	lookupEnv := opts.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	homeDir := opts.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}

	envVal := func(key string) (string, bool) {
		v, ok := lookupEnv(key)
		if !ok {
			return "", false
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", false
		}
		return v, true
	}

	explicitPath := strings.TrimSpace(opts.ConfigPath)
	if explicitPath == "" {
		if envPath, ok := envVal("HFDOWNLOADER_CONFIG"); ok {
			explicitPath = envPath
		}
	}

	var configPath string
	if explicitPath != "" {
		expanded, err := expandHome(explicitPath, homeDir)
		if err != nil {
			return Config{}, err
		}
		configPath = expanded
	} else {
		p, err := DefaultConfigPath(lookupEnv, homeDir)
		if err != nil {
			return Config{}, err
		}
		configPath = p
	}

	fc, err := readConfigFile(configPath, explicitPath != "")
	if err != nil {
		return Config{}, err
	}

	pickString := func(envKey string, fileVal *string) string {
		if v, ok := envVal(envKey); ok {
			return v
		}
		if fileVal != nil {
			return strings.TrimSpace(*fileVal)
		}
		return ""
	}

	rawMode := strings.TrimSpace(opts.ModeOverride)
	if rawMode == "" {
		rawMode = pickString("HFDOWNLOADER_MODE", fc.Mode)
	}
	if rawMode == "" {
		rawMode = string(ModeSSH)
	}
	mode := Mode(rawMode)
	if mode != ModeSSH && mode != ModeLocal {
		return Config{}, &ConfigError{
			Message: fmt.Sprintf("unknown mode %q: expected %q or %q", rawMode, ModeSSH, ModeLocal),
		}
	}

	routerURL := pickString("HFDOWNLOADER_ROUTER_URL", fc.RouterURL)
	if routerURL == "" {
		routerURL = DefaultRouterURL
	}
	normalizedRouterURL, err := normalizeURL(routerURL)
	if err != nil {
		return Config{}, err
	}

	sshTarget := pickString("HFDOWNLOADER_SSH_TARGET", fc.SSHTarget)
	ggufDir := pickString("HFDOWNLOADER_GGUF_DIRECTORY", fc.GGUFDirectory)
	presetPath := pickString("HFDOWNLOADER_PRESET_PATH", fc.PresetPath)
	restartCommand := pickString("HFDOWNLOADER_RESTART_COMMAND", fc.RestartCommand)
	helperModel := pickString("HFDOWNLOADER_HELPER_MODEL", fc.HelperModel)
	hubToken := pickString("HFDOWNLOADER_HUB_TOKEN", fc.HubToken)
	routerAPIKey := pickString("HFDOWNLOADER_ROUTER_API_KEY", fc.RouterAPIKey)

	helperTimeoutMs := DefaultHelperTimeoutMs
	if envTimeout, ok := envVal("HFDOWNLOADER_HELPER_TIMEOUT_MS"); ok {
		parsed, parseErr := strconv.Atoi(envTimeout)
		if parseErr != nil || parsed <= 0 {
			return Config{}, &ConfigError{Message: "helperTimeoutMs must be a positive integer"}
		}
		helperTimeoutMs = parsed
	} else if fc.HelperTimeoutMs != nil {
		if *fc.HelperTimeoutMs <= 0 {
			return Config{}, &ConfigError{Message: "helperTimeoutMs must be a positive integer"}
		}
		helperTimeoutMs = *fc.HelperTimeoutMs
	}

	var missing []string
	if mode == ModeSSH && sshTarget == "" {
		missing = append(missing, "sshTarget")
	}
	if ggufDir == "" {
		missing = append(missing, "ggufDirectory")
	}
	if presetPath == "" {
		missing = append(missing, "presetPath")
	}
	if restartCommand == "" {
		missing = append(missing, "restartCommand")
	}
	if helperModel == "" {
		missing = append(missing, "helperModel")
	}
	if len(missing) > 0 {
		return Config{}, &ConfigError{
			Message: fmt.Sprintf(
				"missing required configuration: %s. Set them in %s or with HFDOWNLOADER_* environment variables.",
				strings.Join(missing, ", "),
				configPath,
			),
		}
	}

	if mode == ModeLocal {
		var expandErr error
		ggufDir, expandErr = expandHome(ggufDir, homeDir)
		if expandErr != nil {
			return Config{}, expandErr
		}
		presetPath, expandErr = expandHome(presetPath, homeDir)
		if expandErr != nil {
			return Config{}, expandErr
		}
	}

	return Config{
		Mode:            mode,
		RouterURL:       normalizedRouterURL,
		SSHTarget:       sshTarget,
		GGUFDirectory:   ggufDir,
		PresetPath:      presetPath,
		RestartCommand:  restartCommand,
		HelperModel:     helperModel,
		HubToken:        hubToken,
		RouterAPIKey:    routerAPIKey,
		HelperTimeoutMs: helperTimeoutMs,
		ConfigPath:      configPath,
	}, nil
}

func readConfigFile(path string, explicit bool) (fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return fileConfig{}, nil
		}
		return fileConfig{}, &ConfigError{Message: fmt.Sprintf("cannot read config file %s: %v", path, err)}
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return fileConfig{}, &ConfigError{Message: fmt.Sprintf("config file %s must contain a JSON object", path)}
	}

	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return fileConfig{}, &ConfigError{Message: fmt.Sprintf("config file %s is not valid JSON: %v", path, err)}
	}
	return fc, nil
}

func expandHome(path string, homeDir func() (string, error)) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := homeDir()
	if err != nil {
		return "", &ConfigError{Message: fmt.Sprintf("cannot expand home directory in %q: %v", path, err)}
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func normalizeURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", &ConfigError{Message: fmt.Sprintf("routerUrl is not a valid URL: %s", raw)}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", &ConfigError{Message: fmt.Sprintf("routerUrl must use http or https: %s", raw)}
	}
	return strings.TrimRight(raw, "/"), nil
}
