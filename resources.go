package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
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
	// FromDaemon distinguishes an authoritative answer from /info from the
	// goruntime.NumCPU() fallback. The fallback is this process's view of the
	// machine, which over-estimates on Docker Desktop (containers run in a VM
	// with its own allocation), so a limit that looks valid here can still be
	// rejected there. Which source produced the number changes what the user
	// should go and check, so it is reported alongside it.
	FromDaemon bool
}

// CPUSource describes where CPUs came from, for messages aimed at the user.
func (h hostResources) CPUSource() string {
	if h.FromDaemon {
		return "from the Docker daemon"
	}
	return "this computer's own count — the Docker daemon may have fewer"
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
			a.hostRes = hostResources{CPUs: res.Info.NCPU, MemBytes: res.Info.MemTotal, DockerRootDir: res.Info.DockerRootDir, FromDaemon: true}
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
	// Long ABFE/RBFE runs contend for the same GPU, so one at a time unless the
	// machine is genuinely large. Ships as 1 in the template — the divisor only
	// matters for a file where someone raised it by hand.
	"GPU_LONG_CONCURRENCY": 8,
}

// workerCPUHeadroom is the share of the machine the Celery workers may claim.
// Clamping a worker to exactly host.CPUs is *legal* — Docker's check is `>`, so
// cpus == NCPU is accepted — but it hands one container every thread, leaving
// nothing for the gateway, the frontend, Docker itself and the user's desktop.
// A batch docking run then makes the machine unusable rather than merely busy.
const workerCPUHeadroom = 0.75

