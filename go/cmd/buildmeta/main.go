// buildmeta exposes release metadata to Make and CI without parsing Go source.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"cpa-cmdcode-go/internal/buildinfo"
)

func run(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("buildmeta", flag.ContinueOnError)
	flags.SetOutput(errOut)
	field := flags.String("field", "", "print provider, version, or filename; default: JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	metadata := struct {
		Provider string `json:"provider"`
		Version  string `json:"version"`
		Filename string `json:"filename"`
	}{buildinfo.Provider, buildinfo.Version, buildinfo.Filename}
	var value string
	switch *field {
	case "":
		return json.NewEncoder(out).Encode(metadata)
	case "provider":
		value = metadata.Provider
	case "version":
		value = metadata.Version
	case "filename":
		value = metadata.Filename
	default:
		return fmt.Errorf("unknown metadata field %q", *field)
	}
	_, err := fmt.Fprintln(out, value)
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
