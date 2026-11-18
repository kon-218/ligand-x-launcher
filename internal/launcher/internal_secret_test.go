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

// TestEnsureProductionEnvRotatesWeakInternalSecret is the core P1-F regression:
// a hand-set weak value that is NOT a CHANGE_ME placeholder must be randomized.
func TestEnsureProductionEnvRotatesWeakInternalSecret(t *testing.T) {
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME_GENERATE_RANDOM_KEY_HERE\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=ligandx\n"
	app := secretTestApp(t, template, existing)
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv: %v", err)
	}
	got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET")
	if got == "ligandx" {
		t.Fatal("weak hand-set INTERNAL_WORKER_SECRET persisted (not rotated)")
	}
	if !internalSecretMeetsFloor(got) {
		t.Fatalf("rotated INTERNAL_WORKER_SECRET does not meet floor: %q", got)
	}
}

// TestEnsureProductionEnvPreservesStrongInternalSecret guards idempotency and the
// upgrade-preservation invariant: a strong value (generated or hand-set) is never
// rotated on repeat starts, so containers stay in sync across an upgrade.
func TestEnsureProductionEnvPreservesStrongInternalSecret(t *testing.T) {
	strong, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	template := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nINTERNAL_WORKER_SECRET=" + strong + "\n"
	app := secretTestApp(t, template, existing)
	for i := 0; i < 2; i++ {
		if err := app.ensureProductionEnv(); err != nil {
			t.Fatalf("ensureProductionEnv pass %d: %v", i, err)
		}
		if got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET"); got != strong {
			t.Fatalf("pass %d rotated a strong secret: got %q want %q", i, got, strong)
		}
	}
}

// TestEnsureProductionEnvGeneratesInternalSecretFromPlaceholder covers the
// fresh-install path: the template marker is replaced with a strong key.
func TestEnsureProductionEnvGeneratesInternalSecretFromPlaceholder(t *testing.T) {
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

// TestEnsureProductionEnvDoesNotRotateWeakVolumePassword documents the deliberate
// scope boundary: rotating a live Postgres/RabbitMQ/Redis password would desync
// the data volume initialised with it, so weak-secret hardening applies only to
// the volume-free INTERNAL_WORKER_SECRET.
func TestEnsureProductionEnvDoesNotRotateWeakVolumePassword(t *testing.T) {
	template := "VERSION=v2026.06.21\nPOSTGRES_PASSWORD=CHANGE_ME\nINTERNAL_WORKER_SECRET=CHANGE_ME\n"
	existing := "VERSION=v2026.06.21\nPOSTGRES_PASSWORD=ligandx\nINTERNAL_WORKER_SECRET=ligandx\n"
	app := secretTestApp(t, template, existing)
	if err := app.ensureProductionEnv(); err != nil {
		t.Fatalf("ensureProductionEnv: %v", err)
	}
	if got := readProdSecret(t, app, "POSTGRES_PASSWORD"); got != "ligandx" {
		t.Fatalf("volume-bound POSTGRES_PASSWORD was rotated: %q", got)
	}
	got := readProdSecret(t, app, "INTERNAL_WORKER_SECRET")
	if got == "ligandx" || !internalSecretMeetsFloor(got) {
		t.Fatalf("INTERNAL_WORKER_SECRET not hardened: %q", got)
	}
}