// isResourceEnvKey reports whether a key is one of the hardware-sizing knobs:
// the per-container CPU/memory limits and reservations, plus the Celery pool
// sizes. These are the keys that must stay identical between the two copies of
// .env.production.template and consistent with docker-compose.yml's fallbacks.
func isResourceEnvKey(key string) bool {
	if _, ok := concurrencyKeys[key]; ok {
		return true
	}
	for _, suffix := range []string{"_CPU_LIMIT", "_CPU_RES", "_MEM_LIMIT", "_MEM_RES"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return strings.HasSuffix(key, "_CONCURRENCY")
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
	// The Celery workers get a lower, soft ceiling so no single one can claim
	// the whole machine (see workerCPUHeadroom). Never below 1: a `cpus: 0`
	// limit is rejected just as hard as an oversized one.
	workerCeiling := math.Floor(cpuCeiling * workerCPUHeadroom)
	if workerCeiling < 1 {
		workerCeiling = 1
	}
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
		ceiling := cpuCeiling
		if strings.HasPrefix(key, "WORKER_") {
			ceiling = workerCeiling
		}
		if v > ceiling {
			record(key, formatCPU(v), formatCPU(ceiling))
			v = ceiling
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
	// Always say what was detected, even when nothing needed changing. This
	// used to log only on a change, which made "did the fitting run on this
	// user's machine?" unanswerable from a screenshot of the log panel — and on
	// an already-correct file the silence was indistinguishable from an older
	// binary that has no fitting at all. That ambiguity cost several rounds on
	// the original report, so the line is cheap at the price.
	machine := fmt.Sprintf("%d CPUs", host.CPUs)
	if host.MemBytes > 0 {
		machine += ", " + formatBytes(uint64(host.MemBytes)) + " RAM"
	}
	summary := fmt.Sprintf("Detected %s (%s); resource limits fitted", machine, host.CPUSource())
	if len(notes) > 0 {
		summary += ": " + strings.Join(notes, ", ")
	} else {
		summary += ": no changes needed"
	}
	a.emitAndLog("launcher", summary)

	if len(updates) == 0 {
		return nil
	}
	if err := a.setProductionEnvValues(updates); err != nil {
		return err
	}
	for k, v := range updates {
		cur[k] = v
	}
	return nil
}

// --- Validating the model Docker actually receives -------------------------
//
// fitResourceEnv works from .env.production's keys, which is not the same thing
// as what compose hands the daemon. It cannot see a limit that comes from an
// inline compose default (`${WORKER_REINVENT_CPU_LIMIT:-4}` — a key the
// template never defines), one injected through the shell environment
// (composeEnv appends to os.Environ, and compose gives shell env precedence
// over --env-file), or a user who edited a different file than the one compose
// reads. `docker compose config` resolves every one of those, so it is the only
// authoritative answer to "what will the daemon be asked to create?".

// resolvedCPULimit is one service's CPU limit as compose actually resolved it.
type resolvedCPULimit struct {
	Service string
	CPUs    float64
}

// composeConfigArgs derives a `compose ... config --format json` arg list from
// an `up` arg list by reusing every global flag that precedes "up" (--env-file
// and any -f overlays), so the model we validate is the model that gets
// created. Same rule as composePsArgs, for the same reason.
func composeConfigArgs(upArgs []string) []string {
	args := make([]string, 0, len(upArgs)+3)
	for _, arg := range upArgs {
		if arg == "up" {
			break
		}
		args = append(args, arg)
	}
	return append(args, "config", "--format", "json")
}

// overCPUServices returns every service in a resolved compose model whose CPU
// limit exceeds ceiling, sorted by service name. Docker's own check is `>`, so
// a limit equal to the daemon's count is legal and is not reported.
func overCPUServices(configJSON []byte, ceiling float64) ([]resolvedCPULimit, error) {
	var model struct {
		Services map[string]struct {
			Deploy struct {
				Resources struct {
					Limits struct {
						// compose renders cpus as a quoted string in some
						// versions and a bare number in others.
						CPUs json.Number `json:"cpus"`
					} `json:"limits"`
				} `json:"resources"`
			} `json:"deploy"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &model); err != nil {
		return nil, err
	}
	var over []resolvedCPULimit
	for name, svc := range model.Services {
		raw := strings.TrimSpace(svc.Deploy.Resources.Limits.CPUs.String())
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 {
			continue
		}
		if v > ceiling {
			over = append(over, resolvedCPULimit{Service: name, CPUs: v})
		}
	}
	sort.Slice(over, func(i, j int) bool { return over[i].Service < over[j].Service })
	return over, nil
}

// composeCPULimitKeys maps each service to the .env key backing its CPU limit,
// e.g. "worker-cpu" -> "WORKER_CPU_CPU_LIMIT". Knowing the key is what turns
// "worker-cpu resolved to 16 CPUs" into something the launcher can clamp rather
// than merely complain about.
//
// This scans the compose source rather than the resolved model on purpose: the
// resolved model has already substituted the variable away, so the name only
// survives in the file. A simple indentation scan is enough for our own compose
// file, and a service we fail to map just falls through to the fail-fast path.
func composeCPULimitKeys(composeYAML string) map[string]string {
	keys := map[string]string{}
	service := ""
	inLimits := false
	for _, line := range strings.Split(composeYAML, "\n") {
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		// A service header is a two-space-indented "name:" under `services:`.
		if indent == 2 && strings.HasSuffix(trimmed, ":") {
			service = strings.TrimSuffix(trimmed, ":")
			inLimits = false
			continue
		}
		if trimmed == "limits:" {
			inLimits = true
			continue
		}
		// `reservations:` and anything else at the same level ends the block.
		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") && trimmed != "limits:" {
			inLimits = false
		}
		if !inLimits || service == "" || !strings.HasPrefix(trimmed, "cpus:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "cpus:"))
		if key := envVarNameIn(value); key != "" {
			keys[service] = key
		}
	}
	return keys
}

// envVarNameIn extracts KEY from a compose interpolation like "${KEY:-4}",
// "${KEY}" or "$KEY". Returns "" for a literal value.
func envVarNameIn(value string) string {
	i := strings.Index(value, "${")
	if i < 0 {
		return ""
	}
	rest := value[i+2:]
	end := strings.IndexAny(rest, ":-}")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// verifyFittedModel checks the resolved compose model against the daemon's CPU
// count and brings any oversized limit back under it, or refuses to start.
//
// Failing before `up` is deliberate: the daemon rejects the offending container
// at *creation*, so continuing would leave a half-created stack and an error
// naming a container the user has never heard of.
//
// A failure of `docker compose config` itself (older compose without --format,
// daemon gone, an overlay we cannot parse) is not a reason to refuse to start:
// it is logged and start continues, since this check is a safety net over
// fitResourceEnv rather than a replacement for it.
func (a *App) verifyFittedModel(upArgs []string) error {
	host := a.hostResources()
	if host.CPUs <= 0 {
		return nil
	}
	out, err := a.runComposeConfig(composeConfigArgs(upArgs))
	if err != nil {
		a.emitAndLog("launcher", fmt.Sprintf(
			"Could not verify resolved resource limits (docker compose config failed: %v); starting anyway", err))
		return nil
	}
	over, err := overCPUServices(out, float64(host.CPUs))
	if err != nil {
		a.emitAndLog("launcher", fmt.Sprintf(
			"Could not parse the resolved compose model (%v); starting anyway", err))
		return nil
	}
	if len(over) == 0 {
		return nil
	}

	var composeYAML string
	if data, readErr := os.ReadFile(filepath.Join(a.projectPath, "docker-compose.yml")); readErr == nil {
		composeYAML = string(data)
	}
	backing := composeCPULimitKeys(composeYAML)

	// Clamp to the same ceilings fitResourceEnv uses — the soft one for workers,
	// the daemon's own count for everything else — so a value rewritten here
	// does not immediately look wrong to the next fit.
	workerCeiling := math.Floor(float64(host.CPUs) * workerCPUHeadroom)
	if workerCeiling < 1 {
		workerCeiling = 1
	}
	updates := map[string]string{}
	var unfixable []string
	for _, svc := range over {
		key, ok := backing[svc.Service]
		if !ok {
			unfixable = append(unfixable, fmt.Sprintf("%s (cpus: %s)", svc.Service, formatCPU(svc.CPUs)))
			continue
		}
		ceiling := float64(host.CPUs)
		if strings.HasPrefix(key, "WORKER_") {
			ceiling = workerCeiling
		}
		updates[key] = formatCPU(ceiling)
	}
	if len(updates) > 0 {
		if err := a.setProductionEnvValues(updates); err != nil {
			return err
		}
		a.emitAndLog("launcher", fmt.Sprintf(
			"Resolved compose model asked for more CPUs than the daemon has (%d); clamped %s",
			host.CPUs, strings.Join(sortedKeys(updates), ", ")))
	}
	if len(unfixable) > 0 {
		return fmt.Errorf(
			"docker will refuse to create these containers: %s — the Docker daemon has only %d CPUs, "+
				"and no .env.production key backs these limits, so they must be lowered in docker-compose.yml",
			strings.Join(unfixable, ", "), host.CPUs)
	}
	return nil
}

// explainComposeFailure turns a raw docker/compose failure into something the
// user can act on, or returns "" when it has nothing to add.
//
// The daemon's own wording — "range of CPUs is from 0.01 to 8.00, as there are
// only 8 CPUs available" — names neither the offending service, nor the setting
// that produced the value, nor the file that setting lives in. On Windows that
// file is under %AppData%, where nobody looks by accident. Supplying all three
// is the difference between a fix and another round of correspondence.
func (a *App) explainComposeFailure(reason string, upArgs []string) string {
	if !strings.Contains(reason, "range of CPUs is from") {
		return ""
	}
	host := a.hostResources()

	// Name the offending service(s) from the resolved model rather than making
	// the user map a container name back to a setting. Best-effort: the
	// explanation is still worth printing without it.
	var over []resolvedCPULimit
	if out, err := a.runComposeConfig(composeConfigArgs(upArgs)); err == nil {
		over, _ = overCPUServices(out, float64(host.CPUs))
	}
	return cpuRangeExplanation(host, a.productionEnvPath(), over,
		strayProductionEnvFiles(a.projectPath), goruntime.GOOS)
}

// strayProductionEnvFiles returns, sorted, any near-miss neighbours of
// .env.production in dir — the files a user may have edited by mistake while
// compose kept reading the real one.
//
// The usual culprit is Notepad on Windows: File Explorer hides known
// extensions, and "Save as type: Text Documents" appends .txt, so
// `.env.production.txt` and `.env.production` are both displayed as
// ".env.production". Naming the file we found beats advising the user to go
// looking for it.
func strayProductionEnvFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var stray []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Only near-misses: the real file and the template are both expected.
		if !strings.HasPrefix(name, ".env.production.") || name == ".env.production.template" {
			continue
		}
		stray = append(stray, name)
	}
	sort.Strings(stray)
	return stray
}

// cpuRangeExplanation is the message body, split out from explainComposeFailure
// so both the Windows and non-Windows wording are testable from either.
func cpuRangeExplanation(host hostResources, envPath string, over []resolvedCPULimit, stray []string, goos string) string {
	var b strings.Builder
	b.WriteString("\n--- what this means ---\n")
	b.WriteString("Docker refuses to create a container whose CPU limit exceeds the number of CPUs it has.\n")
	fmt.Fprintf(&b, "Detected %d CPUs (%s).\n", host.CPUs, host.CPUSource())
	if len(over) > 0 {
		var parts []string
		for _, svc := range over {
			parts = append(parts, fmt.Sprintf("%s (asks for %s)", svc.Service, formatCPU(svc.CPUs)))
		}
		fmt.Fprintf(&b, "Over the limit: %s.\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "Settings file Docker is reading: %s\n", envPath)
	b.WriteString("Lower the *_CPU_LIMIT values there, editing each line in place — compose uses the\n")
	b.WriteString("LAST definition of a key, so a line added above an existing one has no effect.\n")
	if len(stray) > 0 {
		// We found the near-miss rather than merely warning it might exist, so
		// say so plainly — this is very likely the file they actually edited.
		fmt.Fprintf(&b, "NOTE: %s also exists in that folder and Docker does NOT read it.\n",
			strings.Join(stray, ", "))
		b.WriteString("If that is where your changes went, copy them into .env.production and delete it.\n")
	} else if goos == "windows" {
		b.WriteString("Also check Notepad did not save it as .env.production.txt: File Explorer hides\n")
		b.WriteString("known extensions, so the real file may sit untouched next to your edited copy.\n")
	}
	return b.String()
}

// runComposeConfig executes `docker compose ... config`, using the test hook
// when set so the check can be exercised without a daemon.
func (a *App) runComposeConfig(args []string) ([]byte, error) {
	if a.composeConfigFn != nil {
		return a.composeConfigFn(args)
	}
	cmd := exec.Command("docker", args...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ResetResourceLimits puts every hardware-sizing key in .env.production back to
// the shipped template's value and re-fits the result to this machine. Bound to
// the frontend so a user whose file has been edited into a state they no longer
// understand has one action that recovers it — without opening a folder under
// %AppData% by hand, and without the sledgehammer of deleting the file, which
// would also discard their generated passwords and desync the Postgres volume.
//
// Only resource keys are touched (isResourceEnvKey): secrets, VERSION, ports
// and CORS settings are left exactly as they are.
//
// LIGANDX_FITTED_CPUS is cleared so the fit that follows counts as the first on
// this hardware and is allowed to re-tune the Celery pool sizes too — otherwise
// a "reset" would restore the limits but leave a hand-raised concurrency alone.
func (a *App) ResetResourceLimits() error {
	data, err := os.ReadFile(filepath.Join(a.projectPath, ".env.production.template"))
	if err != nil {
		return fmt.Errorf("cannot read .env.production.template to reset from: %w", err)
	}
	defaults := map[string]string{}
	for key, value := range parseEnvFile(string(data)) {
		if isResourceEnvKey(key) {
			defaults[key] = value
		}
	}
	if len(defaults) == 0 {
		return fmt.Errorf("no resource settings found in .env.production.template")
	}
	defaults[fittedCPUsKey] = ""
	if err := a.setProductionEnvValues(defaults); err != nil {
		return err
	}
	a.emitAndLog("launcher", fmt.Sprintf(
		"Reset %d resource settings in %s to the shipped defaults", len(defaults)-1, a.productionEnvPath()))

	content, err := a.GetEnvContent("prod")
	if err != nil {
		return err
	}
	return a.fitResourceLimits(parseEnvFile(content))
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
