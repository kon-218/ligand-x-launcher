package launcher

import (
	"slices"
	"testing"
)

func TestUnusedGroupImagesNeverSelectsSharedOrRequiredImages(t *testing.T) {
	groups := []ServiceGroup{
		{ID: "core", Required: true, Images: []string{"gateway:v1", "postgres:16"}},
		{ID: "md", Images: []string{"md:v1", "worker-gpu-short:v1"}},
		{ID: "admet", Images: []string{"admet:v1", "worker-gpu-short:v1"}},
		{ID: "odd", Images: []string{"odd:v1", "postgres:16"}},
	}

	// md is removed while admet stays: the worker image they share must survive.
	got := unusedGroupImages(groups, []string{"md"}, []string{"core", "admet"})
	if !slices.Equal(got, []string{"md:v1"}) {
		t.Fatalf("removing md while keeping admet = %v, want only md:v1", got)
	}

	// Both gone: the shared image is now unused, and listed once.
	got = unusedGroupImages(groups, []string{"md", "admet"}, []string{"core"})
	if !slices.Equal(got, []string{"md:v1", "worker-gpu-short:v1", "admet:v1"}) {
		t.Fatalf("removing md and admet = %v", got)
	}

	// An image a required group uses is kept even if the caller forgets to
	// list that group as kept.
	got = unusedGroupImages(groups, []string{"odd"}, nil)
	if !slices.Equal(got, []string{"odd:v1"}) {
		t.Fatalf("removing odd = %v, want postgres kept for core", got)
	}

	// A required group can never be removed, and a group that is both removed
	// and kept is kept.
	if got := unusedGroupImages(groups, []string{"core"}, nil); len(got) != 0 {
		t.Fatalf("required group images offered for removal: %v", got)
	}
	if got := unusedGroupImages(groups, []string{"md"}, []string{"md"}); len(got) != 0 {
		t.Fatalf("kept group images offered for removal: %v", got)
	}
}

func TestServiceGroupsFlagGPUOnlyModules(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	byID := make(map[string]ServiceGroup)
	for _, g := range app.GetServiceGroups() {
		byID[g.ID] = g
	}
	if g, ok := byID["free-energy"]; !ok || !g.RequiresGPU {
		t.Fatalf("free-energy must be flagged as GPU-only (found=%v, flag=%v)", ok, g.RequiresGPU)
	}
	// md falls back to the CPU platform, so it must stay selectable anywhere.
	if g, ok := byID["md"]; !ok || g.RequiresGPU {
		t.Fatalf("md must not be flagged as GPU-only (found=%v, flag=%v)", ok, g.RequiresGPU)
	}
	if g := byID["core"]; g.RequiresGPU {
		t.Fatal("core must not be flagged as GPU-only")
	}
}

func TestDismissedUpdateVersionSurvivesConfigRoundTrip(t *testing.T) {
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	app := NewApp()
	want := LauncherConfig{FirstRunDone: true, SelectedGroups: []string{"core"}, DismissedUpdateVersion: "v1.2.0"}
	if err := app.SaveLauncherConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetLauncherConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.DismissedUpdateVersion != "v1.2.0" {
		t.Fatalf("DismissedUpdateVersion = %q, want v1.2.0", got.DismissedUpdateVersion)
	}
}

func TestRestartServiceRejectsUnknownService(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	if err := app.RestartService("gateway; rm -rf /"); err == nil {
		t.Fatal("a name that is not a Ligand-X service must be rejected before docker runs")
	}
}
