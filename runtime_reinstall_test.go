package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
	got := parseEnvFile(content)["WORKER_KINETICS_CPU_LIMIT"]
	if got != "6" {
		t.Errorf("WORKER_KINETICS_CPU_LIMIT = %q, want %q (floor(8 * 0.75))", got, "6")
	}
}
