package launcher

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strings"
	"time"

	"ligandx-launcher/internal/gpuprobe"
)

// gpuProbeTTL is how long one probe result is reused. A single start builds its
// compose arguments in several places, and starting a container per call would
// cost seconds; a new start invalidates the result (invalidateGPUProbe) so a
// driver or CDI fix takes effect on the next click.
const gpuProbeTTL = 30 * time.Second

// gpuStatus reports whether Docker can attach the host GPU, probing at most once
// per gpuProbeTTL. See package gpuprobe for why nvidia-smi alone is not enough.
func (a *App) gpuStatus() gpuprobe.Result {
	a.gpuMu.Lock()
	defer a.gpuMu.Unlock()
	if !a.gpuAt.IsZero() && time.Since(a.gpuAt) < gpuProbeTTL {
		return a.gpuRes
	}
	var res gpuprobe.Result
	if a.gpuProbeFn != nil {
		res = a.gpuProbeFn()
	} else {
		res = gpuprobe.Probe(context.Background(), gpuprobe.Options{GOOS: goruntime.GOOS})
	}
	a.gpuRes, a.gpuAt = res, time.Now()
	a.noticeGPUStatusLocked(res)
	return res
}

// invalidateGPUProbe discards the cached result so the next gpuStatus re-probes.
func (a *App) invalidateGPUProbe() {
	a.gpuMu.Lock()
	a.gpuAt = time.Time{}
	a.gpuMu.Unlock()
}

// noteGPUAttachFailure records that a real `compose up` failed to attach the
// GPU, so later argument builds in this session stop layering the overlay
// instead of failing the same way again.
func (a *App) noteGPUAttachFailure(detail string) {
	a.gpuMu.Lock()
	a.gpuRes = gpuprobe.Result{Reason: gpuprobe.ReasonCDIUnusable, Detail: detail}
	a.gpuAt = time.Now()
	a.gpuMu.Unlock()
}

// noticeGPUStatusLocked tells the user, once per distinct state, when the GPU
// they have will not be used. Callers hold gpuMu.
func (a *App) noticeGPUStatusLocked(res gpuprobe.Result) {
	var msg string
	switch {
	case res.Reason == gpuprobe.ReasonNoDriver:
		return // no GPU: the ordinary CPU path, nothing to report
	case !res.Usable:
		msg = gpuprobe.Message(goruntime.GOOS, res) +
			" Molecular Dynamics and other Core GPU services will run on the CPU (slower)."
	case !res.Verified:
		msg = "GPU support could not be verified (" + res.Detail + "); starting with GPU support enabled."
	default:
		return
	}
	if msg == a.gpuNotice {
		return
	}
	a.gpuNotice = msg
	a.emitAndLog("launcher", msg)
}

// namesGPURequiredService reports whether a compose invocation explicitly names
// a service that cannot run without the GPU.
func namesGPURequiredService(args []string) bool {
	for _, arg := range args {
		if gpuRequiredRuntime[arg] {
			return true
		}
	}
	return false
}

// namesAnyLigandxService reports whether a compose invocation is scoped to
// explicit services rather than "everything in the file".
func namesAnyLigandxService(args []string) bool {
	for _, arg := range args {
		if ligandxServiceSet[arg] {
			return true
		}
	}
	return false
}

// runDockerComposeDirect executes one Compose command without the lease
// activation wrapper. The wrapper uses this for the explicit gateway-stop,
// worker-health, and gateway-enable phases without recursively restarting the
// sequence.
//
// If the command failed because Docker could not attach the GPU (a stale or
// missing CDI spec that the pre-start probe could not see) and it only starts
// services that have a CPU path, it is retried once without the GPU overlay, so
// the user gets a running stack and an explanation instead of a half-created
// one. A command that names a GPU-only service is never retried: starting those
// on the CPU would look like success and then fail every job.
func (a *App) runDockerComposeDirect(args []string, message string) error {
	once := a.runDockerComposeOnce
	if a.composeOnceFn != nil {
		once = a.composeOnceFn
	}
	err := once(args, message)
	if err == nil || !gpuprobe.IsGPUAttachFailure(err.Error()) {
		return err
	}
	stripped, hadOverlay := gpuprobe.StripComposeFile(args, gpuprobe.OverlayFile)
	if !hadOverlay || !namesAnyLigandxService(stripped) || namesGPURequiredService(stripped) {
		return fmt.Errorf("%w\n%s", err, gpuprobe.Message(goruntime.GOOS, gpuprobe.Result{
			Reason: gpuprobe.ReasonCDIUnusable, Detail: gpuprobeDetail(err.Error()),
		}))
	}
	detail := gpuprobeDetail(err.Error())
	a.noteGPUAttachFailure(detail)
	a.emitAndLog("launcher", gpuprobe.Message(goruntime.GOOS, gpuprobe.Result{
		Reason: gpuprobe.ReasonCDIUnusable, Detail: detail,
	})+" Retrying without GPU support; Molecular Dynamics will run on the CPU (slower).")
	return once(stripped, "Retrying without GPU support...")
}

// gpuprobeDetail is the line of a compose failure that names the GPU problem.
func gpuprobeDetail(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if gpuprobe.IsGPUAttachFailure(line) {
			return gpuprobe.Detail(line)
		}
	}
	return ""
}
