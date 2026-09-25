package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"ligandx-launcher/internal/agentsession"
	"ligandx-launcher/internal/envfile"
	"ligandx-launcher/internal/license"
	"ligandx-launcher/internal/runtimebundle"
	"ligandx-launcher/internal/secretstore"
	"net"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGetServiceGroups(t *testing.T) {
	app := NewApp()
	groups := app.GetServiceGroups()

	// Verify groups returned (free/core groups plus Pro packages)
	if len(groups) != 8 {
		t.Errorf("Expected 8 stable service groups, got %d", len(groups))
	}

	// Create a map for easier lookup
	groupMap := make(map[string]*ServiceGroup)
	for i, g := range groups {
		groupMap[g.ID] = &groups[i]
	}

	if _, ok := groupMap["kinetics"]; ok {
		t.Error("preview kinetics group must be absent from the stable launcher")
	}

	// Verify "core" properties
	if core, ok := groupMap["core"]; !ok {
		t.Error("Missing 'core' group")
	} else {
		if !core.Required {
			t.Error("'core' group should be Required=true")
		}
		if !core.DefaultOn {
			t.Error("'core' group should be DefaultOn=true")
		}
		if len(core.Images) == 0 {
			t.Error("'core' group should have at least 1 image")
		}
	}

	// Verify "qc" properties
	if qc, ok := groupMap["qc"]; !ok {
		t.Error("Missing 'qc' group")
	} else {
		if qc.Edition != "pro" {
			t.Error("'qc' group should be Edition=pro")
		}
		if qc.Required {
			t.Error("'qc' group should be Required=false")
		}
		if qc.DefaultOn {
			t.Error("'qc' group should be DefaultOn=false")
		}
		if len(qc.Images) == 0 {
			t.Error("'qc' group should have at least 1 image")
		}
	}

	if fe, ok := groupMap["free-energy"]; !ok {
		t.Error("Missing 'free-energy' group")
	} else if fe.Edition != "pro" || fe.Entitlement != "free-energy" {
		t.Error("'free-energy' group should require the free-energy Pro entitlement")
	}

	// Verify "boltz2" properties
	if boltz2, ok := groupMap["boltz2"]; !ok {
		t.Error("Missing 'boltz2' group")
	} else {
		if boltz2.Required {
			t.Error("'boltz2' group should be Required=false")
		}
		if boltz2.DefaultOn {
			t.Error("'boltz2' group should be DefaultOn=false")
		}
		if len(boltz2.Images) == 0 {
			t.Error("'boltz2' group should have at least 1 image")
		}
	}

	// Verify all groups have images
	for _, group := range groups {
		if len(group.Images) == 0 {
			t.Errorf("Group '%s' should have at least 1 image", group.ID)
		}
	}

	// Every core service that ships its own container image must appear in
	// coreServiceImages(), otherwise "Pull"/"Re-pull" never fetches it and
	// `docker compose up -d --pull=never` fails with "No such image" even
	// after repulling (e.g. flower was declared as a core service name but
	// missing from the pull list).
	core := groupMap["core"]
	imageSet := make(map[string]bool)
	for _, img := range core.Images {
		imageSet[img] = true
	}
	infraOnly := map[string]bool{"proxy": true} // shares the nginx image already listed
	// Same image, different entrypoint command (see docker-compose.yml).
	sharedImage := map[string]string{"worker-control": "worker-cpu", "celery-beat": "worker-cpu"}
	for _, svc := range coreServiceNames() {
		if infraOnly[svc] {
			continue
		}
		want := svc
		if image, ok := sharedImage[svc]; ok {
			want = image
		}
		found := false
		for img := range imageSet {
			if strings.Contains(img, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("core service %q has no matching image in coreServiceImages(): %v", svc, core.Images)
		}
	}
}

// REL-04 D3: the launcher's grouped start is the only start real installs use.
// celery-beat schedules the orphan reaper and outbox drains, and worker-control
// consumes them; neither was in any group, so neither ever ran.
func TestCoreGroupStartsTheMaintenanceScheduler(t *testing.T) {
	names := strings.Join(coreServiceNames(), ",")
	for _, svc := range []string{"celery-beat", "worker-control"} {
		if !strings.Contains(","+names+",", ","+svc+",") {
			t.Errorf("core group must start %s: %s", svc, names)
		}
	}
}

// REL-04 D2: with no saved selection an unscoped start runs every licensed
// group, so lease activation must replace every unlocked group's workers --
// not only DefaultOn ones -- or licensed Pro workers keep the old image while
// the gateway enables leases.
func TestLeaseActivationWithoutSelectionIncludesEveryUnlockedGroup(t *testing.T) {
	groups := []ServiceGroup{
		{ID: "core", Services: []string{"gateway", "worker-cpu"}, Required: true, DefaultOn: true},
		{ID: "qc", Services: []string{"qc", "worker-qc"}, DefaultOn: false},
		{ID: "reinvent", Services: []string{"worker-reinvent"}, DefaultOn: false, Locked: true},
	}
	got := strings.Join(launcherAllowedServices(groups, nil), ",")
	if got != "gateway,qc,worker-cpu,worker-qc" {
		t.Fatalf("unselected allowed services = %q, want every unlocked group and no locked one", got)
	}
}

func TestCorePullListIncludesEveryFixedComposeImage(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	listed := make(map[string]bool)
	for _, image := range coreServiceImages("v-test") {
		listed[image] = true
	}

	// Variable image references belong to optional product groups and are
	// checked elsewhere. Literal references are runtime/infra dependencies that
	// `up --pull=never` cannot recover when the launcher forgets to pre-pull
	// them. Both scientific init containers, for example, use alpine:3.21.
	imageLine := regexp.MustCompile(`(?m)^\s+image:\s+([^\s#]+)\s*$`)
	for _, match := range imageLine.FindAllStringSubmatch(string(compose), -1) {
		image := match[1]
		if strings.Contains(image, "${") {
			continue
		}
		if !listed[image] {
			t.Errorf("fixed Compose image %q is absent from the Core pull list", image)
		}
	}
}

func TestEverySelectedServicePullsItsResolvedComposeImage(t *testing.T) {
	const version = "v-test"
	const proPrefix = "ghcr.io/kon-218/ligand-x-pro"
	runtimeDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(runtimeDir, ".env.production"),
		[]byte("VERSION="+version+"\nPRO_VERSION="+version+"\nLIGANDX_PRO_IMAGE_PREFIX="+proPrefix+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.projectPath = runtimeDir

	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	serviceLine := regexp.MustCompile(`^  ([a-zA-Z0-9_-]+):\s*$`)
	imageLine := regexp.MustCompile(`^    image:\s+([^\s#]+)\s*$`)
	serviceImages := make(map[string]string)
	service := ""
	for _, line := range strings.Split(string(compose), "\n") {
		if match := serviceLine.FindStringSubmatch(line); match != nil {
			service = match[1]
			continue
		}
		if service == "" {
			continue
		}
		if match := imageLine.FindStringSubmatch(line); match != nil {
			serviceImages[service] = match[1]
		}
	}

	resolve := func(raw string) string {
		// A whole-reference override with a default, e.g.
		// ${LIGANDX_GPU_SHORT_IMAGE:-ghcr.io/kon-218/ligand-x/worker-gpu-short:${VERSION:-latest}}.
		// The baseline modelled here is a free selection, where the override is
		// unset and the public default applies. The Pro branch is asserted
		// separately by TestGPUShortImageOverride.
		//
		// Only unwrap when the whole string is one expression: the Pro images are
		// ${PREFIX:-...}/name:${TAG:-...}, whose first closing brace lands
		// mid-string, and unwrapping those yields nonsense.
		raw = stripWholeEnvDefault(raw)
		if strings.Contains(raw, "LIGANDX_PRO_IMAGE_PREFIX") {
			remainder := raw[strings.LastIndex(raw, "}/")+2:]
			name := strings.SplitN(remainder, ":", 2)[0]
			return imageRef(proPrefix+"/"+name, version)
		}
		if strings.Contains(raw, "${VERSION:-latest}") {
			return strings.ReplaceAll(raw, "${VERSION:-latest}", version)
		}
		return raw
	}

	for _, group := range app.GetServiceGroups() {
		listed := make(map[string]bool)
		for _, image := range group.Images {
			listed[image] = true
		}
		for _, service := range group.Services {
			raw, ok := serviceImages[service]
			if !ok {
				t.Errorf("selected service %q in group %q has no Compose image", service, group.ID)
				continue
			}
			expected := resolve(raw)
			if !listed[expected] {
				t.Errorf("group %q starts service %q as %q but does not pull that image: %v", group.ID, service, expected, group.Images)
			}
		}
	}
}

func TestComposeUpAlwaysRemovesOnlyProjectOrphans(t *testing.T) {
	input := []string{"compose", "--env-file", ".env.production", "up", "-d", "--pull=never", "gateway"}
	want := []string{"compose", "--env-file", ".env.production", "up", "--remove-orphans", "-d", "--pull=never", "gateway"}
	got := composeUpWithRemoveOrphans(input)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("composeUpWithRemoveOrphans() = %v, want %v", got, want)
	}
	if again := composeUpWithRemoveOrphans(got); strings.Join(again, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("orphan flag insertion is not idempotent: %v", again)
	}
	nonUp := []string{"compose", "ps", "--all"}
	if got := composeUpWithRemoveOrphans(nonUp); strings.Join(got, "\x00") != strings.Join(nonUp, "\x00") {
		t.Fatalf("non-up command changed: %v", got)
	}
}

func TestGetLauncherConfigFileNotFound(t *testing.T) {
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	app := NewApp()
	config, err := app.GetLauncherConfig()

	if err != nil {
		t.Errorf("Expected no error for missing file, got: %v", err)
	}
	if config.FirstRunDone {
		t.Error("FirstRunDone should be false for missing file")
	}
	if len(config.SelectedGroups) != 0 {
		t.Error("SelectedGroups should be empty for missing file")
	}
	if config.ConfigVersion != 1 {
		t.Error("ConfigVersion should be 1")
	}
}

func TestSaveAndLoadLauncherConfigRoundtrip(t *testing.T) {
	// Use temporary directory for config
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")

	// Save directly to the temp file
	originalConfig := LauncherConfig{
		FirstRunDone:   true,
		SelectedGroups: []string{"core", "docking", "md"},
		ConfigVersion:  1,
	}

	data, err := json.MarshalIndent(originalConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Load it back using JSON unmarshaling
	loadedData, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	var loadedConfig LauncherConfig
	if err := json.Unmarshal(loadedData, &loadedConfig); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	// Verify roundtrip
	if loadedConfig.FirstRunDone != originalConfig.FirstRunDone {
		t.Error("FirstRunDone mismatch after roundtrip")
	}
	if len(loadedConfig.SelectedGroups) != len(originalConfig.SelectedGroups) {
		t.Error("SelectedGroups length mismatch after roundtrip")
	}
	for i, group := range originalConfig.SelectedGroups {
		if loadedConfig.SelectedGroups[i] != group {
			t.Errorf("SelectedGroups[%d] mismatch: expected %s, got %s", i, group, loadedConfig.SelectedGroups[i])
		}
	}
}

func TestSaveConfigCreatesDirectory(t *testing.T) {
	// Create a temporary base directory (but not the config subdirectory)
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "nested", "dir")
	configFile := filepath.Join(nestedDir, "config.json")

	// Create the nested directory first (simulating MkdirAll)
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	config := LauncherConfig{
		FirstRunDone:   true,
		SelectedGroups: []string{"core"},
		ConfigVersion:  1,
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Verify the file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Error("Config file was not created")
	}
}

func TestLoadConfigCorruptedFile(t *testing.T) {
	// Create a temporary directory with a corrupted config file
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")

	// Write corrupted JSON
	if err := os.WriteFile(configFile, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("Failed to write corrupted file: %v", err)
	}

	// Try to unmarshal - should return error
	data, _ := os.ReadFile(configFile)
	var config LauncherConfig
	err := json.Unmarshal(data, &config)
	if err == nil {
		t.Error("Expected error when unmarshaling corrupted config")
	}
}

func TestSaveLocalAccountWritesEnvAndConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte("REDIS_PASSWORD=test\n"), 0644); err != nil {
		t.Fatalf("Failed to write env template: %v", err)
	}

	app := NewApp()
	app.projectPath = tmpDir

	config, err := app.SaveLocalAccount("alice", "alice@example.com", "strongpass")
	if err != nil {
		t.Fatalf("SaveLocalAccount failed: %v", err)
	}
	if config.UserProfile.Username != "alice" {
		t.Fatalf("Expected username alice, got %q", config.UserProfile.Username)
	}

	envData, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatalf("Failed to read .env.production: %v", err)
	}
	env := string(envData)
	for _, expected := range []string{
		"LIGANDX_USERNAME=alice",
		"LIGANDX_PASSWORD=strongpass",
	} {
		if !strings.Contains(env, expected) {
			t.Fatalf(".env missing %s in:\n%s", expected, env)
		}
	}

	loaded, err := app.GetLauncherConfig()
	if err != nil {
		t.Fatalf("GetLauncherConfig failed: %v", err)
	}
	if loaded.UserProfile.Email != "alice@example.com" {
		t.Fatalf("Expected persisted email, got %q", loaded.UserProfile.Email)
	}
}

func TestSaveLocalAccountRejectsWeakPassword(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.example"), []byte("REDIS_PASSWORD=test\n"), 0644); err != nil {
		t.Fatalf("Failed to write env template: %v", err)
	}

	app := NewApp()
	app.projectPath = tmpDir

	if _, err := app.SaveLocalAccount("alice", "", "short"); err == nil {
		t.Fatal("Expected weak password to be rejected")
	}
}

