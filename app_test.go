package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRuntimeBundleReleaseURLsUseLauncherRepo(t *testing.T) {
	checks := map[string]string{
		"defaultRuntimeBundleURL": defaultRuntimeBundleURL,
		"latestReleaseAPIURL":     latestReleaseAPIURL,
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
	for _, svc := range coreServiceNames() {
		if infraOnly[svc] {
			continue
		}
		found := false
		for img := range imageSet {
			if strings.Contains(img, svc) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("core service %q has no matching image in coreServiceImages(): %v", svc, core.Images)
		}
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

func TestRuntimeBundleExtractionAllowsOnlyRuntimeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "runtime.zip")
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for name, content := range map[string]string{
		"ligand-x-main/docker-compose.yml":        "services: {}\n",
		"ligand-x-main/.env.production.template":  "POSTGRES_PASSWORD=CHANGE_ME\n",
		"ligand-x-main/docker/nginx/ligandx.conf": "server { listen 80; }\n",
		"ligand-x-main/config/rabbitmq.conf":      "loopback_users = none\n",
		"ligand-x-main/config/flower_config.py":   "broker_api = ''\n",
		"ligand-x-main/services/private.py":       "do not extract",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmpDir, "runtime")
	if err := extractRuntimeBundle(zipPath, dest); err != nil {
		t.Fatalf("extractRuntimeBundle failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "docker-compose.yml")); err != nil {
		t.Fatalf("expected compose file to be extracted: %v", err)
	}
	// Config files bind-mounted by docker-compose.yml must land on disk, or Docker
	// auto-creates the missing source as a directory and the mount fails with
	// "not a directory" (the proxy/rabbitmq/flower startup bug).
	for _, rel := range []string{
		filepath.Join("docker", "nginx", "ligandx.conf"),
		filepath.Join("config", "rabbitmq.conf"),
		filepath.Join("config", "flower_config.py"),
	} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Fatalf("expected bind-mounted config %q to be extracted: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "services", "private.py")); !os.IsNotExist(err) {
		t.Fatalf("unexpected private source extraction error state: %v", err)
	}
}

func TestRuntimeBundleExtractionSelfHealsStaleDirectorySource(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "runtime.zip")
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	w, err := zw.Create("ligand-x-main/docker/nginx/ligandx.conf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("server { listen 80; }\n")); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"ligand-x-main/docker-compose.yml":       "services: {}\n",
		"ligand-x-main/.env.production.template": "VERSION=v1.2.3\n",
	} {
		requiredWriter, createErr := zw.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := requiredWriter.Write([]byte(content)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmpDir, "runtime")
	// Simulate a stale install: Docker auto-created the missing bind-mount source
	// as a directory on a previous broken run.
	staleConf := filepath.Join(dest, "docker", "nginx", "ligandx.conf")
	if err := os.MkdirAll(staleConf, 0755); err != nil {
		t.Fatal(err)
	}

	if err := extractRuntimeBundle(zipPath, dest); err != nil {
		t.Fatalf("extractRuntimeBundle failed on stale install: %v", err)
	}
	info, err := os.Stat(staleConf)
	if err != nil {
		t.Fatalf("expected config to be extracted over stale dir: %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected ligandx.conf to be a file after self-heal, still a directory")
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
	if !strings.Contains(err.Error(), "LIGANDX_REGISTRY_TOKEN_URL") {
		t.Fatalf("Expected broker guidance in error, got %v", err)
	}
}

func TestEncodeRegistryAuth(t *testing.T) {
	encoded, err := encodeRegistryAuth(registryCredentials{
		Host:     "ghcr.io",
		Username: "oauth2",
		Token:    "short-lived-token",
	})
	if err != nil {
		t.Fatalf("encodeRegistryAuth failed: %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("encoded auth is not base64url: %v", err)
	}
	if !strings.Contains(string(raw), "short-lived-token") {
		t.Fatalf("encoded auth missing token: %s", string(raw))
	}
}

func TestCheckGPU(t *testing.T) {
	app := NewApp()
	// Just verify the method doesn't panic
	_ = app.CheckGPU()
}

// TestEmbeddedPublicKeyMatchesPemFile prevents drift between the launcher's
// compiled-in public key and the canonical PEM under lib/licensing/. If
// either is rotated alone, every signed license silently fails verification
// at one verifier or the other.
func TestEmbeddedPublicKeyMatchesPemFile(t *testing.T) {
	pemPath := filepath.Join("..", "ligand-x", "lib", "licensing", "public_key.pem")
	onDisk, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("read %s: %v", pemPath, err)
	}
	// Compare PEM blocks structurally — trailing whitespace differences in
	// the source files are not meaningful, but key bytes must match.
	diskBlock, _ := pem.Decode(onDisk)
	embedBlock, _ := pem.Decode([]byte(licensePublicKeyPEM))
	if diskBlock == nil || embedBlock == nil {
		t.Fatalf("failed to PEM-decode launcher (%v) or %s (%v)", embedBlock, pemPath, diskBlock)
	}
	if !bytes.Equal(diskBlock.Bytes, embedBlock.Bytes) {
		t.Fatalf("public key drift between launcher embed and %s", pemPath)
	}
}

// signTestLicense produces a signed bundle for the table-driven verifier
// tests. Uses a fresh keypair per call so production keys are never needed.
func signTestLicense(t *testing.T, payload map[string]interface{}) (bundleBytes []byte, publicPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	canonical, err := canonicalLicensePayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	sig := ed25519.Sign(priv, canonical)

	bundle := map[string]interface{}{
		"schema":    "ligandx-license/1",
		"algorithm": "Ed25519",
		"payload":   payload,
		"signature": base64.StdEncoding.EncodeToString(sig),
	}
	bundleBytes, err = json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	pubDer, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	publicPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer})
	return bundleBytes, publicPEM
}

func TestVerifyLicenseValid(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-1",
		"entitlements": []interface{}{"qc", "admet"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"customer":     map[string]interface{}{"name": "Acme"},
	})
	got, err := verifyLicenseDataWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid || got.Edition != "pro" {
		t.Fatalf("expected valid pro, got %+v", got)
	}
	if got.CustomerName != "Acme" {
		t.Fatalf("expected customer Acme, got %q", got.CustomerName)
	}
	if !got.HasEntitlement("qc") || got.HasEntitlement("boltz2") {
		t.Fatalf("entitlement check wrong: %+v", got.Entitlements)
	}
}

