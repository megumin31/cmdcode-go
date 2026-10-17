// Package buildinfo owns plugin identity and the CPA-compatible numeric version.
package buildinfo

const Provider = "cmdcode-go"

// Version is injected from the Git tag at build time. 0.0.0 is an unreleased
// development build, not a release version. Keep the ABI filename numeric.
var Version = "0.0.0"

// BuildTag preserves the full release tag or identifies a development build.
var BuildTag = "dev"

var Filename = Provider + "-v" + Version + ".so"