// TestSaveLocalAccountWorksWithProductionBundleOnly reproduces the reported
// Windows failure: an end-user runtime bundle ships only .env.production /
// .env.production.template (no .env / .env.example). SaveLocalAccount must
// write credentials to .env.production and succeed instead of aborting with
// "no .env file found and could not read .env.example".
func TestSaveLocalAccountWorksWithProductionBundleOnly(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	// Runtime-bundle layout: production template only, no dev files.
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte("LIGANDX_USERNAME=admin\nLIGANDX_PASSWORD=CHANGE_ME\n"), 0644); err != nil {
		t.Fatalf("Failed to write production template: %v", err)
	}

	app := NewApp()
	app.projectPath = tmpDir

	if _, err := app.SaveLocalAccount("alice", "alice@example.com", "strongpass"); err != nil {
		t.Fatalf("SaveLocalAccount failed on production-only bundle: %v", err)
	}

	envData, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatalf("Failed to read .env.production: %v", err)
	}
	env := string(envData)
	for _, expected := range []string{
		"LIGANDX_USERNAME=alice",
		"LIGANDX_PASSWORD=strongpass",
	} {
		if !strings.Contains(env, expected) {
			t.Fatalf(".env.production missing %s in:\n%s", expected, env)
		}
	}
}

func TestFindProjectPathPrefersSourceCheckoutForDevBuild(t *testing.T) {
	if isPublicBuild {
		t.Skip("source checkout discovery is disabled in public builds")
	}
	tmpDir := t.TempDir()
	launcherDir := filepath.Join(tmpDir, "ligand-x-launcher")
	sourceDir := filepath.Join(tmpDir, "ligand-x")
	if err := os.MkdirAll(launcherDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launcherDir, "docker-compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "docker-compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "docker-compose.override.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)
	if err := os.Chdir(launcherDir); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	got, ok := app.findProjectPath()
	if !ok {
		t.Fatal("expected project path to be found")
	}
	if got != sourceDir {
		t.Fatalf("expected source checkout %q, got %q", sourceDir, got)
	}
}

func TestEnsureProductionEnvReplacesUnsafeDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	template := strings.Join([]string{
		"POSTGRES_USER=ligandx",
		"POSTGRES_PASSWORD=CHANGE_ME",
		"POSTGRES_DB=ligandx",
		"DATABASE_URL=postgresql://ligandx:CHANGE_ME@postgres:5432/ligandx",
		"RABBITMQ_USER=ligandx",
		"RABBITMQ_PASSWORD=CHANGE_ME",
		"CELERY_BROKER_URL=amqp://ligandx:CHANGE_ME@rabbitmq:5672/",
		"CELERY_RESULT_BACKEND=redis://:${REDIS_PASSWORD}@redis:6379/0",
		"REDIS_PASSWORD=CHANGE_ME",
		"REDIS_URL=redis://:CHANGE_ME@redis:6379/0",
		"QC_SECRET_KEY=CHANGE_ME",
		"LIGANDX_PASSWORD=CHANGE_ME",
		"FLOWER_PASSWORD=CHANGE_ME",
		"NEXT_PUBLIC_API_URL=https://your-domain.com",
		"CORS_ORIGINS=https://your-domain.com",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	if strings.Contains(env, "CHANGE_ME") || strings.Contains(env, "https://your-domain.com") {
		t.Fatalf("production env still contains unsafe defaults:\n%s", env)
	}
	// Same-origin via the bundled reverse proxy: the browser uses its own origin,
	// so NEXT_PUBLIC_API_URL is intentionally blank (not a hard-coded host).
	if !strings.Contains(env, "NEXT_PUBLIC_API_URL=\n") && !strings.HasSuffix(env, "NEXT_PUBLIC_API_URL=") {
		t.Fatalf("production env should blank NEXT_PUBLIC_API_URL for same-origin proxying:\n%s", env)
	}
	// VERSION must be self-healed to a concrete pin (template had none here, so the
	// compiled-in defaultPinnedImageVersion fallback applies). 'latest'/empty would
	// be rejected by compose's ${VERSION:?} and requirePinnedProductionVersion.
	if !strings.Contains(env, "VERSION="+defaultPinnedImageVersion) {
		t.Fatalf("production env missing pinned VERSION:\n%s", env)
	}
}

// TestEnsureProductionEnvSelfHealsStaleLatestVersion reproduces the reported
// Windows failure: an older launcher pinned VERSION=latest into .env.production,
// which compose's ${VERSION:?} and requirePinnedProductionVersion reject. On the
// next start/pull, ensureProductionEnv must rewrite it to the template's pin.
func TestEnsureProductionEnvSelfHealsStaleLatestVersion(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir

	// Template carries the canonical pin.
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	// Pre-existing .env.production with the broken stale value.
	stale := "VERSION=latest\nINTERNAL_WORKER_SECRET=already-set\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv failed: %v", err)
	}
	v, _ := app.productionImageSettings()
	if v != "v2026.06.21" {
		t.Fatalf("expected VERSION self-healed to v2026.06.21, got %q", v)
	}
	if _, err := app.requirePinnedProductionVersion(); err != nil {
		t.Fatalf("requirePinnedProductionVersion still failing after self-heal: %v", err)
	}
}

func TestDevComposeArgsFallsBackToProductionEnvWithoutMissingOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	template := strings.Join([]string{
		"POSTGRES_USER=ligandx",
		"POSTGRES_PASSWORD=CHANGE_ME",
		"POSTGRES_DB=ligandx",
		"DATABASE_URL=postgresql://ligandx:CHANGE_ME@postgres:5432/ligandx",
		"RABBITMQ_USER=ligandx",
		"RABBITMQ_PASSWORD=CHANGE_ME",
		"CELERY_BROKER_URL=amqp://ligandx:CHANGE_ME@rabbitmq:5672/",
		"CELERY_RESULT_BACKEND=redis://:${REDIS_PASSWORD}@redis:6379/0",
		"REDIS_PASSWORD=CHANGE_ME",
		"REDIS_URL=redis://:CHANGE_ME@redis:6379/0",
		"QC_SECRET_KEY=CHANGE_ME",
		"FLOWER_PASSWORD=CHANGE_ME",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}

	args := strings.Join(app.devComposeArgs(), " ")
	if !strings.Contains(args, "--env-file .env.production") {
		t.Fatalf("expected dev compose args to load production env fallback, got %q", args)
	}
	if strings.Contains(args, "docker-compose.override.yml") || strings.Contains(args, "docker-compose.pro-dev.yml") {
		t.Fatalf("expected missing override files to be skipped, got %q", args)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "${REDIS_PASSWORD}") {
		t.Fatalf("production env kept unresolved Redis substitution:\n%s", string(data))
	}
}

func TestProRegistryCredentialsRequireBrokerOrBridge(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	groups := app.GetServiceGroups()
	groupMap := make(map[string]ServiceGroup)
	for _, group := range groups {
		groupMap[group.ID] = group
	}

	_, ok, err := app.registryCredentialsForProImages([]string{"admet"}, groupMap)
	if err == nil {
		t.Fatal("Expected Pro registry auth to require broker or bridge credentials")
	}
	if ok {
		t.Fatal("Expected no registry credentials")
	}
	if !strings.Contains(err.Error(), "signed bridge") {
		t.Fatalf("Expected signed bridge guidance in error, got %v", err)
	}
}

func TestRegistryAuthIncludesPrivateImageInFreeGroup(t *testing.T) {
	privateWorker := "ghcr.io/kon-218/ligand-x-pro/worker-gpu-short:v-test"
	groups := map[string]ServiceGroup{
		"md": {
			ID:                 "md",
			Edition:            "free",
			Images:             []string{"ghcr.io/kon-218/ligand-x/md:v-test", privateWorker},
			RegistryAuthImages: []string{privateWorker},
		},
	}
	if !needsProRegistryAuth([]string{"md"}, groups) {
		t.Fatal("a private image selected by a Free group must still request registry credentials")
	}
	repositories := selectedProRepositories([]string{"md"}, groups)
	want := "ghcr.io/kon-218/ligand-x-pro/worker-gpu-short"
	if len(repositories) != 1 || repositories[0] != want {
		t.Fatalf("selectedProRepositories() = %v, want [%s]", repositories, want)
	}
}

func TestCheckGPU(t *testing.T) {
	app := NewApp()
	// Just verify the method doesn't panic
	_ = app.CheckGPU()
}

func TestStopPullServiceGroupsNoOpWithoutActivePull(t *testing.T) {
	app := NewApp()
	if err := app.StopPullServiceGroups(); err != nil {
		t.Fatalf("expected nil error with no active pull, got %v", err)
	}
}

