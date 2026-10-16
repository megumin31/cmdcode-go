package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"cpa-cmdcode-go/internal/buildinfo"
)

func TestMetadata(t *testing.T) {
	fields := map[string]string{
		"provider": buildinfo.Provider,
		"version":  buildinfo.Version,
		"filename": buildinfo.Filename,
	}
	var out bytes.Buffer
	if err := run(nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(fields) {
		t.Fatalf("unexpected fields: %v", got)
	}
	for field, want := range fields {
		t.Run(field, func(t *testing.T) {
			if got[field] != want {
				t.Fatalf("JSON %s = %q, want %q", field, got[field], want)
			}
			var out bytes.Buffer
			if err := run([]string{"--field", field}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			if out.String() != want+"\n" {
				t.Fatalf("field output = %q, want %q", out.String(), want+"\n")
			}
		})
	}
	if !strings.HasPrefix(got["filename"], got["provider"]+"-v"+got["version"]) {
		t.Fatal("filename does not match metadata")
	}
}

func TestRejectInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--field", "unknown"}, {"extra"}, {"--unknown"}, {"--field"}} {
		if err := run(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
