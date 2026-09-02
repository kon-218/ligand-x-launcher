package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeOrcaProbeEnv(t *testing.T, dir, hostPath, version, proVersion, prefix string) {
	t.Helper()
	content := strings.Join([]string{
		"ORCA_HOST_PATH=" + hostPath,
		"VERSION=" + version,
		"PRO_VERSION=" + proVersion,
		"LIGANDX_PRO_IMAGE_PREFIX=" + prefix,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env.production"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOrcaHostPathRequiresLinuxBinaryName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orca.exe"), []byte("windows"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateOrcaHostPath(dir); err == nil || !strings.Contains(err.Error(), "named orca") {
		t.Fatalf("orca.exe must not satisfy the Linux-container preflight: %v", err)
	}
}

func TestPinnedWorkerQCImageUsesIndependentProPin(t *testing.T) {
	dir := t.TempDir()
	writeOrcaProbeEnv(t, dir, "/orca", "v-core", "v-pro", "registry.example/ligandx-pro/")
	app := NewApp()
	app.projectPath = dir
	got, err := app.pinnedWorkerQCImage()
	if err != nil {
		t.Fatal(err)
	}
	if want := "registry.example/ligandx-pro/worker-qc:v-pro"; got != want {
		t.Fatalf("pinnedWorkerQCImage() = %q, want %q", got, want)
	}
}

func TestPinnedWorkerQCImageRejectsMutableTag(t *testing.T) {
	dir := t.TempDir()
	writeOrcaProbeEnv(t, dir, "/orca", "v-core", "latest", "")
	app := NewApp()
	app.projectPath = dir
	if _, err := app.pinnedWorkerQCImage(); err == nil || !strings.Contains(err.Error(), "pinned release") {
		t.Fatalf("expected mutable Pro tag to fail closed, got %v", err)
	}
}

func TestOrcaRuntimeProbeArgsAreIsolatedAndBounded(t *testing.T) {
	args := orcaRuntimeProbeArgs("registry.example/worker-qc:v1", "/licensed/orca")
	for _, want := range []string{
		"run", "--rm", "--pull=never", "--network=none", "--read-only",
		"--user", orcaWorkerUser, "--tmpfs", "--entrypoint", "/bin/sh",
		"registry.example/worker-qc:v1", "-c",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("probe args missing %q: %v", want, args)
		}
	}
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "type=bind,source=/licensed/orca,target=/opt/orca,readonly") {
		t.Errorf("probe must use a read-only ORCA bind: %v", args)
	}
	if !strings.Contains(joined, "/usr/bin/timeout "+orcaCommandTimeout+" /opt/orca/orca") {
		t.Errorf("probe must execute ORCA with an in-container bound: %v", args)
	}
	if !strings.Contains(joined, "ORCA TERMINATED NORMALLY") {
		t.Errorf("probe must verify an actual successful calculation: %v", args)
	}
}

func TestCheckOrcaForServicesRunsProbeForQC(t *testing.T) {
	dir := t.TempDir()
	orcaDir := filepath.Join(dir, "orca")
	if err := os.Mkdir(orcaDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeOrcaInstall(t, orcaDir)
	writeOrcaProbeEnv(t, dir, orcaDir, "v-core", "v-pro", "registry.example/pro")

	app := NewApp()
	app.projectPath = dir
	called := 0
	app.orcaProbeFn = func(ctx context.Context, args []string) ([]byte, error) {
		called++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > orcaProbeTimeout || time.Until(deadline) < 20*time.Second {
			t.Errorf("probe context does not carry the expected bound: %v, %v", deadline, ok)
		}
		if !slices.Contains(args, "registry.example/pro/worker-qc:v-pro") {
			t.Errorf("probe did not use exact Pro worker pin: %v", args)
		}
		return nil, nil
	}
	if err := app.checkOrcaForServices([]string{"worker-qc"}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("probe calls = %d, want 1", called)
	}
}

func TestProbeOrcaRuntimeExplainsPermissionFailure(t *testing.T) {
	dir := t.TempDir()
	writeOrcaProbeEnv(t, dir, "/licensed/orca", "v-core", "v-pro", "")
	app := NewApp()
	app.projectPath = dir
	app.orcaProbeFn = func(context.Context, []string) ([]byte, error) {
		return []byte("/bin/sh: /opt/orca/orca: Permission denied"), errors.New("exit status 126")
	}
	err := app.probeOrcaRuntime("/licensed/orca")
	if err == nil || !strings.Contains(err.Error(), "1001:1001") || !strings.Contains(err.Error(), "read and execute") {
		t.Fatalf("permission failure is not actionable: %v", err)
	}
}

func TestProbeOrcaRuntimeExplainsMissingPinnedImage(t *testing.T) {
	dir := t.TempDir()
	writeOrcaProbeEnv(t, dir, "/licensed/orca", "v-core", "v-pro", "")
	app := NewApp()
	app.projectPath = dir
	app.orcaProbeFn = func(context.Context, []string) ([]byte, error) {
		return []byte("docker: Error response from daemon: No such image"), errors.New("exit status 125")
	}
	err := app.probeOrcaRuntime("/licensed/orca")
	if err == nil || !strings.Contains(err.Error(), "worker-qc:v-pro") || !strings.Contains(err.Error(), "Download") {
		t.Fatalf("missing-image failure is not actionable: %v", err)
	}
}

// TestRelaxOrcaPermissionsWidensOtherToMatchOwner is the regression case for
// orca-validated-as-desktop-user: a normal `tar xf orca.tar.gz` extract is
// 0700, unreadable by the container's fixed unprivileged runtime user (1001)
// -- and the operator should never have to fix that with their own chmod.
func TestRelaxOrcaPermissionsWidensOtherToMatchOwner(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "basis")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "orca")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(sub, "def2-SVP.gbw")
	if err := os.WriteFile(data, []byte("basis data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := relaxOrcaPermissions(root); err != nil {
		t.Fatalf("relaxOrcaPermissions: %v", err)
	}

	cases := []struct {
		path string
		want os.FileMode
	}{
		{root, 0o705}, // directory, owner had x -> other gets rx
		{sub, 0o705},
		{bin, 0o705},  // owner had x -> other gets rx, not just r
		{data, 0o604}, // owner had no x -> other gets r only, no execute
	}
	for _, c := range cases {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != c.want {
			t.Errorf("%s: mode = %o, want %o", c.path, got, c.want)
		}
	}
}

func TestRelaxOrcaPermissionsLeavesSymlinksAlone(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside-root")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "orca")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := relaxOrcaPermissions(root); err != nil {
		t.Fatalf("relaxOrcaPermissions: %v", err)
	}

	info, err := os.Stat(target) // follows the symlink
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("symlink target mode changed: %o, want unchanged 0600", got)
	}
}
