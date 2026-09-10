package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Remote model roster. The Action-maintained models.json (see
// scripts/extract-models.py) may replace the compiled modelTable at
// runtime. The compiled table is always the fallback: any fetch, parse, or
// validation failure keeps the previous snapshot (or the compiled table),
// never an empty list, so registration works offline and on first boot.

const (
	modelsSchemaVersion = 1
	// defaultModelsURL tracks the main branch of this repository. The
	// workflow in .github/workflows/models.yml refreshes it from the
	// latest official CLI release.
	defaultModelsURL = "https://raw.githubusercontent.com/megumin31/cmdcode-go/main/models.json"
	// defaultRefreshInterval mirrors the daily Action cadence.
	defaultRefreshInterval = 24 * time.Hour
	// modelsFetchTimeout bounds one refresh attempt; model registration
	// is not latency-critical but must never hang the host.
	modelsFetchTimeout = 8 * time.Second
	// modelsMinRetryInterval rate-limits attempts after failures so a
	// dead URL cannot slow every registration call.
	modelsMinRetryInterval = 5 * time.Minute
	// modelsMustContain guards against truncated/foreign payloads.
	modelsMustContain = "deepseek/deepseek-v4-flash"
	// modelsFallbackContext/Output fill entries that omit budgets,
	// erring on the side of 256K/64K like the compiled table.
	modelsFallbackContext int64 = 262144
	modelsFallbackOutput  int64 = 65536
)

type remoteModelFile struct {
	SchemaVersion    int           `json:"schema_version"`
	SourceCLIVersion string        `json:"source_cli_version"`
	Models           []remoteModel `json:"models"`
}

type remoteModel struct {
	ID          string `json:"id"`
	Display     string `json:"display"`
	Description string `json:"description"`
	Context     int64  `json:"context"`
	Output      int64  `json:"output"`
}

var dynMu sync.Mutex
var dynTable []modelDef
var dynOrigin string
var dynFetchedAt time.Time
var dynAttemptAt time.Time
var dynFileMT time.Time

// activeModelTable returns the remote snapshot when one is loaded,
// otherwise the compiled fallback.
func activeModelTable() []modelDef {
	dynMu.Lock()
	defer dynMu.Unlock()
	if len(dynTable) > 0 {
		return dynTable
	}
	return modelTable
}

// modelsRefreshInterval parses the operator's refresh interval. Empty means
// the default; "0" or a negative duration disables remote refresh.
func modelsRefreshInterval(cfg pluginConfig) (time.Duration, bool) {
	raw := strings.TrimSpace(cfg.ModelsRefreshInterval)
	if raw == "" {
		return defaultRefreshInterval, true
	}
	dur, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"cmdcode-go: invalid models_refresh_interval %q, using %s\n",
			raw, defaultRefreshInterval)
		return defaultRefreshInterval, true
	}
	if dur <= 0 {
		return 0, false
	}
	return dur, true
}

