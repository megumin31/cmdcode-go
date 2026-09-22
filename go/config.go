package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const ProviderKey = "cmdcode-go"

type pluginConfig struct {
	APIKey                string   `yaml:"api_key"`
	BaseURL               string   `yaml:"base_url"`
	CLIVersion            string   `yaml:"cli_version"`
	ProjectSlug           string   `yaml:"project_slug"`
	Permission            string   `yaml:"permission_mode"`
	Models                []string `yaml:"models"`
	DisableModels         []string `yaml:"disable_models"`
	ModelsURL             string   `yaml:"models_url"`
	ModelsFile            string   `yaml:"models_file"`
	ModelsRefreshInterval string   `yaml:"models_refresh_interval"`
}

func cloneConfig(c pluginConfig) pluginConfig {
	c.Models = append([]string(nil), c.Models...)
	c.DisableModels = append([]string(nil), c.DisableModels...)
	return c
}

// ConfigStore publishes validated, immutable copies. Failed updates never
// partially apply. Missing keys retain their values; null/empty clears them.
type ConfigStore struct {
	mu    sync.RWMutex
	value pluginConfig
}

func (s *ConfigStore) Snapshot() pluginConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.value)
}

func decodeConfig(request []byte, previous pluginConfig) (pluginConfig, error) {
	if len(request) == 0 {
		return previous, nil
	}
	var payload struct {
		ConfigYAML string `json:"config_yaml"`
	}
	if err := json.Unmarshal(request, &payload); err != nil {
		return previous, fmt.Errorf("config envelope: %w", err)
	}
	text := payload.ConfigYAML
	if text == "" {
		return previous, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil {
		text = string(decoded)
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return previous, fmt.Errorf("config YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return previous, fmt.Errorf("config must contain exactly one YAML document")
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return previous, fmt.Errorf("config must be a mapping")
	}
	// Host-owned fields (enabled, priority, etc.) are deliberately allowed.
	// Decode into a map first to validate duplicates; map values let null clear.
	var fields map[string]yaml.Node
	if err := document.Decode(&fields); err != nil {
		return previous, err
	}
	next := cloneConfig(previous)
	scalars := map[string]*string{
		"api_key": &next.APIKey, "base_url": &next.BaseURL, "cli_version": &next.CLIVersion,
		"project_slug": &next.ProjectSlug, "permission_mode": &next.Permission,
		"models_url": &next.ModelsURL, "models_file": &next.ModelsFile,
		"models_refresh_interval": &next.ModelsRefreshInterval,
	}
	for key, dst := range scalars {
		if node, ok := fields[key]; ok {
			if node.Tag == "!!null" {
				*dst = ""
				continue
			}
			if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
				return previous, fmt.Errorf("%s must be a string", key)
			}
			if err := node.Decode(dst); err != nil {
				return previous, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	for key, dst := range map[string]*[]string{"models": &next.Models, "disable_models": &next.DisableModels} {
		if node, ok := fields[key]; ok {
			*dst = nil
			if node.Tag == "!!null" {
				continue
			}
			// Keep the formerly documented comma-separated scalar syntax.
			if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
				for _, item := range strings.Split(node.Value, ",") {
					if item = strings.TrimSpace(item); item != "" {
						*dst = append(*dst, item)
					}
				}
			} else if node.Kind == yaml.SequenceNode {
				for _, item := range node.Content {
					if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || strings.TrimSpace(item.Value) == "" {
						return previous, fmt.Errorf("%s must contain nonempty strings", key)
					}
					*dst = append(*dst, strings.TrimSpace(item.Value))
				}
			} else {
				return previous, fmt.Errorf("%s must be a list or comma-separated string", key)
			}
		}
	}
	if err := validateConfig(next); err != nil {
		return previous, err
	}
	return next, nil
}

func validateConfig(c pluginConfig) error {
	for name, raw := range map[string]string{"base_url": c.BaseURL, "models_url": c.ModelsURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("%s must be an HTTP(S) URL without credentials or fragment", name)
		}
	}
	if c.ModelsRefreshInterval != "" {
		if _, err := time.ParseDuration(c.ModelsRefreshInterval); err != nil {
			return fmt.Errorf("models_refresh_interval: %w", err)
		}
	}
	for _, value := range []string{c.APIKey, c.CLIVersion, c.ProjectSlug} {
		if bytes.ContainsAny([]byte(value), "\r\n") {
			return fmt.Errorf("config header values must not contain newlines")
		}
	}
	return nil
}

func (s *ConfigStore) Update(request []byte) (pluginConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := decodeConfig(request, s.value)
	if err != nil {
		return pluginConfig{}, err
	}
	s.value = cloneConfig(next)
	return cloneConfig(next), nil
}

// resolveAPIKey prefers auth-bound material selected by the host, then the
// plugin config, then environment, then the official CLI auth file so a plain
// `cmd login` keeps working without duplicating the key.
func (cfg pluginConfig) resolveAPIKey(authAttrs map[string]string) string {
	if key := strings.TrimSpace(authAttrs["api_key"]); key != "" {
		return key
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
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

func (cfg pluginConfig) resolveBaseURL() string {
	if strings.TrimSpace(cfg.BaseURL) != "" {
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
func (cfg pluginConfig) cliVersion() string {
	if strings.TrimSpace(cfg.CLIVersion) != "" {
		return cfg.CLIVersion
	}
	if ver := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION")); ver != "" {
		return ver
	}
	return "1.47.1"
}

func (cfg pluginConfig) projectSlug() string {
	if strings.TrimSpace(cfg.ProjectSlug) != "" {
		return cfg.ProjectSlug
	}
	return "cliproxyapi"
}

func (cfg pluginConfig) permissionMode() string {
	if strings.TrimSpace(cfg.Permission) != "" {
		return cfg.Permission
	}
	return "standard"
}
