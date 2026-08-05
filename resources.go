package main

import (
	"context"
	"fmt"
	"math"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// hostResources are the ceilings a container's declared limits are measured
// against. CPUs is authoritative: the Docker daemon rejects container creation
// outright when `cpus` exceeds it ("range of CPUs is from 0.01 to 8.00"), which
// is a hard start failure, not a slow container. MemBytes is advisory — Docker
// accepts an oversized memory limit and simply OOM-kills later — so it is only
// used to keep the declared limits honest, and 0 means "unknown, leave alone".
//
// Both values come from the *daemon*, not the host process: on Docker Desktop
// the containers run inside a VM with its own CPU/RAM allocation, and the VM is
// what performs the validation.
type hostResources struct {
	CPUs     int
	MemBytes int64
	// DockerRootDir is the daemon's image store path. Empty when unknown; on
	// Docker Desktop it names a path inside the VM that does not exist here.
	DockerRootDir string
}

// detectHostResources asks the Docker daemon what it has available, falling
// back to this process's view of the machine when the daemon is unreachable
// (fresh install, Docker not started yet). The fallback can over-estimate on
// Docker Desktop, but a wrong-but-plausible ceiling still beats the template's
// hard-coded 16-core assumption.
// Cached across calls because prodEnvArgs() re-runs the fitting on every single
// compose invocation (up, down, ps), and a machine does not grow cores
// mid-session. Only a successful daemon answer is cached: the fallback may be
// taken merely because Docker had not started yet, and it should not stick once
// the daemon can be asked properly.
func (a *App) detectHostResources() hostResources {
	a.hostResMux.Lock()
	defer a.hostResMux.Unlock()
	if a.hostResFromDaemon {
		return a.hostRes
	}
	if a.dockerClient == nil {
		a.initDockerClient()
	}
	if a.dockerClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if res, err := a.dockerClient.Info(ctx, client.InfoOptions{}); err == nil && res.Info.NCPU > 0 {
			a.hostRes = hostResources{CPUs: res.Info.NCPU, MemBytes: res.Info.MemTotal, DockerRootDir: res.Info.DockerRootDir}
			a.hostResFromDaemon = true
			return a.hostRes
		}
	}
	_, memTotal, _ := readHostMemory() // Linux-only; 0 elsewhere
	return hostResources{CPUs: goruntime.NumCPU(), MemBytes: int64(memTotal)}
}

// fittedCPUsKey records the CPU count the file was last fitted to. It marks the
// difference between "first fit on this hardware" (where the values are still
// template defaults, so pool sizes may be re-tuned) and "already fitted" (where
// a pool size is the user's own choice from the settings panel).
const fittedCPUsKey = "LIGANDX_FITTED_CPUS"

// concurrencyKeys are Celery worker pool sizes that are not a boot failure when
// oversized, but do thrash a small machine (4 parallel Vina jobs on 8 threads is
// slower than 2). Each is capped at cores/divisor and only ever lowered, so a
// large machine keeps the template's tuned value.
//
// Unlike the CPU/memory ceilings these are only applied on the first fit: they
// are exposed in the launcher's settings panel (SaveUserSettings), and silently
// undoing a deliberate setting on every start would be worse than a slow queue.
var concurrencyKeys = map[string]int{
	"CPU_WORKER_CONCURRENCY": 4,
	"QC_WORKER_CONCURRENCY":  4,
	"GPU_SHORT_CONCURRENCY":  4,
}

