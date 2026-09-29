package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ligandx-launcher/internal/gpuprobe"
)

func gpuTestApp(t *testing.T, res gpuprobe.Result, withOverlay bool) (*App, *int) {
	t.Helper()
	dir := t.TempDir()
	if withOverlay {
		if err := os.WriteFile(filepath.Join(dir, gpuprobe.OverlayFile), []byte("services: {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probes := 0
	app := NewApp()
	app.projectPath = dir
	app.gpuProbeFn = func() gpuprobe.Result { probes++; return res }
	return app, &probes
}

var (
	gpuOK        = gpuprobe.Result{Usable: true, Verified: true, Reason: gpuprobe.ReasonOK}
	gpuStaleCDI  = gpuprobe.Result{Reason: gpuprobe.ReasonCDIUnusable, Detail: "failed to fulfil mount request: open libEGL_nvidia.so.595.84"}
	gpuNoDriver  = gpuprobe.Result{Reason: gpuprobe.ReasonNoDriver}
	cdiComposeEr = errors.New("docker compose failed: exit status 1\n--- last docker output ---\n" +
		"Error response from daemon: failed to create task for container: failed to fulfil mount request: open /usr/lib/x86_64-linux-gnu/libEGL_nvidia.so.595.84: no such file or directory")
)

func TestGPUOverlayLayeredOnlyWhenDockerCanUseTheGPU(t *testing.T) {
	overlay := []string{"-f", gpuprobe.OverlayFile}
	cases := []struct {
		name string
		res  gpuprobe.Result
		file bool
		want []string
	}{
		{"usable", gpuOK, true, overlay},
		{"stale CDI spec: driver works, Docker cannot attach", gpuStaleCDI, true, nil},
		{"no driver", gpuNoDriver, true, nil},
		{"usable but overlay missing from the runtime", gpuOK, false, nil},
	}
	for _, tc := range cases {
		app, _ := gpuTestApp(t, tc.res, tc.file)
		if got := app.gpuComposeArgs(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestGPUProbeIsCachedWithinAStartAndRefreshedByTheNext(t *testing.T) {
	app, probes := gpuTestApp(t, gpuOK, true)
	for i := 0; i < 5; i++ {
		app.gpuComposeArgs()
	}
	if *probes != 1 {
		t.Fatalf("one start builds args several times but must probe once, probed %d", *probes)
	}
	app.invalidateGPUProbe()
	app.gpuComposeArgs()
	if *probes != 2 {
		t.Fatalf("a new start must re-probe, probed %d", *probes)
	}
}

func TestGPUOnlyServicesAreBlockedWhenDockerCannotAttachTheGPU(t *testing.T) {
	app, _ := gpuTestApp(t, gpuStaleCDI, true)
	err := app.checkGPUForServices([]string{"gateway", "abfe", "boltz2"})
	if err == nil {
		t.Fatal("Pro GPU services must be refused before compose starts a half-built stack")
	}
	for _, want := range []string{"Docker cannot attach", "abfe, boltz2", "libEGL_nvidia.so.595.84", "Molecular Dynamics runs on CPU"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestCoreGPUServicesStillStartOnTheCPUWhenTheGPUIsUnusable(t *testing.T) {
	for _, res := range []gpuprobe.Result{gpuStaleCDI, gpuNoDriver} {
		app, _ := gpuTestApp(t, res, true)
		if err := app.checkGPUForServices([]string{"gateway", "md", "worker-gpu-short"}); err != nil {
			t.Errorf("md and worker-gpu-short have a CPU path and must not be blocked (%s): %v", res.Reason, err)
		}
		if args := app.gpuComposeArgs(); args != nil {
			t.Errorf("overlay must not be layered (%s): %v", res.Reason, args)
		}
	}
}

func TestGPUServicesAllowedWhenUsable(t *testing.T) {
	app, _ := gpuTestApp(t, gpuOK, true)
	if err := app.checkGPUForServices([]string{"abfe", "rbfe", "boltz2", "worker-gpu-long"}); err != nil {
		t.Fatal(err)
	}
}

func TestNoDriverKeepsItsOriginalMessage(t *testing.T) {
	app, _ := gpuTestApp(t, gpuNoDriver, true)
	err := app.checkGPUForServices([]string{"rbfe"})
	if err == nil || !strings.Contains(err.Error(), "driver not loaded") {
		t.Fatalf("got %v", err)
	}
}

func composeArgs(services ...string) []string {
	return append([]string{"compose", "-f", "docker-compose.yml", "-f", gpuprobe.OverlayFile, "up", "-d", "--pull=never"}, services...)
}

func TestComposeFailureToAttachGPURetriesOnceWithoutOverlayForCoreServices(t *testing.T) {
	app, _ := gpuTestApp(t, gpuOK, true)
	var calls [][]string
	app.composeOnceFn = func(args []string, _ string) error {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return cdiComposeEr
		}
		return nil
	}
	if err := app.runDockerComposeDirect(composeArgs("gateway", "md", "worker-gpu-short"), "start"); err != nil {
		t.Fatalf("the retry succeeded, so the start must: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected one retry, got %d calls", len(calls))
	}
	if slicesContain(calls[1], gpuprobe.OverlayFile) {
		t.Fatalf("retry must drop the overlay: %v", calls[1])
	}
	if !slicesContain(calls[1], "md") {
		t.Fatalf("retry must keep the services: %v", calls[1])
	}
	// The failure is remembered so later argument builds do not repeat it.
	if got := app.gpuComposeArgs(); got != nil {
		t.Fatalf("overlay must stay off after a real attach failure, got %v", got)
	}
}

func TestGPUOnlyServiceFailureIsNeverRetriedOnTheCPU(t *testing.T) {
	app, _ := gpuTestApp(t, gpuOK, true)
	calls := 0
	app.composeOnceFn = func([]string, string) error { calls++; return cdiComposeEr }
	err := app.runDockerComposeDirect(composeArgs("gateway", "rbfe"), "start")
	if err == nil || calls != 1 {
		t.Fatalf("a GPU-only service must fail loudly, not start on the CPU: err=%v calls=%d", err, calls)
	}
	if !strings.Contains(err.Error(), "Docker cannot attach") {
		t.Fatalf("the failure must be explained: %v", err)
	}
}

func TestUnscopedComposeUpIsNotRetriedBecauseItMayContainGPUOnlyServices(t *testing.T) {
	app, _ := gpuTestApp(t, gpuOK, true)
	calls := 0
	app.composeOnceFn = func([]string, string) error { calls++; return cdiComposeEr }
	if err := app.runDockerComposeDirect(composeArgs(), "start"); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestUnrelatedComposeFailureIsPassedThroughUntouched(t *testing.T) {
	app, _ := gpuTestApp(t, gpuOK, true)
	calls := 0
	boom := errors.New("docker compose failed: pull access denied for ghcr.io/x")
	app.composeOnceFn = func([]string, string) error { calls++; return boom }
	if err := app.runDockerComposeDirect(composeArgs("md"), "start"); err != boom || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestComposeWithoutOverlayIsNotRetried(t *testing.T) {
	app, _ := gpuTestApp(t, gpuOK, true)
	calls := 0
	app.composeOnceFn = func([]string, string) error { calls++; return cdiComposeEr }
	args := []string{"compose", "up", "-d", "md"}
	if err := app.runDockerComposeDirect(args, "start"); err == nil || calls != 1 {
		t.Fatalf("nothing to strip, so nothing to retry: err=%v calls=%d", err, calls)
	}
}

func slicesContain(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