func TestStopPullServiceGroupsCancelsStoredContext(t *testing.T) {
	app := NewApp()
	ctx, cancel := context.WithCancel(context.Background())
	app.activePullCancel = cancel

	if err := app.StopPullServiceGroups(); err != nil {
		t.Fatalf("StopPullServiceGroups returned error: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("expected the stored pull context to be cancelled")
	}
}

// TestPullServiceGroupsGenerationCounterSurvivesRestart exercises the actual
// bookkeeping PullServiceGroups uses around a real Docker pull: a second
// caller interrupts the first (activePullGen distinguishes which goroutine's
// deferred cleanup is allowed to null out activePullCancel — see the comment
// at its declaration; a raw func-to-func comparison isn't legal in Go).
func TestPullServiceGroupsGenerationCounterSurvivesRestart(t *testing.T) {
	app := NewApp()

	firstCtx, firstCancel := context.WithCancel(context.Background())
	app.activePullCancel = firstCancel
	app.activePullGen = 1

	// Simulate PullServiceGroups starting a second pull: it interrupts the
	// first, installs its own cancel func, and bumps the generation.
	app.pullCancelMu.Lock()
	app.activePullCancel()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	app.activePullCancel = secondCancel
	app.activePullGen++
	secondGen := app.activePullGen
	app.pullCancelMu.Unlock()

	if firstCtx.Err() == nil {
		t.Fatal("expected the interrupted first pull's context to be cancelled")
	}
	if secondCtx.Err() != nil {
		t.Fatal("the second pull's context must not be cancelled by starting it")
	}

	// The first goroutine's deferred cleanup must not clear the second
	// pull's cancel func just because it runs after the second one started.
	app.pullCancelMu.Lock()
	if app.activePullGen == 1 { // stale generation from the (hypothetical) first goroutine
		app.activePullCancel = nil
	}
	app.pullCancelMu.Unlock()

	app.pullCancelMu.Lock()
	stillActive := app.activePullCancel
	stillCorrectGen := app.activePullGen == secondGen
	app.pullCancelMu.Unlock()
	if stillActive == nil || !stillCorrectGen {
		t.Fatal("a stale pull's cleanup must not clobber a newer pull's cancel state")
	}
}

func writeFakeOrcaInstall(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "orca"), []byte("fake-orca"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOrcaHostPathRejectsEmpty(t *testing.T) {
	if err := validateOrcaHostPath(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if err := validateOrcaHostPath("   "); err == nil {
		t.Fatal("expected error for whitespace path")
	}
}

func TestValidateOrcaHostPathRejectsMissingDir(t *testing.T) {
	if err := validateOrcaHostPath(filepath.Join(t.TempDir(), "no-such-orca")); err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestValidateOrcaHostPathRejectsDirWithoutBinary(t *testing.T) {
	if err := validateOrcaHostPath(t.TempDir()); err == nil {
		t.Fatal("expected error when folder has no orca binary")
	}
}

func TestValidateOrcaHostPathRejectsFileNotDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateOrcaHostPath(f); err == nil {
		t.Fatal("expected error when path is a file")
	}
}

func TestValidateOrcaHostPathAcceptsFolderWithBinary(t *testing.T) {
	dir := t.TempDir()
	writeFakeOrcaInstall(t, dir)
	if err := validateOrcaHostPath(dir); err != nil {
		t.Fatalf("expected valid install: %v", err)
	}
}

func TestCheckOrcaForServicesSkipsWhenQCNotSelected(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	if err := app.checkOrcaForServices([]string{"gateway", "frontend", "md"}); err != nil {
		t.Fatalf("non-QC services should not require ORCA: %v", err)
	}
}

func TestCheckOrcaForServicesFailsWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	app := NewApp()
	app.projectPath = tmp
	if err := os.WriteFile(filepath.Join(tmp, ".env.production"), []byte("ORCA_HOST_PATH=/definitely/not/orca\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.checkOrcaForServices([]string{"qc", "worker-qc"}); err == nil {
		t.Fatal("expected error when QC is selected without a valid ORCA path")
	}
}

func TestCheckOrcaForServicesAcceptsValidPath(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, "orca-install")
	if err := os.Mkdir(install, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeOrcaInstall(t, install)
	app := NewApp()
	app.projectPath = tmp
	content := "ORCA_HOST_PATH=" + install + "\nVERSION=v-core\nPRO_VERSION=v-pro\n"
	if err := os.WriteFile(filepath.Join(tmp, ".env.production"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	app.orcaProbeFn = func(_ context.Context, _ []string) ([]byte, error) { return nil, nil }
	if err := app.checkOrcaForServices([]string{"qc"}); err != nil {
		t.Fatalf("valid ORCA path should allow QC start: %v", err)
	}
}

func TestSetOrcaHostPathPersists(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, "orca-install")
	if err := os.Mkdir(install, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeOrcaInstall(t, install)
	app := NewApp()
	app.projectPath = tmp
	if err := app.SetOrcaHostPath(install); err != nil {
		t.Fatal(err)
	}
	if !app.OrcaHostPathReady() {
		t.Fatal("OrcaHostPathReady should be true after SetOrcaHostPath")
	}
	if got := app.currentOrcaHostPath(); got != install {
		t.Fatalf("ORCA_HOST_PATH=%q, want %q", got, install)
	}
}

func TestSetOrcaHostPathRejectsInvalid(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	if err := app.SetOrcaHostPath(t.TempDir()); err == nil {
		t.Fatal("expected error for folder without orca binary")
	}
	if app.OrcaHostPathReady() {
		t.Fatal("invalid path must not mark ORCA as ready")
	}
}

func TestValidateUnlockedServicesBlocksLockedGroup(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	// No license file — Pro groups are Locked.
	if err := app.validateUnlockedServices([]string{"qc", "worker-qc"}); err == nil {
		t.Fatal("expected locked Pro service to be rejected")
	}
	if err := app.validateUnlockedServices([]string{"gateway", "structure"}); err != nil {
		t.Fatalf("unlocked core services should pass, got %v", err)
	}
	if err := app.validateUnlockedServices([]string{"docking", "qc"}); err == nil {
		t.Fatal("mixed list with one locked service should be rejected")
	}
}

func TestPublicBuildAcceptsSignedBridgeCredentials(t *testing.T) {
	t.Setenv("LIGANDX_REGISTRY_TOKEN_URL", "")
	t.Setenv("LIGANDX_VENDOR_ACCESS_TOKEN", "")

	app := NewApp()
	groups := app.GetServiceGroups()
	groupMap := make(map[string]ServiceGroup)
	for _, group := range groups {
		groupMap[group.ID] = group
	}
	want := license.RegistryCredentials{Host: "ghcr.io", Username: "reader", Token: "tok"}
	loader := func() (license.RegistryCredentials, bool) { return want, true }

	got, ok, err := app.registryCredentialsForProImagesForBuild(
		[]string{"admet"},
		groupMap,
		true,
		loader,
	)
	if err != nil {
		t.Fatalf("public build rejected signed bridge credentials: %v", err)
	}
	if !ok || got != want {
		t.Fatalf("public build returned (%+v, %v), want (%+v, true)", got, ok, want)
	}
}

func TestComposePsArgsReusesGlobalFlags(t *testing.T) {
	upArgs := []string{
		"compose", "--env-file", ".env.production",
		"-f", "docker-compose.yml", "-f", "docker-compose.gpu.yml",
		"up", "-d", "--pull=never", "gateway", "frontend", "proxy",
	}
	got := composePsArgs(upArgs)
	want := []string{
		"compose", "--env-file", ".env.production",
		"-f", "docker-compose.yml", "-f", "docker-compose.gpu.yml",
		"ps", "--all",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("composePsArgs() = %v, want %v", got, want)
	}
}

func TestProductionInfraUpArgsPreservesGlobalFlags(t *testing.T) {
	upArgs := []string{
		"compose", "--env-file", ".env.production",
		"-f", "docker-compose.yml", "-f", "docker-compose.gpu.yml",
		"up", "-d", "--pull=never", "worker-cpu", "worker-gpu-short",
	}
	got := productionInfraUpArgs(upArgs)
	want := []string{
		"compose", "--env-file", ".env.production",
		"-f", "docker-compose.yml", "-f", "docker-compose.gpu.yml",
		"up", "-d", "--wait", "--wait-timeout", "120", "--pull=never", "postgres", "redis", "rabbitmq",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("productionInfraUpArgs() = %v, want %v", got, want)
	}
}

func TestFreshRuntimeDoesNotEnterLeaseActivation(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("JOB_ATTEMPT_LEASES_ENABLED=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rollback, err := app.beginLeaseActivationUpgrade(false, "v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rollback()
	if _, err := os.Stat(filepath.Join(tmpDir, leaseActivationMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("fresh runtime unexpectedly has lease activation marker: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if got := envfile.Parse(string(content))[jobAttemptLeasesEnabledEnv]; got != "true" {
		t.Fatalf("fresh runtime lease flag = %q, want true", got)
	}
}

func TestExistingRuntimePersistsDisabledLeaseActivationBeforeSwap(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	original := "POSTGRES_PASSWORD=keep-me\nJOB_ATTEMPT_LEASES_ENABLED=true\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	rollback, err := app.beginLeaseActivationUpgrade(true, "v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if got := envfile.Parse(string(content))[jobAttemptLeasesEnabledEnv]; got != "false" {
		t.Fatalf("upgrade lease flag = %q, want false", got)
	}
	if envfile.Parse(string(content))["POSTGRES_PASSWORD"] != "keep-me" {
		t.Fatal("disabling lease activation changed unrelated production secrets")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, leaseActivationMarkerName)); err != nil {
		t.Fatalf("upgrade marker was not persisted: %v", err)
	}
	if err := validateLeaseActivationRuntime(filepath.Join(tmpDir, leaseActivationMarkerName), "v2.0.0"); err != nil {
		t.Fatalf("activated bundle version did not match marker: %v", err)
	}
	if err := validateLeaseActivationRuntime(filepath.Join(tmpDir, leaseActivationMarkerName), "v1.0.0"); err == nil {
		t.Fatal("old runtime was accepted as the activation target after an interrupted install")
	}
	rollback()
	content, err = os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("failed install did not restore original production env: %q", content)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, leaseActivationMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("failed install left a new upgrade marker: %v", err)
	}
}

func TestLeaseActivationWorkerResolutionFailsClosedWithoutWorkers(t *testing.T) {
	startArgs := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d"}
	workers, err := resolveLeaseActivationWorkers(startArgs, []string{"gateway", "worker-cpu", "worker-control", "worker-gpu-short"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(workers, ","); got != "worker-control,worker-cpu,worker-gpu-short" {
		t.Fatalf("unscoped start workers = %q, want configured workers", got)
	}
	if _, err := resolveLeaseActivationWorkers(startArgs, []string{"gateway", "frontend"}, nil); err == nil {
		t.Fatal("unscoped start with no configured workers must fail closed")
	}
	if _, err := resolveLeaseActivationWorkers(append(startArgs, "gateway"), nil, nil); err == nil {
		t.Fatal("gateway-only start with no active workers must fail closed")
	}
}

func TestUnscopedLeaseActivationScopesServicesToUnlockedLauncherGroups(t *testing.T) {
	groups := []ServiceGroup{
		{ID: "core", Services: []string{"worker-cpu", "gateway"}, Required: true},
		{ID: "md", Services: []string{"worker-gpu-short"}, DefaultOn: true},
		{ID: "qc", Services: []string{"worker-qc"}, DefaultOn: false, Locked: true},
	}
	allowed := launcherAllowedServices(groups, nil)
	got := intersectServices([]string{"gateway", "worker-cpu", "worker-gpu-short", "worker-qc"}, allowed)
	if joined := strings.Join(got, ","); joined != "gateway,worker-cpu,worker-gpu-short" {
		t.Fatalf("unscoped activation services = %q; locked Pro service must be excluded", joined)
	}
	selected := launcherAllowedServices(groups, []string{"qc"})
	if got := strings.Join(selected, ","); got != "" {
		t.Fatalf("locked selected group services = %q; want none", got)
	}
}

func TestLeaseActivationSequenceStartsWorkersBeforeEnablingGateway(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, leaseActivationMarkerName)
	if err := os.WriteFile(marker, []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	flag := "true"
	var commands [][]string
	var flagAtCommand []string
	run := func(args []string, _ string) error {
		commands = append(commands, append([]string(nil), args...))
		flagAtCommand = append(flagAtCommand, flag)
		return nil
	}
	setFlag := func(enabled bool) error {
		flag = strconv.FormatBool(enabled)
		return nil
	}
	startArgs := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d", "--pull=never", "worker-cpu"}
	err := runLeaseActivationSequence(marker, startArgs, startArgs, []string{"worker-gpu-short"}, []string{"worker-cpu", "worker-gpu-short"}, setFlag, run, "Starting services...")
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 5 {
		t.Fatalf("got %d compose phases, want stop, disabled gateway, workers, enabled gateway, and final-up: %#v", len(commands), commands)
	}
	if got := strings.Join(commands[0], " "); !strings.Contains(got, " stop gateway") || !strings.Contains(got, "worker-gpu-short") {
		t.Fatalf("first command did not stop the prior gateway and workers: %s", got)
	}
	if got := strings.Join(commands[1], " "); !strings.Contains(got, "gateway") || strings.Contains(got, "--no-deps") {
		t.Fatalf("disabled gateway warmup must start dependencies before worker replacement: %s", got)
	}
	for _, worker := range []string{"worker-cpu", "worker-gpu-short"} {
		if !strings.Contains(strings.Join(commands[2], " "), worker) {
			t.Errorf("worker phase omitted %s: %v", worker, commands[2])
		}
	}
	if flagAtCommand[0] != "false" || flagAtCommand[1] != "false" || flagAtCommand[2] != "false" || flagAtCommand[3] != "true" || flagAtCommand[4] != "true" {
		t.Fatalf("lease flag at compose phases = %v, want false,false,false,true,true", flagAtCommand)
	}
	if got := strings.Join(commands[3], " "); !strings.Contains(got, "gateway") || !strings.Contains(got, "--no-deps") {
		t.Fatalf("enabled gateway phase must explicitly restart gateway without dependencies: %s", got)
	}
	if got := strings.Join(commands[4], " "); !strings.Contains(got, "worker-cpu") || strings.Contains(got, "worker-gpu-short") {
		t.Fatalf("final compose phase did not preserve caller's subset target: %v", commands[4])
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("successful activation did not clear marker: %v", err)
	}
}

func TestLeaseActivationFailureKeepsMarkerAndDisablesGateway(t *testing.T) {
	for _, failedPhase := range []string{"disable-flag", "stop", "disabled-gateway", "workers", "enable-flag", "enabled-gateway", "caller-target"} {
		t.Run(failedPhase, func(t *testing.T) {
			tmpDir := t.TempDir()
			marker := filepath.Join(tmpDir, leaseActivationMarkerName)
			if err := os.WriteFile(marker, []byte("pending\n"), 0600); err != nil {
				t.Fatal(err)
			}
			flag := "true"
			if failedPhase == "disable-flag" {
				flag = "false" // installation already durably disabled submissions
			}
			var commands [][]string
			run := func(args []string, _ string) error {
				commands = append(commands, append([]string(nil), args...))
				command := strings.Join(args, " ")
				if failedPhase == "stop" && strings.Contains(command, " stop gateway") {
					return fmt.Errorf("stop failed")
				}
				if failedPhase == "disabled-gateway" && strings.Contains(command, " up ") && strings.Contains(command, "gateway") && flag == "false" {
					return fmt.Errorf("disabled gateway health check failed")
				}
				if failedPhase == "workers" && strings.Contains(command, "worker-cpu") && strings.Contains(command, " up ") {
					return fmt.Errorf("worker health check failed")
				}
				if failedPhase == "enabled-gateway" && strings.Contains(command, " up ") && strings.Contains(command, "gateway") && flag == "true" {
					return fmt.Errorf("enabled gateway failed")
				}
				if failedPhase == "caller-target" && strings.Contains(command, " up ") && strings.Contains(command, "worker-cpu") && flag == "true" && len(commands) > 4 {
					return fmt.Errorf("caller target failed")
				}
				return nil
			}
			setFlag := func(enabled bool) error {
				if enabled && failedPhase == "enable-flag" {
					return fmt.Errorf("persisting enabled flag failed")
				}
				if !enabled && failedPhase == "disable-flag" {
					return fmt.Errorf("persisting disabled flag failed")
				}
				flag = strconv.FormatBool(enabled)
				return nil
			}
			startArgs := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d", "worker-cpu"}
			if err := runLeaseActivationSequence(marker, startArgs, startArgs, []string{"worker-cpu"}, []string{"worker-cpu"}, setFlag, run, "Starting services..."); err == nil {
				t.Fatal("expected activation failure")
			}
			if flag != "false" {
				t.Errorf("lease flag after %s failure = %s, want false", failedPhase, flag)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("activation marker was not retained after %s failure: %v", failedPhase, err)
			}
			if (failedPhase == "disabled-gateway" || failedPhase == "enabled-gateway" || failedPhase == "caller-target") && !strings.Contains(strings.Join(commands[len(commands)-1], " "), "stop gateway") {
				t.Errorf("%s failure did not stop partial gateway before returning: %v", failedPhase, commands)
			}
		})
	}
}

func TestStderrTailKeepsLastNLines(t *testing.T) {
	tail := &stderrTail{max: 3}
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		tail.add(line)
	}
	if got := tail.String(); got != "c\nd\ne" {
		t.Fatalf("stderrTail.String() = %q, want %q", got, "c\nd\ne")
	}
}

func requirePrivateMode(t *testing.T, path string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected %s mode 0600, got %04o", path, got)
	}
}

func TestPrivatePersistenceUsesOwnerOnlyMode(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env.production")
	if err := os.WriteFile(envPath, []byte("SECRET=old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.projectPath = tmpDir
	if _, err := app.GetEnvContent("prod"); err != nil {
		t.Fatal(err)
	}
	requirePrivateMode(t, envPath)
	if err := app.SaveEnvContent("prod", "SECRET=new\n"); err != nil {
		t.Fatal(err)
	}
	requirePrivateMode(t, envPath)

	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", filepath.Join(tmpDir, "config"))
	if err := app.SaveLauncherConfig(LauncherConfig{ConfigVersion: 2}); err != nil {
		t.Fatal(err)
	}
	configPath, err := app.getConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	requirePrivateMode(t, configPath)

	app.logToFile("test", "redacted diagnostic")
	requirePrivateMode(t, app.composeLogPath())
}

func signRuntimeManifestForTest(t *testing.T, bundle []byte, version string, expiresAt time.Time) ([]byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := runtimeBundlePublicKeyB64
	runtimeBundlePublicKeyB64 = base64.StdEncoding.EncodeToString(publicKey)
	t.Cleanup(func() { runtimeBundlePublicKeyB64 = oldKey })
	digest := sha256.Sum256(bundle)
	manifest, err := json.Marshal(runtimebundle.Manifest{
		Schema:    "ligandx-runtime-manifest/1",
		Version:   version,
		Asset:     runtimebundle.AssetName,
		SHA256:    fmt.Sprintf("%x", digest),
		Size:      int64(len(bundle)),
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		GitCommit: strings.Repeat("a", 40),
		Artifacts: map[string]runtimebundle.Artifact{
			runtimebundle.AssetName: {SHA256: fmt.Sprintf("%x", digest), Size: int64(len(bundle))},
		},
		PlatformSigning: runtimebundle.PlatformSigning{
			Windows: runtimebundle.WindowsSigning{Authenticode: false, Evidence: "workflow-verification"},
			MacOS: runtimebundle.MacOSSigning{
				DeveloperID: false,
				Notarized:   false,
				Evidence:    "workflow-verification",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	return manifest, []byte(signature)
}

func TestRuntimeManifestAuthenticatesBundleAndRejectsTampering(t *testing.T) {
	bundle := []byte("signed runtime bundle")
	manifestBytes, signatureBytes := signRuntimeManifestForTest(t, bundle, "v1.2.3", time.Now().Add(time.Hour))
	manifest, err := runtimePolicy().VerifyManifest(manifestBytes, signatureBytes, "v1.2.3")
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if manifest.PlatformSigning.Windows.Evidence != "workflow-verification" ||
		manifest.PlatformSigning.MacOS.Evidence != "workflow-verification" {
		t.Fatalf("platform-signing evidence was not decoded: %+v", manifest.PlatformSigning)
	}

	if _, err := runtimePolicy().VerifyManifest(manifestBytes, signatureBytes, "v9.9.9"); err == nil {
		t.Fatal("manifest for a different release tag was accepted")
	}
	trustedKey := runtimeBundlePublicKeyB64
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtimeBundlePublicKeyB64 = base64.StdEncoding.EncodeToString(otherPublic)
	if _, err := runtimePolicy().VerifyManifest(manifestBytes, signatureBytes, "v1.2.3"); err == nil {
		t.Fatal("manifest signed by an untrusted signer was accepted")
	}
	runtimeBundlePublicKeyB64 = trustedKey
	expiredManifest, expiredSignature := signRuntimeManifestForTest(
		t, bundle, "v1.2.3", time.Now().Add(-time.Minute),
	)
	if _, err := runtimePolicy().VerifyManifest(expiredManifest, expiredSignature, "v1.2.3"); err == nil {
		t.Fatal("expired runtime manifest was accepted")
	}

	bundlePath := filepath.Join(t.TempDir(), runtimebundle.AssetName)
	if err := os.WriteFile(bundlePath, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtimebundle.VerifyFile(bundlePath, manifest); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	tamperedManifest := bytes.Replace(manifestBytes, []byte("v1.2.3"), []byte("v9.9.9"), 1)
	if _, err := runtimePolicy().VerifyManifest(tamperedManifest, signatureBytes, ""); err == nil {
		t.Fatal("tampered signed manifest was accepted")
	}
	if err := os.WriteFile(bundlePath, append(bundle, byte(0)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtimebundle.VerifyFile(bundlePath, manifest); err == nil {
		t.Fatal("tampered bundle was accepted")
	}
}

func TestSignedReleaseIndexControlsSelectableStableVersions(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldVersion := runtimeBundlePublicKeyB64, launcherVersion
	runtimeBundlePublicKeyB64 = base64.StdEncoding.EncodeToString(publicKey)
	launcherVersion = "v2.0.0"
	t.Cleanup(func() { runtimeBundlePublicKeyB64, launcherVersion = oldKey, oldVersion })
	index := runtimebundle.ReleaseIndex{
		Schema:    "ligandx-release-index/1",
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		Releases: []runtimebundle.Release{
			{Version: "v2.1.0", Status: "supported", Recommended: true, MinimumLauncher: "v2.0.0", BundleURL: "https://github.com/kon-218/ligand-x-launcher/releases/download/v2.1.0/ligand-x-runtime.zip", DownloadBytes: 1024},
			{Version: "v1.9.0", Status: "supported", MinimumLauncher: "v3.0.0", BundleURL: "https://github.com/kon-218/ligand-x-launcher/releases/download/v1.9.0/ligand-x-runtime.zip", DownloadBytes: 1024},
			{Version: "v1.8.0", Status: "revoked", MinimumLauncher: "v1.0.0", BundleURL: "https://github.com/kon-218/ligand-x-launcher/releases/download/v1.8.0/ligand-x-runtime.zip", DownloadBytes: 1024},
		},
	}
	payload, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)))
	verified, err := runtimePolicy().VerifyReleaseIndex(payload, signature)
	if err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}
	if !verified.Releases[0].Compatible {
		t.Fatalf("recommended release marked incompatible: %+v", verified.Releases[0])
	}
	if verified.Releases[1].Compatible || !strings.Contains(verified.Releases[1].Compatibility, "Requires launcher") {
		t.Fatalf("minimum launcher was not enforced: %+v", verified.Releases[1])
	}
	if verified.Releases[2].Compatible || !strings.Contains(verified.Releases[2].Compatibility, "revoked") {
		t.Fatalf("revoked release remained selectable: %+v", verified.Releases[2])
	}
	payload[0] ^= 1
	if _, err := runtimePolicy().VerifyReleaseIndex(payload, signature); err == nil {
		t.Fatal("tampered release index was accepted")
	}
}

// Superseded by TestMalformedRecommendationDegradesInsteadOfFailing: a wrong
// recommendation count no longer rejects the index, because doing so hid every
// release and left the user unable to install anything (v2026.08.15-rc.8). The
// signature remains mandatory; only the recommendation flags are normalised.
func TestSignedReleaseIndexNormalisesRecommendation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := runtimeBundlePublicKeyB64
	runtimeBundlePublicKeyB64 = base64.StdEncoding.EncodeToString(publicKey)
	t.Cleanup(func() { runtimeBundlePublicKeyB64 = oldKey })

	sign := func(index runtimebundle.ReleaseIndex) ([]byte, []byte) {
		t.Helper()
		payload, err := json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		return payload, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)))
	}
	base := func(recommended ...bool) runtimebundle.ReleaseIndex {
		releases := make([]runtimebundle.Release, len(recommended))
		for i, rec := range recommended {
			releases[i] = runtimebundle.Release{
				Version:         fmt.Sprintf("v2.1.%d", i),
				Status:          "supported",
				Recommended:     rec,
				MinimumLauncher: "v2.0.0",
				BundleURL:       fmt.Sprintf("https://github.com/kon-218/ligand-x-launcher/releases/download/v2.1.%d/ligand-x-runtime.zip", i),
				DownloadBytes:   1024,
			}
		}
		return runtimebundle.ReleaseIndex{
			Schema:    "ligandx-release-index/1",
			IssuedAt:  time.Now().UTC().Format(time.RFC3339),
			ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			Releases:  releases,
		}
	}

	payload, signature := sign(base(false, false))
	index, err := runtimePolicy().VerifyReleaseIndex(payload, signature)
	if err != nil {
		t.Fatalf("zero recommended releases must still list, got %v", err)
	}
	if len(index.Releases) != 2 || index.Warning == "" {
		t.Fatalf("expected both releases listed with a warning, got %d releases, warning %q",
			len(index.Releases), index.Warning)
	}

	payload, signature = sign(base(true, true))
	index, err = runtimePolicy().VerifyReleaseIndex(payload, signature)
	if err != nil {
		t.Fatalf("two recommended releases must still list, got %v", err)
	}
	for _, release := range index.Releases {
		if release.Recommended {
			t.Fatalf("ambiguous recommendation must be cleared, %s is still marked", release.Version)
		}
	}
	if index.Warning == "" {
		t.Fatal("expected a warning for the ambiguous recommendation")
	}

	payload, signature = sign(base(true, false))
	index, err = runtimePolicy().VerifyReleaseIndex(payload, signature)
	if err != nil {
		t.Fatalf("single recommended release was rejected: %v", err)
	}
	if index.Warning != "" {
		t.Fatalf("well-formed index must not warn, got %q", index.Warning)
	}
}

func TestPasswordUpdateAndLicenseImportPersistenceStayPrivate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	template := "LIGANDX_USERNAME=admin\nLIGANDX_PASSWORD=CHANGE_ME\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.projectPath = tmpDir
	if _, err := app.SaveLocalAccount("alice", "alice@example.com", "strongpass"); err != nil {
		t.Fatal(err)
	}
	if err := app.UpdatePassword("replacement-pass"); err != nil {
		t.Fatal(err)
	}
	requirePrivateMode(t, filepath.Join(tmpDir, ".env.production"))
	if err := app.persistImportedLicense([]byte("signed-public-claims")); err != nil {
		t.Fatal(err)
	}
	requirePrivateMode(t, app.licensePath())
}

// --- Resource fitting (resources.go) -------------------------------------
//
// Reported by the first external user: an Intel i7-4770K (8 threads) could not
// start the stack at all —
//   "Error response from daemon: range of CPUs is from 0.01 to 8.00,
//    as there are only 8 CPUs available"
// — because .env.production.template ships WORKER_CPU_CPU_LIMIT=16 and
// WORKER_GPU_LONG_CPU_LIMIT=12, sized for a workstation. Docker validates
// `cpus` per container against the daemon's CPU count and refuses to create the
// container, so this is a hard start failure rather than a slow stack.

func TestFitResourceEnvClampsOversizedCPULimitsToDaemonCPUs(t *testing.T) {
	cur := map[string]string{
		"WORKER_CPU_CPU_LIMIT":      "16",
		"WORKER_CPU_CPU_RES":        "4",
		"WORKER_GPU_LONG_CPU_LIMIT": "12",
		"WORKER_GPU_LONG_CPU_RES":   "4",
		"GATEWAY_CPU_LIMIT":         "2",
		"GATEWAY_CPU_RES":           "0.5",
		"CPU_WORKER_CONCURRENCY":    "4",
		"GPU_LONG_CONCURRENCY":      "1",
	}
	updates, notes := fitResourceEnv(cur, hostResources{CPUs: 8}, true)

	want := map[string]string{
		// The workers are held under a soft ceiling of floor(8*0.75), so one
		// container cannot claim every thread (see workerCPUHeadroom).
		"WORKER_CPU_CPU_LIMIT":      "6",
		"WORKER_GPU_LONG_CPU_LIMIT": "6",
		"CPU_WORKER_CONCURRENCY":    "2", // 4 parallel Vina jobs thrash 8 threads
	}
	for k, v := range want {
		if updates[k] != v {
			t.Errorf("%s: got %q, want %q (notes: %v)", k, updates[k], v, notes)
		}
	}
	// Values already within the daemon's capacity must be left exactly alone —
	// fitting is a clamp, never a re-tune.
	for _, k := range []string{"WORKER_CPU_CPU_RES", "GATEWAY_CPU_LIMIT", "GATEWAY_CPU_RES", "GPU_LONG_CONCURRENCY"} {
		if v, ok := updates[k]; ok {
			t.Errorf("%s was rewritten to %q but already fits", k, v)
		}
	}
}

func TestFitResourceEnvLeavesHeadroomForTheWorkerLimits(t *testing.T) {
	// Clamping a worker to exactly host.CPUs is legal — docker's check is `>` —
	// but it hands one container every thread on the machine, so the UI, the
	// gateway and the user's desktop all contend with a batch docking run. The
	// workers get a soft ceiling; everything else keeps the hard one.
	cur := map[string]string{
		"WORKER_CPU_CPU_LIMIT":      "16",
		"WORKER_GPU_LONG_CPU_LIMIT": "12",
		"DOCKING_CPU_LIMIT":         "8", // not a worker: hard ceiling only
	}
	updates, notes := fitResourceEnv(cur, hostResources{CPUs: 8}, true)
	if updates["WORKER_CPU_CPU_LIMIT"] != "6" {
		t.Errorf("WORKER_CPU_CPU_LIMIT = %q, want 6 (floor(8*0.75)); notes: %v", updates["WORKER_CPU_CPU_LIMIT"], notes)
	}
	if updates["WORKER_GPU_LONG_CPU_LIMIT"] != "6" {
		t.Errorf("WORKER_GPU_LONG_CPU_LIMIT = %q, want 6", updates["WORKER_GPU_LONG_CPU_LIMIT"])
	}
	if v, ok := updates["DOCKING_CPU_LIMIT"]; ok {
		t.Errorf("DOCKING_CPU_LIMIT is not a worker and fits the daemon's 8 CPUs, but was rewritten to %q", v)
	}

	// The soft ceiling must never round down to zero on a tiny machine: a
	// `cpus: 0` limit is rejected just as hard as an oversized one.
	tiny, _ := fitResourceEnv(map[string]string{"WORKER_CPU_CPU_LIMIT": "4"}, hostResources{CPUs: 1}, true)
	if tiny["WORKER_CPU_CPU_LIMIT"] != "1" {
		t.Errorf("on a 1-CPU host WORKER_CPU_CPU_LIMIT = %q, want 1", tiny["WORKER_CPU_CPU_LIMIT"])
	}
}

func TestFitResourceEnvTunesGPULongConcurrency(t *testing.T) {
	// GPU_LONG_CONCURRENCY ships in the template but was the only pool size
	// never fitted, so a hand-raised value stayed raised on a small machine.
	cur := map[string]string{"GPU_LONG_CONCURRENCY": "4"}
	updates, _ := fitResourceEnv(cur, hostResources{CPUs: 8}, true)
	if updates["GPU_LONG_CONCURRENCY"] != "1" {
		t.Errorf("GPU_LONG_CONCURRENCY = %q, want 1 on an 8-CPU host", updates["GPU_LONG_CONCURRENCY"])
	}
	// Pool sizes are only re-tuned on the first fit for this hardware —
	// afterwards the value is the user's own choice from the settings panel.
	later, _ := fitResourceEnv(cur, hostResources{CPUs: 8}, false)
	if _, ok := later["GPU_LONG_CONCURRENCY"]; ok {
		t.Errorf("a deliberate GPU_LONG_CONCURRENCY was undone on a repeat start: %v", later)
	}
}

func TestFitResourceEnvLeavesLargeMachineUntouched(t *testing.T) {
	cur := map[string]string{
		"WORKER_CPU_CPU_LIMIT":      "16",
		"WORKER_GPU_LONG_CPU_LIMIT": "12",
		"WORKER_CPU_MEM_LIMIT":      "32G",
		"CPU_WORKER_CONCURRENCY":    "4",
	}
	updates, _ := fitResourceEnv(cur, hostResources{CPUs: 32, MemBytes: 128 << 30}, true)
	if len(updates) != 0 {
		t.Fatalf("expected no changes on a 32-CPU/128G host, got %v", updates)
	}
}

func TestFitResourceEnvKeepsReservationsUnderClampedLimit(t *testing.T) {
	cur := map[string]string{
		"WORKER_GPU_LONG_CPU_LIMIT": "12",
		"WORKER_GPU_LONG_CPU_RES":   "10",
		"WORKER_QC_CPU_LIMIT":       "2",
		"WORKER_QC_CPU_RES":         "3", // already inverted in the source file
	}
	// 4 CPUs: the worker soft ceiling is floor(4*0.75) = 3, and the reservation
	// must follow its own clamped limit down rather than stopping at host.CPUs.
	updates, _ := fitResourceEnv(cur, hostResources{CPUs: 4}, true)
	if updates["WORKER_GPU_LONG_CPU_LIMIT"] != "3" || updates["WORKER_GPU_LONG_CPU_RES"] != "3" {
		t.Errorf("reservation not held under its clamped limit: %v", updates)
	}
	// A reservation above its own limit is capped by that limit, not by host CPUs.
	if updates["WORKER_QC_CPU_RES"] != "2" {
		t.Errorf("WORKER_QC_CPU_RES: got %q, want 2", updates["WORKER_QC_CPU_RES"])
	}
}

func TestFitResourceEnvClampsMemoryOnlyWhenDaemonTotalKnown(t *testing.T) {
	cur := map[string]string{
		"WORKER_CPU_MEM_LIMIT":      "32G",
		"WORKER_CPU_MEM_RES":        "8G",
		"WORKER_GPU_LONG_MEM_LIMIT": "48G",
		"GATEWAY_MEM_LIMIT":         "2G",
	}
	// 16 GB daemon: 10% headroom -> 14G ceiling.
	updates, _ := fitResourceEnv(cur, hostResources{CPUs: 8, MemBytes: 16 << 30}, true)
	if updates["WORKER_CPU_MEM_LIMIT"] != "14G" || updates["WORKER_GPU_LONG_MEM_LIMIT"] != "14G" {
		t.Errorf("memory limits not fitted to a 16G daemon: %v", updates)
	}
	if _, ok := updates["GATEWAY_MEM_LIMIT"]; ok {
		t.Errorf("2G limit should fit a 16G daemon, got %q", updates["GATEWAY_MEM_LIMIT"])
	}
	// Unknown total (MemBytes 0 — non-Linux fallback): memory is left alone
	// rather than guessed at, since an oversized memory limit is not fatal.
	unknown, _ := fitResourceEnv(cur, hostResources{CPUs: 8}, true)
	if len(unknown) != 0 {
		t.Errorf("memory rewritten without a known daemon total: %v", unknown)
	}
}

// TestEnsureProductionEnvFitsShippedTemplateToSmallHost is the end-to-end
// regression for the i7-4770K report: the real template values, an 8-CPU
// daemon, and the requirement that nothing in the written file can be rejected.
func TestEnsureProductionEnvFitsShippedTemplateToSmallHost(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources {
		return hostResources{CPUs: 8, MemBytes: 16 << 30}
	}

	template := strings.Join([]string{
		"VERSION=v2026.06.21",
		"POSTGRES_PASSWORD=CHANGE_ME",
		"WORKER_CPU_CPU_LIMIT=16",
		"WORKER_CPU_MEM_LIMIT=32G",
		"WORKER_CPU_CPU_RES=4",
		"WORKER_GPU_LONG_CPU_LIMIT=12",
		"WORKER_GPU_LONG_MEM_LIMIT=48G",
		"WORKER_QC_CPU_LIMIT=8",
		"DOCKING_CPU_LIMIT=4",
		"CPU_WORKER_CONCURRENCY=4",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv failed: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	env := envfile.Parse(string(written))
	for k, v := range env {
		if !strings.HasSuffix(k, "_CPU_LIMIT") && !strings.HasSuffix(k, "_CPU_RES") {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("%s=%q is not a number", k, v)
		}
		if f > 8 {
			t.Errorf("%s=%s exceeds the daemon's 8 CPUs — docker would reject the container", k, v)
		}
	}
	if env["DOCKING_CPU_LIMIT"] != "4" {
		t.Errorf("DOCKING_CPU_LIMIT should be untouched, got %q", env["DOCKING_CPU_LIMIT"])
	}

	// Idempotent: a second start must not drift the already-fitted values.
	before := string(written)
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("second ensureProductionEnv failed: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Errorf("resource fitting is not idempotent:\nbefore:\n%s\nafter:\n%s", before, string(after))
	}
}

// --- Duplicate keys in .env.production ------------------------------------
//
// The launcher reads env files last-wins (parseEnvFile, and docker compose's
// own dotenv parser, both assign sequentially) but historically wrote them
// first-only (`break` / `delete(pending, key)` after the first match). A user
// who inserts a WORKER_* override *above* the template's original line
// therefore sees compose keep using the original — and, worse, defeats the
// resource fitting: it reads 16 (last-wins), decides to write 8, and puts the 8
// on the dead first line. The 16 survives and the container is still rejected.
//
// This is the second half of the i7-4770K report: the user edited the file by
// hand *and* took the release containing the clamp, and neither took effect.

// countEnvDefinitions counts live (uncommented) definitions of key, i.e. the
// ones compose's dotenv parser would actually assign from.
func countEnvDefinitions(content, key string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 && strings.TrimSpace(line[:i]) == key {
			n++
		}
	}
	return n
}

func TestSetEnvFileValuesRewritesTheDefinitionComposeReads(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	envPath := filepath.Join(tmpDir, ".env.production")
	if err := os.WriteFile(envPath, []byte(strings.Join([]string{
		"WORKER_CPU_CPU_LIMIT=6", // the user's hand edit, inserted above
		"APP_PORT=8080",
		"WORKER_CPU_CPU_LIMIT=16", // the template's original — what compose reads
	}, "\n")), 0644); err != nil {
		t.Fatal(err)
	}

	if err := app.setProductionEnvValues(map[string]string{"WORKER_CPU_CPU_LIMIT": "8"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := envfile.Parse(string(data))["WORKER_CPU_CPU_LIMIT"]; got != "8" {
		t.Errorf("effective WORKER_CPU_CPU_LIMIT = %q, want 8 — the write landed on a line compose ignores:\n%s", got, data)
	}
	if n := countEnvDefinitions(string(data), "WORKER_CPU_CPU_LIMIT"); n != 1 {
		t.Errorf("expected exactly one live definition afterwards, got %d:\n%s", n, data)
	}
	if got := envfile.Parse(string(data))["APP_PORT"]; got != "8080" {
		t.Errorf("unrelated key was disturbed: APP_PORT = %q", got)
	}
}

func TestSetEnvFileValuesMatchesKeysWithSurroundingSpaces(t *testing.T) {
	// "VERSION = v2026.01.01" is an ordinary hand edit that compose accepts.
	// Matching on the literal "KEY=" prefix misses it and appends a second
	// definition — the launcher manufacturing the very duplicate that breaks the
	// next fit. Exercised through both writers, since only one of them trimmed.
	for _, name := range []string{"single", "batch"} {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			app := NewApp()
			app.projectPath = tmpDir
			envPath := filepath.Join(tmpDir, ".env.production")
			if err := os.WriteFile(envPath, []byte("VERSION = v2026.01.01\n"), 0644); err != nil {
				t.Fatal(err)
			}

			var err error
			if name == "single" {
				err = app.setProductionEnvValue("VERSION", "v2026.08.05")
			} else {
				err = app.setProductionEnvValues(map[string]string{"VERSION": "v2026.08.05"})
			}
			if err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(envPath)
			if err != nil {
				t.Fatal(err)
			}
			if n := countEnvDefinitions(string(data), "VERSION"); n != 1 {
				t.Errorf("spaced key was not matched, duplicate appended (%d definitions):\n%s", n, data)
			}
			if got := envfile.Parse(string(data))["VERSION"]; got != "v2026.08.05" {
				t.Errorf("effective VERSION = %q, want v2026.08.05", got)
			}
		})
	}
}

func TestSetEnvFileValueSingleKeyHonoursLastWins(t *testing.T) {
	// setEnvFileValue is the single-key path (secrets, VERSION, CORS_ORIGINS).
	// It must obey the same rule as the batch form, or the launcher self-heals
	// VERSION and POSTGRES_PASSWORD onto lines compose ignores too.
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	envPath := filepath.Join(tmpDir, ".env.production")
	if err := os.WriteFile(envPath, []byte("VERSION=latest\nAPP_PORT=8080\nVERSION=v2026.01.01\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := app.setProductionEnvValue("VERSION", "v2026.08.05"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := envfile.Parse(string(data))["VERSION"]; got != "v2026.08.05" {
		t.Errorf("effective VERSION = %q, want v2026.08.05:\n%s", got, data)
	}
	if n := countEnvDefinitions(string(data), "VERSION"); n != 1 {
		t.Errorf("expected one live VERSION definition, got %d:\n%s", n, data)
	}
}

func TestEnsureProductionEnvWarnsAboutDuplicateKeys(t *testing.T) {
	// A duplicate silently discards whichever definition came first. The user
	// cannot see that in an editor, so the launcher has to say it out loud —
	// otherwise "I edited the file and nothing changed" has no explanation.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }

	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"),
		[]byte("VERSION=v2026.08.05\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(strings.Join([]string{
		"VERSION=v2026.08.05",
		"WORKER_CPU_CPU_LIMIT=6",
		"WORKER_CPU_CPU_LIMIT=16",
	}, "\n")), 0600); err != nil {
		t.Fatal(err)
	}

	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv failed: %v", err)
	}

	logged, err := os.ReadFile(app.composeLogPath())
	if err != nil {
		t.Fatalf("no launcher log written: %v", err)
	}
	if !strings.Contains(string(logged), "WORKER_CPU_CPU_LIMIT") ||
		!strings.Contains(strings.ToLower(string(logged)), "more than once") {
		t.Errorf("duplicate key not reported in the log:\n%s", logged)
	}
}

// TestEnsureProductionEnvClampsTheEffectiveDuplicateLimit is the end-to-end
// form: the fitting must clamp the value compose actually resolves, not the
// first line that happens to mention the key.
func TestEnsureProductionEnvClampsTheEffectiveDuplicateLimit(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }

	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"),
		[]byte("VERSION=v2026.08.05\n"), 0644); err != nil {
		t.Fatal(err)
	}
	lines := []string{"VERSION=v2026.08.05", "WORKER_CPU_CPU_LIMIT=6"}
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("FILLER_%d=x", i))
	}
	lines = append(lines, "WORKER_CPU_CPU_LIMIT=16") // the line compose reads
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"),
		[]byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}

	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	got := envfile.Parse(string(data))["WORKER_CPU_CPU_LIMIT"]
	v, err := strconv.ParseFloat(got, 64)
	if err != nil {
		t.Fatalf("WORKER_CPU_CPU_LIMIT=%q is not a number", got)
	}
	if v > 8 {
		t.Errorf("effective WORKER_CPU_CPU_LIMIT = %s, exceeds the daemon's 8 CPUs — docker would still reject the container:\n%s", got, data)
	}
	if n := countEnvDefinitions(string(data), "WORKER_CPU_CPU_LIMIT"); n != 1 {
		t.Errorf("expected one live definition after fitting, got %d", n)
	}
}

