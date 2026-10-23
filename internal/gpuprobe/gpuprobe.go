// Package gpuprobe decides whether Docker can actually attach the host's NVIDIA
// GPU to a container, so the launcher layers docker-compose.gpu.yml only when
// that will work.
//
// A working `nvidia-smi` on the host says nothing about that. The GPU overlay
// asks for the CDI device nvidia.com/gpu=all, and Docker resolves it against a
// spec file (/etc/cdi/nvidia.yaml) that is generated once and goes stale when the
// driver is upgraded, or is missing when nvidia-container-toolkit is not
// installed. In that state `nvidia-smi` passes, `docker info` still lists the
// device, and every container that requests it fails at create with
// "failed to fulfil mount request". Layering the overlay there turns services
// that are meant to fall back to CPU (md, worker-gpu-short) into services that do
// not start at all. The only reliable test is to ask Docker to start a container
// with the same device request the overlay makes.
package gpuprobe

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// ProbeImage is the throwaway container the probe starts. It is the image the
// runtime compose file already uses for its init containers, so it is on disk
// by the time a stack starts and needs no CUDA, no pull and no extra download.
// Pinned by digest (alpine:3.21) exactly as that file names it; a test keeps it
// in step with the bundled docker-compose.yml.
const ProbeImage = "alpine@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507"

// Device is the CDI device the GPU overlay requests. A test keeps it in step
// with docker-compose.gpu.yml.
const Device = "nvidia.com/gpu=all"

// OverlayFile is the compose overlay this probe gates.
const OverlayFile = "docker-compose.gpu.yml"

// DefaultTimeout bounds one probe. Starting a tiny container takes well under a
// second; the rest is headroom for a cold daemon.
const DefaultTimeout = 20 * time.Second

// Reason says why the GPU is or is not usable.
type Reason string

const (
	ReasonOK                Reason = "ok"
	ReasonNoDriver          Reason = "no_driver"          // nvidia-smi does not run on the host
	ReasonCDIUnusable       Reason = "cdi_unusable"       // Docker cannot resolve/inject the GPU
	ReasonDockerUnavailable Reason = "docker_unavailable" // the daemon cannot be reached
	ReasonProbeTimeout      Reason = "probe_timeout"
	ReasonProbeFailed       Reason = "probe_failed" // the probe container failed for another reason
)

// Result is the outcome of one probe.
type Result struct {
	// Usable is true when the GPU overlay should be layered.
	Usable bool
	// Verified is true when a container was actually started with the GPU. It is
	// false only when the probe image was not on disk, in which case Usable is
	// true (the overlay is applied as it was before probing existed) and the
	// start-failure fallback is the safety net.
	Verified bool
	Reason   Reason
	// Detail is the last line of the failing command's output, for the log.
	Detail string
}

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// Options configures Probe. The zero value probes the real host.
type Options struct {
	GOOS    string
	Run     Runner
	Timeout time.Duration
}

// ExecRunner is the production Runner.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// cdiFailureMarkers are fragments of the errors Docker and the NVIDIA runtime
// print when a requested GPU cannot be attached. They are matched
// case-insensitively against a container's or compose's output.
var cdiFailureMarkers = []string{
	"cdi device injection failed",
	"unresolvable cdi devices",
	"failed to fulfil mount request",
	"could not select device driver",
	"unknown or invalid runtime name: nvidia",
	"nvidia-container-cli",
}

var daemonDownMarkers = []string{
	"cannot connect to the docker daemon",
	"is the docker daemon running",
	"error during connect",
}

