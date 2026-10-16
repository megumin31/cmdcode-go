package buildinfo

import (
	"regexp"
	"strings"
	"testing"
)

func TestHostFilenameContract(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Version) {
		t.Fatalf("plugin version must be numeric, got %q", Version)
	}
	// Pinned CPA's pluginFileFromPath splits at the last -v before .so.
	name := strings.TrimSuffix(Filename, ".so")
	i := strings.LastIndex(name, "-v")
	if i <= 0 || name[:i] != Provider || name[i+2:] != Version {
		t.Fatalf("filename %q does not resolve to %s version %s", Filename, Provider, Version)
	}
}