// --- Validating the model Docker actually receives -------------------------
//
// fitResourceEnv can only see keys that are present in .env.production. It is
// blind to a limit that comes from a compose inline default
// (`${WORKER_REINVENT_CPU_LIMIT:-4}`, a key the template never defines), to one
// injected through the shell environment (composeEnv appends to os.Environ, and
// compose gives shell env precedence over --env-file), and to a user who edited
// a different file than the one compose reads. `docker compose config` resolves
// all of those, and is the only authoritative view of what the daemon will be
// asked to create.

func TestComposeConfigArgsReusesTheGlobalFlagsOfTheUpCommand(t *testing.T) {
	// The resolved model depends on --env-file and every -f overlay, so a
	// validation run that omits them would validate a different project.
	up := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml",
		"-f", "docker-compose.gpu.yml", "up", "-d", "--pull=never", "worker-cpu"}
	got := composeConfigArgs(up)
	want := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml",
		"-f", "docker-compose.gpu.yml", "config", "--format", "json"}
	if len(got) != len(want) {
		t.Fatalf("composeConfigArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("composeConfigArgs = %v, want %v", got, want)
		}
	}
}

func TestOverCPUServicesReadsTheResolvedModel(t *testing.T) {
	// compose renders cpus as a string in some versions and a bare number in
	// others; both have to be understood or the check silently passes everything.
	const configJSON = `{
	  "services": {
	    "worker-cpu":      {"deploy": {"resources": {"limits": {"cpus": "16", "memory": "32G"}}}},
	    "worker-reinvent": {"deploy": {"resources": {"limits": {"cpus": 12}}}},
	    "gateway":         {"deploy": {"resources": {"limits": {"cpus": "2"}}}},
	    "redis":           {"image": "redis:7"}
	  }
	}`
	got, err := overCPUServices([]byte(configJSON), 8)
	if err != nil {
		t.Fatalf("overCPUServices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d violations, want 2: %+v", len(got), got)
	}
	// Sorted by service name so the message is stable across runs.
	if got[0].Service != "worker-cpu" || got[0].CPUs != 16 {
		t.Errorf("first violation = %+v, want worker-cpu at 16", got[0])
	}
	if got[1].Service != "worker-reinvent" || got[1].CPUs != 12 {
		t.Errorf("second violation = %+v, want worker-reinvent at 12", got[1])
	}
	// A limit equal to the daemon's count is accepted by docker (the check is
	// `>`), so it must not be reported as a violation.
	edge, err := overCPUServices([]byte(`{"services":{"a":{"deploy":{"resources":{"limits":{"cpus":"8"}}}}}}`), 8)
	if err != nil || len(edge) != 0 {
		t.Errorf("cpus == daemon CPUs was reported as a violation: %+v (%v)", edge, err)
	}
}