// maybeRefreshModels best-effort refreshes the roster from models_file (when
// set, reloaded on mtime change) or models_url (when TTL-expired). It never
// returns an error: failures keep the previous snapshot and are logged.
func maybeRefreshModels() {
	cfg := getConfig()
	interval, enabled := modelsRefreshInterval(cfg)
	if !enabled {
		dynMu.Lock()
		if len(dynTable) > 0 {
			dynTable = nil
			dynOrigin = ""
			fmt.Fprintf(os.Stderr,
				"cmdcode-go: remote model refresh disabled, using compiled roster\n")
		}
		dynMu.Unlock()
		return
	}

	file := strings.TrimSpace(cfg.ModelsFile)
	url := strings.TrimSpace(cfg.ModelsURL)
	if url == "" {
		url = defaultModelsURL
	}
	origin := "url:" + url
	if file != "" {
		origin = "file:" + file
	}

	dynMu.Lock()
	staleOrigin := origin != dynOrigin
	var fileMT time.Time
	haveFile := false
	if file != "" {
		if fi, err := os.Stat(file); err != nil {
			if len(dynTable) == 0 && time.Since(dynAttemptAt) >= modelsMinRetryInterval {
				dynAttemptAt = time.Now()
				dynMu.Unlock()
				fmt.Fprintf(os.Stderr,
					"cmdcode-go: models file %q unreadable (%v), using compiled roster\n",
					file, err)
				return
			}
			dynMu.Unlock()
			return
		} else {
			haveFile = true
			fileMT = fi.ModTime()
		}
	}
	need := staleOrigin || len(dynTable) == 0
	if !need && haveFile {
		need = fileMT.After(dynFileMT)
	}
	if !need && !haveFile {
		need = time.Since(dynFetchedAt) >= interval &&
			time.Since(dynAttemptAt) >= modelsMinRetryInterval
	}
	if need && !haveFile {
		dynAttemptAt = time.Now()
	}
	dynMu.Unlock()
	if !need {
		return
	}

	var (
		raw []byte
		err error
	)
	if haveFile {
		raw, err = os.ReadFile(file)
	} else {
		raw, err = fetchModels(url)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"cmdcode-go: models refresh from %s failed (%v), keeping %s\n",
			origin, err, describeRoster())
		return
	}
	defs, cliVer, err := parseModelsFile(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"cmdcode-go: models refresh from %s rejected (%v), keeping %s\n",
			origin, err, describeRoster())
		return
	}
	dynMu.Lock()
	dynTable = defs
	dynOrigin = origin
	dynFetchedAt = time.Now()
	dynFileMT = fileMT
	dynMu.Unlock()
	fmt.Fprintf(os.Stderr,
		"cmdcode-go: models refreshed from %s: %d models (cli %s)\n",
		origin, len(defs), cliVer)
}

func describeRoster() string {
	dynMu.Lock()
	defer dynMu.Unlock()
	if len(dynTable) > 0 {
		return fmt.Sprintf("previous snapshot (%d models)", len(dynTable))
	}
	return fmt.Sprintf("compiled roster (%d models)", len(modelTable))
}

func fetchModels(url string) ([]byte, error) {
	client := &http.Client{Timeout: modelsFetchTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// parseModelsFile validates a models.json payload and converts it to model
// defs. Unknown fields are ignored so the schema can grow without breaking
// older plugins.
func parseModelsFile(raw []byte) ([]modelDef, string, error) {
	var doc remoteModelFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, "", fmt.Errorf("invalid JSON: %w", err)
	}
	if doc.SchemaVersion != modelsSchemaVersion {
		return nil, "", fmt.Errorf("schema_version %d, want %d",
			doc.SchemaVersion, modelsSchemaVersion)
	}
	if len(doc.Models) == 0 {
		return nil, "", fmt.Errorf("no models in payload")
	}
	if len(doc.Models) > 300 {
		return nil, "", fmt.Errorf("%d models, want at most 300",
			len(doc.Models))
	}
	defs := make([]modelDef, 0, len(doc.Models))
	skipped := 0
	for _, m := range doc.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			skipped++
			continue
		}
		display := strings.TrimSpace(m.Display)
		if display == "" {
			display = id
		}
		context := m.Context
		if context <= 0 {
			context = modelsFallbackContext
		}
		output := m.Output
		if output <= 0 {
			output = modelsFallbackOutput
		}
		defs = append(defs, modelDef{
			id:      id,
			display: display,
			context: context,
			output:  output,
		})
	}
	if len(defs) == 0 {
		return nil, "", fmt.Errorf("no usable model entries (%d skipped)",
			skipped)
	}
	found := false
	for _, def := range defs {
		if strings.EqualFold(def.id, modelsMustContain) {
			found = true
			break
		}
	}
	if !found {
		return nil, "", fmt.Errorf("missing anchor model %q", modelsMustContain)
	}
	cliVer := strings.TrimSpace(doc.SourceCLIVersion)
	if cliVer == "" {
		cliVer = "unknown"
	}
	return defs, cliVer, nil
}
