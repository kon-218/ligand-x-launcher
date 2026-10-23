package gpuprobe

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// script maps a command line to its scripted output and error.
type script map[string]struct {
	out string
	err error
}

func (s script) runner(calls *[]string) Runner {
	return func(ctx context.Context, name string, args ...string) (string, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		*calls = append(*calls, line)
		for prefix, r := range s {
			if strings.HasPrefix(line, prefix) {
				return r.out, r.err
			}
		}
		return "", errors.New("unscripted command: " + line)
	}
}

var (
	ok = struct {
		out string
		err error
	}{}
	failure = errors.New("exit status 125")
)

func probeWith(t *testing.T, s script) (Result, []string) {
	t.Helper()
	var calls []string
	return Probe(context.Background(), Options{GOOS: "linux", Run: s.runner(&calls)}), calls
}

func TestProbeUsableWhenDockerStartsAContainerWithTheGPU(t *testing.T) {
	res, calls := probeWith(t, script{
		"nvidia-smi":           ok,
		"docker image inspect": ok,
		"docker run":           ok,
	})
	if !res.Usable || !res.Verified || res.Reason != ReasonOK {
		t.Fatalf("expected verified usable GPU, got %+v", res)
	}
	want := "docker run --rm --pull=never --device nvidia.com/gpu=all " + ProbeImage + " true"
	if calls[len(calls)-1] != want {
		t.Fatalf("probe must make the overlay's own device request\n got %q\nwant %q", calls[len(calls)-1], want)
	}
}

func TestProbeNoDriverNeverTouchesDocker(t *testing.T) {
	res, calls := probeWith(t, script{"nvidia-smi": {err: errors.New("not found")}})
	if res.Usable || res.Reason != ReasonNoDriver {
		t.Fatalf("got %+v", res)
	}
	if len(calls) != 1 {
		t.Fatalf("no driver means no docker probe; calls=%v", calls)
	}
}

// The stale-spec failure this package exists for: the driver works, the device
// request does not. The output below is what Docker printed on the host where
// the overlay first failed.
func TestProbeStaleCDISpecIsUnusable(t *testing.T) {
	res, _ := probeWith(t, script{
		"nvidia-smi":           ok,
		"docker image inspect": ok,
		"docker run": {out: "docker: Error response from daemon: failed to create task for container: " +
			"failed to create shim task: OCI runtime create failed: runc create failed: unable to start " +
			"container process: error during container init: failed to fulfil mount request: open " +
			"/usr/lib/x86_64-linux-gnu/libEGL_nvidia.so.595.84: no such file or directory\n", err: failure},
	})
	if res.Usable || res.Reason != ReasonCDIUnusable {
		t.Fatalf("stale CDI spec must be unusable, got %+v", res)
	}
	if !strings.Contains(res.Detail, "libEGL_nvidia.so.595.84") {
		t.Fatalf("detail should carry Docker's own reason, got %q", res.Detail)
	}
}

// Docker ends a failed `run` with a help hint. The detail must be the reason.
func TestDetailSkipsDockersTrailingHelpLine(t *testing.T) {
	out := "docker: Error response from daemon: CDI device injection failed: unresolvable CDI devices nvidia.com/gpu=7\n\nRun 'docker run --help' for more information\n"
	if got := Detail(out); !strings.Contains(got, "unresolvable CDI devices") {
		t.Fatalf("got %q", got)
	}
	if got := Detail("first\nsecond\nRun 'docker run --help' for more information"); got != "second" {
		t.Fatalf("fallback should be the last real line, got %q", got)
	}
	if got := Detail(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := Detail(strings.Repeat("x", 500)); len(got) != 303 {
		t.Fatalf("long lines must be truncated, len=%d", len(got))
	}
}

func TestProbeUnresolvableDevice(t *testing.T) {
	res, _ := probeWith(t, script{
		"nvidia-smi":           ok,
		"docker image inspect": ok,
		"docker run":           {out: "docker: Error response from daemon: CDI device injection failed: unresolvable CDI devices nvidia.com/gpu=all\n", err: failure},
	})
	if res.Usable || res.Reason != ReasonCDIUnusable {
		t.Fatalf("got %+v", res)
	}
}

func TestProbeMissingImageIsUnverifiedButKeepsTheOverlay(t *testing.T) {
	res, calls := probeWith(t, script{
		"nvidia-smi":           ok,
		"docker image inspect": {out: "Error response from daemon: No such image: " + ProbeImage + "\n", err: failure},
	})
	if !res.Usable || res.Verified {
		t.Fatalf("missing probe image cannot prove or disprove the GPU: %+v", res)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "docker run") {
			t.Fatalf("must not attempt a run (it would pull) when the image is absent: %v", calls)
		}
	}
}

func TestProbeDaemonDown(t *testing.T) {
	res, _ := probeWith(t, script{
		"nvidia-smi":           ok,
		"docker image inspect": {out: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n", err: failure},
	})
	if res.Usable || res.Reason != ReasonDockerUnavailable {
		t.Fatalf("got %+v", res)
	}
}

func TestProbeOtherContainerFailureIsNotTreatedAsUsable(t *testing.T) {
	res, _ := probeWith(t, script{
		"nvidia-smi":           ok,
		"docker image inspect": ok,
		"docker run":           {out: "docker: something unexpected\n", err: failure},
	})
	if res.Usable || res.Reason != ReasonProbeFailed || res.Detail != "docker: something unexpected" {
		t.Fatalf("got %+v", res)
	}
}

func TestProbeTimeout(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "docker" && args[0] == "run" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "", nil
	}
	res := Probe(context.Background(), Options{Run: run, Timeout: 30 * time.Millisecond})
	if res.Usable || res.Reason != ReasonProbeTimeout {
		t.Fatalf("got %+v", res)
	}
}