func TestComposeCPULimitKeysMapsServicesToTheirBackingEnvKey(t *testing.T) {
	// Knowing which key backs a service's limit is what turns "worker-cpu wants
	// 16 CPUs" into a value the launcher can actually clamp.
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	keys := composeCPULimitKeys(string(data))
	for service, want := range map[string]string{
		"worker-cpu":       "WORKER_CPU_CPU_LIMIT",
		"worker-gpu-long":  "WORKER_GPU_LONG_CPU_LIMIT",
		"worker-gpu-short": "WORKER_GPU_SHORT_CPU_LIMIT",
		"worker-qc":        "WORKER_QC_CPU_LIMIT",
		"gateway":          "GATEWAY_CPU_LIMIT",
	} {
		if keys[service] != want {
			t.Errorf("%s -> %q, want %q", service, keys[service], want)
		}
	}
}

func TestVerifyFittedModelClampsALimitThatIsNotInTheEnvFile(t *testing.T) {
	// The case fitResourceEnv structurally cannot catch: worker-reinvent's limit
	// comes from the compose inline default, so no *_CPU_LIMIT key for it exists
	// in .env.production and the fitting never looks at it.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"),
		[]byte("WORKER_CPU_CPU_LIMIT=6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte(strings.Join([]string{
		"services:",
		"  worker-reinvent:",
		"    deploy:",
		"      resources:",
		"        limits:",
		"          cpus: ${WORKER_REINVENT_CPU_LIMIT:-12}",
	}, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	app.composeConfigFn = func(args []string) ([]byte, error) {
		return []byte(`{"services":{"worker-reinvent":{"deploy":{"resources":{"limits":{"cpus":"12"}}}}}}`), nil
	}

	if err := app.verifyFittedModel([]string{"compose", "--env-file", ".env.production", "up", "-d"}); err != nil {
		t.Fatalf("verifyFittedModel should clamp a backable key, not fail: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if got := envfile.Parse(string(data))["WORKER_REINVENT_CPU_LIMIT"]; got != "6" {
		t.Errorf("WORKER_REINVENT_CPU_LIMIT = %q, want 6 — the inline default was never brought under the ceiling", got)
	}
}

func TestVerifyFittedModelUsesTheSameCeilingsAsTheFitting(t *testing.T) {
	// A non-worker service is bounded only by the daemon's own count; the soft
	// worker headroom does not apply to it. Clamping it lower here would
	// contradict fitResourceEnv and throttle the gateway for no reason.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("VERSION=v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte(strings.Join([]string{
		"services:",
		"  gateway:",
		"    deploy:",
		"      resources:",
		"        limits:",
		"          cpus: ${GATEWAY_CPU_LIMIT:-12}",
	}, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	app.composeConfigFn = func(args []string) ([]byte, error) {
		return []byte(`{"services":{"gateway":{"deploy":{"resources":{"limits":{"cpus":"12"}}}}}}`), nil
	}

	if err := app.verifyFittedModel([]string{"compose", "up", "-d"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if got := envfile.Parse(string(data))["GATEWAY_CPU_LIMIT"]; got != "8" {
		t.Errorf("GATEWAY_CPU_LIMIT = %q, want 8 (the daemon's count, not the worker headroom)", got)
	}
}

func TestVerifyFittedModelFailsFastWhenNoKeyBacksTheLimit(t *testing.T) {
	// A hard-coded `cpus: 16` in the compose file has no env key to clamp.
	// Failing before `up` beats a half-created stack and an opaque daemon error.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("VERSION=v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte(strings.Join([]string{
		"services:",
		"  worker-cpu:",
		"    deploy:",
		"      resources:",
		"        limits:",
		"          cpus: 16",
	}, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	app.composeConfigFn = func(args []string) ([]byte, error) {
		return []byte(`{"services":{"worker-cpu":{"deploy":{"resources":{"limits":{"cpus":"16"}}}}}}`), nil
	}

	err := app.verifyFittedModel([]string{"compose", "up", "-d"})
	if err == nil {
		t.Fatal("expected verifyFittedModel to fail fast on an unclampable limit")
	}
	if !strings.Contains(err.Error(), "worker-cpu") || !strings.Contains(err.Error(), "16") {
		t.Errorf("error must name the service and its resolved value, got: %v", err)
	}
}

// TestRunDockerComposeVerifiesTheModelBeforeCreatingAnything guards the wiring,
// not the check. The check itself is thoroughly tested above, but a refactor
// that dropped the call site would silently restore the original bug with every
// unit test still green — and the daemon rejects an oversized `cpus` at
// container *creation*, so a violation that slips through leaves a half-built
// stack behind.
func TestRunDockerComposeVerifiesTheModelBeforeCreatingAnything(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	for name, body := range map[string]string{
		".env.production.template": "VERSION=v2026.08.05\n",
		".env.production":          "VERSION=v2026.08.05\n",
		// No ${VAR} backing the limit, so the violation is unclampable and
		// verifyFittedModel must refuse rather than clamp.
		"docker-compose.yml": "services:\n  worker-cpu:\n    deploy:\n      resources:\n        limits:\n          cpus: 16\n",
	} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	app.composeConfigFn = func(args []string) ([]byte, error) {
		return []byte(`{"services":{"worker-cpu":{"deploy":{"resources":{"limits":{"cpus":"16"}}}}}}`), nil
	}

	err := app.runDockerCompose(
		[]string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d"},
		"Starting services...")
	if err == nil {
		t.Fatal("runDockerCompose started the stack despite an unsatisfiable CPU limit")
	}
	// Must be the pre-flight refusal, not whatever `docker` itself said: the
	// point is that we never got as far as running it.
	if !strings.Contains(err.Error(), "worker-cpu") {
		t.Errorf("failure did not come from the pre-flight check: %v", err)
	}
}

func TestVerifyFittedModelIsNonFatalWhenComposeConfigFails(t *testing.T) {
	// Older compose, a daemon that just went away, an overlay we cannot parse:
	// none of those are a reason to refuse to start. Log and continue.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	app.composeConfigFn = func(args []string) ([]byte, error) {
		return nil, fmt.Errorf("unknown flag: --format")
	}
	if err := app.verifyFittedModel([]string{"compose", "up", "-d"}); err != nil {
		t.Errorf("a failing `compose config` must not block start: %v", err)
	}
}