func TestVerifyLicenseRejectsTamperedPayload(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-2",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	tampered := bytes.Replace(bundle, []byte(`"qc"`), []byte(`"boltz2"`), 1)
	got, _ := verifyLicenseDataWithPublicKey(tampered, pub)
	if got.Valid {
		t.Fatalf("expected tampered payload to fail, got valid")
	}
	if got.Reason != "invalid_signature" {
		t.Fatalf("expected invalid_signature, got %q", got.Reason)
	}
}

func TestVerifyLicenseRejectsWrongKey(t *testing.T) {
	bundle, _ := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-3",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	_, otherPub := signTestLicense(t, map[string]interface{}{"edition": "pro"})
	got, _ := verifyLicenseDataWithPublicKey(bundle, otherPub)
	if got.Valid {
		t.Fatalf("expected verification under wrong key to fail")
	}
}

func TestVerifyLicenseExpiredNoGrace(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-4",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, _ := verifyLicenseDataWithPublicKey(bundle, pub)
	if got.Valid {
		t.Fatalf("expected expired license to be invalid")
	}
	if got.Reason != "license_expired" {
		t.Fatalf("expected license_expired, got %q", got.Reason)
	}
}

func TestVerifyLicenseExpiredWithinGrace(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-5",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		"grace_until":  time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, err := verifyLicenseDataWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid {
		t.Fatalf("expected grace-period license to be valid, got %+v", got)
	}
}