func TestMessageIsActionablePerPlatform(t *testing.T) {
	cdi := Result{Reason: ReasonCDIUnusable, Detail: "failed to fulfil mount request"}
	linux := Message("linux", cdi)
	for _, want := range []string{"nvidia-container-toolkit", "sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml", "failed to fulfil mount request"} {
		if !strings.Contains(linux, want) {
			t.Errorf("linux message missing %q: %s", want, linux)
		}
	}
	if win := Message("windows", cdi); strings.Contains(win, "nvidia-ctk") || !strings.Contains(win, "Docker Desktop") {
		t.Errorf("non-linux hosts must not be told to run a Linux-only command: %s", win)
	}
	if got := Message("linux", Result{Usable: true}); got != "" {
		t.Errorf("a usable GPU has nothing to explain, got %q", got)
	}
	if !strings.Contains(Message("linux", Result{Reason: ReasonNoDriver}), "driver not loaded") {
		t.Error("no-driver wording changed")
	}
}

func TestIsGPUAttachFailure(t *testing.T) {
	for _, s := range []string{
		"Error response from daemon: CDI device injection failed: unresolvable CDI devices nvidia.com/gpu=all",
		"OCI runtime create failed: ... failed to fulfil mount request: open /usr/lib/x/libcuda.so.1",
		"could not select device driver \"nvidia\" with capabilities: [[gpu]]",
		"docker: Error response from daemon: unknown or invalid runtime name: nvidia",
	} {
		if !IsGPUAttachFailure(s) {
			t.Errorf("should classify as GPU attach failure: %s", s)
		}
	}
	for _, s := range []string{
		"range of CPUs is from 0.01 to 8.00, as there are only 8 CPUs available",
		"pull access denied for ghcr.io/kon-218/ligand-x-pro/admet",
		"",
	} {
		if IsGPUAttachFailure(s) {
			t.Errorf("unrelated failure misclassified: %q", s)
		}
	}
}

func TestStripComposeFile(t *testing.T) {
	in := []string{"compose", "-f", "docker-compose.yml", "-f", "docker-compose.gpu.yml", "up", "-d", "md"}
	got, removed := StripComposeFile(in, OverlayFile)
	want := []string{"compose", "-f", "docker-compose.yml", "up", "-d", "md"}
	if !removed || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v removed=%v", got, removed)
	}
	if in[4] != "docker-compose.gpu.yml" {
		t.Fatal("input slice was modified")
	}
	if _, removed := StripComposeFile(want, OverlayFile); removed {
		t.Fatal("nothing to strip")
	}
	// The overlay name must only be matched as the value of -f, never as a service.
	if got, removed := StripComposeFile([]string{"up", "docker-compose.gpu.yml"}, OverlayFile); removed || len(got) != 2 {
		t.Fatalf("stripped a non -f argument: %v", got)
	}
}

// The probe is only meaningful if it asks for what the overlay asks for and
// starts an image the runtime already carries. These read the bundled files.
func TestConstantsMatchBundledComposeFiles(t *testing.T) {
	overlay, err := os.ReadFile("../../runtime/" + OverlayFile)
	if err != nil {
		t.Fatal(err)
	}
	// The overlay is generated; the device may be written with or without quotes.
	if !strings.Contains(string(overlay), "- "+Device) && !strings.Contains(string(overlay), `"`+Device+`"`) {
		t.Fatalf("%s no longer requests %s; update gpuprobe.Device", OverlayFile, Device)
	}
	base, err := os.ReadFile("../../runtime/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(base), "image: "+ProbeImage) {
		t.Fatalf("docker-compose.yml no longer uses %s for its init containers; the probe image would not be on disk", ProbeImage)
	}
}

// TestLiveHost exercises the real nvidia-smi and Docker daemon. It is skipped
// unless LIGANDX_LIVE_GPU=1, because it needs an NVIDIA host with Docker and the
// probe image on disk:
//
//	LIGANDX_LIVE_GPU=1 go test ./internal/gpuprobe -run TestLiveHost -v
func TestLiveHost(t *testing.T) {
	if os.Getenv("LIGANDX_LIVE_GPU") != "1" {
		t.Skip("set LIGANDX_LIVE_GPU=1 on an NVIDIA host with Docker to run")
	}
	res := Probe(context.Background(), Options{GOOS: "linux"})
	t.Logf("probe: %+v", res)
	if !res.Usable || !res.Verified {
		t.Fatalf("this host was expected to have a usable, verified GPU: %+v", res)
	}

	// A real Docker refusal, not a hand-written one: ask for a device that cannot
	// exist and check the classifier recognises what the daemon prints.
	out, err := ExecRunner(context.Background(), "docker", "run", "--rm", "--pull=never",
		"--device", "nvidia.com/gpu=does-not-exist", ProbeImage, "true")
	if err == nil {
		t.Fatal("docker unexpectedly accepted a nonexistent CDI device")
	}
	t.Logf("docker said: %s", Detail(out))
	if !IsGPUAttachFailure(out) {
		t.Fatalf("real Docker output not classified as a GPU attach failure: %q", out)
	}
}
