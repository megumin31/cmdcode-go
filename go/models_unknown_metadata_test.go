package main

import "testing"

func TestUnknownModelBudgetsRemainUnknown(t *testing.T) {
	raw := []byte(`{"schema_version":1,"source":"command-code Go plan page","models":[
 {"id":"test/unknown","context":0,"output":0,"reasoning":true,"reasoning_options_reference":{"options":["high"]}},
 {"id":"test/output-only","context":0,"output":8192},
 {"id":"test/context-only","context":1000000,"output":0},
 {"id":"test/capped","context":0,"output":0,"gateway_output_limit":4096}]}`)
	defs, _, err := parseModelsFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	snap := newModelSnapshot(defs)
	wantExplicit := 200000
	for _, d := range defs {
		registered := newModelSnapshot([]modelDef{d}).Registered(pluginConfig{})[0]
		if registered.ContextLength != d.context {
			t.Fatal("invented context", d.id)
		}
		if d.gatewayOutput == 0 && registered.OutputTokenLimit != d.output {
			t.Fatal("invented output", d.id)
		}
		want := defaultMaxTokens
		if d.output > 0 {
			want = min(want, int(d.output))
		}
		if d.gatewayOutput > 0 {
			want = min(want, int(d.gatewayOutput))
		}
		if got := snap.maxTokens(chatRequest{}, d.id); got != want {
			t.Fatalf("%s default=%d want=%d", d.id, got, want)
		}
		want = wantExplicit
		if d.gatewayOutput > 0 {
			want = int(d.gatewayOutput)
		}
		if got := snap.maxTokens(chatRequest{MaxTokens: &wantExplicit}, d.id); got != want {
			t.Fatalf("reference metadata clamped %s: %d", d.id, got)
		}
	}
	if defs[0].context != 0 || defs[0].output != 0 || defs[1].context != 0 || defs[2].output != 0 {
		t.Fatal("unknown budgets replaced")
	}
	// Reference reasoning flags/options are never a gateway validation policy.
	level := "medium"
	got, err := snap.reasoningEffort(parseModelName("test/unknown"), &level)
	if err != nil || got == nil || *got != level {
		t.Fatal("reference reasoning options restricted gateway", got, err)
	}
}

func TestFallbackProfileIsRequestDefaultNotCapability(t *testing.T) {
	raw := []byte(`{"schema_version":1,"source":"command-code Go plan page","fallback_profile":{"context":262144,"output":65536},"models":[
 {"id":"test/fallback","context":262144,"context_source":"fallback","output":65536,"output_source":"fallback"},
 {"id":"test/output-known","context":262144,"context_source":"fallback","output":300000,"output_source":"modelsdev"},
 {"id":"test/capped-fallback","context":262144,"context_source":"fallback","output":65536,"output_source":"fallback","gateway_output_limit":4096},
 {"id":"test/reference","context":1000000,"context_source":"modelsdev","output":131072,"output_source":"modelsdev"}]}`)
	defs, _, err := parseModelsFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	snap := newModelSnapshot(defs)
	models := snap.Registered(pluginConfig{})
	for i := 0; i < 3; i++ {
		if models[i].ContextLength != 0 || models[i].InputTokenLimit != 0 {
			t.Fatal("fallback context advertised as capacity", models[i])
		}
	}
	if models[0].OutputTokenLimit != 0 || models[1].OutputTokenLimit != 300000 || models[2].OutputTokenLimit != 4096 {
		t.Fatal("incorrect output registration", models)
	}
	explicit := 400000
	for _, tc := range []struct {
		id                            string
		defaultBudget, explicitBudget int
	}{
		{"test/fallback", 65536, explicit}, {"test/output-known", defaultMaxTokens, explicit},
		{"test/capped-fallback", 4096, 4096}, {"test/reference", defaultMaxTokens, explicit},
	} {
		if got := snap.maxTokens(chatRequest{}, tc.id); got != tc.defaultBudget {
			t.Fatalf("%s default %d want %d", tc.id, got, tc.defaultBudget)
		}
		if got := snap.maxTokens(chatRequest{MaxTokens: &explicit}, tc.id); got != tc.explicitBudget {
			t.Fatalf("%s explicit %d want %d", tc.id, got, tc.explicitBudget)
		}
	}
}
