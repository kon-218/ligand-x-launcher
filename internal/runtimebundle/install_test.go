package runtimebundle

import (
	"archive/zip"
	"bytes"
	"fmt"
	"ligandx-launcher/internal/envfile"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

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
	if err := Extract(zipPath, dest); err != nil {
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

	if err := Extract(zipPath, dest); err != nil {
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

func TestRuntimeRollbackPolicyRejectsOlderVersion(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := envfile.WritePrivate(filepath.Join(runtimeDir, ".ligandx-runtime-version"), []byte("v2.1.0\n")); err != nil {
		t.Fatal(err)
	}
	if err := EnforceRollbackPolicy(runtimeDir, "v2.0.9"); err == nil {
		t.Fatal("runtime downgrade was accepted")
	}
	if err := EnforceRollbackPolicy(runtimeDir, "v2.1.1"); err != nil {
		t.Fatalf("runtime upgrade was rejected: %v", err)
	}
}

func TestRuntimeStageActivationCanRestorePreviousFiles(t *testing.T) {
	stage, destination, backup := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "docker-compose.yml"), []byte("new compose"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, ".env.production.template"), []byte("VERSION=v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "docker-compose.yml"), []byte("old compose"), 0644); err != nil {
		t.Fatal(err)
	}
	rollback, err := ActivateStage(stage, destination, backup)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(destination, "docker-compose.yml")); string(data) != "new compose" {
		t.Fatalf("stage was not activated: %q", data)
	}
	rollback()
	if data, _ := os.ReadFile(filepath.Join(destination, "docker-compose.yml")); string(data) != "old compose" {
		t.Fatalf("previous runtime was not restored: %q", data)
	}
	if _, err := os.Stat(filepath.Join(destination, ".env.production.template")); !os.IsNotExist(err) {
		t.Fatal("new-only staged file survived rollback")
	}
}

func TestRuntimeDownloadRejectsUnapprovedHost(t *testing.T) {
	if _, err := (Policy{}).ApprovedDownloadURL("https://attacker.example/runtime.zip"); err == nil {
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
	for index := 0; index < MaxFiles+1; index++ {
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
	if err := Extract(zipPath, t.TempDir()); err == nil {
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
	if err := Extract(zipPath, t.TempDir()); err == nil {
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
	if err := rejectSymlinkPath(base, target); err == nil {
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
			if got := ShouldAdvanceVersion(tc.current, tc.release); got != tc.want {
				t.Errorf("shouldAdvanceVersion(%q, %q) = %v, want %v — %s",
					tc.current, tc.release, got, tc.want, tc.why)
			}
		})
	}
}
