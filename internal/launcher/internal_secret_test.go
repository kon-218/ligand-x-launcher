package launcher

import (
	"ligandx-launcher/internal/envfile"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInternalSecretMeetsFloor exercises the P1-F strength floor directly:
// generated keys and adequate hex/base64 tokens pass; short, low-variety and
// low-entropy hand-set values fail. The CHANGE_ME template marker is handled by
// envfile.IsPlaceholder before this floor is consulted, so it is not a floor case.
func TestInternalSecretMeetsFloor(t *testing.T) {
	generated, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"generated", generated, true},
		{"hex40", "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", true},
		{"empty", "", false},
		{"ligandx", "ligandx", false},
		{"password", "password", false},
		{"changeme", "changeme", false},
		{"short_high_entropy", "aB3xY9qZ2wE5rT7", false},            // 15 chars < 32
		{"ligandx_repeated", strings.Repeat("ligandx", 6), false},   // 42 chars, 7 distinct
		{"password_repeated", strings.Repeat("password", 4), false}, // 32 chars, 8 distinct
		{"single_char_run", strings.Repeat("a", 40), false},
		{"two_symbol_skew", strings.Repeat("a", 33) + "b", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := internalSecretMeetsFloor(c.val); got != c.want {
				t.Fatalf("internalSecretMeetsFloor(%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

// TestGenerateAPIKeyMeetsInternalSecretFloor proves the generator always clears
// the floor it is paired with, so the startup gate never rejects a fresh key.
func TestGenerateAPIKeyMeetsInternalSecretFloor(t *testing.T) {
	for i := 0; i < 200; i++ {
		k, err := generateAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		if len(k) < internalSecretMinLen {
			t.Fatalf("generated key shorter than floor: %d < %d", len(k), internalSecretMinLen)
		}
		if !internalSecretMeetsFloor(k) {
			t.Fatalf("generated key fails strength floor: %q", k)
		}
	}
}

// TestEffectiveInternalWorkerSecret pins the Compose precedence rule the P1-F
// gate relies on: a set process-env variable (even empty) wins over the dotenv
// value; an unset one leaves the dotenv value effective.
func TestEffectiveInternalWorkerSecret(t *testing.T) {
	unsetInternalSecretEnv(t)
	if v, fromEnv := effectiveInternalWorkerSecret("dotenv-value"); v != "dotenv-value" || fromEnv {
		t.Fatalf("unset process env: got (%q, %v), want (dotenv-value, false)", v, fromEnv)
	}
	t.Setenv("INTERNAL_WORKER_SECRET", "inherited-value")
	if v, fromEnv := effectiveInternalWorkerSecret("dotenv-value"); v != "inherited-value" || !fromEnv {
		t.Fatalf("set process env: got (%q, %v), want (inherited-value, true)", v, fromEnv)
	}
	t.Setenv("INTERNAL_WORKER_SECRET", "")
	if v, fromEnv := effectiveInternalWorkerSecret("dotenv-value"); v != "" || !fromEnv {
		t.Fatalf("empty process env: got (%q, %v), want (\"\", true)", v, fromEnv)
	}
}

func secretTestApp(t *testing.T, template, existing string) *App {
	t.Helper()
	dir := t.TempDir()
	app := NewApp()
	app.projectPath = dir
	if err := os.WriteFile(filepath.Join(dir, ".env.production.template"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if existing != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env.production"), []byte(existing), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func readProdSecret(t *testing.T, app *App, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(app.projectPath, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	return envfile.Parse(string(data))[key]
}

// unsetInternalSecretEnv removes any inherited INTERNAL_WORKER_SECRET for the
// duration of the test so the dotenv value is the effective one, restoring it
// afterwards (testing.T has no Unsetenv).
func unsetInternalSecretEnv(t *testing.T) {
	t.Helper()
	if old, ok := os.LookupEnv("INTERNAL_WORKER_SECRET"); ok {
		t.Cleanup(func() { _ = os.Setenv("INTERNAL_WORKER_SECRET", old) })
		_ = os.Unsetenv("INTERNAL_WORKER_SECRET")
	}
}

// stubCompose records every compose command the launcher would execute, so a
// test can prove a start was refused BEFORE any docker command ran, without a
// Docker daemon (same injection point the GPU-fallback tests use).
func stubCompose(app *App) *[][]string {
	var calls [][]string
	app.composeOnceFn = func(args []string, _ string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	return &calls
}

// prodStartArgs mirrors what StartServices builds for a production start.
func prodStartArgs() []string {
	return []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d", "--pull=never"}
}

// TestProductionStartRejectsInheritedWeakProcessEnvSecret is the P1-F review
// regression: production Compose inherits the launcher's process environment,
// which overrides the validated dotenv secret. Starting with a weak inherited
// INTERNAL_WORKER_SECRET must be refused before any compose command runs, even
// though the dotenv file holds a strong value.
func TestProductionStartRejectsInheritedWeakProcessEnvSecret(t *testing.T) {
	strong, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTERNAL_WORKER_SECRET", "ligandx")
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=" + strong + "\n"
	app := secretTestApp(t, template, existing)
	calls := stubCompose(app)

	err = app.runDockerCompose(prodStartArgs(), "Starting services...")
	if err == nil {
		t.Fatal("production start accepted a weak inherited INTERNAL_WORKER_SECRET")
	}
	if !strings.Contains(err.Error(), "process environment") {
		t.Fatalf("error should name the inherited process-env override, got: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("compose ran despite the weak inherited secret: %v", *calls)
	}
	if got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET"); got != strong {
		t.Fatalf("strong dotenv secret was modified: got %q want %q", got, strong)
	}
}

// TestProductionStartRejectsInheritedOverrideEvenWithWeakDotenv proves rotating
// the dotenv file cannot cure an inherited override: the effective value is
// still the weak process-env one, so the start must be refused.
func TestProductionStartRejectsInheritedOverrideEvenWithWeakDotenv(t *testing.T) {
	t.Setenv("INTERNAL_WORKER_SECRET", "ligandx")
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=ligandx\n"
	app := secretTestApp(t, template, existing)
	calls := stubCompose(app)

	err := app.runDockerCompose(prodStartArgs(), "Starting services...")
	if err == nil || !strings.Contains(err.Error(), "process environment") {
		t.Fatalf("expected rejection naming the process-env override, got: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("compose ran despite the weak inherited secret: %v", *calls)
	}
}

// TestProductionStartRejectsEmptyInheritedOverride covers a variable set to the
// empty string: Compose still treats it as an override (and ${VAR:?} rejects an
// empty value), so the start must be refused rather than fall back to dotenv.
func TestProductionStartRejectsEmptyInheritedOverride(t *testing.T) {
	strong, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTERNAL_WORKER_SECRET", "")
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=" + strong + "\n"
	app := secretTestApp(t, template, existing)
	calls := stubCompose(app)

	if err := app.runDockerCompose(prodStartArgs(), "Starting services..."); err == nil {
		t.Fatal("production start accepted an empty inherited INTERNAL_WORKER_SECRET")
	}
	if len(*calls) != 0 {
		t.Fatalf("compose ran despite the empty inherited secret: %v", *calls)
	}
}

// TestProductionStartAllowsStrongInheritedOverride: a strong process-env value
// meets the floor, so the start proceeds and the dotenv file is left untouched
// (the override is the effective secret, not a reason to rotate the file).
func TestProductionStartAllowsStrongInheritedOverride(t *testing.T) {
	inherited, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	dotenv, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTERNAL_WORKER_SECRET", inherited)
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=" + dotenv + "\n"
	app := secretTestApp(t, template, existing)
	calls := stubCompose(app)

	if err := app.runDockerCompose(prodStartArgs(), "Starting services..."); err != nil {
		t.Fatalf("strong inherited override rejected: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one compose command, got %d", len(*calls))
	}
	if got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET"); got != dotenv {
		t.Fatalf("dotenv secret rotated despite a strong inherited override: got %q want %q", got, dotenv)
	}
}

// TestProductionStartRotatesWeakDotenvSecret is the original P1-F regression on
// the start path: with no process-env override, a hand-set weak value that is
// NOT a CHANGE_ME placeholder is randomized before compose runs.
func TestProductionStartRotatesWeakDotenvSecret(t *testing.T) {
	unsetInternalSecretEnv(t)
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME_GENERATE_RANDOM_KEY_HERE\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=ligandx\n"
	app := secretTestApp(t, template, existing)
	calls := stubCompose(app)

	if err := app.runDockerCompose(prodStartArgs(), "Starting services..."); err != nil {
		t.Fatalf("production start: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one compose command, got %d", len(*calls))
	}
	got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET")
	if got == "ligandx" {
		t.Fatal("weak hand-set INTERNAL_WORKER_SECRET persisted (not rotated)")
	}
	if !internalSecretMeetsFloor(got) {
		t.Fatalf("rotated INTERNAL_WORKER_SECRET does not meet floor: %q", got)
	}
}

// TestStartPreservesStrongInternalSecret guards idempotency and the
// upgrade-preservation invariant: a strong dotenv value is never rotated on
// repeat starts, so containers stay in sync across an upgrade.
func TestStartPreservesStrongInternalSecret(t *testing.T) {
	unsetInternalSecretEnv(t)
	strong, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=" + strong + "\n"
	app := secretTestApp(t, template, existing)
	for i := 0; i < 2; i++ {
		if err := app.hardenInternalWorkerSecretForStart(); err != nil {
			t.Fatalf("start pass %d: %v", i, err)
		}
		if got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET"); got != strong {
			t.Fatalf("pass %d rotated a strong secret: got %q want %q", i, got, strong)
		}
	}
}

// TestEnsureProductionEnvGeneratesInternalSecretFromPlaceholder covers the
// fresh-install seeding path: the template marker is replaced with a strong key
// by the shared placeholder fill (no rotation of a live value involved).
func TestEnsureProductionEnvGeneratesInternalSecretFromPlaceholder(t *testing.T) {
	unsetInternalSecretEnv(t)
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME_GENERATE_RANDOM_KEY_HERE\n"
	app := secretTestApp(t, template, "") // no existing file -> seeds from template
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv: %v", err)
	}
	got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET")
	if envfile.IsPlaceholder(got) || !internalSecretMeetsFloor(got) {
		t.Fatalf("placeholder not replaced with a strong secret: %q", got)
	}
}

// TestStartDoesNotRotateWeakVolumePassword documents the deliberate scope
// boundary: rotating a live Postgres/RabbitMQ/Redis password would desync the
// data volume initialised with it, so weak-secret hardening applies only to the
// volume-free INTERNAL_WORKER_SECRET.
func TestStartDoesNotRotateWeakVolumePassword(t *testing.T) {
	unsetInternalSecretEnv(t)
	template := "VERSION=v2026.06.21\nPOSTGRES_PASSWORD=CHANGE_ME\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nPOSTGRES_PASSWORD=ligandx\nINTERNAL_WORKER_SECRET=ligandx\n"
	app := secretTestApp(t, template, existing)
	if err := app.hardenInternalWorkerSecretForStart(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := readProdSecret(t, app, "POSTGRES_PASSWORD"); got != "ligandx" {
		t.Fatalf("volume-bound POSTGRES_PASSWORD was rotated: %q", got)
	}
	got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET")
	if got == "ligandx" || !internalSecretMeetsFloor(got) {
		t.Fatalf("INTERNAL_WORKER_SECRET not hardened: %q", got)
	}
}

// TestStopReadAndPullPathsNeverRotateInternalSecret is the second half of the
// P1-F finding: rotation and the strength gate belong to the start path only.
// The stop fallback (`compose down`), the shared prodEnvArgs/requirePinned
// preparation, and `compose pull` must leave a weak hand-set dotenv value
// untouched and must not fail on it.
func TestStopReadAndPullPathsNeverRotateInternalSecret(t *testing.T) {
	unsetInternalSecretEnv(t)
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=ligandx\n"
	app := secretTestApp(t, template, existing)
	calls := stubCompose(app)

	// Stop fallback (StopServices without the Docker API): down, not up.
	stopArgs := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "down", "--remove-orphans"}
	if err := app.runDockerCompose(stopArgs, "Stopping services..."); err != nil {
		t.Fatalf("stop path failed on a weak secret: %v", err)
	}
	// Pull: preparation, not a start.
	pullArgs := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "pull"}
	if err := app.runDockerCompose(pullArgs, "Pulling selected services..."); err != nil {
		t.Fatalf("pull path failed on a weak secret: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected stop and pull commands to run, got %d", len(*calls))
	}
	// Read/prep paths shared by stop and status.
	if args := app.prodEnvArgs(); len(args) == 0 {
		t.Fatal("prodEnvArgs returned no --env-file args")
	}
	if _, err := app.requirePinnedProductionVersion(); err != nil {
		t.Fatalf("requirePinnedProductionVersion failed on a weak secret: %v", err)
	}
	if _, err := app.GetEnvContent("prod"); err != nil {
		t.Fatalf("GetEnvContent: %v", err)
	}
	if got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET"); got != "ligandx" {
		t.Fatalf("read/stop/pull path rotated INTERNAL_WORKER_SECRET: got %q want ligandx", got)
	}
}
