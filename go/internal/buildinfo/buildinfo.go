// Package buildinfo owns plugin identity, version and its CPA-compatible filename.
// It has no runtime dependencies so both the plugin and build tooling can use it.
package buildinfo

const (
	Provider = "cmdcode-go"
	Version  = "0.1.0"
	Filename = Provider + "-v" + Version + ".so"
)
