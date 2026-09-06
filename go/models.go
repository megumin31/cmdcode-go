package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Go-plan model roster. Canonical gateway ids served over /alpha/generate:
// the open-weight family plus the documented premium exceptions
// (GPT-5.6 Luna, Grok 4.5, Qwen Max/Plus, Muse Spark contributors).
// Context/output numbers follow the CLI model registry; unknown entries err
// on the side of 256K/64K. The list is intentionally static so model
// registration never depends on network access.
type modelDef struct {
	id      string
	display string
	context int64
	output  int64
}

var modelTable = []modelDef{
	{"deepseek/deepseek-v4-flash", "DeepSeek V4 Flash", 1048576, 131072},
	{"deepseek/deepseek-v4-flash-fast", "DeepSeek V4 Flash Fast", 1000000, 131072},
	{"deepseek/deepseek-v4-flash-vision-exp", "DeepSeek V4 Flash Vision (exp)", 1000000, 131072},
	{"deepseek/deepseek-v4-pro", "DeepSeek V4 Pro", 1048576, 131072},
	{"moonshotai/Kimi-K3", "Kimi K3", 1048576, 65536},
	{"moonshotai/Kimi-K2.7-Code", "Kimi K2.7 Code", 262144, 65536},
	{"moonshotai/Kimi-K2.7-Code-Highspeed", "Kimi K2.7 Code HighSpeed", 262144, 65536},
	{"moonshotai/Kimi-K2.6", "Kimi K2.6", 262144, 65536},
	{"moonshotai/Kimi-K2.5", "Kimi K2.5", 262144, 65536},
	{"zai-org/GLM-5.3", "GLM 5.3", 1000000, 131072},
	{"z-ai/glm-5.3-flash", "GLM 5.3 Flash", 1048576, 65536},
	{"zai-org/GLM-5.2", "GLM 5.2", 1048576, 131072},
	{"zai-org/GLM-5.2-Fast", "GLM 5.2 Fast", 1048576, 65536},
	{"zai-org/GLM-5.1", "GLM 5.1", 204800, 32768},
	{"zai-org/GLM-5", "GLM 5", 204800, 32768},
	{"MiniMaxAI/MiniMax-M3", "MiniMax M3", 1048576, 131072},
	{"MiniMaxAI/MiniMax-M2.7", "MiniMax M2.7", 204800, 65536},
	{"MiniMaxAI/MiniMax-M2.5", "MiniMax M2.5", 204800, 65536},
	{"xiaomi/mimo-v2.5-pro", "MiMo V2.5 Pro", 1048576, 131072},
	{"xiaomi/mimo-v2.5", "MiMo V2.5", 1048576, 131072},
	{"Qwen/Qwen3.8-Max", "Qwen 3.8 Max", 1000000, 131072},
	{"Qwen/Qwen3.8-Max-0902", "Qwen 3.8 Max 0902", 1000000, 131072},
	{"Qwen/Qwen3.8-27B", "Qwen 3.8 27B", 262144, 65536},
	{"Qwen/Qwen3.8-Flash", "Qwen 3.8 Flash", 1000000, 131072},
	{"Qwen/Qwen3.7-Max", "Qwen 3.7 Max", 1048576, 131072},
	{"Qwen/Qwen3.7-Plus", "Qwen 3.7 Plus", 1048576, 131072},
	{"Qwen/Qwen3.7-Flash", "Qwen 3.7 Flash", 1000000, 131072},
	{"Qwen/Qwen3.6-Max-Preview", "Qwen 3.6 Max Preview", 204800, 32768},
	{"Qwen/Qwen3.6-Plus", "Qwen 3.6 Plus", 204800, 32768},
	{"stepfun/Step-3.7-Flash", "Step 3.7 Flash", 262144, 65536},
	{"stepfun/Step-3.5-Flash", "Step 3.5 Flash", 1048576, 65536},
	{"tencent/hy3-paid", "Tencent Hy3", 262144, 65536},
	{"tencent/hy4-preview", "Tencent Hy4 Preview", 1048576, 131072},
	{"nvidia/nemotron-3-ultra-550b-a55b", "Nemotron 3 Ultra", 1048576, 131072},
	{"thinkingmachines/inkling", "Inkling", 262144, 65536},
	{"thinkingmachines/inkling-small", "Inkling Small", 1000000, 65536},
	{"meituan/LongCat-2.0:free", "LongCat 2.0 (free)", 1048576, 131072},
	{"poolside/laguna-s-2.1-free", "Laguna S 2.1 (free)", 256000, 65536},
	{"gpt-5.6-luna", "GPT-5.6 Luna", 1050000, 131072},
	{"xai/grok-4.5", "Grok 4.5", 500000, 131072},
	{"meta/muse-spark-1.2-contributor", "Muse Spark 1.2 Contributor", 262144, 65536},
	{"meta/muse-spark-1.3-contributor", "Muse Spark 1.3 Contributor", 1048576, 65536},
}

func registeredModels() []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(modelTable))
	for _, def := range modelTable {
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
	for _, def := range modelTable {
		if strings.EqualFold(def.id, trimmed) {
			return def.id, true
		}
	}
	if !strings.Contains(trimmed, "/") {
		for _, def := range modelTable {
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