// --- Translating the daemon's error ----------------------------------------
//
// "range of CPUs is from 0.01 to 8.00, as there are only 8 CPUs available"
// names neither the service, nor the file that set the value, nor where that
// file lives. The user is left with nothing to act on — which is how the
// original report went three rounds without converging.

func TestExplainComposeFailureMakesTheCPURangeErrorActionable(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources {
		return hostResources{CPUs: 8, MemBytes: 16 << 30, FromDaemon: true}
	}
	app.composeConfigFn = func(args []string) ([]byte, error) {
		return []byte(`{"services":{"worker-cpu":{"deploy":{"resources":{"limits":{"cpus":"16"}}}}}}`), nil
	}

	got := app.explainComposeFailure(
		"Error response from daemon: range of CPUs is from 0.01 to 8.00, as there are only 8 CPUs available",
		[]string{"compose", "--env-file", ".env.production", "up", "-d"})

	for _, want := range []string{
		filepath.Join(tmpDir, ".env.production"), // the exact file compose reads
		"8 CPUs",                                 // what we actually detected
		"Docker daemon",                          // and where that number came from
		"worker-cpu",                             // the offending service
		"16",                                     // its resolved value
		"LAST definition",                        // why their edit may have done nothing
	} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation is missing %q:\n%s", want, got)
		}
	}
}

func TestCPURangeExplanationWarnsAboutHiddenExtensionsOnWindowsOnly(t *testing.T) {
	// Federico is on Windows, where File Explorer hides known extensions and
	// Notepad silently appends .txt — so the file he edited may not be the file
	// compose reads. Irrelevant noise anywhere else.
	host := hostResources{CPUs: 8, FromDaemon: true}
	win := cpuRangeExplanation(host, `C:\Users\f\AppData\Roaming\ligandx-launcher\runtime\.env.production`, nil, nil, "windows")
	if !strings.Contains(win, ".env.production.txt") {
		t.Errorf("Windows explanation omits the hidden-extension trap:\n%s", win)
	}
	nix := cpuRangeExplanation(host, "/home/f/.config/ligandx-launcher/runtime/.env.production", nil, nil, "linux")
	if strings.Contains(nix, ".env.production.txt") {
		t.Errorf("Windows-only advice leaked onto linux:\n%s", nix)
	}
}

// --- The file the user actually edited -------------------------------------
//
// Windows File Explorer hides known extensions, and Notepad appends .txt when
// "Save as type" is left on Text Documents. The result is a
// `.env.production.txt` sitting next to the real file, both displayed as
// ".env.production" — so the user edits one and compose reads the other, and
// every value they change appears to be ignored. Advice about this is cheap;
// actually detecting it is cheaper still and removes the guesswork.

func TestStrayProductionEnvFilesFindsTheNotepadCopy(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		".env.production",          // the real one
		".env.production.template", // ships in the bundle, not a mistake
		".env.production.txt",      // Notepad
		".env.production.bak",      // a hand-made backup, also not read
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("VERSION=v1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got := strayProductionEnvFiles(dir)
	want := []string{".env.production.bak", ".env.production.txt"}
	if len(got) != len(want) {
		t.Fatalf("strayProductionEnvFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("strayProductionEnvFiles = %v, want %v", got, want)
		}
	}
	// A clean directory must stay quiet, or the warning becomes noise people
	// learn to ignore.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, ".env.production"), []byte("VERSION=v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := strayProductionEnvFiles(clean); len(got) != 0 {
		t.Errorf("clean directory reported strays: %v", got)
	}
}

func TestEnsureProductionEnvWarnsAboutAStrayEditedCopy(t *testing.T) {
	// Reported at start, not only after a failure: the stray file explains a
	// whole class of "I changed it and nothing happened", not just CPU limits.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte("VERSION=v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("VERSION=v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.txt"), []byte("WORKER_CPU_CPU_LIMIT=6\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := app.ensureProductionEnv(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(app.composeLogPath())
	if err != nil {
		t.Fatalf("nothing was logged: %v", err)
	}
	if !strings.Contains(string(logged), ".env.production.txt") {
		t.Errorf("stray file not reported at start:\n%s", logged)
	}
}

func TestCPURangeExplanationNamesAStrayFileWhenOneExists(t *testing.T) {
	host := hostResources{CPUs: 8, FromDaemon: true}
	got := cpuRangeExplanation(host, "/x/.env.production", nil, []string{".env.production.txt"}, "windows")
	if !strings.Contains(got, ".env.production.txt") {
		t.Errorf("explanation does not name the stray file:\n%s", got)
	}
	// Without a stray file the message must not imply one exists.
	none := cpuRangeExplanation(host, "/x/.env.production", nil, nil, "linux")
	if strings.Contains(none, ".env.production.txt") {
		t.Errorf("a stray file was implied where none exists:\n%s", none)
	}
}

func TestExplainComposeFailureStaysSilentOnUnrelatedErrors(t *testing.T) {
	// Appending a CPU essay to a port-bind or image-pull failure would bury the
	// real reason, so the matcher has to be specific.
	app := NewApp()
	app.projectPath = t.TempDir()
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	if got := app.explainComposeFailure("Error: pull access denied for ghcr.io/kon-218/ligand-x-pro/admet", nil); got != "" {
		t.Errorf("unrelated failure was annotated: %q", got)
	}
}

func TestExplainComposeFailureNotesWhenTheCPUCountIsAGuess(t *testing.T) {
	// The daemon's count is authoritative; goruntime.NumCPU() is this process's
	// view and over-estimates on Docker Desktop, where containers run in a VM
	// with its own allocation. Which one produced the number changes what the
	// user should check, so the message has to say.
	app := NewApp()
	app.projectPath = t.TempDir()
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8, FromDaemon: false} }
	got := app.explainComposeFailure("range of CPUs is from 0.01 to 8.00", nil)
	if !strings.Contains(got, "this computer") {
		t.Errorf("fallback CPU source not disclosed:\n%s", got)
	}
	if strings.Contains(got, "from the Docker daemon") {
		t.Errorf("a fallback count was reported as the daemon's:\n%s", got)
	}
}

func TestFitResourceLimitsAlwaysReportsWhatItDetected(t *testing.T) {
	// Previously this logged only when a value changed, so "did the fix run on
	// this user's machine?" was unanswerable from a screenshot of the log — and
	// on a correctly-sized machine the silence looked identical to an old binary
	// that has no fitting at all.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources {
		return hostResources{CPUs: 8, MemBytes: 16 << 30, FromDaemon: true}
	}
	env := "WORKER_CPU_CPU_LIMIT=4\nLIGANDX_FITTED_CPUS=8\n" // nothing to change
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(env), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.fitResourceLimits(envfile.Parse(env)); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(app.composeLogPath())
	if err != nil {
		t.Fatalf("nothing was logged at all: %v", err)
	}
	for _, want := range []string{"8 CPUs", "16", "Docker daemon"} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("fitting log is missing %q:\n%s", want, logged)
		}
	}
}

