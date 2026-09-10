package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProviderKey is the executor identifier and model provider key.
const ProviderKey = "cmdcode-go"

type pluginConfig struct {
	APIKey                string   `json:"api_key"`
	BaseURL               string   `json:"base_url"`
	CLIVersion            string   `json:"cli_version"`
	ProjectSlug           string   `json:"project_slug"`
	Permission            string   `json:"permission_mode"`
	Models                []string `json:"models"`
	DisableModels         []string `json:"disable_models"`
	ModelsURL             string   `json:"models_url"`
	ModelsFile            string   `json:"models_file"`
	ModelsRefreshInterval string   `json:"models_refresh_interval"`
}

var configMu sync.RWMutex
var activeConfig = pluginConfig{}

// configure ingests the host register/reconfigure payload best-effort.
// The host sends {"config_yaml": "<yaml>"}; parsing is a dependency-free
// line scan for the handful of scalar keys we support. Anything missing
// keeps its previous value so reconfigure without keys is a no-op.
func configure(request []byte) {
	if len(request) == 0 {
		return
	}
	// The host sends {"config_yaml": "<base64>"} (Go []byte JSON encoding).
	var payload struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	yamlText := ""
	if err := json.Unmarshal(request, &payload); err == nil && len(payload.ConfigYAML) > 0 {
		yamlText = string(payload.ConfigYAML)
	} else {
		// Fallback for plain-text payloads (tests, older hosts).
		var text struct {
			ConfigYAML string `json:"config_yaml"`
		}
		if err := json.Unmarshal(request, &text); err != nil || text.ConfigYAML == "" {
			return
		}
		yamlText = text.ConfigYAML
	}
	next := getConfig()
	var listTarget *[]string
	for _, line := range strings.Split(yamlText, "\n") {
		if item := parseListItem(line); item != "" && listTarget != nil {
			*listTarget = append(*listTarget, item)
			continue
		}
		listTarget = nil
		key, value := splitYAMLKV(line)
		switch key {
		case "api_key":
			next.APIKey = value
		case "base_url":
			next.BaseURL = value
		case "cli_version":
			next.CLIVersion = value
		case "project_slug":
			next.ProjectSlug = value
		case "permission_mode":
			next.Permission = value
		case "models":
			next.Models = parseModelList(value)
			listTarget = &next.Models
		case "disable_models":
			next.DisableModels = parseModelList(value)
			listTarget = &next.DisableModels
		case "models_url":
			next.ModelsURL = value
		case "models_file":
			next.ModelsFile = value
		case "models_refresh_interval":
			next.ModelsRefreshInterval = value
		}
	}
	setConfig(next)
}

// parseModelList splits flow values ("a, b") into entries.
func parseModelList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if entry := unquote(strings.TrimSpace(part)); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// parseListItem matches block-style list entries ("  - model-id").
func parseListItem(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "- ") {
		return ""
	}
	return unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
}

func unquote(s string) string {
	return strings.Trim(s, `"'`)
}

func splitYAMLKV(line string) (string, string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", ""
	}
	idx := strings.Index(trimmed, ":")
	if idx < 0 {
		return "", ""
	}
	key := strings.TrimSpace(trimmed[:idx])
	value := strings.TrimSpace(trimmed[idx+1:])
	value = strings.Trim(value, `"'`)
	return key, value
}

func getConfig() pluginConfig {
	configMu.RLock()
	defer configMu.RUnlock()
	return activeConfig
}

func setConfig(cfg pluginConfig) {
	configMu.Lock()
	defer configMu.Unlock()
	activeConfig = cfg
}

// resolveAPIKey prefers auth-bound material selected by the host, then the
// plugin config, then environment, then the official CLI auth file so a plain
// `cmd login` keeps working without duplicating the key.
func resolveAPIKey(authAttrs map[string]string) string {
	if key := strings.TrimSpace(authAttrs["api_key"]); key != "" {
		return key
	}
	if cfg := getConfig(); strings.TrimSpace(cfg.APIKey) != "" {
		return cfg.APIKey
	}
	for _, env := range []string{"COMMANDCODE_KEY", "COMMANDCODE_API_KEY", "COMMAND_CODE_API_KEY"} {
		if key := strings.TrimSpace(os.Getenv(env)); key != "" {
			return key
		}
	}
	return readCLIAuthFile()
}

// readCLIAuthFile mirrors the CLI storage layout (~/.commandcode/auth.json,
// $COMMANDCODE_CONFIG_DIR/auth.json). The file is mode 0600 and holds the
// long-lived user_ key minted by the browser login flow.
func readCLIAuthFile() string {
	candidates := []string{}
	if dir := strings.TrimSpace(os.Getenv("COMMANDCODE_CONFIG_DIR")); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "auth.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".commandcode", "auth.json"),
			filepath.Join(home, ".config", "commandcode", "auth.json"),
		)
	}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var auth struct {
			APIKey string `json:"apiKey"`
			Key    string `json:"api_key"`
		}
		if err := json.Unmarshal(raw, &auth); err != nil {
			continue
		}
		if key := strings.TrimSpace(auth.APIKey); key != "" {
			return key
		}
		if key := strings.TrimSpace(auth.Key); key != "" {
			return key
		}
	}
	return ""
}

func resolveBaseURL() string {
	if cfg := getConfig(); strings.TrimSpace(cfg.BaseURL) != "" {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	if base := strings.TrimSpace(os.Getenv("COMMANDCODE_BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	return "https://api.commandcode.ai"
}

// cliVersion tracks the installed CLI release. The gateway rejects stale
// versions, so prefer an explicit config value and otherwise default to the
// release this plugin was reverse-engineered against.
func cliVersion() string {
	if cfg := getConfig(); strings.TrimSpace(cfg.CLIVersion) != "" {
		return cfg.CLIVersion
	}
	if ver := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION")); ver != "" {
		return ver
	}
	return "1.47.1"
}

func projectSlug() string {
	if cfg := getConfig(); strings.TrimSpace(cfg.ProjectSlug) != "" {
		return cfg.ProjectSlug
	}
	return "cliproxyapi"
}

func permissionMode() string {
	if cfg := getConfig(); strings.TrimSpace(cfg.Permission) != "" {
		return cfg.Permission
	}
	return "standard"
}
