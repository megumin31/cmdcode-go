package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Go-plan model roster. Canonical gateway ids served over /alpha/generate.
// The compiled modelTable (go/models_generated.go, extracted from the
// official package's models.md by scripts/extract-models.py) is the fallback; at
// runtime maybeRefreshModels() may replace it with the Action-maintained
// models.json when the operator configures models_url/models_file and the
// payload validates. All lookups below go through activeModelTable so the
// two sources never diverge in behavior. Metadata comes from models.md and
// models.dev, with labelled operational fallbacks for missing budgets.
type modelDef struct {
	id      string
	display string
	context int64
	output  int64
}

func registeredModels() []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(activeModelTable()))
	for _, def := range activeModelTable() {
		if !modelAllowed(def.id) {
			continue
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                         def.id,
			Object:                     "model",
			OwnedBy:                    ProviderKey,
			Type:                       ProviderKey,
			DisplayName:                def.display,
			Name:                       def.id,
			Description:                "CommandCode Go plan model via " + ProviderKey,
			ContextLength:              def.context,
			MaxCompletionTokens:        def.output,
			InputTokenLimit:            def.context,
			OutputTokenLimit:           def.output,
			SupportedGenerationMethods: []string{"chat"},
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
			SupportedParameters:        []string{"temperature", "max_tokens", "tools", "stream"},
			UserDefined:                true,
		})
	}
	return models
}

// modelAllowed applies the operator's models allowlist (empty = all) and
// disable_models denylist. Entries accept the same flexible forms as client
// requests (canonical id, cmdcode-go/ prefix, short name).
func modelAllowed(canonical string) bool {
	cfg := getConfig()
	for _, denied := range cfg.DisableModels {
		if c, ok := matchModel(denied); ok && c == canonical {
			return false
		}
	}
	if len(cfg.Models) == 0 {
		return true
	}
	for _, allowed := range cfg.Models {
		if c, ok := matchModel(allowed); ok && c == canonical {
			return true
		}
	}
	return false
}

// matchModel reports whether name addresses this provider. Accepted forms:
// the canonical gateway id, the id prefixed with cmdcode-go/, or the
// bare short name after the last slash (case-insensitive).
func matchModel(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", false
	}
	lowered := strings.ToLower(trimmed)
	for _, prefix := range []string{"cmdcode-go/"} {
		if strings.HasPrefix(lowered, prefix) {
			trimmed = trimmed[len(prefix):]
			lowered = strings.ToLower(trimmed)
			break
		}
	}
	for _, def := range activeModelTable() {
		if strings.EqualFold(def.id, trimmed) {
			return def.id, true
		}
	}
	if !strings.Contains(trimmed, "/") {
		for _, def := range activeModelTable() {
			short := def.id[strings.LastIndex(def.id, "/")+1:]
			if strings.EqualFold(short, trimmed) {
				return def.id, true
			}
		}
	}
	return "", false
}

// resolveUpstreamModel maps a client-requested model to the canonical gateway
// id. Unknown ids pass through untouched so the gateway itself reports the
// authoritative error instead of this plugin guessing.
func resolveUpstreamModel(name string) string {
	if canonical, ok := matchModel(name); ok {
		return canonical
	}
	return strings.TrimSpace(name)
}

func registration() map[string]any {
	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": map[string]any{
			"Name":             "cmdcode-go",
			"Version":          "0.1.0",
			"Author":           "cmdcode-go contributors",
			"GitHubRepository": "https://github.com/router-for-me/CLIProxyAPI",
			"Description":      "CommandCode Go ($1/mo) plan via the CLI /alpha/generate gateway. Unofficial.",
			"ConfigFields": []map[string]any{
				{"Name": "api_key", "Type": "string", "Description": "CommandCode user_ key. Falls back to COMMANDCODE_KEY env and ~/.commandcode/auth.json."},
				{"Name": "base_url", "Type": "string", "Description": "Gateway base URL. Default https://api.commandcode.ai."},
				{"Name": "cli_version", "Type": "string", "Description": "x-command-code-version fingerprint. Default tracks the reversed CLI release."},
				{"Name": "project_slug", "Type": "string", "Description": "x-project-slug fingerprint header."},
				{"Name": "permission_mode", "Type": "string", "Description": "Envelope permissionMode. Default standard."},
				{"Name": "models_url", "Type": "string", "Description": "Remote models.json URL (Action-maintained). Default tracks this repo's main branch; empty = default."},
				{"Name": "models_file", "Type": "string", "Description": "Local models.json path override (reloaded on change). Takes precedence over models_url."},
				{"Name": "models_refresh_interval", "Type": "string", "Description": "Remote refresh interval (Go duration, default 24h; 0 disables remote refresh)."},
			},
		},
		"capabilities": map[string]any{
			"model_registrar":            true,
			"model_provider":             true,
			"model_router":               true,
			"executor":                   true,
			"executor_model_scope":       "both",
			"executor_input_formats":     []string{"chat-completions"},
			"executor_output_formats":    []string{"chat-completions"},
			"response_before_translator": true,
		},
	}
}
