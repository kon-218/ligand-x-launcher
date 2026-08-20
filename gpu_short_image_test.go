package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gpu-short queue is the only queue that carries both free tasks
// (md_optimize, workflow_run) and Pro tasks (admet_predict, boltz_predict,
// boltz_batch, rbfe_mapping_preview). The public worker image serves the free
// tasks without registry credentials; the Pro image is required only when a
// group that submits Pro gpu-short work is selected.
func TestGPUShortImageOverride(t *testing.T) {
	const proPrefix = "ghcr.io/kon-218/ligand-x-pro"
	const proVersion = "v2026.08.15-rc.10"
	proImage := proPrefix + "/worker-gpu-short:" + proVersion

	cases := []struct {
		name   string
		groups []string
		want   string
	}{
		{"no selection uses the public default", nil, ""},
		{"free groups only use the public default", []string{"md", "docking", "structure"}, ""},
		{"admet submits admet_predict to gpu-short", []string{"md", "admet"}, proImage},
		{"boltz2 submits boltz_predict to gpu-short", []string{"boltz2"}, proImage},
		{"free-energy submits rbfe_mapping_preview to gpu-short", []string{"free-energy"}, proImage},
		{"qc uses its own worker and queue", []string{"md", "qc"}, ""},
		{"reinvent uses its own worker and queue", []string{"md", "reinvent"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gpuShortImageOverride(tc.groups, proPrefix, proVersion)
			if got != tc.want {
				t.Fatalf("gpuShortImageOverride(%v) = %q, want %q", tc.groups, got, tc.want)
			}
		})
	}
}

// compose resolves Pro image tags as ${PRO_VERSION:-${VERSION:-latest}}. The
// launcher writes a complete image reference for worker-gpu-short, so it has to
// resolve the tag the same way or it will pull one tag and run another.
func TestProductionProVersionMatchesComposeResolution(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"PRO_VERSION wins when set", "VERSION=v-core\nPRO_VERSION=v-pro\n", "v-pro"},
		{"falls back to VERSION when unset", "VERSION=v-core\n", "v-core"},
		{"falls back to VERSION when blank", "VERSION=v-core\nPRO_VERSION=\n", "v-core"},
		{"latest when neither is set", "SOMETHING=else\n", "latest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			app := NewApp()
			app.projectPath = tmpDir
			if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(tc.env), 0644); err != nil {
				t.Fatal(err)
			}
			if got := app.productionProVersion(); got != tc.want {
				t.Fatalf("productionProVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A free-tier user must be able to run MD without private-registry credentials.
// The md group used to list the Pro worker-gpu-short image under
// RegistryAuthImages, which made needsProRegistryAuth true for a free-only
// selection and failed the pull with "public launcher requires the short-lived
// registry token broker or signed bridge credentials".
func TestFreeSelectionNeedsNoProRegistryAuth(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"),
		[]byte("VERSION=v-core\nPRO_VERSION=v-pro\n"), 0644); err != nil {
		t.Fatal(err)
	}

	groupMap := make(map[string]ServiceGroup)
	for _, g := range app.GetServiceGroups() {
		groupMap[g.ID] = g
	}

	freeOnly := []string{"md", "docking", "structure"}
	if needsProRegistryAuth(freeOnly, groupMap) {
		t.Fatalf("free-only selection %v must not require Pro registry auth", freeOnly)
	}

	// The Pro groups that submit gpu-short work still must.
	for _, id := range []string{"admet", "boltz2", "free-energy"} {
		if !needsProRegistryAuth([]string{id}, groupMap) {
			t.Fatalf("group %q submits Pro gpu-short work and must require registry auth", id)
		}
	}
}

// The md group must pull the public worker image, and it must be tagged with
// VERSION -- not PRO_VERSION, which names a tag that does not exist in the
// public repository.
func TestMDGroupPullsPublicGPUShortWorker(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"),
		[]byte("VERSION=v-core\nPRO_VERSION=v-pro\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var md ServiceGroup
	for _, g := range app.GetServiceGroups() {
		if g.ID == "md" {
			md = g
		}
	}
	if md.ID == "" {
		t.Fatal("md group not found")
	}

	want := "ghcr.io/kon-218/ligand-x/worker-gpu-short:v-core"
	found := false
	for _, image := range md.Images {
		if image == want {
			found = true
		}
		if strings.Contains(image, "ligand-x-pro") {
			t.Fatalf("md is a free group but pulls a Pro image: %s", image)
		}
	}
	if !found {
		t.Fatalf("md group images %v do not include %s", md.Images, want)
	}
}

// The override has to be recomputed on every compose invocation, not written
// once at selection time: enabling a Pro group later must start pulling and
// running the Pro worker, and disabling it must go back to the public one.
// Getting this wrong fails silently -- _register_optional_task logs a warning
// and the worker boots fine, then Pro jobs die at dispatch with a KeyError.
func TestEnsureProductionEnvSyncsGPUShortImage(t *testing.T) {
	writeSelection := func(t *testing.T, configDir string, groups []string) {
		t.Helper()
		payload, err := json.Marshal(LauncherConfig{ConfigVersion: 1, SelectedGroups: groups})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), payload, 0644); err != nil {
			t.Fatal(err)
		}
	}

	tmpDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", configDir)

	app := NewApp()
	app.projectPath = tmpDir
	template := "VERSION=v-core\nPRO_VERSION=v-pro\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}

	readOverride := func(t *testing.T) string {
		t.Helper()
		content, err := app.GetEnvContent("prod")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(parseEnvFile(content)["LIGANDX_GPU_SHORT_IMAGE"])
	}

	// Free-only selection: the public default must apply.
	writeSelection(t, configDir, []string{"core", "md", "docking"})
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv: %v", err)
	}
	if got := readOverride(t); got != "" {
		t.Fatalf("free selection must leave LIGANDX_GPU_SHORT_IMAGE unset, got %q", got)
	}

	// Enabling admet later must switch to the Pro worker, at the Pro tag.
	writeSelection(t, configDir, []string{"core", "md", "admet"})
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv: %v", err)
	}
	want := "ghcr.io/kon-218/ligand-x-pro/worker-gpu-short:v-pro"
	if got := readOverride(t); got != want {
		t.Fatalf("after enabling admet, LIGANDX_GPU_SHORT_IMAGE = %q, want %q", got, want)
	}

	// Disabling it again must go back to the public default rather than leaving
	// a stale Pro reference that needs credentials nobody has any more.
	writeSelection(t, configDir, []string{"core", "md"})
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv: %v", err)
	}
	if got := readOverride(t); got != "" {
		t.Fatalf("after disabling admet, LIGANDX_GPU_SHORT_IMAGE must be cleared, got %q", got)
	}
}

