package launcher

import (
	"os"
	"path/filepath"
	"testing"

	"ligandx-launcher/internal/envfile"
)

func TestCompiledPreviewSelectsPrereleasesOnFreshInstall(t *testing.T) {
	previous := enabledPreviewBundles
	previousVersion := launcherVersion
	t.Cleanup(func() { SetPreviewBundles(previous); launcherVersion = previousVersion })
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	app := NewApp()
	launcherVersion = "v0.1.4"
	SetPreviewBundles("")
	if app.GetShowPrereleases() {
		t.Fatal("fresh stable install must stay on stable")
	}
	SetPreviewBundles("proto-mutation")
	if app.GetShowPrereleases() {
		t.Fatal("optional Proto must not move a stable installation to prereleases")
	}
	launcherVersion = "v0.1.4-preview.1"
	if !app.GetShowPrereleases() {
		t.Fatal("fresh compiled preview would download an incompatible stable runtime")
	}
}

func TestSelectingProtoPreservesPreparedManifestRoots(t *testing.T) {
	previous := enabledPreviewBundles
	previousVersion := launcherVersion
	t.Cleanup(func() { SetPreviewBundles(previous); launcherVersion = previousVersion })
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	SetPreviewBundles("proto-mutation")
	app := NewApp()
	app.projectPath = t.TempDir()
	path := filepath.Join(app.projectPath, ".env.production")
	original := "PROTO_HOME=/home/tester/proto\nPROTO_MODEL_CACHE=/home/tester/proto-models\nLIGANDX_PREVIEW_MODULES=\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveLauncherConfig(LauncherConfig{ConfigVersion: 1, SelectedGroups: []string{"protein-mutation"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.syncPreviewEnv(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := envfile.Parse(string(content))
	if values["PROTO_HOME"] != "/home/tester/proto" || values["PROTO_MODEL_CACHE"] != "/home/tester/proto-models" {
		t.Fatalf("selecting Proto changed manifest paths: %v", values)
	}
	if values["LIGANDX_PREVIEW_MODULES"] != "protein_tools" {
		t.Fatalf("selecting Proto did not turn its preview on: %v", values)
	}
	if err := app.SaveLauncherConfig(LauncherConfig{ConfigVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := app.syncPreviewEnv(); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values = envfile.Parse(string(content))
	if values["LIGANDX_PREVIEW_MODULES"] != "" || values["PROTO_HOME"] != "/home/tester/proto" {
		t.Fatalf("deselecting Proto must turn its preview off and preserve assets: %v", values)
	}
}
