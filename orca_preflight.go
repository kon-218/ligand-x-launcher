package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	orcaContainerPath  = "/opt/orca"
	orcaWorkerUser     = "1001:1001"
	orcaProbeTimeout   = 30 * time.Second
	orcaCommandTimeout = "20s"
)

// relaxOrcaPermissions grants "other" read (and, where the owner already
// marks an entry executable, execute) access under root -- equivalent to
// `chmod -R o+rX`. The QC container always runs as a fixed, unprivileged
// user baked into the image (uid 1001; see orcaWorkerUser) that is a
// stranger to any host filesystem, so a typical `tar xf orca.tar.gz` extract
// (mode 0700, owned by the person who ran it) is unreadable to it. The
// desktop user who selected this folder already owns it and can freely
// widen its own "other" bits -- the operator should never see a Docker/uid
// error and have to fix this by hand.
//
// Never propagates the write bit, and the mount this feeds is always
// read-only (see orcaRuntimeProbeArgs) regardless, so this does not widen
// what the container can do to the install even though it widens who can
// read it. Symlinks are left alone -- their mode is not meaningful and
// os.Chmod on one affects the target, which may lie outside root.
// Best-effort: a failure here does not necessarily mean ORCA is unusable
// (e.g. a file legitimately owned by someone else), so callers should still
// run the real probe afterward rather than treat this as the verification.
func relaxOrcaPermissions(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		want := mode | ((mode & 0o500) >> 6)
		if want == mode {
			return nil
		}
		if chErr := os.Chmod(path, want); chErr != nil {
			return fmt.Errorf("cannot widen permissions on %s: %w", path, chErr)
		}
		return nil
	})
}

// A tiny single-atom calculation proves more than a host-side executable-bit
// check: the Linux loader, bundled ORCA helpers and basis data must all be
// readable by the exact unprivileged identity used by worker-qc. Both the
// process in the container and the docker client are bounded independently.
const orcaProbeScript = `set -eu
printf '%s\n' '! HF STO-3G MiniPrint' '* xyz 0 1' 'He 0 0 0' '*' > ligandx-orca-probe.inp
if ! /usr/bin/timeout ` + orcaCommandTimeout + ` /opt/orca/orca ligandx-orca-probe.inp > ligandx-orca-probe.out 2>&1; then
  cat ligandx-orca-probe.out >&2
  exit 1
fi
if ! grep -q 'ORCA TERMINATED NORMALLY' ligandx-orca-probe.out; then
  cat ligandx-orca-probe.out >&2
  exit 1
fi
`

// pinnedWorkerQCImage returns the exact image Compose will use for worker-qc.
// A mutable Pro tag would make the probe and the subsequent worker launch race
// different artifacts, so fail closed instead of silently probing "latest".
func (a *App) pinnedWorkerQCImage() (string, error) {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return "", fmt.Errorf("cannot read the production image pin: %w", err)
	}
	parsed := parseEnvFile(content)
	version := strings.TrimSpace(parsed["PRO_VERSION"])
	if version == "" {
		version = strings.TrimSpace(parsed["VERSION"])
	}
	if !isPinnedImageVersion(version) {
		return "", fmt.Errorf(
			"PRO_VERSION (or VERSION when PRO_VERSION is unset) must name a pinned release, not %q",
			version,
		)
	}
	prefix := strings.TrimSuffix(strings.TrimSpace(parsed["LIGANDX_PRO_IMAGE_PREFIX"]), "/")
	if prefix == "" {
		prefix = "ghcr.io/kon-218/ligand-x-pro"
	}
	return imageRef(prefix+"/worker-qc", version), nil
}

func orcaRuntimeProbeArgs(image, hostPath string) []string {
	return []string{
		"run",
		"--rm",
		"--pull=never",
		"--network=none",
		"--read-only",
		"--user", orcaWorkerUser,
		"--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", hostPath, orcaContainerPath),
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=16m,uid=1001,gid=1001,mode=0700",
		"--workdir", "/tmp",
		"--entrypoint", "/bin/sh",
		image,
		"-c", orcaProbeScript,
	}
}

func defaultOrcaProbe(ctx context.Context, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// probeOrcaRuntime bind-mounts the operator-supplied install read-only into the
// exact pinned worker image. It never pulls and has no network, so a QC start
// cannot turn this validation into an implicit download or egress path.
func (a *App) probeOrcaRuntime(hostPath string) error {
	image, err := a.pinnedWorkerQCImage()
	if err != nil {
		return fmt.Errorf("cannot validate ORCA for Quantum Chemistry: %w", err)
	}
	run := a.orcaProbeFn
	if run == nil {
		run = defaultOrcaProbe
	}
	ctx, cancel := context.WithTimeout(context.Background(), orcaProbeTimeout)
	defer cancel()
	output, runErr := run(ctx, orcaRuntimeProbeArgs(image, hostPath))
	if runErr == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf(
			"ORCA did not complete its startup check within %s. Verify that this is a complete Linux x86-64 ORCA installation and try again",
			orcaProbeTimeout,
		)
	}
	detail := strings.TrimSpace(string(output))
	lower := strings.ToLower(detail + " " + runErr.Error())
	switch {
	case strings.Contains(lower, "no such image") || strings.Contains(lower, "unable to find image"):
		return fmt.Errorf(
			"the pinned Quantum Chemistry worker image %s is not downloaded. Download the Quantum Chemistry images, then start again",
			image,
		)
	case strings.Contains(lower, "permission denied"):
		return fmt.Errorf(
			"the Quantum Chemistry container cannot execute %s/orca as user %s. Give container users read and execute permission on the extracted ORCA folder and files, then try again",
			hostPath, orcaWorkerUser,
		)
	case strings.Contains(lower, "exec format error"):
		return fmt.Errorf(
			"%s/orca is not a compatible Linux executable. Install the Linux x86-64 ORCA archive; Windows or macOS ORCA binaries cannot run in the QC container",
			hostPath,
		)
	}
	if detail != "" {
		if len(detail) > 1200 {
			detail = detail[len(detail)-1200:]
		}
		return fmt.Errorf(
			"ORCA could not run inside the pinned Quantum Chemistry worker (%s). Verify a complete Linux x86-64 ORCA install with readable/executable files. Probe output: %s",
			image, detail,
		)
	}
	return fmt.Errorf(
		"ORCA could not run inside the pinned Quantum Chemistry worker (%s): %v. Verify Docker is running and the Linux ORCA folder is shared with Docker Desktop",
		image, runErr,
	)
}