// A typo in proGPUShortGroups fails silently: the group would never trigger the
// Pro worker, and its jobs would die at dispatch exactly the way admet's did.
// Every ID in that map must name a real Pro group.
func TestProGPUShortGroupsNameRealProGroups(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir

	known := make(map[string]ServiceGroup)
	for _, g := range app.GetServiceGroups() {
		known[g.ID] = g
	}

	for id := range proGPUShortGroups {
		group, ok := known[id]
		if !ok {
			t.Errorf("proGPUShortGroups names %q, which is not a service group ID", id)
			continue
		}
		if group.Edition != "pro" {
			t.Errorf("proGPUShortGroups names %q, which is edition %q, not pro", id, group.Edition)
		}
	}
}

// Air-gapped and mirrored installs redirect Pro images with
// LIGANDX_PRO_IMAGE_PREFIX. The computed override must honour it rather than
// hard-coding ghcr.io, or the sync would overwrite a working mirror with an
// unreachable reference on every start.
func TestGPUShortOverrideHonoursMirrorPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", configDir)

	payload, err := json.Marshal(LauncherConfig{ConfigVersion: 1, SelectedGroups: []string{"admet"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), payload, 0644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.projectPath = tmpDir
	env := "VERSION=v-core\nPRO_VERSION=v-pro\nLIGANDX_PRO_IMAGE_PREFIX=registry.internal/ligandx-pro\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(env), 0644); err != nil {
		t.Fatal(err)
	}

	if err := app.syncGPUShortImage(); err != nil {
		t.Fatalf("syncGPUShortImage: %v", err)
	}

	content, err := app.GetEnvContent("prod")
	if err != nil {
		t.Fatal(err)
	}
	want := "registry.internal/ligandx-pro/worker-gpu-short:v-pro"
	if got := strings.TrimSpace(parseEnvFile(content)["LIGANDX_GPU_SHORT_IMAGE"]); got != want {
		t.Fatalf("LIGANDX_GPU_SHORT_IMAGE = %q, want %q", got, want)
	}
}