func containsAny(haystack string, markers []string) bool {
	lower := strings.ToLower(haystack)
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// IsGPUAttachFailure reports whether output is Docker failing to attach the GPU.
func IsGPUAttachFailure(output string) bool {
	return containsAny(output, cdiFailureMarkers)
}

// Detail picks the line of a failing command's output worth showing a person:
// the one naming the GPU or daemon problem, else the last line that is not
// Docker's boilerplate. Docker ends a failed `run` with "Run 'docker run --help'
// for more information", which is never the reason.
func Detail(output string) string {
	var fallback string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Run 'docker") {
			continue
		}
		if containsAny(line, cdiFailureMarkers) || containsAny(line, daemonDownMarkers) {
			return truncate(line)
		}
		fallback = line
	}
	return truncate(fallback)
}

func truncate(line string) string {
	const max = 300
	if len(line) > max {
		return line[:max] + "..."
	}
	return line
}

// Probe reports whether Docker can attach the GPU. It never returns an error:
// every failure is a Result the caller can act on.
func Probe(ctx context.Context, opts Options) Result {
	run := opts.Run
	if run == nil {
		run = ExecRunner
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if out, err := run(ctx, "nvidia-smi"); err != nil {
		return Result{Reason: ReasonNoDriver, Detail: Detail(out)}
	}

	if out, err := run(ctx, "docker", "image", "inspect", ProbeImage); err != nil {
		switch {
		case containsAny(out, daemonDownMarkers):
			return Result{Reason: ReasonDockerUnavailable, Detail: Detail(out)}
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return Result{Reason: ReasonProbeTimeout}
		default:
			// Not on disk: cannot verify, so keep the pre-probe behaviour.
			return Result{Usable: true, Reason: ReasonOK, Detail: ProbeImage + " not present locally"}
		}
	}

	out, err := run(ctx, "docker", "run", "--rm", "--pull=never", "--device", Device, ProbeImage, "true")
	if err == nil {
		return Result{Usable: true, Verified: true, Reason: ReasonOK}
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Result{Reason: ReasonProbeTimeout}
	case IsGPUAttachFailure(out):
		return Result{Reason: ReasonCDIUnusable, Detail: Detail(out)}
	case containsAny(out, daemonDownMarkers):
		return Result{Reason: ReasonDockerUnavailable, Detail: Detail(out)}
	default:
		return Result{Reason: ReasonProbeFailed, Detail: Detail(out)}
	}
}

// Message explains an unusable result to the person who has to fix it.
// It returns "" for a usable result.
func Message(goos string, r Result) string {
	if r.Usable {
		return ""
	}
	switch r.Reason {
	case ReasonNoDriver:
		return "NVIDIA GPU not available (driver not loaded)."
	case ReasonCDIUnusable:
		return cdiMessage(goos, r.Detail)
	case ReasonDockerUnavailable:
		return "Docker is not reachable, so GPU support could not be checked."
	case ReasonProbeTimeout:
		return "Checking whether Docker can use the GPU timed out."
	default:
		msg := "Docker could not start a test container with the GPU."
		if r.Detail != "" {
			msg += " (" + r.Detail + ")"
		}
		return msg
	}
}

func cdiMessage(goos, detail string) string {
	msg := "An NVIDIA GPU is present but Docker cannot attach it to containers."
	if goos == "linux" {
		msg += " Its GPU (CDI) configuration is missing or out of date, which usually happens after an NVIDIA driver update or when nvidia-container-toolkit is not installed." +
			" Install nvidia-container-toolkit, then run: sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml"
	} else {
		msg += " Update Docker Desktop and your NVIDIA driver, and make sure GPU support is enabled in Docker Desktop."
	}
	if detail != "" {
		msg += " (Docker reported: " + detail + ")"
	}
	return msg
}

// StripComposeFile returns args without the `-f <file>` pair naming file (or
// `--file <file>`), and whether anything was removed. It does not modify args.
func StripComposeFile(args []string, file string) ([]string, bool) {
	out := make([]string, 0, len(args))
	removed := false
	for i := 0; i < len(args); i++ {
		if (args[i] == "-f" || args[i] == "--file") && i+1 < len(args) && args[i+1] == file {
			removed = true
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out, removed
}