// fitResourceEnv clamps oversized resource declarations in cur to what host can
// actually satisfy. It returns only the keys whose value changed, plus
// human-readable "KEY 16->8" notes for the log.
//
// It never raises a value: a user who deliberately throttled a service keeps
// their setting, and a machine that is big enough gets no changes at all (so
// repeat calls on every start are idempotent no-ops).
//
// initial is true the first time this hardware is seen, which additionally
// allows worker pool sizes to be re-tuned (see concurrencyKeys).
func fitResourceEnv(cur map[string]string, host hostResources, initial bool) (map[string]string, []string) {
	if host.CPUs <= 0 {
		return nil, nil
	}
	updates := map[string]string{}
	var notes []string

	record := func(key, oldVal, newVal string) {
		updates[key] = newVal
		notes = append(notes, fmt.Sprintf("%s %s->%s", key, oldVal, newVal))
	}

	// --- CPU limits: the ceiling the daemon enforces, per container. ---
	cpuCeiling := float64(host.CPUs)
	// Effective limit per service prefix, used below to keep reservations <= limit.
	cpuLimits := map[string]float64{}
	for _, key := range sortedKeys(cur) {
		prefix, ok := strings.CutSuffix(key, "_CPU_LIMIT")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(cur[key]), 64)
		if err != nil || v <= 0 {
			continue
		}
		if v > cpuCeiling {
			record(key, formatCPU(v), formatCPU(cpuCeiling))
			v = cpuCeiling
		}
		cpuLimits[prefix] = v
	}

	// --- CPU reservations: must not exceed their own (clamped) limit. ---
	for _, key := range sortedKeys(cur) {
		prefix, ok := strings.CutSuffix(key, "_CPU_RES")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(cur[key]), 64)
		if err != nil || v <= 0 {
			continue
		}
		ceiling := cpuCeiling
		if limit, ok := cpuLimits[prefix]; ok && limit < ceiling {
			ceiling = limit
		}
		if v > ceiling {
			record(key, formatCPU(v), formatCPU(ceiling))
		}
	}

	// --- Memory: advisory, and only when the daemon told us how much it has. ---
	if host.MemBytes > 0 {
		// 10% headroom for the daemon itself and everything outside this stack.
		memCeiling := floorGiB(host.MemBytes * 9 / 10)
		if memCeiling > 0 {
			memLimits := map[string]int64{}
			for _, key := range sortedKeys(cur) {
				prefix, ok := strings.CutSuffix(key, "_MEM_LIMIT")
				if !ok {
					continue
				}
				v, ok := parseBytes(cur[key])
				if !ok {
					continue
				}
				if v > memCeiling {
					record(key, formatBytesEnv(v), formatBytesEnv(memCeiling))
					v = memCeiling
				}
				memLimits[prefix] = v
			}
			for _, key := range sortedKeys(cur) {
				prefix, ok := strings.CutSuffix(key, "_MEM_RES")
				if !ok {
					continue
				}
				v, ok := parseBytes(cur[key])
				if !ok {
					continue
				}
				ceiling := memCeiling
				if limit, ok := memLimits[prefix]; ok && limit < ceiling {
					ceiling = limit
				}
				if v > ceiling {
					record(key, formatBytesEnv(v), formatBytesEnv(ceiling))
				}
			}
		}
	}

	// --- Worker pool sizes (first fit on this hardware only). ---
	for _, key := range sortedKeys(cur) {
		divisor, ok := concurrencyKeys[key]
		if !ok || !initial {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(cur[key]))
		if err != nil || v <= 1 {
			continue
		}
		ceiling := host.CPUs / divisor
		if ceiling < 1 {
			ceiling = 1
		}
		if v > ceiling {
			record(key, strconv.Itoa(v), strconv.Itoa(ceiling))
		}
	}

	if len(updates) == 0 {
		return nil, nil
	}
	sort.Strings(notes)
	return updates, notes
}

// fitResourceLimits rewrites .env.production so no container asks for more CPU
// than the Docker daemon has. Called on every start, so it also self-heals an
// .env.production written by an earlier install on different hardware.
func (a *App) fitResourceLimits(cur map[string]string) error {
	host := a.hostResources()
	if host.CPUs <= 0 {
		return nil
	}
	fingerprint := strconv.Itoa(host.CPUs)
	initial := strings.TrimSpace(cur[fittedCPUsKey]) != fingerprint
	updates, notes := fitResourceEnv(cur, host, initial)
	if initial {
		if updates == nil {
			updates = map[string]string{}
		}
		updates[fittedCPUsKey] = fingerprint
	}
	if len(updates) == 0 {
		return nil
	}
	if err := a.setProductionEnvValues(updates); err != nil {
		return err
	}
	if len(notes) == 0 {
		// Nothing needed changing; only the fingerprint was recorded.
		cur[fittedCPUsKey] = fingerprint
		return nil
	}
	for k, v := range updates {
		cur[k] = v
	}
	machine := fmt.Sprintf("%d CPUs", host.CPUs)
	if host.MemBytes > 0 {
		machine += ", " + formatBytes(uint64(host.MemBytes)) + " RAM"
	}
	a.emitAndLog("launcher", fmt.Sprintf(
		"Fitted resource limits to this machine (%s): %s",
		machine, strings.Join(notes, ", ")))
	return nil
}

// hostResources returns the detected ceilings, using the test hook when set.
func (a *App) hostResources() hostResources {
	if a.hostResourcesFn != nil {
		return a.hostResourcesFn()
	}
	return a.detectHostResources()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formatCPU renders a core count the way compose expects: "8", "0.5" — never
// "8.000000" or scientific notation.
func formatCPU(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// parseBytes reads docker-style sizes ("32G", "512M", "2048k"), 1024-based.
func parseBytes(raw string) (int64, bool) {
	s := strings.TrimSpace(strings.ToUpper(raw))
	s = strings.TrimSuffix(s, "B")
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int64(n * float64(mult)), true
}

// formatBytesEnv renders bytes back into a compose-friendly size, preferring
// whole G then whole M.
func formatBytesEnv(n int64) string {
	if n%(1<<30) == 0 {
		return fmt.Sprintf("%dG", n/(1<<30))
	}
	return fmt.Sprintf("%dM", int64(math.Max(1, float64(n/(1<<20)))))
}

// floorGiB rounds down to a whole GiB so clamped limits read as "14G" rather
// than "14371M". Values under 1 GiB are left exact.
func floorGiB(n int64) int64 {
	if n < 1<<30 {
		return n
	}
	return (n / (1 << 30)) * (1 << 30)
}