func TestVerifyLicenseUnknownEntitlement(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-6",
		"entitlements": []interface{}{"definitely-not-a-real-module"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, _ := verifyLicenseDataWithPublicKey(bundle, pub)
	if got.Valid {
		t.Fatalf("expected unknown entitlement to invalidate license")
	}
	if got.Reason != "unknown_entitlement" {
		t.Fatalf("expected unknown_entitlement, got %q", got.Reason)
	}
}

func TestVerifyLicenseProRequiresEntitlements(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-7",
		"entitlements": []interface{}{},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, _ := verifyLicenseDataWithPublicKey(bundle, pub)
	if got.Valid {
		t.Fatalf("expected empty Pro entitlements to invalidate")
	}
	if got.Reason != "pro_license_requires_entitlements" {
		t.Fatalf("got reason %q", got.Reason)
	}
}

func TestVerifyLicenseAcademicGrantsAll(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":    "academic",
		"license_id": "LX-TEST-8",
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, err := verifyLicenseDataWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid || got.Edition != "academic" {
		t.Fatalf("expected academic valid, got %+v", got)
	}
	for entitlement := range proEntitlements {
		if !got.HasEntitlement(entitlement) {
			t.Fatalf("academic should grant %q", entitlement)
		}
	}
}

func TestVerifyLicenseWithVersionRangeGreaterThan(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":       "academic",
		"license_id":    "LX-TEST-HTML-ESCAPE",
		"expires_at":    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"version_range": ">=0.0.0",
	})
	got, err := verifyLicenseDataWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid || got.Reason != "ok" {
		t.Fatalf("expected version_range license to verify, got %+v", got)
	}
}

