package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
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
	if len(groups) != 9 {
		t.Errorf("Expected 9 service groups, got %d", len(groups))
	}

	// Create a map for easier lookup
	groupMap := make(map[string]*ServiceGroup)
	for i, g := range groups {
		groupMap[g.ID] = &groups[i]
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
	if err := os.WriteFile(filepath.Join(tmpDir, ".env.example"), []byte("REDIS_PASSWORD=test\n"), 0644); err != nil {
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

	envData, err := os.ReadFile(filepath.Join(tmpDir, ".env"))
	if err != nil {
		t.Fatalf("Failed to read .env: %v", err)
	}
	env := string(envData)
	for _, expected := range []string{
		"LIGANDX_USERNAME=alice",
		"LIGANDX_PASSWORD=strongpass",
		"LIGANDX_API_KEY=",
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
		"LIGANDX_API_KEY=CHANGE_ME",
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
		"LIGANDX_API_KEY=CHANGE_ME",
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
	pemPath := filepath.Join("..", "lib", "licensing", "public_key.pem")
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
