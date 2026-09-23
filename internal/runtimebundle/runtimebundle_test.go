package runtimebundle

import (
	"strings"
	"testing"
)

func TestRuntimeBundleReleaseURLsUseLauncherRepo(t *testing.T) {
	checks := map[string]string{
		"DefaultBundleURL":   DefaultBundleURL,
		"releasesListAPIURL": releasesListAPIURL,
	}
	for name, value := range checks {
		if !strings.Contains(value, "kon-218/ligand-x-launcher") {
			t.Fatalf("%s should use ligand-x-launcher releases, got %q", name, value)
		}
		if strings.Contains(value, "kon-218/ligand-x/releases") {
			t.Fatalf("%s still points at the core app release repo: %q", name, value)
		}
	}
}