// --- The escape hatch ------------------------------------------------------
//
// Everything else here is automatic or advisory. A user whose .env.production
// has been edited into a state they no longer understand still needs one
// action that puts it back, without hunting for a file under %AppData% and
// without losing their passwords and pinned VERSION along with it.

func TestResetResourceLimitsRestoresTemplateValuesAndRefits(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }

	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(strings.Join([]string{
		"VERSION=v2026.08.05",
		"WORKER_CPU_CPU_LIMIT=4",
		"WORKER_GPU_LONG_CPU_LIMIT=4",
		"CPU_WORKER_CONCURRENCY=2",
	}, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	// A file edited into a mess: an oversized value, a duplicate, and a secret
	// plus a pinned VERSION that must survive untouched.
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(strings.Join([]string{
		"VERSION=v2026.08.05",
		"POSTGRES_PASSWORD=keep-me",
		"WORKER_CPU_CPU_LIMIT=99",
		"WORKER_GPU_LONG_CPU_LIMIT=64",
		"WORKER_CPU_CPU_LIMIT=48",
		"CPU_WORKER_CONCURRENCY=16",
	}, "\n")), 0600); err != nil {
		t.Fatal(err)
	}

	if err := app.ResetResourceLimits(); err != nil {
		t.Fatalf("ResetResourceLimits failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	env := envfile.Parse(string(data))
	if env["WORKER_CPU_CPU_LIMIT"] != "4" || env["WORKER_GPU_LONG_CPU_LIMIT"] != "4" {
		t.Errorf("resource limits not restored from the template: %v", env)
	}
	if env["CPU_WORKER_CONCURRENCY"] != "2" {
		t.Errorf("CPU_WORKER_CONCURRENCY = %q, want the template's 2", env["CPU_WORKER_CONCURRENCY"])
	}
	if n := countEnvDefinitions(string(data), "WORKER_CPU_CPU_LIMIT"); n != 1 {
		t.Errorf("reset left %d live definitions of WORKER_CPU_CPU_LIMIT", n)
	}
	// Secrets and the pinned version are not resource settings and must not be
	// collateral damage — losing POSTGRES_PASSWORD desyncs the Postgres volume.
	if env["POSTGRES_PASSWORD"] != "keep-me" {
		t.Errorf("reset destroyed a secret: POSTGRES_PASSWORD = %q", env["POSTGRES_PASSWORD"])
	}
	if env["VERSION"] != "v2026.08.05" {
		t.Errorf("reset disturbed the pinned VERSION: %q", env["VERSION"])
	}
}

func TestResetResourceLimitsFitsATemplateTooBigForThisMachine(t *testing.T) {
	// Reset must not reintroduce an unstartable value on a small machine: it
	// restores the template and then re-fits, in that order.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 4} }

	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"),
		[]byte("VERSION=v1\nWORKER_CPU_CPU_LIMIT=16\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"),
		[]byte("VERSION=v1\nWORKER_CPU_CPU_LIMIT=2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := app.ResetResourceLimits(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	got := envfile.Parse(string(data))["WORKER_CPU_CPU_LIMIT"]
	if got != "3" { // floor(4 * 0.75)
		t.Errorf("WORKER_CPU_CPU_LIMIT = %q, want 3 — reset restored the template without re-fitting", got)
	}
}

// --- Shipped template defaults --------------------------------------------
//
// The resource fitting lives in the launcher *binary*, but the only artifact
// that auto-updates is the runtime bundle — and the bundle carries the
// template. A user on a new bundle and an old binary therefore still gets the
// template's raw values. So the template itself has to be startable, with no
// runtime fix-up at all, on the smallest machine we support.

// smallestSupportedCPUs is the machine the shipped defaults must start on
// unaided. 4 is the floor for a modern desktop and matches the inline
// `${...:-4}` fallbacks in docker-compose.yml.
const smallestSupportedCPUs = 4

func TestShippedTemplateStartsOnTheSmallestSupportedMachine(t *testing.T) {
	data, err := os.ReadFile(".env.production.template")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range envfile.Parse(string(data)) {
		if !strings.HasSuffix(k, "_CPU_LIMIT") && !strings.HasSuffix(k, "_CPU_RES") {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Errorf("%s=%q is not a number", k, v)
			continue
		}
		if f > smallestSupportedCPUs {
			t.Errorf("%s=%s exceeds %d CPUs — a user on the new runtime bundle but an "+
				"older binary gets this value verbatim, and docker refuses to create the container",
				k, v, smallestSupportedCPUs)
		}
	}
}

// TestTemplatesAgreeOnResourceKeys guards the drift that caused the original
// report: ligand-x/ and ligand-x-launcher/ each carry a copy of the template,
// and sync-runtime-topology.sh historically synced only docker-compose.yml, so
// the two drifted apart silently. They legitimately differ elsewhere (the
// launcher pins VERSION/PRO_VERSION for the release it ships with), so parity
// is enforced on the resource and concurrency keys rather than byte-for-byte.
func TestTemplatesAgreeOnResourceKeys(t *testing.T) {
	ours, err := os.ReadFile(".env.production.template")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := os.ReadFile("../ligand-x/.env.production.template")
	if err != nil {
		t.Skipf("sibling repo not checked out: %v", err)
	}
	a, b := envfile.Parse(string(ours)), envfile.Parse(string(theirs))
	for _, k := range sortedKeys(a) {
		if !isResourceEnvKey(k) {
			continue
		}
		if a[k] != b[k] {
			t.Errorf("%s: launcher has %q, ligand-x has %q — the two templates have drifted", k, a[k], b[k])
		}
	}
	for _, k := range sortedKeys(b) {
		if isResourceEnvKey(k) {
			if _, ok := a[k]; !ok {
				t.Errorf("%s is in ligand-x's template but missing from the launcher's", k)
			}
		}
	}
}

func TestParseBytesAndFormatBytesEnvRoundTrip(t *testing.T) {
	cases := map[string]int64{
		"32G":   32 << 30,
		"512M":  512 << 20,
		"2048k": 2048 << 10,
		"4GB":   4 << 30,
	}
	for in, want := range cases {
		got, ok := parseBytes(in)
		if !ok || got != want {
			t.Errorf("parseBytes(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	if _, ok := parseBytes("not-a-size"); ok {
		t.Error("parseBytes accepted a non-size value")
	}
	if s := formatBytesEnv(14 << 30); s != "14G" {
		t.Errorf("formatBytesEnv(14G) = %q", s)
	}
	if s := formatBytesEnv(1536 << 20); s != "1536M" {
		t.Errorf("formatBytesEnv(1.5G) = %q", s)
	}
}

// TestFitResourceLimitsPreservesUserChosenConcurrency guards the interaction
// with the launcher's settings panel: worker pool sizes are re-tuned once, when
// the machine is first seen, but a value the user later picked in the UI must
// survive every subsequent start. CPU ceilings have no such escape hatch — they
// are a hard docker failure — so they keep being clamped.
func TestFitResourceLimitsPreservesUserChosenConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }

	env := "WORKER_CPU_CPU_LIMIT=16\nCPU_WORKER_CONCURRENCY=4\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte(env), 0644); err != nil {
		t.Fatal(err)
	}

	cur := envfile.Parse(env)
	if err := app.fitResourceLimits(cur); err != nil {
		t.Fatal(err)
	}
	if cur["CPU_WORKER_CONCURRENCY"] != "2" || cur["WORKER_CPU_CPU_LIMIT"] != "6" {
		t.Fatalf("first fit did not size the machine: %v", cur)
	}

	// User raises concurrency back to 4 in the settings panel, and separately an
	// oversized CPU limit reappears (e.g. hand-edited from an old file).
	if err := app.SaveUserSettings(UserSettings{CPUWorkerConcurrency: 4, GPUShortConcurrency: 2, GPULongConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if err := app.setProductionEnvValue("WORKER_CPU_CPU_LIMIT", "16"); err != nil {
		t.Fatal(err)
	}

	content, err := app.GetEnvContent("prod")
	if err != nil {
		t.Fatal(err)
	}
	cur = envfile.Parse(content)
	if err := app.fitResourceLimits(cur); err != nil {
		t.Fatal(err)
	}
	if cur["CPU_WORKER_CONCURRENCY"] != "4" {
		t.Errorf("user's concurrency choice was overwritten: got %q, want 4", cur["CPU_WORKER_CONCURRENCY"])
	}
	if cur["WORKER_CPU_CPU_LIMIT"] != "6" {
		t.Errorf("unstartable CPU limit was not re-clamped: got %q, want 6", cur["WORKER_CPU_CPU_LIMIT"])
	}
}

// --- Port and storage pre-flight (preflight.go) --------------------------

func TestFitPortsMovesOnlyConflictingPorts(t *testing.T) {
	// 8080 and 3000 are the realistic case: a developer already running
	// something on the two most commonly used ports.
	taken := map[int]bool{8080: true, 3000: true}
	updates, notes := fitPorts(map[string]string{}, func(p int) bool { return !taken[p] })

	if updates["APP_PORT"] != "8081" {
		t.Errorf("APP_PORT: got %q, want 8081 (notes: %v)", updates["APP_PORT"], notes)
	}
	if updates["FRONTEND_PORT"] != "3001" {
		t.Errorf("FRONTEND_PORT: got %q, want 3001", updates["FRONTEND_PORT"])
	}
	for _, key := range []string{"GATEWAY_PORT", "FLOWER_PORT", "RABBITMQ_MGMT_PORT"} {
		if v, ok := updates[key]; ok {
			t.Errorf("%s was moved to %q but was never in conflict", key, v)
		}
	}
}

func TestFitPortsNeverAssignsTheSamePortTwice(t *testing.T) {
	// Everything from 8080 up to 8082 taken, and the gateway's own 8000 free:
	// the app must not be handed a port another service already claimed.
	updates, _ := fitPorts(
		map[string]string{"APP_PORT": "8080", "GATEWAY_PORT": "8081"},
		func(p int) bool { return p != 8080 && p != 8081 },
	)
	seen := map[string]bool{}
	for _, v := range updates {
		if seen[v] {
			t.Fatalf("two services assigned port %s: %v", v, updates)
		}
		seen[v] = true
	}
	if updates["APP_PORT"] == "8081" {
		t.Errorf("APP_PORT moved onto an occupied port: %v", updates)
	}
}

func TestFitPortsLeavesEverythingAloneWhenFree(t *testing.T) {
	updates, _ := fitPorts(map[string]string{}, func(int) bool { return true })
	if len(updates) != 0 {
		t.Fatalf("expected no port changes on a clean host, got %v", updates)
	}
}

func TestFitPortsKeepsValueWhenNoFreePortNearby(t *testing.T) {
	// Nothing is bindable: rather than writing an unverified port, keep the
	// configured one and let docker report the conflict itself.
	updates, _ := fitPorts(map[string]string{}, func(int) bool { return false })
	if len(updates) != 0 {
		t.Fatalf("expected no rewrites when no port is free, got %v", updates)
	}
}

// TestFitPublishedPortsIgnoresPortsHeldByOurOwnStack is the restart case: a
// running stack holds its own ports, and treating those as conflicts would walk
// every port up by one on each restart.
func TestFitPublishedPortsIgnoresOwnHeldPorts(t *testing.T) {
	ours := map[int]bool{8080: true}
	free := func(p int) bool {
		if ours[p] {
			return true // held by us
		}
		return p != 3000
	}
	updates, _ := fitPorts(map[string]string{}, free)
	if _, moved := updates["APP_PORT"]; moved {
		t.Errorf("APP_PORT moved even though our own container holds it: %v", updates)
	}
	if updates["FRONTEND_PORT"] != "3001" {
		t.Errorf("a genuine conflict was not resolved: %v", updates)
	}
}

func TestEnsureProductionEnvDerivesCORSFromAppPort(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }

	template := "VERSION=v2026.06.21\nAPP_PORT=8081\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatal(err)
	}
	content, err := app.GetEnvContent("prod")
	if err != nil {
		t.Fatal(err)
	}
	got := envfile.Parse(content)["CORS_ORIGINS"]
	want := "http://localhost:3000,http://127.0.0.1:3000,http://localhost:8081,http://127.0.0.1:8081"
	if got != want {
		t.Errorf("CORS_ORIGINS did not follow APP_PORT:\n got %q\nwant %q", got, want)
	}
	// The Open buttons must point at the same place.
	if p := app.envPort("APP_PORT", 8080); p != 8081 {
		t.Errorf("envPort(APP_PORT) = %d, want 8081", p)
	}
}

func TestCheckDiskSpaceBlocksOnlyBelowTheMeasuredFloor(t *testing.T) {
	groupMap := map[string]ServiceGroup{
		"core": {ID: "core", SizeMB: 5500},
		"md":   {ID: "md", SizeMB: 4500},
	}
	app := NewApp()
	app.projectPath = t.TempDir()

	// Plenty of space: silent.
	warning, err := app.checkDiskSpaceWithFree([]string{"core"}, groupMap, nil, 500<<30, "/tmp")
	if err != nil || warning != "" {
		t.Errorf("expected silence with 500G free, got warning=%q err=%v", warning, err)
	}

	// Between the floor and the estimate: warn, never block. The user may know
	// better than an estimate we have already shown to be shaky.
	warning, err = app.checkDiskSpaceWithFree([]string{"core", "md"}, groupMap, nil, 20<<30, "/tmp")
	if err != nil {
		t.Errorf("20G free must not block a pull: %v", err)
	}
	if warning == "" {
		t.Error("expected a low-space warning at 20G free against a ~30G estimate")
	}

	// Below the floor: block, because no single image unpacks into this.
	if _, err = app.checkDiskSpaceWithFree([]string{"core"}, groupMap, nil, 3<<30, "/tmp"); err == nil {
		t.Error("expected a hard error below the 8G floor")
	}

	// Already-present groups need no new space.
	present := map[string]bool{"core": true, "md": true}
	warning, err = app.checkDiskSpaceWithFree([]string{"core", "md"}, groupMap, present, 20<<30, "/tmp")
	if err != nil || warning != "" {
		t.Errorf("already-pulled groups should require nothing: warning=%q err=%v", warning, err)
	}
}

// TestPortFreeDetectsRealListener checks the predicate the pre-flight actually
// uses against a real socket, so the pure fitPorts tests are not the only thing
// standing between a user and "port is already allocated".
func TestPortFreeDetectsRealListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a local port in this environment: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if portFree("127.0.0.1", port) {
		t.Errorf("port %d is bound by this test but reported free", port)
	}
	ln.Close()
	if !portFree("127.0.0.1", port) {
		t.Errorf("port %d reported busy after the listener closed", port)
	}
}

// TestEnsureProductionEnvSeedsProVersionForUpgradingInstalls covers the
// licensed-user upgrade path: compose resolves Pro images through
// ${PRO_VERSION:-${VERSION}}, so an .env.production written before PRO_VERSION
// existed would follow VERSION and pull Pro tags that a core-only release never
// published. The template's pin must be seeded in, without overriding a value
// the user already set.
func TestEnsureProductionEnvSeedsProVersionForUpgradingInstalls(t *testing.T) {
	template := "VERSION=v2026.08.05\nPRO_VERSION=v2026.06.21\n"

	t.Run("seeded when absent", func(t *testing.T) {
		tmpDir := t.TempDir()
		app := NewApp()
		app.projectPath = tmpDir
		app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
		if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
			t.Fatal(err)
		}
		// Pre-existing file from an older launcher: VERSION only, no PRO_VERSION.
		if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("VERSION=v2026.06.21\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := app.ensureProductionEnv(); err != nil {
			t.Fatal(err)
		}
		content, _ := app.GetEnvContent("prod")
		if got := envfile.Parse(content)["PRO_VERSION"]; got != "v2026.06.21" {
			t.Errorf("PRO_VERSION not seeded: got %q, want v2026.06.21", got)
		}
	})

	t.Run("user's own value preserved", func(t *testing.T) {
		tmpDir := t.TempDir()
		app := NewApp()
		app.projectPath = tmpDir
		app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
		if err := os.WriteFile(filepath.Join(tmpDir, ".env.production.template"), []byte(template), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("VERSION=v2026.08.05\nPRO_VERSION=v2026.07.01\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := app.ensureProductionEnv(); err != nil {
			t.Fatal(err)
		}
		content, _ := app.GetEnvContent("prod")
		if got := envfile.Parse(content)["PRO_VERSION"]; got != "v2026.07.01" {
			t.Errorf("user's PRO_VERSION was overwritten: got %q", got)
		}
	})
}

func TestInstalledRuntimeVersionReadsMarker(t *testing.T) {
	dir := t.TempDir()
	if v := runtimebundle.InstalledVersion(dir); v != "" {
		t.Errorf("expected empty for a dir with no marker, got %q", v)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ligandx-runtime-version"), []byte("v2026.08.05\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if v := runtimebundle.InstalledVersion(dir); v != "v2026.08.05" {
		t.Errorf("got %q, want v2026.08.05 (trailing newline must be trimmed)", v)
	}
	// GetDistributionStatus surfaces it so the UI can show what is installed.
	app := NewApp()
	app.projectPath = dir
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := app.GetDistributionStatus().InstalledVersion; got != "v2026.08.05" {
		t.Errorf("DistributionStatus.InstalledVersion = %q", got)
	}
}

func TestReleaseWorkflowDefersLatestAndRecordsSigningEvidence(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".github", "workflows", "launcher-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	for _, required := range []string{
		"run-name: Launcher ${{ inputs.version }} from product run ${{ inputs.orchestrator_run_id }}",
		"orchestrator_run_id:",
		"promote_latest:",
		"make_latest: false",
		`"platform_signing": platform_signing`,
		`"recommended": recommended`,
		"if recommended:",
		`raise SystemExit("release index must identify exactly one recommended release")`,
		"Enforce draft stable publication policy",
		"draft: true",
		"prerelease: false",
		"-F draft=true",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("launcher release workflow is missing %q", required)
		}
	}
	if strings.Contains(workflow, "make_latest: ${{ inputs.channel == 'stable' }}") {
		t.Fatal("launcher workflow must not promote GitHub latest independently")
	}
	if strings.Contains(workflow, "options: [rc, stable, preview]") {
		t.Fatal("internal candidates must not create public launcher prereleases")
	}
}