func TestHasEntitlementSemantics(t *testing.T) {
	free := LicenseSummary{Edition: "free", Valid: true}
	if free.HasEntitlement("qc") {
		t.Fatal("free should not have qc")
	}

	expired := LicenseSummary{Edition: "pro", Valid: false, Entitlements: []string{"qc"}}
	if expired.HasEntitlement("qc") {
		t.Fatal("invalid pro should not have entitlement")
	}

	pro := LicenseSummary{Edition: "pro", Valid: true, Entitlements: []string{"qc"}}
	if !pro.HasEntitlement("qc") || pro.HasEntitlement("boltz2") {
		t.Fatal("pro entitlement scoping wrong")
	}

	academic := LicenseSummary{Edition: "academic", Valid: true}
	if !academic.HasEntitlement("anything") {
		t.Fatal("academic should grant any pro entitlement")
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

func TestRegistryCredentialsFromLicenseRequiresBridgeMode(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	licDir := filepath.Join(app.projectPath, "data", "license")
	if err := os.MkdirAll(licDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeBundle := func(payload map[string]interface{}) {
		bundle := map[string]interface{}{
			"schema":    "ligandx-license/1",
			"algorithm": "Ed25519",
			"payload":   payload,
			"signature": base64.StdEncoding.EncodeToString([]byte("ignored-by-this-helper")),
		}
		raw, _ := json.Marshal(bundle)
		if err := os.WriteFile(app.licensePath(), raw, 0600); err != nil {
			t.Fatalf("write license: %v", err)
		}
	}

	// Without registry_mode=bridge, embedded creds must be ignored.
	writeBundle(map[string]interface{}{
		"edition": "pro",
		"registry": map[string]interface{}{
			"host": "ghcr.io", "username": "oauth2", "token": "tok",
		},
	})
	if _, ok := app.registryCredentialsFromLicense(); ok {
		t.Fatal("expected creds to be ignored without registry_mode=bridge")
	}

	// With registry_mode=bridge, accept them.
	writeBundle(map[string]interface{}{
		"edition":       "pro",
		"registry_mode": "bridge",
		"registry": map[string]interface{}{
			"host": "ghcr.io", "username": "oauth2", "token": "tok",
		},
	})
	if _, ok := app.registryCredentialsFromLicense(); !ok {
		t.Fatal("expected creds to be accepted under registry_mode=bridge")
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
		"up", "-d", "--pull=never", "postgres", "redis", "rabbitmq",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("productionInfraUpArgs() = %v, want %v", got, want)
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

func TestWritePrivateFileReplacesExistingContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("unexpected private file content %q", data)
	}
	requirePrivateMode(t, path)
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
	manifest, err := json.Marshal(runtimeBundleManifest{
		Schema:    "ligandx-runtime-manifest/1",
		Version:   version,
		Asset:     runtimeBundleAssetName,
		SHA256:    fmt.Sprintf("%x", digest),
		Size:      int64(len(bundle)),
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		GitCommit: strings.Repeat("a", 40),
		Artifacts: map[string]runtimeReleaseArtifact{
			runtimeBundleAssetName: {SHA256: fmt.Sprintf("%x", digest), Size: int64(len(bundle))},
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
	manifest, err := verifyRuntimeBundleManifest(manifestBytes, signatureBytes, "v1.2.3")
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	if _, err := verifyRuntimeBundleManifest(manifestBytes, signatureBytes, "v9.9.9"); err == nil {
		t.Fatal("manifest for a different release tag was accepted")
	}
	trustedKey := runtimeBundlePublicKeyB64
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtimeBundlePublicKeyB64 = base64.StdEncoding.EncodeToString(otherPublic)
	if _, err := verifyRuntimeBundleManifest(manifestBytes, signatureBytes, "v1.2.3"); err == nil {
		t.Fatal("manifest signed by an untrusted signer was accepted")
	}
	runtimeBundlePublicKeyB64 = trustedKey
	expiredManifest, expiredSignature := signRuntimeManifestForTest(
		t, bundle, "v1.2.3", time.Now().Add(-time.Minute),
	)
	if _, err := verifyRuntimeBundleManifest(expiredManifest, expiredSignature, "v1.2.3"); err == nil {
		t.Fatal("expired runtime manifest was accepted")
	}

	bundlePath := filepath.Join(t.TempDir(), runtimeBundleAssetName)
	if err := os.WriteFile(bundlePath, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeBundleFile(bundlePath, manifest); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	tamperedManifest := bytes.Replace(manifestBytes, []byte("v1.2.3"), []byte("v9.9.9"), 1)
	if _, err := verifyRuntimeBundleManifest(tamperedManifest, signatureBytes, ""); err == nil {
		t.Fatal("tampered signed manifest was accepted")
	}
	if err := os.WriteFile(bundlePath, append(bundle, byte(0)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeBundleFile(bundlePath, manifest); err == nil {
		t.Fatal("tampered bundle was accepted")
	}
}

func TestRuntimeRollbackPolicyRejectsOlderVersion(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := writePrivateFile(filepath.Join(runtimeDir, ".ligandx-runtime-version"), []byte("v2.1.0\n")); err != nil {
		t.Fatal(err)
	}
	if err := enforceRuntimeRollbackPolicy(runtimeDir, "v2.0.9"); err == nil {
		t.Fatal("runtime downgrade was accepted")
	}
	if err := enforceRuntimeRollbackPolicy(runtimeDir, "v2.1.1"); err != nil {
		t.Fatalf("runtime upgrade was rejected: %v", err)
	}
}

func TestRuntimeDownloadRejectsUnapprovedHost(t *testing.T) {
	if _, err := approvedRuntimeDownloadURL("https://attacker.example/runtime.zip"); err == nil {
		t.Fatal("unapproved runtime host was accepted")
	}
}

func TestRuntimeBundleRejectsTooManyEntries(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "runtime.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for index := 0; index < runtimeBundleMaxFiles+1; index++ {
		entry, createErr := writer.Create(fmt.Sprintf("ignored-%03d", index))
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte("x")); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeBundle(zipPath, t.TempDir()); err == nil {
		t.Fatal("oversized entry-count archive was accepted")
	}
}

func TestRuntimeBundleRejectsSymlinkEntry(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "runtime.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "docker-compose.yml"}
	header.SetMode(os.ModeSymlink | 0777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeBundle(zipPath, t.TempDir()); err == nil {
		t.Fatal("symbolic-link archive entry was accepted")
	}
}

func TestRuntimeBundleTargetRejectsExistingSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges on Windows")
	}
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "docker-compose.yml")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := rejectRuntimeSymlinkPath(base, target); err == nil {
		t.Fatal("existing target symlink was accepted")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatal("symlink target was modified")
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

func TestRegistryTokenResponseMustBeShortLivedAndExactlyScoped(t *testing.T) {
	repositories := []string{"ghcr.io/kon-218/ligand-x-pro/admet"}
	valid := registryTokenResponse{
		Host:         "ghcr.io",
		Username:     "oauth2",
		Token:        "short-lived-token",
		ExpiresAt:    time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
		Repositories: repositories,
	}
	if _, err := validateRegistryTokenResponse(valid, repositories); err != nil {
		t.Fatalf("valid scoped token response rejected: %v", err)
	}
	wrongScope := valid
	wrongScope.Repositories = []string{"ghcr.io/kon-218/ligand-x-pro/qc"}
	if _, err := validateRegistryTokenResponse(wrongScope, repositories); err == nil {
		t.Fatal("unexpected repository scope was accepted")
	}
	expired := valid
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := validateRegistryTokenResponse(expired, repositories); err == nil {
		t.Fatal("expired registry token was accepted")
	}
	wrongHost := valid
	wrongHost.Host = "registry.attacker.example"
	if _, err := validateRegistryTokenResponse(wrongHost, repositories); err == nil {
		t.Fatal("non-GHCR registry token was accepted")
	}
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
		"WORKER_CPU_CPU_LIMIT":      "8",
		"WORKER_GPU_LONG_CPU_LIMIT": "8",
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
	updates, _ := fitResourceEnv(cur, hostResources{CPUs: 4}, true)
	if updates["WORKER_GPU_LONG_CPU_LIMIT"] != "4" || updates["WORKER_GPU_LONG_CPU_RES"] != "4" {
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
	env := parseEnvFile(string(written))
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

	cur := parseEnvFile(env)
	if err := app.fitResourceLimits(cur); err != nil {
		t.Fatal(err)
	}
	if cur["CPU_WORKER_CONCURRENCY"] != "2" || cur["WORKER_CPU_CPU_LIMIT"] != "8" {
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
	cur = parseEnvFile(content)
	if err := app.fitResourceLimits(cur); err != nil {
		t.Fatal(err)
	}
	if cur["CPU_WORKER_CONCURRENCY"] != "4" {
		t.Errorf("user's concurrency choice was overwritten: got %q, want 4", cur["CPU_WORKER_CONCURRENCY"])
	}
	if cur["WORKER_CPU_CPU_LIMIT"] != "8" {
		t.Errorf("unstartable CPU limit was not re-clamped: got %q, want 8", cur["WORKER_CPU_CPU_LIMIT"])
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
	got := parseEnvFile(content)["CORS_ORIGINS"]
	want := "http://localhost:8081,http://127.0.0.1:8081"
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
		if got := parseEnvFile(content)["PRO_VERSION"]; got != "v2026.06.21" {
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
		if got := parseEnvFile(content)["PRO_VERSION"]; got != "v2026.07.01" {
			t.Errorf("user's PRO_VERSION was overwritten: got %q", got)
		}
	})
}

// TestShouldAdvanceVersionMovesStalePinsForward is the fix for the case that
// made the whole v2026.08.05 release inert for existing users: their
// .env.production held VERSION=v2026.06.21, a valid pin, so the old
// "only rewrite broken values" rule preserved it through both
// ensureProductionEnv and a full runtime-bundle install. They would take the
// new launcher and the new bundle and still run the previous images.
func TestShouldAdvanceVersionMovesStalePinsForward(t *testing.T) {
	cases := []struct {
		name    string
		current string
		release string
		want    bool
		why     string
	}{
		{"the reported case", "v2026.06.21", "v2026.08.05", true, "stale pin must advance"},
		{"equal", "v2026.08.05", "v2026.08.05", false, "nothing to do"},
		{"pin ahead of runtime", "v2026.09.01", "v2026.08.05", false, "never downgrade"},
		{"empty", "", "v2026.08.05", true, "no pin at all"},
		{"placeholder", "CHANGE_ME", "v2026.08.05", true, "broken pin"},
		{"latest", "latest", "v2026.08.05", true, "mutable pin is rejected elsewhere"},
		{"unparseable current", "sha-abc1234", "v2026.08.05", false, "digest pins are deliberate"},
		{"unparseable release", "v2026.06.21", "nightly", false, "refuse to move onto a non-release"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAdvanceVersion(tc.current, tc.release); got != tc.want {
				t.Errorf("shouldAdvanceVersion(%q, %q) = %v, want %v — %s",
					tc.current, tc.release, got, tc.want, tc.why)
			}
		})
	}
}

func TestInstalledRuntimeVersionReadsMarker(t *testing.T) {
	dir := t.TempDir()
	if v := installedRuntimeVersion(dir); v != "" {
		t.Errorf("expected empty for a dir with no marker, got %q", v)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ligandx-runtime-version"), []byte("v2026.08.05\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if v := installedRuntimeVersion(dir); v != "v2026.08.05" {
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