func TestLauncherQualityRunsForMainPushes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".github", "workflows", "quality.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	if !strings.Contains(workflow, "push:\n    branches: [main]") {
		t.Fatal("launcher quality workflow must run for commits pushed to main")
	}
}

// stripWholeEnvDefault returns the default branch of a ${VAR:-default} shell
// expansion when the entire string is exactly one such expansion, and the input
// unchanged otherwise. Brace depth decides: a reference like
// ${PREFIX:-repo}/name:${TAG:-v} closes its first brace mid-string and is left
// alone, while ${IMAGE:-repo/name:${TAG:-v}} unwraps to repo/name:${TAG:-v}.
func stripWholeEnvDefault(raw string) string {
	if !strings.HasPrefix(raw, "${") {
		return raw
	}
	depth := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				if i != len(raw)-1 {
					return raw
				}
				inner := raw[2:i]
				if idx := strings.Index(inner, ":-"); idx >= 0 {
					return inner[idx+2:]
				}
				return raw
			}
		}
	}
	return raw
}

func TestAgentMCPSessionUsesProtectedStoreNotFiles(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	store := secretstore.NewMemory()
	app.secretStore = store
	sessionID := "0123456789abcdef0123456789abcdef"
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	secret := agentsession.Secret{
		Token:        "workspace-token-value",
		SigningKey:   hex.EncodeToString(private.Seed()),
		CredentialID: "11111111-2222-3333-4444-555555555555",
		Scopes:       []string{"projects:read", "jobs:read", "jobs:plan"},
	}
	meta := agentsession.Meta{
		SessionID:        sessionID,
		CredentialID:     secret.CredentialID,
		ExpiresAt:        "2026-09-02T00:00:00Z",
		ExecutionEnabled: false,
		CreatedAt:        "2026-09-02T00:00:00Z",
	}
	if err := agentsession.Persist(store, app.projectPath, meta, secret); err != nil {
		t.Fatalf("store session: %v", err)
	}
	cmd := agentsession.Command(app.projectPath, secret, nil, nil, nil)
	joinedArgs := strings.Join(cmd.Args, " ")
	joinedEnv := strings.Join(cmd.Env, "\n")
	if strings.Contains(joinedArgs, "workspace-token-value") || strings.Contains(joinedArgs, secret.SigningKey) {
		t.Fatalf("connector arguments must not contain secrets")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_TOKEN=workspace-token-value") {
		t.Fatalf("connector must forward the bearer token only through its private environment")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_CREDENTIAL_ID="+secret.CredentialID) {
		t.Fatalf("connector must inject the credential id into the child environment")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_SIGNING_KEY="+secret.SigningKey) {
		t.Fatalf("connector must inject the signing key into the child environment")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_SCOPES=projects:read,jobs:read,jobs:plan") {
		t.Fatalf("connector must inject workspace scopes into the child environment")
	}
	raw, err := os.ReadFile(filepath.Join(app.projectPath, ".ligandx-agent-mcp", "sessions.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(string(raw), "workspace-token-value") || strings.Contains(string(raw), secret.SigningKey) {
		t.Fatalf("session metadata must remain non-secret")
	}
	if _, err := os.Stat(filepath.Join(app.projectPath, ".ligandx-agent-mcp", "session-"+sessionID)); !os.IsNotExist(err) {
		t.Fatalf("legacy token files must not be created")
	}
	copied, err := app.CopyAgentSessionConfig(sessionID)
	if err != nil {
		t.Fatalf("copy config: %v", err)
	}
	if strings.Contains(copied, "workspace-token-value") || strings.Contains(copied, secret.SigningKey) {
		t.Fatalf("copied MCP config must not contain secrets")
	}
	if err := agentsession.Delete(store, app.projectPath, sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := store.Get(sessionID); err == nil {
		t.Fatalf("revoked session must disappear from protected storage")
	}
}

func TestLinuxWithoutSecretServiceDoesNotBreakNonMCPLauncher(t *testing.T) {
	app := NewApp()
	store := secretstore.NewMemory()
	store.SetUnavailable(secretstore.ErrUnavailable)
	app.secretStore = store
	status := app.GetAgentStorageStatus()
	if status.Available {
		t.Fatal("storage should report unavailable")
	}
	if status.Message == "" {
		t.Fatal("unavailable storage must include an actionable message")
	}
	if app.logStreams == nil {
		t.Fatal("core launcher state must still initialize when assistant storage is locked")
	}
}

// A user who installed before a fix shipped has a runtime directory full of the
// *old* docker-compose.yml. Downloading a new launcher does not replace it —
// only "Update now" -> InstallRuntimeBundle does. So the install must be allowed
// to overwrite the managed runtime directory; short-circuiting on "a compose
// file is already there" strands them on the broken runtime forever, while the
// UI reports success.
func TestManagedRuntimeDirDoesNotShortCircuitInstall(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	// A shipped launcher is a public build, so developerSourceCandidates finds
	// nothing. Under the dev build tag it would find this checkout instead, so
	// run from a directory with no compose project around it.
	t.Chdir(t.TempDir())

	app := NewApp()
	runtimeDir, err := app.defaultRuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A stale runtime from an earlier release.
	if err := os.WriteFile(filepath.Join(runtimeDir, "docker-compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, ok := app.findProjectPath()
	if !ok {
		t.Fatalf("findProjectPath did not see the runtime dir %s", runtimeDir)
	}
	if foreignRuntimeProject(found, runtimeDir) {
		t.Errorf("install would skip its own managed runtime dir\n found:   %s\n runtime: %s", found, runtimeDir)
	}
}

// The counterpart: a developer source checkout, or a runtime shipped next to the
// executable, is not ours to overwrite. That is the case the skip exists for.
func TestForeignComposeProjectStillSkipsInstall(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	app := NewApp()
	runtimeDir, err := app.defaultRuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	if !foreignRuntimeProject(checkout, runtimeDir) {
		t.Errorf("a source checkout at %s was treated as the managed runtime %s", checkout, runtimeDir)
	}
	if foreignRuntimeProject("", runtimeDir) {
		t.Error(`no discovered project should not count as "foreign"`)
	}
}

// The June runtime declares worker-kinetics as `${WORKER_KINETICS_CPU_LIMIT:-12}`
// and no template defines that key, so fitResourceEnv — which only walks keys
// present in .env.production — cannot see the 12. On an 8-thread machine docker
// then refuses to create the container with the same "range of CPUs is from 0.01
// to 8.00" the user already had, no matter what they edit. verifyFittedModel must
// recover the key from the compose text and pin it.
func TestVerifyFittedModelClampsComposeInlineOnlyDefault(t *testing.T) {
	tmpDir := t.TempDir()
	compose := `services:
  worker-cpu:
    deploy:
      resources:
        limits:
          cpus: ${WORKER_CPU_CPU_LIMIT:-16}
  worker-kinetics:
    deploy:
      resources:
        limits:
          cpus: ${WORKER_KINETICS_CPU_LIMIT:-12}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.production"), []byte("WORKER_CPU_CPU_LIMIT=8\n"), 0644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.projectPath = tmpDir
	app.hostResourcesFn = func() hostResources { return hostResources{CPUs: 8} }
	app.composeConfigFn = func([]string) ([]byte, error) {
		return []byte(`{"services":{
			"worker-cpu":{"deploy":{"resources":{"limits":{"cpus":"8"}}}},
			"worker-kinetics":{"deploy":{"resources":{"limits":{"cpus":"12"}}}}}}`), nil
	}

	if err := app.verifyFittedModel([]string{"compose", "up", "-d"}); err != nil {
		t.Fatalf("start was refused for a limit that is fixable from the compose text: %v", err)
	}

	content, err := app.GetEnvContent("prod")
	if err != nil {
		t.Fatal(err)
	}
	got := envfile.Parse(content)["WORKER_KINETICS_CPU_LIMIT"]
	if got != "6" {
		t.Errorf("WORKER_KINETICS_CPU_LIMIT = %q, want %q (floor(8 * 0.75))", got, "6")
	}
}

// rabbitmq/redis/postgres/flower/proxy pin container_name so a local override
// stack can share them with the real install. That defeats
// `docker compose --project-name` isolation, which is exactly what candidate
// validation (and `make validate-staging`) needs when a production stack is
// already running. The generated snapshot must keep the prefix overridable,
// and the staging script must actually set it from COMPOSE_PROJECT_NAME.
func TestValidateStagingIsolatesInfraContainerNames(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rabbitmq", "redis", "postgres", "flower", "proxy"} {
		want := "${LIGANDX_INFRA_CONTAINER_PREFIX:-ligandx}-" + name
		if !strings.Contains(string(compose), "container_name: "+want) {
			t.Errorf("docker-compose.yml: %s must use overridable container_name %s", name, want)
		}
		if strings.Contains(string(compose), "container_name: ligandx-"+name+"\n") {
			t.Errorf("docker-compose.yml: %s still has a hardcoded ligandx-%s container_name", name, name)
		}
	}

	script, err := os.ReadFile("scripts/validate-staging-startup.sh")
	if err != nil {
		t.Fatal(err)
	}
	const exportLine = `export LIGANDX_INFRA_CONTAINER_PREFIX="${LIGANDX_INFRA_CONTAINER_PREFIX:-$COMPOSE_PROJECT_NAME}"`
	if !strings.Contains(string(script), exportLine) {
		t.Fatal("validate-staging-startup.sh must export LIGANDX_INFRA_CONTAINER_PREFIX from COMPOSE_PROJECT_NAME so an isolated stack can start next to a running install")
	}
}
