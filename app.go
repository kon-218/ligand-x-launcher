package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"ligandx-launcher/internal/agentsession"
	"ligandx-launcher/internal/envfile"
	"ligandx-launcher/internal/hostmetrics"
	"ligandx-launcher/internal/license"
	"ligandx-launcher/internal/runtimebundle"
	"ligandx-launcher/internal/secretstore"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type ServiceStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Health  string `json:"health"`
	Running bool   `json:"running"`
}

type SystemStatus struct {
	DockerInstalled       bool            `json:"dockerInstalled"`
	DockerRunning         bool            `json:"dockerRunning"`
	Services              []ServiceStatus `json:"services"`
	TotalRunning          int             `json:"totalRunning"`
	TotalServices         int             `json:"totalServices"`
	PlatformQualification string          `json:"platformQualification"`
}

type LogEntry struct {
	Service   string `json:"service"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

type ResourceMetrics struct {
	CPUPercent       float64                   `json:"cpuPercent"`
	LoadAverage      string                    `json:"loadAverage"`
	MemoryUsedBytes  uint64                    `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64                    `json:"memoryTotalBytes"`
	MemoryPercent    float64                   `json:"memoryPercent"`
	GPUPercent       float64                   `json:"gpuPercent"`
	GPUMemoryUsedMB  uint64                    `json:"gpuMemoryUsedMb"`
	GPUMemoryTotalMB uint64                    `json:"gpuMemoryTotalMb"`
	NetRxBytes       uint64                    `json:"netRxBytes"`
	NetTxBytes       uint64                    `json:"netTxBytes"`
	DiskUsedBytes    uint64                    `json:"diskUsedBytes"`
	DiskTotalBytes   uint64                    `json:"diskTotalBytes"`
	Containers       []ContainerResourceMetric `json:"containers"`
}

type ContainerResourceMetric struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Service     string  `json:"service"`
	Image       string  `json:"image"`
	Port        string  `json:"port"`
	Status      string  `json:"status"`
	Running     bool    `json:"running"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes uint64  `json:"memoryBytes"`
	MemoryLimit uint64  `json:"memoryLimit"`
	MemoryText  string  `json:"memoryText"`
	GPUPercent  float64 `json:"gpuPercent"`
	Uptime      string  `json:"uptime"`
}

type PullProgress struct {
	GroupID         string  `json:"groupId"`
	GroupName       string  `json:"groupName"`
	ImageIndex      int     `json:"imageIndex"`
	TotalImages     int     `json:"totalImages"`
	CurrentImage    string  `json:"currentImage"`
	ImagePercent    float64 `json:"imagePercent"`
	OverallPercent  float64 `json:"overallPercent"`
	Status          string  `json:"status"`
	BytesTotal      int64   `json:"bytesTotal"`
	BytesDownloaded int64   `json:"bytesDownloaded"`
}

type ServiceGroup struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Services           []string `json:"services"`
	Images             []string `json:"images"`
	RegistryAuthImages []string `json:"-"`
	SizeMB             int      `json:"sizeMb"`
	Required           bool     `json:"required"`
	DefaultOn          bool     `json:"defaultOn"`
	Edition            string   `json:"edition"`
	Entitlement        string   `json:"entitlement"`
	Licensed           bool     `json:"licensed"`
	Locked             bool     `json:"locked"`
}

type LauncherConfig struct {
	FirstRunDone   bool        `json:"firstRunDone"`
	SelectedGroups []string    `json:"selectedGroups"`
	UserProfile    UserProfile `json:"userProfile"`
	ConfigVersion  int         `json:"configVersion"`

	// ShowPrereleases opts this install into release candidates, in the version
	// picker and in the version-less "install latest" path alike. Defaults to
	// false, so an install that never touches the toggle keeps seeing only
	// stable releases.
	ShowPrereleases bool `json:"showPrereleases"`
}

type UserProfile struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

type DistributionStatus struct {
	ProjectPath      string `json:"projectPath"`
	Installed        bool   `json:"installed"`
	Bundled          bool   `json:"bundled"`
	NeedsInstall     bool   `json:"needsInstall"`
	RuntimeBundleURL string `json:"runtimeBundleUrl"`
	Message          string `json:"message"`
	InstalledVersion string `json:"installedVersion"`
}

// RuntimeUpdateStatus answers "is there a newer release than what is installed".
// Separate from DistributionStatus because it costs a GitHub API call, and the
// distribution status is read on every dashboard refresh.
type RuntimeUpdateStatus struct {
	InstalledVersion string `json:"installedVersion"`
	LatestVersion    string `json:"latestVersion"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	UpdateRequired   bool   `json:"updateRequired"`
	Message          string `json:"message"`
}

type UserSettings struct {
	CPUWorkerConcurrency int    `json:"cpuWorkerConcurrency"`
	GPUShortConcurrency  int    `json:"gpuShortConcurrency"`
	GPULongConcurrency   int    `json:"gpuLongConcurrency"`
	OrcaHostPath         string `json:"orcaHostPath"`
	BoltzMSAUsername     string `json:"boltzMsaUsername"`
	BoltzMSAPassword     string `json:"boltzMsaPassword"`
	BoltzMSAApiKey       string `json:"boltzMsaApiKey"`
}

// AgentSetup is intentionally short-lived. It contains paste-ready MCP setup text
// and a workspace-token expiry timestamp, never the user's password.
type AgentSetup struct {
	Instructions   string `json:"instructions"`
	ExpiresAt      string `json:"expiresAt"`
	SessionID      string `json:"sessionId"`
	CredentialID   string `json:"credentialId"`
	MigratedLegacy bool   `json:"migratedLegacy"`
}

// gpuRequiredRuntime lists services that genuinely cannot run without a GPU and
// must be hard-blocked on CPU-only hosts. The core services md and
// worker-gpu-short are deliberately absent: OpenMM falls back to its CPU
// platform (see services/md/main.py), so CPU-only users can still run them —
// just slower. Used by the pre-flight checks before pull and start.
//
// The set of services that *reserve* a GPU when one is present is broader (md
// and worker-gpu-short included); that coverage lives in docker-compose.gpu.yml,
// which the launcher layers on top of the CPU-safe base only when an NVIDIA GPU
// is detected (see gpuComposeArgs).
var gpuRequiredRuntime = map[string]bool{
	"abfe":            true,
	"rbfe":            true,
	"boltz2":          true,
	"worker-gpu-long": true,
}

// ligandxServiceSet is every docker-compose service name that belongs to the
// Ligand-X stack. Used to recognize our containers when listing status and when
// stopping the stack.
var ligandxServiceSet = map[string]bool{
	"gateway": true, "frontend": true, "proxy": true, "structure": true,
	"docking": true, "md": true, "admet": true, "boltz2": true,
	"qc": true, "alignment": true, "ketcher": true, "msa": true,
	"abfe": true, "rbfe": true, "reinvent": true,
	"pocket-finder": true, "postgres": true, "redis": true, "rabbitmq": true,
	"worker-qc": true, "worker-gpu-short": true, "worker-gpu-long": true,
	"worker-cpu": true, "worker-reinvent": true, "flower": true,
}

// isLigandxProject reports whether a compose project name looks like a Ligand-X
// stack (matches the filter used across status detection).
func isLigandxProject(projectName string) bool {
	return strings.Contains(projectName, "ligand") || projectName == "ligandx"
}

// Injected into public builds with: -ldflags "-X main.runtimeBundlePublicKeyB64=<base64 raw Ed25519 public key>".
// A public build without a trust root fails closed before downloading a runtime bundle.
var runtimeBundlePublicKeyB64 string

// Injected by production builds. The fallback matches the last runtime known
// to older build scripts, while CI always supplies the product release.
var launcherVersion = defaultPinnedImageVersion

// runtimePolicy is the verification policy this build enforces on runtime
// releases. It reads the injected values at call time so tests can override
// them.
func runtimePolicy() runtimebundle.Policy {
	return runtimebundle.Policy{
		PublicKeyB64:      runtimeBundlePublicKeyB64,
		LauncherVersion:   launcherVersion,
		AllowLocalSources: !isPublicBuild,
	}
}

// defaultPinnedImageVersion is the image tag this launcher build was published
// against. It is the last-resort fallback for VERSION self-healing when the
// on-disk .env.production.template is missing or stale (e.g. an older runtime
// dir whose template still says CHANGE_ME). Keep in sync with the published
// core image tag and .env.production.template's VERSION.
const defaultPinnedImageVersion = "v2026.08.05"

const (
	jobAttemptLeasesEnabledEnv = "JOB_ATTEMPT_LEASES_ENABLED"
	leaseActivationMarkerName  = ".ligandx-lease-activation-pending"
	leaseActivationWaitSeconds = "300"
)

type App struct {
	ctx           context.Context
	dockerClient  *client.Client
	projectPath   string
	logStreams    map[string]context.CancelFunc
	logStreamsMux sync.Mutex
	composeLogMux sync.Mutex

	// lastReleaseIndexWarning holds any non-fatal defect from the most recent
	// index verification, for ListRuntimeReleaseOptions to surface.
	lastReleaseIndexWarning string

	// Pull cancellation state for PullServiceGroups.
	// Allows the UI to stop an in-progress Docker image pull and return to
	// the services selection screen.
	pullCancelMu     sync.Mutex
	activePullCancel context.CancelFunc
	activePullGen    uint64

	// Docker-daemon capacity, used to fit resource limits (see resources.go).
	// hostResourcesFn overrides detection: nil in production, set by tests so
	// fitting is deterministic instead of depending on the build machine.
	hostResourcesFn   func() hostResources
	hostRes           hostResources
	hostResFromDaemon bool
	hostResMux        sync.Mutex

	// composeConfigFn overrides `docker compose config` for verifyFittedModel:
	// nil in production, set by tests so the resolved-model check can be
	// exercised without a running daemon.
	composeConfigFn func(args []string) ([]byte, error)

	// orcaProbeFn overrides the isolated `docker run` used to prove that the
	// configured Linux ORCA install is executable by the QC worker runtime.
	// Nil in production; tests inject it to inspect arguments without Docker.
	orcaProbeFn func(context.Context, []string) ([]byte, error)

	// secretStore holds assistant MCP credentials. Tests inject a memory
	// store; production uses OS Keychain / Credential Manager / Secret Service.
	secretStore secretstore.Store
}

func NewApp() *App {
	return &App{
		logStreams:  make(map[string]context.CancelFunc),
		secretStore: secretstore.Default(),
	}
}

func (a *App) sessionStore() secretstore.Store {
	if a.secretStore != nil {
		return a.secretStore
	}
	return secretstore.Default()
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Don't initialize Docker client here - do it lazily in CheckDocker() to avoid blocking on startup
	a.detectProjectPath()
}

func (a *App) shutdown(ctx context.Context) {
	_ = a.StopPullServiceGroups()
	a.stopAllLogStreams()
	if a.dockerClient != nil {
		a.dockerClient.Close()
	}
}

func (a *App) initDockerClient() {
	opts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
		// No global timeout — pull operations can take many minutes for large images.
		// Short timeouts are applied per-operation via context.WithTimeout where needed.
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err == nil {
		a.dockerClient = cli
	}
	// If there's an error (Docker not running), dockerClient stays nil
	// This is safe - CheckDocker() will handle it
}

func (a *App) detectProjectPath() {
	if path, ok := a.findProjectPath(); ok {
		a.projectPath = path
		return
	}

	if runtimeDir, err := a.defaultRuntimeDir(); err == nil {
		a.projectPath = runtimeDir
		return
	}

	if cwd, err := os.Getwd(); err == nil {
		a.projectPath = cwd
		return
	}

	if execPath, err := os.Executable(); err == nil {
		a.projectPath = filepath.Dir(execPath)
		return
	}

	a.projectPath = "."
}

func (a *App) findProjectPath() (string, bool) {
	if configured := os.Getenv("LIGANDX_PROJECT_PATH"); configured != "" {
		if path, ok := firstComposeProject([]string{configured}, false); ok {
			return path, true
		}
	}

	// Developer/operator builds should prefer a source checkout over the bundled
	// launcher compose. The source checkout carries docker-compose.override.yml
	// and docker-compose.pro-dev.yml, which are required for dev hot reload and
	// for mounting shared lib source over ABI-specific compiled image artifacts.
	if !isPublicBuild {
		if path, ok := firstComposeProject(developerSourceCandidates(), true); ok {
			return path, true
		}
	}

	var searchPaths []string
	if runtimeDir, err := a.defaultRuntimeDir(); err == nil {
		searchPaths = append(searchPaths, runtimeDir)
	}

	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		searchPaths = append(searchPaths,
			execDir,
			filepath.Join(execDir, "runtime"),
			filepath.Join(execDir, ".."),
			filepath.Join(execDir, "..", "runtime"),
			filepath.Join(execDir, "..", ".."),
			filepath.Join(execDir, "..", "..", ".."),
		)
	}

	if cwd, err := os.Getwd(); err == nil {
		searchPaths = append(searchPaths, cwd, filepath.Join(cwd, "runtime"), filepath.Join(cwd, ".."), filepath.Join(cwd, "..", ".."))
	}

	return firstComposeProject(searchPaths, false)
}

// foreignRuntimeProject reports whether an already-discovered compose project is
// one the launcher must leave alone — a developer source checkout, or a runtime
// shipped alongside the executable — as opposed to the managed runtime directory
// under os.UserConfigDir(), which the launcher installs into and may replace.
//
// An empty found means nothing was discovered, which is not foreign: there is
// simply nothing there yet.
func foreignRuntimeProject(found, runtimeDir string) bool {
	if found == "" {
		return false
	}
	foundAbs, err := filepath.Abs(found)
	if err != nil {
		return true
	}
	runtimeAbs, err := filepath.Abs(runtimeDir)
	if err != nil {
		return true
	}
	foundAbs, runtimeAbs = filepath.Clean(foundAbs), filepath.Clean(runtimeAbs)
	if goruntime.GOOS == "windows" {
		return !strings.EqualFold(foundAbs, runtimeAbs)
	}
	return foundAbs != runtimeAbs
}

func developerSourceCandidates() []string {
	var candidates []string
	addAround := func(base string) {
		if base == "" {
			return
		}
		candidates = append(candidates,
			base,
			filepath.Join(base, "ligand-x"),
			filepath.Join(base, "..", "ligand-x"),
			filepath.Join(base, ".."),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		addAround(cwd)
	}
	if execPath, err := os.Executable(); err == nil {
		addAround(filepath.Dir(execPath))
	}
	return candidates
}

func firstComposeProject(paths []string, requireDevOverride bool) (string, bool) {
	seen := make(map[string]bool)
	for _, path := range paths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		if _, err := os.Stat(filepath.Join(abs, "docker-compose.yml")); err != nil {
			continue
		}
		if requireDevOverride {
			if _, err := os.Stat(filepath.Join(abs, "docker-compose.override.yml")); err != nil {
				continue
			}
		}
		return abs, true
	}
	return "", false
}

func (a *App) defaultRuntimeDir() (string, error) {
	if dir := os.Getenv("LIGANDX_RUNTIME_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	dataDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "ligandx-launcher", "runtime"), nil
}

func (a *App) runtimeBundleURL() string {
	if u := strings.TrimSpace(os.Getenv("LIGANDX_RUNTIME_BUNDLE_URL")); u != "" {
		return u
	}
	return runtimebundle.DefaultBundleURL
}

// ListRuntimeReleases returns authenticated stable release choices. Older
// servers without an index retain the latest-only behavior; the selected
// runtime manifest is still signature-verified during installation.
func (a *App) ListRuntimeReleases() ([]runtimebundle.Release, error) {
	// A user already running a pre-release must keep seeing it even with the
	// toggle off, or the picker hides the version they are on.
	channel := a.includePrereleases() || runtimebundle.IsPrerelease(runtimebundle.InstalledVersion(a.projectPath))
	assets, tag, err := runtimebundle.ResolveReleaseAssets(channel, runtimebundle.IndexAssetName, runtimebundle.IndexSignatureAssetName)
	if err != nil {
		bundleURL, latest, latestErr := runtimebundle.ResolveBundleURL(channel)
		if latestErr != nil {
			return nil, err
		}
		version := runtimebundle.VersionFromTag(latest)
		summary := "Latest stable release"
		if runtimebundle.IsPrerelease(version) {
			summary = "Latest release candidate"
		}
		return []runtimebundle.Release{{Version: version, Status: "supported", Summary: summary, Recommended: true, Compatible: true, Compatibility: "Compatible", BundleURL: bundleURL}}, nil
	}
	tempDir, err := os.MkdirTemp("", "ligandx-release-index-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	indexPath := filepath.Join(tempDir, runtimebundle.IndexAssetName)
	signaturePath := filepath.Join(tempDir, runtimebundle.IndexSignatureAssetName)
	if err := runtimePolicy().DownloadLimited(assets[runtimebundle.IndexAssetName], indexPath, 2*1024*1024); err != nil {
		return nil, err
	}
	if err := runtimePolicy().DownloadLimited(assets[runtimebundle.IndexSignatureAssetName], signaturePath, 4096); err != nil {
		return nil, err
	}
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	signatureBytes, err := os.ReadFile(signaturePath)
	if err != nil {
		return nil, err
	}
	index, err := runtimePolicy().VerifyReleaseIndex(indexBytes, signatureBytes)
	if err != nil {
		return nil, err
	}
	a.lastReleaseIndexWarning = index.Warning
	slices.SortFunc(index.Releases, runtimebundle.CompareNewestFirst)
	_ = tag
	return runtimebundle.FilterForChannel(index.Releases, channel, runtimebundle.InstalledVersion(a.projectPath)), nil
}

// ReleaseOptions is what the version picker renders: the releases available on
// the current channel, plus any non-fatal defect found in the signed index.
type ReleaseOptions struct {
	Releases        []runtimebundle.Release `json:"releases"`
	Warning         string                  `json:"warning"`
	ShowPrereleases bool                    `json:"showPrereleases"`
	Installed       string                  `json:"installed"`
}

// ListRuntimeReleaseOptions is the picker-facing wrapper around
// ListRuntimeReleases. It exists so the UI can show why an index looks odd --
// no recommendation, or several -- instead of the list silently going empty.
func (a *App) ListRuntimeReleaseOptions() (ReleaseOptions, error) {
	options := ReleaseOptions{
		ShowPrereleases: a.includePrereleases(),
		Installed:       runtimebundle.InstalledVersion(a.projectPath),
	}
	releases, err := a.ListRuntimeReleases()
	if err != nil {
		return options, err
	}
	options.Releases = releases
	options.Warning = a.lastReleaseIndexWarning
	return options, nil
}

// includePrereleases reports whether this install has opted into release
// candidates. Unreadable config means stable, the safe default.
func (a *App) includePrereleases() bool {
	config, err := a.GetLauncherConfig()
	if err != nil {
		return false
	}
	return config.ShowPrereleases
}

// GetShowPrereleases reports the current release channel for the UI toggle.
func (a *App) GetShowPrereleases() bool {
	return a.includePrereleases()
}

// SetShowPrereleases switches the release channel and persists it.
func (a *App) SetShowPrereleases(enabled bool) error {
	config, err := a.GetLauncherConfig()
	if err != nil {
		config = LauncherConfig{ConfigVersion: 1}
	}
	config.ShowPrereleases = enabled
	return a.SaveLauncherConfig(config)
}

// CheckForRuntimeUpdate reports whether a newer runtime release than the
// installed one is published, so the UI can prompt. Costs a GitHub API call.
func (a *App) CheckForRuntimeUpdate() RuntimeUpdateStatus {
	current := runtimebundle.InstalledVersion(a.projectPath)
	status := RuntimeUpdateStatus{InstalledVersion: current}
	releases, err := a.ListRuntimeReleases()
	if err != nil {
		status.Message = "Could not check for updates: " + err.Error()
		return status
	}
	latest := ""
	for _, release := range releases {
		if release.Version == current && release.Status == "revoked" {
			status.UpdateAvailable = true
			status.UpdateRequired = true
			status.Message = "Installed runtime " + current + " has been revoked and must be replaced."
		}
		if release.Recommended {
			latest = release.Version
		}
	}
	if latest == "" {
		status.Message = "Signed release index has no recommended compatible release."
		return status
	}
	status.LatestVersion = latest
	if status.UpdateRequired {
		return status
	}

	if current == "" {
		// No marker: an older install that predates version tracking. Offer the
		// update rather than guessing — reinstalling the current release is
		// harmless, and staying silent strands them.
		status.UpdateAvailable = true
		status.Message = "Update to " + latest + " is available."
		return status
	}
	comparison, comparable := runtimebundle.CompareVersions(latest, current)
	if !comparable {
		status.Message = "Installed runtime " + current + " cannot be compared to " + latest + "."
		return status
	}
	if comparison > 0 {
		status.UpdateAvailable = true
		status.Message = "Update available: " + current + " → " + latest + "."
		return status
	}
	status.Message = "Runtime is up to date (" + current + ")."
	return status
}

func (a *App) GetDistributionStatus() DistributionStatus {
	composePath := filepath.Join(a.projectPath, "docker-compose.yml")
	_, err := os.Stat(composePath)
	installed := err == nil
	bundled := false
	if execPath, execErr := os.Executable(); execErr == nil && installed {
		execDir := filepath.Dir(execPath)
		rel, relErr := filepath.Rel(execDir, a.projectPath)
		bundled = relErr == nil && (rel == "." || !strings.HasPrefix(rel, ".."))
	}
	status := DistributionStatus{
		ProjectPath:      a.projectPath,
		Installed:        installed,
		Bundled:          bundled,
		NeedsInstall:     !installed,
		InstalledVersion: runtimebundle.InstalledVersion(a.projectPath),
		RuntimeBundleURL: a.runtimeBundleURL(),
	}
	if installed {
		status.Message = "Ligand-X runtime files are installed."
	} else {
		status.Message = "Ligand-X runtime files are not installed yet."
	}
	return status
}

func (a *App) InstallRuntimeBundle() (DistributionStatus, error) {
	return a.installRuntimeBundleSelected("", "", false)
}

// InstallRuntimeBundleVersion installs a release chosen from the signed stable
// index. Downgrades are accepted only when that index explicitly permits the
// installed version as a safe rollback source.
func (a *App) InstallRuntimeBundleVersion(version string) (DistributionStatus, error) {
	version = strings.TrimSpace(version)
	releases, err := a.ListRuntimeReleases()
	if err != nil {
		return a.GetDistributionStatus(), err
	}
	for _, release := range releases {
		if release.Version != version {
			continue
		}
		if !release.Compatible || release.Status == "revoked" {
			return a.GetDistributionStatus(), fmt.Errorf("release %s is not compatible: %s", version, release.Compatibility)
		}
		allowRollback := false
		current := runtimebundle.InstalledVersion(a.projectPath)
		if comparison, comparable := runtimebundle.CompareVersions(version, current); current != "" && comparable && comparison < 0 {
			if !slices.Contains(release.RollbackSafeFrom, current) {
				return a.GetDistributionStatus(), fmt.Errorf("runtime downgrade rejected: %s is not declared safe from %s", version, current)
			}
			allowRollback = true
			if _, err := a.createPreRollbackBackup(current, version); err != nil {
				return a.GetDistributionStatus(), fmt.Errorf("rollback backup failed; runtime was not changed: %w", err)
			}
		}
		return a.installRuntimeBundleSelected(release.BundleURL, release.Version, allowRollback)
	}
	return a.GetDistributionStatus(), fmt.Errorf("release %q is not present in the signed stable index", version)
}

func (a *App) rollbackComposeCommand(arguments ...string) *exec.Cmd {
	args := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml"}
	args = append(args, arguments...)
	cmd := exec.Command("docker", args...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()
	return cmd
}

// createPreRollbackBackup blocks active work and captures the database plus
// scientific artifacts before an explicitly authorized downgrade. Failure is
// fail-closed: no runtime files or version pins have changed at this point.
func (a *App) createPreRollbackBackup(currentVersion, targetVersion string) (string, error) {
	query := "SELECT count(*) FROM jobs WHERE status IN ('pending','queued','running','processing','preparing');"
	countBytes, err := a.rollbackComposeCommand("exec", "-T", "postgres", "psql", "-U", "ligandx", "-d", "ligandx", "-Atc", query).Output()
	if err != nil {
		return "", fmt.Errorf("could not verify active jobs (the current stack must be running): %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(countBytes)))
	if err != nil {
		return "", fmt.Errorf("invalid active-job count")
	}
	if count > 0 {
		return "", fmt.Errorf("%d active job(s) must finish or be cancelled first", count)
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	backupDir := filepath.Join(a.projectPath, "backups", "pre-rollback-"+stamp)
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(backupDir)
		}
	}()
	writeCommand := func(path string, cmd *exec.Cmd) error {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		cmd.Stdout = file
		cmd.Stderr = &bytes.Buffer{}
		runErr := cmd.Run()
		closeErr := file.Close()
		if runErr != nil {
			return runErr
		}
		return closeErr
	}
	databasePath := filepath.Join(backupDir, "ligandx.dump")
	if err := writeCommand(databasePath, a.rollbackComposeCommand("exec", "-T", "postgres", "pg_dump", "-U", "ligandx", "-Fc", "ligandx")); err != nil {
		return "", fmt.Errorf("database dump failed: %w", err)
	}
	artifactPath := filepath.Join(backupDir, "scientific-artifacts.tar.gz")
	if err := writeCommand(artifactPath, a.rollbackComposeCommand("exec", "-T", "gateway", "tar", "-C", "/app/data/scientific_artifacts", "-czf", "-", ".")); err != nil {
		return "", fmt.Errorf("scientific artifact backup failed: %w", err)
	}
	digest := func(path string) (string, error) {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return "", err
		}
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	databaseDigest, err := digest(databasePath)
	if err != nil {
		return "", err
	}
	artifactDigest, err := digest(artifactPath)
	if err != nil {
		return "", err
	}
	manifest := map[string]any{
		"schema": "ligandx-pre-rollback-backup/1", "created_at": time.Now().UTC().Format(time.RFC3339),
		"from_version": currentVersion, "to_version": targetVersion,
		"files": map[string]string{"ligandx.dump": databaseDigest, "scientific-artifacts.tar.gz": artifactDigest},
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := envfile.WritePrivate(filepath.Join(backupDir, "manifest.json"), append(payload, '\n')); err != nil {
		return "", err
	}
	ok = true
	a.emitAndLog("launcher", "Created pre-rollback backup at "+backupDir)
	return backupDir, nil
}

func (a *App) installRuntimeBundleSelected(selectedURL, selectedVersion string, allowRollback bool) (DistributionStatus, error) {
	runtimeDir, err := a.defaultRuntimeDir()
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("could not determine runtime directory: %w", err)
	}
	// Only a compose project we do not own is a reason not to install. The
	// managed runtime directory is ours, and overwriting it is the entire point
	// of the "Update now" prompt: a user carrying a runtime from an earlier
	// release keeps its docker-compose.yml until this call replaces it, and
	// skipping here reported success while leaving them on the broken one.
	// Re-extraction is safe: the bundle contains no .env.production, so
	// generated secrets and user edits survive, and the download is still gated
	// by signature verification and enforceRuntimeRollbackPolicy.
	if found, ok := a.findProjectPath(); ok && foreignRuntimeProject(found, runtimeDir) {
		return a.GetDistributionStatus(), nil
	}
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to create runtime directory: %w", err)
	}
	stageRoot, err := os.MkdirTemp("", "ligandx-runtime-stage-")
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to create runtime staging directory: %w", err)
	}
	defer os.RemoveAll(stageRoot)

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: "Installing Ligand-X runtime files...", Timestamp: time.Now().Format("15:04:05")})

	bundleURL := strings.TrimSpace(selectedURL)
	releaseTag := strings.TrimSpace(selectedVersion)
	if bundleURL != "" {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Selected verified runtime release: %s", releaseTag), Timestamp: time.Now().Format("15:04:05")})
	} else if override := strings.TrimSpace(os.Getenv("LIGANDX_RUNTIME_BUNDLE_URL")); override != "" {
		bundleURL = override
	} else if resolved, tag, resolveErr := runtimebundle.ResolveBundleURL(a.includePrereleases()); resolveErr == nil {
		bundleURL = resolved
		releaseTag = tag
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Resolved latest runtime bundle: %s", bundleURL), Timestamp: time.Now().Format("15:04:05")})
	} else {
		bundleURL = runtimebundle.DefaultBundleURL
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Could not resolve latest release (%v); falling back to %s", resolveErr, bundleURL), Timestamp: time.Now().Format("15:04:05")})
	}

	manifestURL, err := runtimebundle.CompanionAssetURL(bundleURL, runtimebundle.ManifestAssetName)
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to resolve runtime manifest URL: %w", err)
	}
	signatureURL, err := runtimebundle.CompanionAssetURL(bundleURL, runtimebundle.SignatureAssetName)
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to resolve runtime signature URL: %w", err)
	}
	manifestPath := filepath.Join(stageRoot, runtimebundle.ManifestAssetName)
	signaturePath := filepath.Join(stageRoot, runtimebundle.SignatureAssetName)
	if err := runtimePolicy().DownloadLimited(manifestURL, manifestPath, 64*1024); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to download signed runtime manifest: %w", err)
	}
	defer os.Remove(manifestPath)
	if err := runtimePolicy().DownloadLimited(signatureURL, signaturePath, 4*1024); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to download runtime manifest signature: %w", err)
	}
	defer os.Remove(signaturePath)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return a.GetDistributionStatus(), err
	}
	signatureBytes, err := os.ReadFile(signaturePath)
	if err != nil {
		return a.GetDistributionStatus(), err
	}
	expectedVersion := strings.TrimPrefix(releaseTag, "launcher-")
	manifest, err := runtimePolicy().VerifyManifest(manifestBytes, signatureBytes, expectedVersion)
	if err != nil {
		return a.GetDistributionStatus(), err
	}
	if !allowRollback {
		if err := runtimebundle.EnforceRollbackPolicy(runtimeDir, manifest.Version); err != nil {
			return a.GetDistributionStatus(), err
		}
	}
	releaseTag = manifest.Version

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Downloading verified runtime bundle %s", releaseTag), Timestamp: time.Now().Format("15:04:05")})
	zipPath := filepath.Join(stageRoot, runtimebundle.AssetName)
	if err := runtimePolicy().DownloadLimited(bundleURL, zipPath, manifest.Size); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to download runtime bundle from %s: %w", bundleURL, err)
	}
	defer os.Remove(zipPath)
	if err := runtimebundle.VerifyFile(zipPath, manifest); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("runtime bundle verification failed: %w", err)
	}
	extractedDir := filepath.Join(stageRoot, "extracted")
	if err := os.MkdirAll(extractedDir, 0755); err != nil {
		return a.GetDistributionStatus(), err
	}
	if err := runtimebundle.Extract(zipPath, extractedDir); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to extract runtime bundle: %w", err)
	}
	previousProjectPath := a.projectPath
	a.projectPath = runtimeDir
	_, existingComposeErr := os.Stat(filepath.Join(runtimeDir, "docker-compose.yml"))
	rollbackLeaseActivation, err := a.beginLeaseActivationUpgrade(existingComposeErr == nil, manifest.Version)
	if err != nil {
		a.projectPath = previousProjectPath
		return a.GetDistributionStatus(), fmt.Errorf("failed to prepare safe worker upgrade: %w", err)
	}
	rollback, err := runtimebundle.ActivateStage(extractedDir, runtimeDir, filepath.Join(stageRoot, "backup"))
	if err != nil {
		rollbackLeaseActivation()
		a.projectPath = previousProjectPath
		return a.GetDistributionStatus(), fmt.Errorf("failed to activate staged runtime: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollback()
		rollbackLeaseActivation()
		a.projectPath = previousProjectPath
	}()

	if err := a.ensureProductionEnv(); err != nil {
		return a.GetDistributionStatus(), err
	}
	if releaseTag != "" {
		content, readErr := a.GetEnvContent("prod")
		if readErr == nil {
			current := strings.TrimSpace(envfile.Parse(content)["VERSION"])
			if allowRollback || runtimebundle.ShouldAdvanceVersion(current, releaseTag) {
				if setErr := a.setProductionEnvValues(map[string]string{"VERSION": releaseTag, "PRO_VERSION": releaseTag}); setErr == nil {
					wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Pinned product images to %s in .env.production", releaseTag), Timestamp: time.Now().Format("15:04:05")})
				}
			}
		}
	}
	if err := envfile.WritePrivate(filepath.Join(runtimeDir, ".ligandx-runtime-version"), []byte(releaseTag+"\n")); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to persist runtime version: %w", err)
	}
	committed = true
	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Runtime installed at %s", runtimeDir), Timestamp: time.Now().Format("15:04:05")})
	return a.GetDistributionStatus(), nil
}

func (a *App) CheckDocker() (bool, string) {
	if a.dockerClient == nil {
		a.initDockerClient()
	}

	var sdkErr error
	if a.dockerClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, sdkErr = a.dockerClient.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
		cancel()
		if sdkErr == nil {
			return true, "Docker is running"
		}
	}

	if err := checkDockerCLI(); err == nil {
		if sdkErr != nil {
			return true, fmt.Sprintf("Docker is running via CLI; SDK ping failed: %v", sdkErr)
		}
		return true, "Docker is running via CLI"
	} else if sdkErr != nil {
		return false, fmt.Sprintf("Docker is not running: %v; docker CLI check failed: %v", sdkErr, err)
	} else {
		return false, fmt.Sprintf("Docker client not initialized and docker CLI check failed: %v", err)
	}
}

func checkDockerCLI() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	if output, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func (a *App) ligandXContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	if a.dockerClient == nil {
		return nil, fmt.Errorf("Docker client not initialized")
	}
	listResult, err := a.dockerClient.ContainerList(ctx, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, err
	}
	containers := listResult.Items
	ligandxServices := ligandxServiceSet
	filtered := make([]container.Summary, 0, len(containers))
	for _, c := range containers {
		serviceName := c.Labels["com.docker.compose.service"]
		projectName := c.Labels["com.docker.compose.project"]
		if serviceName == "" || !ligandxServices[serviceName] {
			continue
		}
		if !strings.Contains(projectName, "ligand") && projectName != "ligandx" {
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered, nil
}

func (a *App) GetResourceMetrics() ResourceMetrics {
	metrics := ResourceMetrics{}
	metrics.CPUPercent, metrics.LoadAverage = hostmetrics.CPU()
	metrics.MemoryUsedBytes, metrics.MemoryTotalBytes, metrics.MemoryPercent = hostmetrics.Memory()
	metrics.NetRxBytes, metrics.NetTxBytes = hostmetrics.Network()
	metrics.DiskUsedBytes, metrics.DiskTotalBytes = hostmetrics.DiskUsage(a.projectPath)
	metrics.GPUPercent, metrics.GPUMemoryUsedMB, metrics.GPUMemoryTotalMB = hostmetrics.NvidiaGPU()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	containers, err := a.ligandXContainers(ctx, true)
	if err != nil {
		return metrics
	}
	for _, c := range containers {
		metric := ContainerResourceMetric{
			ID:      c.ID,
			Name:    strings.TrimPrefix(firstContainerName(c.Names), "/"),
			Service: c.Labels["com.docker.compose.service"],
			Image:   c.Image,
			Port:    formatContainerPorts(c.Ports),
			Status:  string(c.State),
			Running: c.State == container.StateRunning,
			Uptime:  c.Status,
		}
		if metric.Name == "" && len(c.ID) > 0 {
			metric.Name = c.ID[:min(12, len(c.ID))]
		}
		if c.State == container.StateRunning {
			if stats, err := a.dockerClient.ContainerStats(ctx, c.ID, client.ContainerStatsOptions{}); err == nil {
				var stat container.StatsResponse
				if decodeErr := json.NewDecoder(stats.Body).Decode(&stat); decodeErr == nil {
					metric.CPUPercent = calculateContainerCPU(stat)
					metric.MemoryBytes = stat.MemoryStats.Usage
					metric.MemoryLimit = stat.MemoryStats.Limit
					metric.MemoryText = fmt.Sprintf("%s / %s", hostmetrics.FormatBytes(metric.MemoryBytes), hostmetrics.FormatBytes(metric.MemoryLimit))
				}
				_ = stats.Body.Close()
			}
		}
		if metric.MemoryText == "" {
			metric.MemoryText = "-"
		}
		if strings.Contains(metric.Service, "gpu") || metric.Service == "md" || metric.Service == "boltz2" || metric.Service == "kinetics" {
			metric.GPUPercent = metrics.GPUPercent
		}
		metrics.Containers = append(metrics.Containers, metric)
	}
	return metrics
}

func firstContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func formatContainerPorts(ports []container.PortSummary) string {
	if len(ports) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		if p.PublicPort > 0 {
			parts = append(parts, fmt.Sprintf("%d", p.PublicPort))
		} else if p.PrivatePort > 0 {
			parts = append(parts, fmt.Sprintf("%d", p.PrivatePort))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func calculateContainerCPU(stat container.StatsResponse) float64 {
	cpuDelta := float64(stat.CPUStats.CPUUsage.TotalUsage - stat.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stat.CPUStats.SystemUsage - stat.PreCPUStats.SystemUsage)
	onlineCPUs := float64(stat.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stat.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta <= 0 || cpuDelta <= 0 || onlineCPUs <= 0 {
		return 0
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100
}

func (a *App) GetSystemStatus() SystemStatus {
	qualification := "qualified"
	if goruntime.GOOS == "darwin" {
		qualification = "preview/untested"
	}
	status := SystemStatus{
		Services:              []ServiceStatus{},
		PlatformQualification: qualification,
	}

	dockerOk, _ := a.CheckDocker()
	status.DockerInstalled = dockerOk || a.dockerClient != nil
	status.DockerRunning = dockerOk

	if !dockerOk || a.dockerClient == nil {
		return status
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listResult, err := a.dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return status
	}
	containers := listResult.Items

	ligandxServices := ligandxServiceSet

	for _, c := range containers {
		serviceName := c.Labels["com.docker.compose.service"]
		projectName := c.Labels["com.docker.compose.project"]

		// Filter: must be a docker compose container for a known ligand-x service.
		// We match on service name (not project name) since the project name varies
		// by directory name (ligand-x, ligandx, etc.).
		// Extra guard: project name must contain "ligand" to avoid false positives.
		if serviceName == "" || !ligandxServices[serviceName] {
			continue
		}
		if !strings.Contains(projectName, "ligand") && projectName != "ligandx" {
			continue
		}

		health := ""
		if strings.Contains(c.Status, "(healthy)") {
			health = "healthy"
		} else if strings.Contains(c.Status, "(unhealthy)") {
			health = "unhealthy"
		} else if strings.Contains(c.Status, "(starting)") {
			health = "starting"
		}

		svc := ServiceStatus{
			Name:    serviceName,
			Status:  string(c.State),
			Health:  health,
			Running: c.State == container.StateRunning,
		}

		if c.State == container.StateRunning {
			status.TotalRunning++
		}

		status.Services = append(status.Services, svc)
		status.TotalServices++
	}

	return status
}

// proSourcePath returns the absolute path of the ligand-x-pro repo if it
// looks present (services/qc exists). Source: $LIGANDX_PRO_SRC_PATH if set,
// else `../ligand-x-pro` relative to the project dir.
func (a *App) proSourcePath() (string, bool) {
	p := os.Getenv("LIGANDX_PRO_SRC_PATH")
	if p == "" {
		p = filepath.Join(a.projectPath, "..", "ligand-x-pro")
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(a.projectPath, p)
	}
	if _, err := os.Stat(filepath.Join(p, "services", "qc")); err != nil {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p, true
	}
	return abs, true
}

func (a *App) projectFileExists(name string) bool {
	_, err := os.Stat(filepath.Join(a.projectPath, name))
	return err == nil
}

func (a *App) devEnvArgs() []string {
	if a.projectFileExists(".env") {
		return []string{"--env-file", ".env"}
	}
	if a.projectFileExists(".env.example") {
		if _, err := a.GetEnvContent("dev"); err == nil && a.projectFileExists(".env") {
			return []string{"--env-file", ".env"}
		}
	}
	if args := a.prodEnvArgs(); len(args) > 0 {
		return args
	}
	return nil
}

// devComposeArgs returns the base docker compose arg list for dev mode.
// When the Pro repo is checked out locally, it layers docker-compose.pro-dev.yml
// so Pro service source hot-reloads from the host. Callers append `up`, `-d`,
// flags, and any service names.
// gpuComposeArgs returns the GPU overlay `-f` args when an NVIDIA GPU is present
// and the overlay file exists. The base docker-compose.yml is CPU-safe; this
// overlay re-adds NVIDIA device reservations so GPU services use the hardware.
// Callers must already pass `-f docker-compose.yml` explicitly — a lone `-f`
// disables compose's auto-discovery of the base file.
func (a *App) gpuComposeArgs() []string {
	if a.projectFileExists("docker-compose.gpu.yml") && a.CheckGPU() {
		return []string{"-f", "docker-compose.gpu.yml"}
	}
	return nil
}

func (a *App) devComposeArgs() []string {
	args := append([]string{"compose"}, a.devEnvArgs()...)
	hasDevOverride := a.projectFileExists("docker-compose.override.yml")
	hasProDevOverride := a.projectFileExists("docker-compose.pro-dev.yml")
	gpuArgs := a.gpuComposeArgs()

	// Any explicit -f (override or GPU overlay) means we must also name the base
	// file explicitly, since a single -f disables auto-discovery of the base.
	if hasDevOverride || hasProDevOverride || len(gpuArgs) > 0 {
		args = append(args, "-f", "docker-compose.yml")
	}
	if hasDevOverride {
		args = append(args, "-f", "docker-compose.override.yml")
	}
	if path, ok := a.proSourcePath(); ok && hasProDevOverride {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Pro source detected at %s — mounting for hot reload", path),
			Timestamp: time.Now().Format("15:04:05"),
		})
		args = append(args, "-f", "docker-compose.pro-dev.yml")
	} else if path, ok := a.proSourcePath(); ok && !hasProDevOverride {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Pro source detected at %s, but docker-compose.pro-dev.yml is not present in %s; starting without Pro hot reload", path, a.projectPath),
			Timestamp: time.Now().Format("15:04:05"),
		})
	}
	// GPU overlay last so its device reservations win the merge.
	args = append(args, gpuArgs...)
	return args
}

// prodEnvArgs returns the top-level `--env-file` args for the prod stack. The
// compose file uses mandatory ${VAR:?} secret substitutions, and `docker
// compose` interpolates the whole model for every subcommand (up AND down), so
// both paths must point at .env.production — compose only auto-loads `.env`.
// For the public build we also guarantee the file exists first (idempotent).
func (a *App) prodEnvArgs() []string {
	_ = a.ensureProductionEnv()
	if _, err := os.Stat(filepath.Join(a.projectPath, ".env.production")); err == nil {
		return []string{"--env-file", ".env.production"}
	}
	return nil
}

func (a *App) StartServices(mode string) error {
	dockerOk, msg := a.CheckDocker()
	if !dockerOk {
		return fmt.Errorf("%s", msg)
	}

	if err := a.ensureDataDirs(); err != nil {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Warning: Could not create data directories: %v", err),
			Timestamp: time.Now().Format("15:04:05"),
		})
	}

	// Resolve host-port conflicts before compose tries to bind (preflight.go).
	// Must run before the compose args are built, because prodEnvArgs ->
	// ensureProductionEnv derives CORS_ORIGINS from the (possibly moved) APP_PORT.
	if err := a.fitPublishedPorts(); err != nil {
		a.emitAndLog("launcher", fmt.Sprintf("Warning: could not check host ports: %v", err))
	}

	var args []string
	var services []string

	// Load launcher config to get selected service groups
	config, err := a.GetLauncherConfig()
	if err != nil || config.SelectedGroups == nil || len(config.SelectedGroups) == 0 {
		// Fallback to legacy mode behavior if config not available
		switch mode {
		case "dev":
			services = []string{"qc"} // unscoped compose up includes QC
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never")
		case "prod":
			services = []string{"qc"} // unscoped compose up includes QC
			if _, err := a.requirePinnedProductionVersion(); err != nil {
				return err
			}
			args = append([]string{"compose"}, a.prodEnvArgs()...)
			args = append(args, "-f", "docker-compose.yml")
			args = append(args, a.gpuComposeArgs()...)
			args = append(args, "up", "-d", "--pull=never")
		case "core":
			coreServices := []string{"postgres", "redis", "rabbitmq", "gateway", "frontend", "proxy", "structure", "flower", "pocket-finder"}
			args = append(a.devComposeArgs(), append([]string{"up", "-d", "--pull=never"}, coreServices...)...)
		case "docking":
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never", "postgres", "redis", "rabbitmq", "gateway", "frontend", "structure", "ketcher", "docking", "worker-cpu")
		case "md":
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never", "postgres", "redis", "rabbitmq", "gateway", "frontend", "structure", "ketcher", "md", "worker-gpu-short")
		default:
			services = []string{"qc"} // unscoped compose up includes QC
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never")
		}
	} else {
		// Use selected service groups from config
		allGroups := a.GetServiceGroups()
		groupMap := make(map[string]ServiceGroup)
		for _, g := range allGroups {
			groupMap[g.ID] = g
		}

		serviceSet := make(map[string]bool)
		for _, groupID := range config.SelectedGroups {
			if group, ok := groupMap[groupID]; ok {
				if group.Locked {
					continue
				}
				for _, svc := range group.Services {
					serviceSet[svc] = true
				}
			}
		}

		for svc := range serviceSet {
			services = append(services, svc)
		}

		if err := a.checkGPUForServices(services); err != nil {
			return err
		}
		args = append(a.devComposeArgs(), "up", "-d", "--pull=never")
		args = append(args, services...)
	}
	if err := a.checkOrcaForServices(services); err != nil {
		return err
	}

	return a.runDockerCompose(args, "Starting services...")
}

func (a *App) StartServiceGroups(env string, groupIDs []string) error {
	dockerOk, msg := a.CheckDocker()
	if !dockerOk {
		return fmt.Errorf("%s", msg)
	}

	if err := a.ensureDataDirs(); err != nil {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Warning: Could not create data directories: %v", err),
			Timestamp: time.Now().Format("15:04:05"),
		})
	}

	// Resolve host-port conflicts before compose tries to bind (preflight.go).
	// Must run before the compose args are built, because prodEnvArgs ->
	// ensureProductionEnv derives CORS_ORIGINS from the (possibly moved) APP_PORT.
	if err := a.fitPublishedPorts(); err != nil {
		a.emitAndLog("launcher", fmt.Sprintf("Warning: could not check host ports: %v", err))
	}

	allGroups := a.GetServiceGroups()
	groupMap := make(map[string]ServiceGroup)
	for _, g := range allGroups {
		groupMap[g.ID] = g
	}

	serviceSet := make(map[string]bool)
	for _, groupID := range groupIDs {
		if group, ok := groupMap[groupID]; ok {
			if group.Locked {
				wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
					Service:   "launcher",
					Message:   fmt.Sprintf("Skipping %s (requires Pro or Academic license)", group.Name),
					Timestamp: time.Now().Format("15:04:05"),
				})
				continue
			}
			for _, svc := range group.Services {
				serviceSet[svc] = true
			}
		}
	}

	var services []string
	for svc := range serviceSet {
		services = append(services, svc)
	}

	if len(services) == 0 {
		return fmt.Errorf("no unlocked services to start; check your license or service selection")
	}

	if err := a.checkGPUForServices(services); err != nil {
		return err
	}
	if err := a.checkOrcaForServices(services); err != nil {
		return err
	}

	var args []string
	if env == "prod" {
		if _, err := a.requirePinnedProductionVersion(); err != nil {
			return err
		}
		args = append([]string{"compose"}, a.prodEnvArgs()...)
		args = append(args, "-f", "docker-compose.yml")
		args = append(args, a.gpuComposeArgs()...)
		args = append(args, "up", "-d", "--pull=never")
	} else {
		args = append(a.devComposeArgs(), "up", "-d", "--pull=never")
	}
	args = append(args, services...)

	return a.runDockerCompose(args, fmt.Sprintf("Starting %s (%d services)...", env, len(services)))
}

func (a *App) StartServicesCustom(env string, services []string) error {
	dockerOk, msg := a.CheckDocker()
	if !dockerOk {
		return fmt.Errorf("%s", msg)
	}

	if err := a.validateUnlockedServices(services); err != nil {
		return err
	}
	if err := a.checkOrcaForServices(services); err != nil {
		return err
	}

	if err := a.ensureDataDirs(); err != nil {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Warning: Could not create data directories: %v", err),
			Timestamp: time.Now().Format("15:04:05"),
		})
	}

	// Resolve host-port conflicts before compose tries to bind (preflight.go).
	// Must run before the compose args are built, because prodEnvArgs ->
	// ensureProductionEnv derives CORS_ORIGINS from the (possibly moved) APP_PORT.
	if err := a.fitPublishedPorts(); err != nil {
		a.emitAndLog("launcher", fmt.Sprintf("Warning: could not check host ports: %v", err))
	}

	var args []string
	if env == "prod" {
		if _, err := a.requirePinnedProductionVersion(); err != nil {
			return err
		}
		args = append([]string{"compose"}, a.prodEnvArgs()...)
		args = append(args, "-f", "docker-compose.yml")
		args = append(args, a.gpuComposeArgs()...)
		args = append(args, "up", "-d")
	} else {
		args = append(a.devComposeArgs(), "up", "-d")
	}

	args = append(args, services...)

	modeLabel := env
	if len(services) > 0 {
		modeLabel = fmt.Sprintf("%s (%d services)", env, len(services))
	}

	return a.runDockerCompose(args, fmt.Sprintf("Starting %s...", modeLabel))
}

func (a *App) StopServices() error {
	if a.dockerClient == nil {
		a.initDockerClient()
	}

	// Stop via the Docker API, tearing down every compose project the launcher
	// recognizes as a Ligand-X stack — exactly the set GetSystemStatus shows.
	// This avoids the brittleness of `compose down` (which only targets one
	// hardcoded project name and must interpolate the whole compose model just
	// to stop). Falls back to `compose down` only if the Docker API is missing.
	if a.dockerClient == nil {
		args := append([]string{"compose"}, a.prodEnvArgs()...)
		args = append(args, "-f", "docker-compose.yml", "down", "--remove-orphans")
		return a.runDockerCompose(args, "Stopping services...")
	}

	emit := func(msg string) {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service: "launcher", Message: msg, Timestamp: time.Now().Format("15:04:05"),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	listResult, err := a.dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return fmt.Errorf("could not list containers: %w", err)
	}
	containers := listResult.Items

	// Identify which compose projects are ours (a project that owns at least one
	// known Ligand-X service), then tear down *all* containers in those projects
	// — including extras like celery-beat that aren't in the service set.
	// Shared with Uninstall (see uninstall.go): the two must agree on what counts
	// as our stack, or uninstall would leave behind exactly what stop tears down.
	ligandProjects := ligandxComposeProjects(containers)

	if len(ligandProjects) == 0 {
		emit("No running services found")
		return nil
	}

	emit("Stopping services...")
	stopTimeout := 30
	var failed []string
	for _, c := range containers {
		if !ligandProjects[c.Labels["com.docker.compose.project"]] {
			continue
		}
		name := strings.TrimPrefix(firstContainerName(c.Names), "/")
		if c.State == container.StateRunning || c.State == container.StateRestarting {
			if _, err := a.dockerClient.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
				emit(fmt.Sprintf("Warning: could not stop %s: %v", name, err))
				failed = append(failed, name)
				continue
			}
		}
		if _, err := a.dockerClient.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			emit(fmt.Sprintf("Warning: could not remove %s: %v", name, err))
			failed = append(failed, name)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("could not stop %d service(s): %s", len(failed), strings.Join(failed, ", "))
	}
	emit("Services stopped")
	return nil
}

func (a *App) RestartServices() error {
	// Use "up -d" instead of "restart" so containers are recreated when .env changes
	// (e.g. REINVENT_MODELS_PATH update). "restart" keeps stale container config.
	// With no service targets Compose includes QC, so apply both ORCA preflights.
	if err := a.checkOrcaForServices([]string{"qc"}); err != nil {
		return err
	}
	return a.runDockerCompose(append(a.devComposeArgs(), "up", "-d", "--pull=never"), "Restarting services...")
}

func (a *App) RestartServiceGroups(groupIDs []string) error {
	allGroups := a.GetServiceGroups()
	groupMap := make(map[string]ServiceGroup)
	for _, g := range allGroups {
		groupMap[g.ID] = g
	}

	serviceSet := make(map[string]bool)
	for _, groupID := range groupIDs {
		if group, ok := groupMap[groupID]; ok {
			if group.Locked {
				wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
					Service:   "launcher",
					Message:   fmt.Sprintf("Skipping %s (requires Pro or Academic license)", group.Name),
					Timestamp: time.Now().Format("15:04:05"),
				})
				continue
			}
			for _, svc := range group.Services {
				serviceSet[svc] = true
			}
		}
	}

	var services []string
	for svc := range serviceSet {
		services = append(services, svc)
	}

	if err := a.checkOrcaForServices(services); err != nil {
		return err
	}

	args := append(a.devComposeArgs(), "up", "-d", "--pull=never")
	args = append(args, services...)
	return a.runDockerCompose(args, fmt.Sprintf("Restarting %d services...", len(services)))
}

func (a *App) RestartServicesCustom(services []string) error {
	if err := a.validateUnlockedServices(services); err != nil {
		return err
	}
	if err := a.checkOrcaForServices(services); err != nil {
		return err
	}
	args := append(a.devComposeArgs(), "up", "-d", "--pull=never")
	args = append(args, services...)
	label := fmt.Sprintf("Restarting %d services...", len(services))
	return a.runDockerCompose(args, label)
}

// validateUnlockedServices rejects calls that touch any service belonging to
// a Pro group the current license does not entitle. Without this guard the
// per-service launcher methods bypass GetServiceGroups()'s Locked flag.
func (a *App) validateUnlockedServices(services []string) error {
	if len(services) == 0 {
		return nil
	}
	groups := a.GetServiceGroups()
	owner := make(map[string]ServiceGroup, 32)
	for _, g := range groups {
		for _, svc := range g.Services {
			owner[svc] = g
		}
	}
	for _, svc := range services {
		if g, ok := owner[svc]; ok && g.Locked {
			return fmt.Errorf("%s requires a Pro or Academic license", g.Name)
		}
	}
	return nil
}

func (a *App) runDockerCompose(args []string, message string) error {
	if isProductionUpCommand(args) && a.leaseActivationPending() {
		return a.runProductionUpWithLeaseActivation(args, message)
	}
	return a.runDockerComposeDirect(args, message)
}

// runDockerComposeDirect executes one Compose command without the lease
// activation wrapper. The wrapper uses this for the explicit gateway-stop,
// worker-health, and gateway-enable phases without recursively restarting the
// sequence.
func (a *App) runDockerComposeDirect(args []string, message string) error {
	// Validate project path has docker-compose.yml
	composePath := filepath.Join(a.projectPath, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		errMsg := fmt.Sprintf("docker-compose.yml not found in %s. Please select the correct project folder.", a.projectPath)
		a.emitAndLog("launcher", errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// A managed runtime intentionally keeps a stable Compose project name so
	// upgrades retain stateful volumes. When a newer runtime removes or renames
	// a service, containers from the previous model otherwise survive forever
	// (and can keep exposing an obsolete Pro/Preview service). This flag removes
	// only containers labeled as services of this same Compose project that are
	// absent from the current model; it does not remove unrelated containers or
	// unselected services that still exist in the model.
	args = composeUpWithRemoveOrphans(args)

	// emitAndLog rather than a bare EventsEmit: it guards against a nil ctx
	// (before Wails startup, and under test, where wails' EventsEmit calls
	// logger.Fatal and takes the process with it) and persists the line to the
	// on-disk log, which is what makes a user's failure report readable.
	a.emitAndLog("launcher", message)
	a.emitAndLog("launcher", fmt.Sprintf("Working directory: %s", a.projectPath))

	// Record exactly what we are about to run. Previously only the working
	// directory was logged, so a reproduction by hand (or reading the on-disk
	// log) had to guess the args and env. Now the full command and the key
	// interpolation inputs are captured.
	a.rotateComposeLogIfLarge()
	a.emitAndLog("launcher", fmt.Sprintf("Command: docker %s", strings.Join(args, " ")))
	a.emitAndLog("launcher", a.composeContextLine())

	if isProductionUpCommand(args) {
		// Check what compose actually resolves before anything is created.
		// Must precede prepareProductionInfra: that already runs an `up` for the
		// stateful services, and the daemon rejects an oversized `cpus` at
		// container *creation*, so a violation there leaves a half-built stack.
		if err := a.verifyFittedModel(args); err != nil {
			return err
		}
		if err := a.prepareProductionInfra(args); err != nil {
			return err
		}
	}

	cmd := exec.Command("docker", args...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker compose: %v", err)
	}

	// Capture stderr into a tail buffer so a failure can report the real reason.
	// The WaitGroup guarantees both pipes are fully drained before we read the
	// tail or call Wait()'s result — reading a StdoutPipe/StderrPipe after Wait()
	// returns is otherwise a race (the pipe is closed on exit).
	tail := &stderrTail{max: 25}
	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go func() { defer streamWG.Done(); a.streamOutput(stdout, "docker") }()
	go func() { defer streamWG.Done(); a.streamOutputCapture(stderr, "docker", tail) }()

	waitErr := cmd.Wait()
	streamWG.Wait()

	// A production "up" can fail outright if Postgres/RabbitMQ's actual stored
	// credentials have drifted from .env.production: dependents declared with
	// depends_on: condition: service_healthy (gateway -> frontend/proxy) never
	// become eligible to start, and compose surfaces that as a non-zero exit
	// rather than just leaving them in "Created". Reconcile credentials and
	// retry once before giving up, so a single Start click self-heals instead
	// of silently leaving frontend/proxy un-started.
	if waitErr != nil && isProductionUpCommand(args) {
		_ = a.reconcileProductionCredentials()
		retryCmd := exec.Command("docker", args...)
		retryCmd.Dir = cmd.Dir
		retryCmd.Env = cmd.Env
		retryStdout, _ := retryCmd.StdoutPipe()
		retryStderr, _ := retryCmd.StderrPipe()
		if startErr := retryCmd.Start(); startErr == nil {
			retryTail := &stderrTail{max: 25}
			var retryWG sync.WaitGroup
			retryWG.Add(2)
			go func() { defer retryWG.Done(); a.streamOutput(retryStdout, "docker") }()
			go func() { defer retryWG.Done(); a.streamOutputCapture(retryStderr, "docker", retryTail) }()
			waitErr = retryCmd.Wait()
			retryWG.Wait()
			tail = retryTail // report the final attempt's output
		}
	} else if isProductionUpCommand(args) {
		_ = a.reconcileProductionCredentials()
	}

	if waitErr != nil {
		// Record which containers ended up in what state so the failing
		// dependency is captured even when the user only sees "the proxy failed"
		// (proxy depends_on gateway/frontend being healthy — it is usually the
		// victim, not the cause).
		a.captureComposePs(args)
		if reason := strings.TrimSpace(tail.String()); reason != "" {
			// Translate the daemon's own wording into something actionable
			// where we can (see explainComposeFailure); "" when we cannot, so
			// an unrelated failure is not buried under advice about CPUs.
			return fmt.Errorf("docker compose failed: %v\n--- last docker output ---\n%s%s",
				waitErr, reason, a.explainComposeFailure(reason, args))
		}
		return fmt.Errorf("docker compose failed: %v", waitErr)
	}

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   "Operation completed successfully",
		Timestamp: time.Now().Format("15:04:05"),
	})

	return nil
}

func (a *App) leaseActivationMarkerPath() string {
	return filepath.Join(a.projectPath, leaseActivationMarkerName)
}

func (a *App) leaseActivationPending() bool {
	info, err := os.Stat(a.leaseActivationMarkerPath())
	return err == nil && !info.IsDir()
}

// beginLeaseActivationUpgrade durably disables lease-required submissions
// before replacing an existing runtime. Its rollback is paired with the
// staged file rollback so failed extraction/activation restores both the old
// env and any pre-existing activation marker.
func (a *App) beginLeaseActivationUpgrade(existingRuntime bool, targetVersion string) (func(), error) {
	if !existingRuntime {
		return func() {}, nil
	}
	markerPath := a.leaseActivationMarkerPath()
	oldMarker, markerErr := os.ReadFile(markerPath)
	markerExisted := markerErr == nil
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return nil, fmt.Errorf("could not read activation marker: %w", markerErr)
	}
	envPath := filepath.Join(a.projectPath, ".env.production")
	oldEnv, envErr := os.ReadFile(envPath)
	if envErr != nil && !os.IsNotExist(envErr) {
		return nil, fmt.Errorf("could not read production env: %w", envErr)
	}
	markerData := []byte("target_runtime_version=" + strings.TrimSpace(targetVersion) + "\n")
	if err := envfile.WritePrivate(markerPath, markerData); err != nil {
		return nil, fmt.Errorf("could not persist activation marker: %w", err)
	}
	if err := a.setProductionEnvValues(map[string]string{jobAttemptLeasesEnabledEnv: "false"}); err != nil {
		restoreLeaseActivationFiles(envPath, oldEnv, envErr, markerPath, oldMarker, markerExisted)
		return nil, fmt.Errorf("could not disable lease-required submissions: %w", err)
	}
	rollback := func() {
		restoreLeaseActivationFiles(envPath, oldEnv, envErr, markerPath, oldMarker, markerExisted)
	}
	return rollback, nil
}

func restoreLeaseActivationFiles(envPath string, oldEnv []byte, envErr error, markerPath string, oldMarker []byte, markerExisted bool) {
	if envErr == nil {
		_ = envfile.WritePrivate(envPath, oldEnv)
	} else if os.IsNotExist(envErr) {
		_ = os.Remove(envPath)
	}
	if markerExisted {
		_ = envfile.WritePrivate(markerPath, oldMarker)
	} else {
		_ = os.Remove(markerPath)
	}
}

func (a *App) setLeaseActivationEnabled(enabled bool) error {
	return a.setProductionEnvValues(map[string]string{jobAttemptLeasesEnabledEnv: strconv.FormatBool(enabled)})
}

func (a *App) activeProductionWorkerServices() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=com.docker.compose.project=ligand-x", "--format", `{{.Label "com.docker.compose.service"}}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not identify running workers before lease activation: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var services []string
	for _, line := range strings.Split(string(output), "\n") {
		service := strings.TrimSpace(line)
		if strings.HasPrefix(service, "worker-") {
			services = append(services, service)
		}
	}
	return sortedUnique(services), nil
}

func (a *App) runProductionUpWithLeaseActivation(args []string, message string) error {
	markerPath := a.leaseActivationMarkerPath()
	if err := a.setLeaseActivationEnabled(false); err != nil {
		return fmt.Errorf("could not keep lease activation disabled: %w", err)
	}
	if err := validateLeaseActivationRuntime(markerPath, runtimebundle.InstalledVersion(a.projectPath)); err != nil {
		return err
	}
	activeWorkers, err := a.activeProductionWorkerServices()
	if err != nil {
		return err
	}
	config, configErr := a.GetLauncherConfig()
	var selected []string
	if configErr == nil {
		selected = config.SelectedGroups
	}
	allowedServices := launcherAllowedServices(a.GetServiceGroups(), selected)
	activeWorkers = intersectServices(activeWorkers, allowedServices)
	var configuredServices []string
	var finalArgs = args
	if len(composeTargetServices(args)) == 0 {
		configuredServices, err = a.configuredProductionServices(args)
		if err != nil {
			return err
		}
		configuredServices = intersectServices(configuredServices, allowedServices)
		if len(configuredServices) == 0 {
			return fmt.Errorf("no configured services are allowed by the launcher selection for lease activation")
		}
		finalArgs = composeUpForServices(args, configuredServices)
	}
	workers, err := resolveLeaseActivationWorkers(args, configuredServices, activeWorkers)
	if err != nil {
		return err
	}
	return runLeaseActivationSequence(markerPath, args, finalArgs, activeWorkers, workers, a.setLeaseActivationEnabled, a.runDockerComposeDirect, message)
}

func launcherAllowedServices(groups []ServiceGroup, selectedGroupIDs []string) []string {
	selected := make(map[string]bool, len(selectedGroupIDs))
	for _, id := range selectedGroupIDs {
		selected[id] = true
	}
	var services []string
	for _, group := range groups {
		if group.Locked {
			continue
		}
		include := selected[group.ID]
		if len(selectedGroupIDs) == 0 {
			include = group.Required || group.DefaultOn
		}
		if include {
			services = append(services, group.Services...)
		}
	}
	return sortedUnique(services)
}

func intersectServices(configured, allowed []string) []string {
	allow := make(map[string]bool, len(allowed))
	for _, service := range allowed {
		allow[service] = true
	}
	var result []string
	for _, service := range configured {
		if allow[service] {
			result = append(result, service)
		}
	}
	return sortedUnique(result)
}

func validateLeaseActivationRuntime(markerPath, installedVersion string) error {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("could not read pending lease activation marker: %w", err)
	}
	targetVersion := envfile.Parse(string(data))["target_runtime_version"]
	if targetVersion == "" || installedVersion == "" || targetVersion != installedVersion {
		return fmt.Errorf("runtime bundle upgrade is incomplete (installed %q, activation target %q); install the selected runtime bundle before starting services", installedVersion, targetVersion)
	}
	return nil
}

func (a *App) configuredProductionServices(startArgs []string) ([]string, error) {
	args := composeConfigServicesArgs(startArgs)
	if len(args) == 0 {
		return nil, fmt.Errorf("could not determine configured production workers from Compose arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not list configured production services before lease activation: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var services []string
	for _, line := range strings.Split(string(output), "\n") {
		service := strings.TrimSpace(line)
		if service != "" {
			services = append(services, service)
		}
	}
	return sortedUnique(services), nil
}

func runLeaseActivationSequence(markerPath string, startArgs, finalArgs, activeWorkers, workers []string, setEnabled func(bool) error, run func([]string, string) error, message string) error {
	if len(workers) == 0 {
		return fmt.Errorf("no configured or running workers found for lease activation")
	}
	if err := setEnabled(false); err != nil {
		return fmt.Errorf("could not keep lease activation disabled: %w", err)
	}
	if err := run(composeStopGatewayArgs(startArgs, activeWorkers), "Stopping gateway before replacing workers..."); err != nil {
		return fmt.Errorf("could not stop gateway for safe worker upgrade: %w", err)
	}
	if err := run(composeUpGatewayArgs(startArgs, false), "Starting the lease-disabled gateway and its dependencies before replacing workers..."); err != nil {
		_ = run(composeStopGatewayArgs(startArgs, nil), "Stopping gateway after failed disabled startup...")
		_ = setEnabled(false)
		return fmt.Errorf("could not start the lease-disabled gateway for worker upgrade: %w", err)
	}
	workers = sortedUnique(workers)
	if len(workers) > 0 {
		if err := run(composeUpForServices(startArgs, workers), "Replacing workers and waiting for lease-capable workers to become healthy..."); err != nil {
			return fmt.Errorf("worker upgrade did not become healthy; lease activation remains disabled: %w", err)
		}
	}
	if err := setEnabled(true); err != nil {
		return fmt.Errorf("workers are healthy but lease activation could not be enabled: %w", err)
	}
	if err := run(composeUpGatewayArgs(startArgs, true), "Restarting the gateway with leases enabled after workers are healthy..."); err != nil {
		_ = run(composeStopGatewayArgs(startArgs, nil), "Stopping gateway after failed lease activation...")
		_ = setEnabled(false)
		return fmt.Errorf("gateway activation failed; lease activation remains disabled: %w", err)
	}
	if err := run(composeUpWithHealthWait(finalArgs), message); err != nil {
		_ = run(composeStopGatewayArgs(startArgs, nil), "Stopping gateway after failed lease activation...")
		_ = setEnabled(false)
		return fmt.Errorf("gateway activation failed; lease activation remains disabled: %w", err)
	}
	if err := os.Remove(markerPath); err != nil {
		_ = run(composeStopGatewayArgs(startArgs, nil), "Stopping gateway because lease activation could not be finalized...")
		_ = setEnabled(false)
		return fmt.Errorf("gateway is healthy but activation marker could not be cleared; lease activation remains disabled: %w", err)
	}
	return nil
}

func composeWorkerServices(args []string) []string {
	var workers []string
	for _, service := range composeTargetServices(args) {
		if strings.HasPrefix(service, "worker-") {
			workers = append(workers, service)
		}
	}
	return sortedUnique(workers)
}

func resolveLeaseActivationWorkers(startArgs, configuredServices, activeServices []string) ([]string, error) {
	workers := append(composeWorkerServices(startArgs), activeServices...)
	if len(composeTargetServices(startArgs)) == 0 {
		for _, service := range configuredServices {
			if strings.HasPrefix(service, "worker-") {
				workers = append(workers, service)
			}
		}
	}
	workers = sortedUnique(workers)
	if len(workers) == 0 {
		return nil, fmt.Errorf("no configured or running workers found for lease activation")
	}
	return workers, nil
}

func composeTargetServices(args []string) []string {
	upIndex := -1
	for i, arg := range args {
		if arg == "up" {
			upIndex = i
			break
		}
	}
	if upIndex < 0 {
		return nil
	}
	servicesStart := false
	var services []string
	for i := upIndex + 1; i < len(args); i++ {
		arg := args[i]
		if !servicesStart && strings.HasPrefix(arg, "-") {
			if composeOptionTakesValue(arg) && i+1 < len(args) {
				i++
			}
			continue
		}
		servicesStart = true
		services = append(services, arg)
	}
	return sortedUnique(services)
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func composeOptionTakesValue(option string) bool {
	switch option {
	case "--pull", "--wait-timeout", "--scale", "--timeout", "-t":
		return true
	default:
		return false
	}
}

func composeStopGatewayArgs(startArgs, workers []string) []string {
	for i, arg := range startArgs {
		if arg == "up" {
			result := append([]string(nil), startArgs[:i]...)
			result = append(result, "stop", "gateway")
			return append(result, sortedUnique(workers)...)
		}
	}
	return append([]string(nil), startArgs...)
}

func composeConfigServicesArgs(startArgs []string) []string {
	for i, arg := range startArgs {
		if arg == "up" {
			result := append([]string(nil), startArgs[:i]...)
			return append(result, "config", "--services")
		}
	}
	return nil
}

func composeUpForServices(startArgs, services []string) []string {
	for i, arg := range startArgs {
		if arg != "up" {
			continue
		}
		result := append([]string(nil), startArgs[:i+1]...)
		for j := i + 1; j < len(startArgs); j++ {
			option := startArgs[j]
			if !strings.HasPrefix(option, "-") {
				break
			}
			result = append(result, option)
			if composeOptionTakesValue(option) && j+1 < len(startArgs) {
				j++
				result = append(result, startArgs[j])
			}
		}
		result = append(result, "--wait", "--wait-timeout", leaseActivationWaitSeconds)
		return append(result, services...)
	}
	return nil
}

func composeUpWithHealthWait(startArgs []string) []string {
	for i, arg := range startArgs {
		if arg != "up" {
			continue
		}
		result := append([]string(nil), startArgs[:i+1]...)
		result = append(result, "--wait", "--wait-timeout", leaseActivationWaitSeconds)
		return append(result, startArgs[i+1:]...)
	}
	return append([]string(nil), startArgs...)
}

func composeUpGatewayArgs(startArgs []string, noDeps bool) []string {
	for i, arg := range startArgs {
		if arg != "up" {
			continue
		}
		result := append([]string(nil), startArgs[:i+1]...)
		for j := i + 1; j < len(startArgs); j++ {
			option := startArgs[j]
			if !strings.HasPrefix(option, "-") {
				break
			}
			result = append(result, option)
			if composeOptionTakesValue(option) && j+1 < len(startArgs) {
				j++
				result = append(result, startArgs[j])
			}
		}
		if noDeps {
			result = append(result, "--no-deps")
		}
		result = append(result, "--wait", "--wait-timeout", leaseActivationWaitSeconds)
		return append(result, "gateway")
	}
	return nil
}

func composeUpWithRemoveOrphans(args []string) []string {
	for _, arg := range args {
		if arg == "--remove-orphans" {
			return args
		}
	}
	for i, arg := range args {
		if arg != "up" {
			continue
		}
		result := append([]string{}, args[:i+1]...)
		result = append(result, "--remove-orphans")
		return append(result, args[i+1:]...)
	}
	return args
}

// prepareProductionInfra starts stateful dependencies first and reconciles their
// stored credentials before stateless app/worker containers are created. Docker
// images pick up .env.production immediately, but Postgres/RabbitMQ keep the
// password stored in their data volumes from first boot; starting workers in the
// same compose call can race ahead and crash-loop with AMQP ACCESS_REFUSED.
func (a *App) prepareProductionInfra(upArgs []string) error {
	args := productionInfraUpArgs(upArgs)
	if len(args) == 0 {
		return nil
	}

	a.emitAndLog("launcher", "Preparing stateful dependencies before starting workers...")
	cmd := exec.Command("docker", args...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()
	out, err := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		a.emitAndLog("docker", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("failed to prepare production dependencies: %v\n%s", err, strings.TrimSpace(string(out)))
	}

	if err := a.reconcileProductionCredentials(); err != nil {
		return fmt.Errorf("failed to reconcile production credentials: %w", err)
	}
	return nil
}

func productionInfraUpArgs(upArgs []string) []string {
	upIndex := -1
	hasPullNever := false
	for i, arg := range upArgs {
		if arg == "up" && upIndex == -1 {
			upIndex = i
		}
		if arg == "--pull=never" {
			hasPullNever = true
		}
	}
	if upIndex == -1 {
		return nil
	}
	args := append([]string{}, upArgs[:upIndex]...)
	// `docker compose up -d` returns as soon as containers are running, before
	// RabbitMQ is necessarily ready to accept rabbitmqctl commands. Waiting for
	// the declared health checks prevents credential rotation from racing broker
	// startup on both fresh installs and upgrades that retain named volumes.
	args = append(args, "up", "-d", "--wait", "--wait-timeout", "120")
	if hasPullNever {
		args = append(args, "--pull=never")
	}
	return append(args, "postgres", "redis", "rabbitmq")
}

// composeEnv builds the environment every docker compose invocation runs with.
// UID/GID feed container user mapping (os.Getuid() returns -1 on Windows, so we
// coerce to 0), and COMPOSE_PROJECT_NAME is pinned so up/down/status agree
// regardless of the install directory's basename and existing named volumes
// (especially ligand-x_postgres_data) stay visible.
func (a *App) composeEnv() []string {
	uid := os.Getuid()
	gid := os.Getgid()
	if uid < 0 {
		uid = 0
	}
	if gid < 0 {
		gid = 0
	}
	env := append(os.Environ(),
		fmt.Sprintf("UID=%d", uid),
		fmt.Sprintf("GID=%d", gid),
		"COMPOSE_PROJECT_NAME=ligand-x",
	)
	if path, ok := a.proSourcePath(); ok {
		env = append(env, fmt.Sprintf("LIGANDX_PRO_SRC_PATH=%s", path))
	}
	return env
}

// composeContextLine summarizes the key interpolation inputs for the log: the
// working directory, the UID/GID passed to compose, and the pinned VERSION read
// from .env.production (an unset/stale VERSION is a known Windows failure mode).
func (a *App) composeContextLine() string {
	uid := os.Getuid()
	gid := os.Getgid()
	if uid < 0 {
		uid = 0
	}
	if gid < 0 {
		gid = 0
	}
	version := "(unknown)"
	if content, err := a.GetEnvContent("prod"); err == nil {
		if v := strings.TrimSpace(envfile.Parse(content)["VERSION"]); v != "" {
			version = v
		}
	}
	return fmt.Sprintf("Context: dir=%s UID=%d GID=%d COMPOSE_PROJECT_NAME=ligand-x VERSION=%s", a.projectPath, uid, gid, version)
}

// captureComposePs runs `docker compose ... ps --all` with the same global flags
// as the failed up command and records the result. This surfaces the container
// that actually failed/stayed unhealthy without the user knowing to ask. upArgs
// is the original "compose ... up ..." arg list; everything before "up" is the
// set of global compose flags (--env-file, -f overlays) we must reuse.
func (a *App) captureComposePs(upArgs []string) {
	cmd := exec.Command("docker", composePsArgs(upArgs)...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		a.emitAndLog("docker", fmt.Sprintf("compose ps --all failed: %v", err))
		return
	}
	a.emitAndLog("docker", "container states after failure (compose ps --all):\n"+strings.TrimSpace(string(out)))
}

// composePsArgs derives a `compose ... ps --all` arg list from an `up` arg list
// by reusing every global flag that precedes "up" (--env-file and any -f
// overlays) so the status query sees the same merged project as the up call.
func composePsArgs(upArgs []string) []string {
	psArgs := make([]string, 0, len(upArgs))
	for _, arg := range upArgs {
		if arg == "up" {
			break
		}
		psArgs = append(psArgs, arg)
	}
	return append(psArgs, "ps", "--all")
}

// isProductionUpCommand reports whether a docker-compose invocation started
// containers (a "compose ... up ..." call) using .env.production, i.e. one
// where reconcileProductionCredentials should run afterward.
func isProductionUpCommand(args []string) bool {
	hasUp, usesProdEnv := false, false
	for i, arg := range args {
		if arg == "up" {
			hasUp = true
		}
		if arg == "--env-file" && i+1 < len(args) && strings.Contains(args[i+1], ".env.production") {
			usesProdEnv = true
		}
	}
	return hasUp && usesProdEnv
}

// reconcileProductionCredentials re-syncs the already-running Postgres and
// RabbitMQ containers' actual stored credentials with whatever is currently in
// .env.production. Postgres only applies POSTGRES_PASSWORD at first initdb, and
// RabbitMQ only applies RABBITMQ_DEFAULT_PASS on its first boot, so a data
// volume reused from an earlier install (e.g. after .env.production was reset
// or a fresh runtime bundle was extracted into a new directory) silently
// desyncs from a newly generated .env.production: every stateless container
// (gateway, workers, frontend, proxy) picks up the new secret, but Postgres and
// RabbitMQ keep authenticating with whatever was baked in at first boot. That
// mismatch surfaces as "password authentication failed" / AMQP ACCESS_REFUSED,
// crash-looping workers, and an unhealthy gateway that blocks frontend/proxy
// from ever starting (their depends_on: condition: service_healthy never
// passes). Both syncs below are idempotent and go through local admin paths
// that don't require knowing the previous password: Postgres via the
// trust-authenticated local socket, RabbitMQ via rabbitmqctl, which changes a
// user's password without needing the old one.
func (a *App) reconcileProductionCredentials() error {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return err
	}
	cur := envfile.Parse(content)
	var failures []string

	if pgUser, pgPass := cur["POSTGRES_USER"], cur["POSTGRES_PASSWORD"]; pgUser != "" && pgPass != "" && isContainerRunning("ligandx-postgres") {
		sql := fmt.Sprintf("ALTER USER %s WITH PASSWORD '%s';", pgUser, strings.ReplaceAll(pgPass, "'", "''"))
		cmd := exec.Command("docker", "exec", "ligandx-postgres", "psql", "-U", pgUser, "-d", pgUser, "-c", sql)
		if out, err := cmd.CombinedOutput(); err != nil {
			failures = append(failures, fmt.Sprintf("postgres: %v: %s", err, strings.TrimSpace(string(out))))
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   fmt.Sprintf("Credential reconciliation (postgres) failed: %v: %s", err, strings.TrimSpace(string(out))),
				Timestamp: time.Now().Format("15:04:05"),
			})
		}
	}

	if rmqUser, rmqPass := cur["RABBITMQ_USER"], cur["RABBITMQ_PASSWORD"]; rmqUser != "" && rmqPass != "" && isContainerRunning("ligandx-rabbitmq") {
		if err := reconcileRabbitMQUser("ligandx-rabbitmq", rmqUser, rmqPass); err != nil {
			failures = append(failures, fmt.Sprintf("rabbitmq: %v", err))
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   fmt.Sprintf("Credential reconciliation (rabbitmq) failed: %v", err),
				Timestamp: time.Now().Format("15:04:05"),
			})
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func reconcileRabbitMQUser(containerName, username, password string) error {
	changeCmd := exec.Command("docker", "exec", containerName, "rabbitmqctl", "change_password", username, password)
	if out, err := changeCmd.CombinedOutput(); err == nil {
		return ensureRabbitMQUserAccess(containerName, username)
	} else if !strings.Contains(strings.ToLower(string(out)), "no_such_user") {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}

	addCmd := exec.Command("docker", "exec", containerName, "rabbitmqctl", "add_user", username, password)
	if out, err := addCmd.CombinedOutput(); err != nil && !strings.Contains(strings.ToLower(string(out)), "user_already_exists") {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return ensureRabbitMQUserAccess(containerName, username)
}

func ensureRabbitMQUserAccess(containerName, username string) error {
	permCmd := exec.Command("docker", "exec", containerName, "rabbitmqctl", "set_permissions", "-p", "/", username, ".*", ".*", ".*")
	if out, err := permCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	tagsCmd := exec.Command("docker", "exec", containerName, "rabbitmqctl", "set_user_tags", username, "administrator")
	if out, err := tagsCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isContainerRunning(name string) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// composeLogPath returns the persistent on-disk log file the launcher tees all
// docker output to. The launcher's only error surface used to be transient
// Wails events ("docker compose failed: exit status 1"), so a Windows failure
// could never be diagnosed after the fact or carried to another machine. This
// file is the durable record. It lives under the user config dir so it survives
// reinstalls of the runtime bundle; if that can't be resolved we fall back to
// the project directory.
func (a *App) composeLogPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = a.projectPath
	}
	dir := filepath.Join(base, "ligandx-launcher", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil && a.projectPath != "" {
		dir = a.projectPath
	}
	return filepath.Join(dir, "launcher-compose.log")
}

// rotateComposeLogIfLarge keeps the persistent log from growing without bound by
// rolling it over to a single .old sibling once it passes ~5 MB. Called once per
// docker invocation, not per line.
func (a *App) rotateComposeLogIfLarge() {
	a.composeLogMux.Lock()
	defer a.composeLogMux.Unlock()
	path := a.composeLogPath()
	if info, err := os.Stat(path); err == nil && info.Size() > 5*1024*1024 {
		_ = os.Rename(path, path+".old")
	}
}

// logToFile appends a timestamped line to the persistent launcher log. Best
// effort: a logging failure must never break a docker operation.
func (a *App) logToFile(service, message string) {
	a.composeLogMux.Lock()
	defer a.composeLogMux.Unlock()
	f, err := os.OpenFile(a.composeLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	envfile.EnsurePrivateMode(a.composeLogPath())
	fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), service, message)
}

// emitAndLog sends a log line to the live UI (as before) and also persists it to
// the on-disk log so the same information is available after the fact.
func (a *App) emitAndLog(service, message string) {
	// ctx is nil before Wails startup (and under test) — emitting then would
	// panic, but the on-disk log should still get the line.
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   service,
			Message:   message,
			Timestamp: time.Now().Format("15:04:05"),
		})
	}
	a.logToFile(service, message)
}

// stderrTail is a thread-safe ring buffer of the most recent output lines. It
// lets runDockerCompose surface *why* docker failed in the returned error
// instead of a bare "exit status 1".
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (t *stderrTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

func (a *App) streamOutput(r io.Reader, service string) {
	a.streamOutputCapture(r, service, nil)
}

// streamOutputCapture streams a docker pipe to the live UI and the on-disk log,
// optionally also collecting lines into a tail buffer for error reporting. The
// larger scanner buffer prevents long compose/pull lines from being dropped.
func (a *App) streamOutputCapture(r io.Reader, service string, sink *stderrTail) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		a.emitAndLog(service, line)
		if sink != nil {
			sink.add(line)
		}
	}
}

func (a *App) ensureDataDirs() error {
	dirs := []string{
		"data/rbfe_outputs", "data/abfe_outputs", "data/docking_outputs",
		"data/md_outputs", "data/boltz_outputs", "data/qc_jobs",
		"data/qc_results_db", "data/msa_cache", "data/reinvent_campaigns", "data/kinetics_jobs",
	}

	for _, dir := range dirs {
		fullPath := filepath.Join(a.projectPath, dir)
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) OpenBrowser(url string) {
	var cmd *exec.Cmd

	switch goruntime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	cmd.Start()
}

// The Open* handlers read the live port from .env.production so they still work
// after a conflict moved one (see fitPublishedPorts).

func (a *App) OpenFrontend() {
	// reverse proxy (APP_PORT); single same-origin entry
	a.OpenBrowser(fmt.Sprintf("http://localhost:%d", a.envPort("APP_PORT", 8080)))
}

func (a *App) OpenAPI() {
	a.OpenBrowser(fmt.Sprintf("http://localhost:%d/docs", a.envPort("GATEWAY_PORT", 8000)))
}

// CreateAgentSetup authenticates locally using the launcher's protected runtime
// configuration, then returns a paste-ready workspace handoff. The password is
// used only for this loopback request and is never included in the result.
func (a *App) CreateAgentSetup() (AgentSetup, error) {
	return a.createAgentSetup(false)
}

// CreateAgentSetupWithExecution makes the execution authority an explicit
// per-session choice in the launcher UI. The default Connect path remains
// planning/read-only.
func (a *App) CreateAgentSetupWithExecution(allowExecution bool) (AgentSetup, error) {
	return a.createAgentSetup(allowExecution)
}

func (a *App) withLocalBrowserSession(fn func(client *http.Client, port int, apiKey string) error) error {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return err
	}
	env := envfile.Parse(content)
	username := strings.TrimSpace(env["LIGANDX_USERNAME"])
	password := env["LIGANDX_PASSWORD"]
	if username == "" || password == "" || password == "CHANGE_ME" {
		return fmt.Errorf("finish setting a local Ligand-X account before managing assistant access")
	}
	port := a.envPort("GATEWAY_PORT", 8000)
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/auth/login", port), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Ligand-X must be running before managing assistant access: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not authenticate to manage assistant access (gateway returned HTTP %d)", resp.StatusCode)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&login); err != nil || login.Token == "" {
		return fmt.Errorf("gateway returned an invalid assistant access response")
	}
	defer func() {
		logoutReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/auth/logout", port), nil)
		logoutReq.Header.Set("X-API-Key", login.Token)
		if logoutResp, _ := client.Do(logoutReq); logoutResp != nil {
			logoutResp.Body.Close()
		}
	}()
	return fn(client, port, login.Token)
}

func (a *App) createAgentSetup(allowExecution bool) (AgentSetup, error) {
	store := a.sessionStore()
	if err := store.Available(); err != nil {
		return AgentSetup{}, fmt.Errorf("%s. Ligand-X itself still works; unlock %s and reconnect the assistant", err.Error(), store.Name())
	}
	sessionID, err := agentsession.NewSessionID()
	if err != nil {
		return AgentSetup{}, err
	}
	privateHex, publicHex, err := agentsession.NewSigningKey()
	if err != nil {
		return AgentSetup{}, err
	}
	legacyFiles := agentsession.ListLegacyTokenFiles(a.projectPath)
	scopes := []string{"projects:read", "projects:create", "inputs:prepare", "jobs:read", "jobs:plan"}
	if allowExecution {
		scopes = append(scopes, "jobs:submit")
	}
	var credential struct {
		Token        string `json:"token"`
		ExpiresAt    string `json:"expires_at"`
		CredentialID string `json:"credential_id"`
	}
	err = a.withLocalBrowserSession(func(client *http.Client, port int, apiKey string) error {
		if len(legacyFiles) > 0 {
			revokeReq, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://127.0.0.1:%d/api/agent/v1/credentials", port), nil)
			revokeReq.Header.Set("X-API-Key", apiKey)
			revokeResp, revokeErr := client.Do(revokeReq)
			if revokeErr != nil {
				return fmt.Errorf("could not revoke the previous file-based assistant session: %w", revokeErr)
			}
			revokeResp.Body.Close()
			if revokeResp.StatusCode != http.StatusNoContent {
				return fmt.Errorf("could not revoke the previous file-based assistant session (HTTP %d)", revokeResp.StatusCode)
			}
		}
		credentialRequest, _ := json.Marshal(map[string]interface{}{
			"expires_in_minutes": 480,
			"scopes":             scopes,
			"session_id":         sessionID,
			"public_key":         publicHex,
		})
		credentialReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/agent/v1/credentials", port), bytes.NewReader(credentialRequest))
		credentialReq.Header.Set("Content-Type", "application/json")
		credentialReq.Header.Set("X-API-Key", apiKey)
		credentialResp, credErr := client.Do(credentialReq)
		if credErr != nil {
			return fmt.Errorf("could not create dedicated assistant access: %w", credErr)
		}
		defer credentialResp.Body.Close()
		if credentialResp.StatusCode != http.StatusCreated {
			return fmt.Errorf("could not create dedicated assistant access (gateway returned HTTP %d)", credentialResp.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(credentialResp.Body, 64*1024)).Decode(&credential); err != nil || credential.Token == "" || credential.CredentialID == "" {
			return fmt.Errorf("gateway returned an invalid assistant credential")
		}
		return nil
	})
	if err != nil {
		return AgentSetup{}, err
	}
	meta := agentsession.Meta{
		SessionID:        sessionID,
		CredentialID:     credential.CredentialID,
		ExpiresAt:        credential.ExpiresAt,
		ExecutionEnabled: allowExecution,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	secret := agentsession.Secret{
		Token:        credential.Token,
		SigningKey:   privateHex,
		CredentialID: credential.CredentialID,
		Scopes:       scopes,
	}
	if err := agentsession.Persist(store, a.projectPath, meta, secret); err != nil {
		_ = a.withLocalBrowserSession(func(client *http.Client, port int, apiKey string) error {
			revokeReq, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://127.0.0.1:%d/api/agent/v1/credentials/%s", port, credential.CredentialID), nil)
			revokeReq.Header.Set("X-API-Key", apiKey)
			if revokeResp, revokeErr := client.Do(revokeReq); revokeResp != nil {
				revokeResp.Body.Close()
			} else if revokeErr != nil {
				return revokeErr
			}
			return nil
		})
		return AgentSetup{}, err
	}
	if len(legacyFiles) > 0 {
		agentsession.DeleteLegacyTokenFiles(a.projectPath)
	}
	executable, err := os.Executable()
	if err != nil {
		return AgentSetup{}, fmt.Errorf("resolve launcher connector executable: %w", err)
	}
	mcpConfig, err := agentsession.MCPConfigJSON(executable, a.projectPath, sessionID)
	if err != nil {
		return AgentSetup{}, err
	}
	config, _ := a.GetLauncherConfig()
	instructions := agentsession.SetupInstructions(string(mcpConfig), credential.ExpiresAt, agentSetupGPUShortWarning(config.SelectedGroups), allowExecution)
	return AgentSetup{
		Instructions:   instructions,
		ExpiresAt:      credential.ExpiresAt,
		SessionID:      sessionID,
		CredentialID:   credential.CredentialID,
		MigratedLegacy: len(legacyFiles) > 0,
	}, nil
}

func (a *App) GetAgentStorageStatus() agentsession.StorageStatus {
	return agentsession.Status(a.sessionStore())
}

func (a *App) ListAgentSessions() (agentsession.List, error) {
	store := a.sessionStore()
	status := agentsession.Status(store)
	sessions, err := agentsession.LoadMetadata(a.projectPath)
	if err != nil {
		return agentsession.List{Storage: status, LegacyFiles: len(agentsession.ListLegacyTokenFiles(a.projectPath))}, err
	}
	infos := make([]agentsession.Info, 0, len(sessions))
	for _, meta := range sessions {
		info := agentsession.Info{
			SessionID:        meta.SessionID,
			CredentialID:     meta.CredentialID,
			ExpiresAt:        meta.ExpiresAt,
			ExecutionEnabled: meta.ExecutionEnabled,
			CreatedAt:        meta.CreatedAt,
		}
		if status.Available {
			if _, err := agentsession.LoadSecret(store, meta.SessionID); err == nil {
				info.SecretPresent = true
			}
		}
		infos = append(infos, info)
	}
	return agentsession.List{
		Sessions:    infos,
		LegacyFiles: len(agentsession.ListLegacyTokenFiles(a.projectPath)),
		Storage:     status,
	}, nil
}

func (a *App) CopyAgentSessionConfig(sessionID string) (string, error) {
	if _, err := hex.DecodeString(sessionID); err != nil || len(sessionID) != 32 {
		return "", fmt.Errorf("assistant session identifier is invalid")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve launcher connector executable: %w", err)
	}
	raw, err := agentsession.MCPConfigJSON(executable, a.projectPath, sessionID)
	if err != nil {
		return "", err
	}
	if strings.Contains(string(raw), "LIGANDX_AGENT_TOKEN") || strings.Contains(string(raw), "signing_key") {
		return "", fmt.Errorf("refusing to copy a configuration that would expose assistant secrets")
	}
	return string(raw), nil
}

func (a *App) CheckAgentSessionHealth(sessionID string) (agentsession.Health, error) {
	store := a.sessionStore()
	health := agentsession.Health{SessionID: sessionID, Status: "unhealthy"}
	if err := store.Available(); err != nil {
		health.Status = "storage_unavailable"
		health.Detail = fmt.Sprintf("%s. Ligand-X itself still works; unlock %s to use this assistant session.", err.Error(), store.Name())
		return health, nil
	}
	secret, err := agentsession.LoadSecret(store, sessionID)
	if err != nil {
		health.Status = "missing_secret"
		health.Detail = "This session is missing from protected storage. Reconnect the assistant from Ligand-X Launcher."
		return health, nil
	}
	headers, err := agentsession.ProofHeaders(secret, http.MethodGet, "/api/agent/v1/capabilities", nil)
	if err != nil {
		health.Detail = err.Error()
		return health, nil
	}
	port := a.envPort("GATEWAY_PORT", 8000)
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/agent/v1/capabilities", port), nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		health.Status = "unreachable"
		health.Detail = "Ligand-X must be running before an assistant session can be checked."
		return health, nil
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	switch resp.StatusCode {
	case http.StatusOK:
		health.Status = "healthy"
		health.Detail = "This assistant session can reach Ligand-X with a signed proof."
	case http.StatusUnauthorized, http.StatusForbidden:
		health.Status = "revoked_or_expired"
		health.Detail = "The server rejected this session. Reconnect the assistant from Ligand-X Launcher."
	default:
		health.Detail = fmt.Sprintf("gateway returned HTTP %d", resp.StatusCode)
	}
	return health, nil
}

func (a *App) RevokeAgentSession(sessionID string) error {
	store := a.sessionStore()
	sessions, _ := agentsession.LoadMetadata(a.projectPath)
	var credentialID string
	for _, meta := range sessions {
		if meta.SessionID == sessionID {
			credentialID = meta.CredentialID
			break
		}
	}
	if credentialID != "" {
		err := a.withLocalBrowserSession(func(client *http.Client, port int, apiKey string) error {
			revokeReq, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://127.0.0.1:%d/api/agent/v1/credentials/%s", port, credentialID), nil)
			revokeReq.Header.Set("X-API-Key", apiKey)
			revokeResp, revokeErr := client.Do(revokeReq)
			if revokeErr != nil {
				return fmt.Errorf("could not revoke assistant session: %w", revokeErr)
			}
			revokeResp.Body.Close()
			if revokeResp.StatusCode != http.StatusNoContent && revokeResp.StatusCode != http.StatusNotFound {
				return fmt.Errorf("gateway could not revoke assistant session (HTTP %d)", revokeResp.StatusCode)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return agentsession.Delete(store, a.projectPath, sessionID)
}

// RevokeAgentAccess invalidates all dedicated assistant workspace tokens for
// this local user without changing browser credentials or account passwords.
func (a *App) RevokeAgentAccess() error {
	store := a.sessionStore()
	err := a.withLocalBrowserSession(func(client *http.Client, port int, apiKey string) error {
		revokeReq, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://127.0.0.1:%d/api/agent/v1/credentials", port), nil)
		revokeReq.Header.Set("X-API-Key", apiKey)
		revokeResp, revokeErr := client.Do(revokeReq)
		if revokeErr != nil {
			return fmt.Errorf("could not revoke assistant access: %w", revokeErr)
		}
		revokeResp.Body.Close()
		if revokeResp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("gateway could not revoke assistant access (HTTP %d)", revokeResp.StatusCode)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sessions, _ := agentsession.LoadMetadata(a.projectPath)
	for _, meta := range sessions {
		_ = agentsession.Delete(store, a.projectPath, meta.SessionID)
	}
	agentsession.DeleteLegacyTokenFiles(a.projectPath)
	return nil
}

func (a *App) OpenFlower() {
	a.OpenBrowser(fmt.Sprintf("http://localhost:%d/flower", a.envPort("FLOWER_PORT", 5555)))
}

func (a *App) GetProjectPath() string {
	return a.projectPath
}

func (a *App) SetProjectPath(path string) error {
	composePath := filepath.Join(path, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return fmt.Errorf("docker-compose.yml not found in %s", path)
	}

	a.projectPath, _ = filepath.Abs(path)
	return nil
}

func (a *App) SelectProjectFolder() (string, error) {
	path, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Ligand-X Project Folder",
	})
	if err != nil {
		return "", err
	}

	if path == "" {
		return "", nil
	}

	if err := a.SetProjectPath(path); err != nil {
		return "", err
	}

	return a.projectPath, nil
}

// BrowseForFolder opens a directory picker with a custom title and returns
// the selected path without changing any app state.
func (a *App) BrowseForFolder(title string) (string, error) {
	path, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: title,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) GetEnvContent(mode string) (string, error) {
	var envFile, templateFile string
	if mode == "prod" {
		envFile = ".env.production"
		templateFile = ".env.production.template"
	} else {
		envFile = ".env"
		templateFile = ".env.example"
	}

	envPath := filepath.Join(a.projectPath, envFile)
	data, err := os.ReadFile(envPath)
	if err == nil {
		envfile.EnsurePrivateMode(envPath)
		return string(data), nil
	}

	// env file doesn't exist — load template and auto-save it as the env file
	templatePath := filepath.Join(a.projectPath, templateFile)
	data, err = os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("no %s file found and could not read %s: %v", envFile, templateFile, err)
	}

	// Write template as the starting env file so docker compose can read it immediately
	_ = envfile.WritePrivate(envPath, data)

	return string(data), nil
}

func (a *App) SaveEnvContent(mode string, content string) error {
	var envFile string
	if mode == "prod" {
		envFile = ".env.production"
	} else {
		envFile = ".env"
	}
	envPath := filepath.Join(a.projectPath, envFile)
	return envfile.WritePrivate(envPath, []byte(content))
}

// getReinventModelsPath reads REINVENT_MODELS_PATH from .env, falling back to /opt/reinvent_models.
func (a *App) getReinventModelsPath() string {
	envPath := filepath.Join(a.projectPath, ".env")
	data, err := os.ReadFile(envPath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "REINVENT_MODELS_PATH=") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "REINVENT_MODELS_PATH="))
				if val != "" {
					return val
				}
			}
		}
	}
	return "/opt/reinvent_models"
}

func (a *App) GetReinventModelsPath() string {
	return a.getReinventModelsPath()
}

func (a *App) CheckReinventModels() bool {
	_, err := os.Stat(filepath.Join(a.getReinventModelsPath(), "reinvent.prior"))
	return err == nil
}

// zenodoFileEntry holds the key, size, and content download URL for a Zenodo file.
type zenodoFileEntry struct {
	Key  string
	Size int64
	URL  string
}

// resolveZenodoFiles queries the Zenodo files API for a record and returns file entries.
func resolveZenodoFiles(recordID string) ([]zenodoFileEntry, error) {
	apiURL := "https://zenodo.org/api/records/" + recordID + "/files"
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Zenodo API returned HTTP %d for record %s", resp.StatusCode, recordID)
	}
	var result struct {
		Entries []struct {
			Key   string `json:"key"`
			Size  int64  `json:"size"`
			Links struct {
				Content string `json:"content"`
			} `json:"links"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse Zenodo file list: %w", err)
	}
	var files []zenodoFileEntry
	for _, e := range result.Entries {
		files = append(files, zenodoFileEntry{Key: e.Key, Size: e.Size, URL: e.Links.Content})
	}
	return files, nil
}

// setEnvValue writes or updates a KEY=VALUE line in the dev .env file.
func (a *App) setEnvValue(key, value string) error {
	return a.setEnvFileValue(".env", key, value)
}

func (a *App) setProductionEnvValue(key, value string) error {
	return a.setEnvFileValue(".env.production", key, value)
}

// setEnvFileValue is the single-key form of setEnvFileValues. It delegates
// rather than duplicating the last-wins rule: two implementations of that rule
// drifted apart once already (setEnvFileValue matched `KEY=` literally, so a
// hand-edited `KEY = value` was missed and a second definition appended).
func (a *App) setEnvFileValue(fileName, key, value string) error {
	return a.setEnvFileValues(fileName, map[string]string{key: value})
}

func (a *App) setProductionEnvValues(values map[string]string) error {
	return a.setEnvFileValues(".env.production", values)
}

// setEnvFileValues writes KEY=VALUE for each entry, in one read/write pass so
// rewriting ~20 resource keys doesn't rewrite the file 20 times.
//
// It writes the *last* definition of a key and retires any earlier ones,
// because last-wins is what both parseEnvFile and compose's dotenv parser do.
// Writing the first occurrence instead — as this did originally — silently
// changes a line nobody reads: a user who inserted WORKER_CPU_CPU_LIMIT=6 above
// the template's =16 saw neither their edit nor the resource fitting take
// effect, because compose kept resolving the 16 further down the file.
func (a *App) setEnvFileValues(fileName string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	envPath := filepath.Join(a.projectPath, fileName)
	data, _ := os.ReadFile(envPath)
	lines := strings.Split(string(data), "\n")

	// Pass 1: find the effective (last) definition of each key we're writing.
	last := make(map[string]int, len(values))
	for i, l := range lines {
		if key := envfile.KeyOnLine(l); key != "" {
			if _, ok := values[key]; ok {
				last[key] = i
			}
		}
	}
	// Pass 2: write it, and comment out every earlier definition of the same key
	// so exactly one live definition survives. Leaving them would let the file
	// keep a value that contradicts the one we just wrote.
	for i, l := range lines {
		key := envfile.KeyOnLine(l)
		if key == "" {
			continue
		}
		j, ok := last[key]
		if !ok {
			continue
		}
		if i == j {
			lines[i] = key + "=" + values[key]
		} else {
			lines[i] = envfile.SupersededComment + strings.TrimSpace(l)
		}
	}
	// Append anything the file didn't already declare, in a stable order.
	for _, k := range sortedKeys(values) {
		if _, ok := last[k]; !ok {
			lines = append(lines, k+"="+values[k])
		}
	}
	return envfile.WritePrivate(envPath, []byte(strings.Join(lines, "\n")))
}

// productionEnvPath is the absolute path of the file compose reads through
// --env-file. Worth naming in full in any message about it: on Windows it sits
// under %AppData% where a user is unlikely to look, and "the .env.production
// you edited" is only actionable if they know which one that is.
func (a *App) productionEnvPath() string {
	return filepath.Join(a.projectPath, ".env.production")
}

// ensureProductionEnv makes sure .env.production exists with real secrets. It is
// idempotent: it only fills a key when its current value is missing or a
// CHANGE_ME placeholder, so repeated calls (e.g. on every start) never rotate
// already-generated passwords and break the Postgres/RabbitMQ data volumes.
func (a *App) ensureProductionEnv() error {
	content, err := a.GetEnvContent("prod") // seeds from template if missing
	if err != nil {
		return err
	}
	cur := envfile.Parse(content)

	// A key defined twice is invisible in an editor but decisive at runtime:
	// compose resolves the last one, so an override inserted above the original
	// does nothing. Say so before anything else, because it is the explanation
	// for "I edited .env.production and it made no difference". The writes below
	// collapse duplicates as they touch each key, but only for keys they touch.
	// Same class of problem as a duplicate key, and the same symptom: a file the
	// user edited that Docker never reads. Reported at start rather than only
	// after a failure, because it explains far more than oversized CPU limits.
	if stray := strayProductionEnvFiles(a.projectPath); len(stray) > 0 {
		a.emitAndLog("launcher", fmt.Sprintf(
			"Warning: found %s next to %s. Docker reads ONLY .env.production — if you edited one of "+
				"those by mistake (Windows File Explorer hides file extensions), your changes are not being applied.",
			strings.Join(stray, ", "), a.productionEnvPath()))
	}

	if dups := envfile.DuplicateKeys(content); len(dups) > 0 {
		a.emitAndLog("launcher", fmt.Sprintf(
			"Warning: %s defines these keys more than once, and only the last definition of each takes effect: %s",
			a.productionEnvPath(), strings.Join(dups, ", ")))
	}

	// setIfPlaceholder writes only when the existing value is empty/CHANGE_ME,
	// and keeps cur in sync so derived URLs can reference fresh secrets.
	setIfPlaceholder := func(key, value string) error {
		if envfile.IsPlaceholder(cur[key]) {
			cur[key] = value
			return a.setProductionEnvValue(key, value)
		}
		return nil
	}

	// Generate any missing secrets.
	secretKeys := []string{"POSTGRES_PASSWORD", "RABBITMQ_PASSWORD", "REDIS_PASSWORD", "QC_SECRET_KEY", "LIGANDX_PASSWORD", "FLOWER_PASSWORD", "INTERNAL_WORKER_SECRET"}
	for _, key := range secretKeys {
		if envfile.IsPlaceholder(cur[key]) {
			v, err := generateAPIKey()
			if err != nil {
				return err
			}
			if err := setIfPlaceholder(key, v); err != nil {
				return err
			}
		}
	}

	// Fixed identities.
	if err := setIfPlaceholder("POSTGRES_USER", "ligandx"); err != nil {
		return err
	}
	if err := setIfPlaceholder("POSTGRES_DB", "ligandx"); err != nil {
		return err
	}
	if err := setIfPlaceholder("RABBITMQ_USER", "ligandx"); err != nil {
		return err
	}

	// Derived connection URLs — only (re)written while still placeholders, using
	// whatever secrets are now in cur.
	if err := setIfPlaceholder("DATABASE_URL", fmt.Sprintf("postgresql://ligandx:%s@postgres:5432/ligandx", cur["POSTGRES_PASSWORD"])); err != nil {
		return err
	}
	if err := setIfPlaceholder("CELERY_BROKER_URL", fmt.Sprintf("amqp://ligandx:%s@rabbitmq:5672/", cur["RABBITMQ_PASSWORD"])); err != nil {
		return err
	}
	if err := setIfPlaceholder("CELERY_RESULT_BACKEND", fmt.Sprintf("redis://:%s@redis:6379/0", cur["REDIS_PASSWORD"])); err != nil {
		return err
	}
	if err := setIfPlaceholder("REDIS_URL", fmt.Sprintf("redis://:%s@redis:6379/0", cur["REDIS_PASSWORD"])); err != nil {
		return err
	}

	// Same-origin via the bundled reverse proxy: browser uses its own origin.
	if err := a.setProductionEnvValue("NEXT_PUBLIC_API_URL", ""); err != nil {
		return err
	}
	// Derived from APP_PORT, not hard-coded: the port moves when 8080 is taken
	// (see fitPublishedPorts), and a stale CORS origin would leave the stack
	// running but the UI unable to call the API — a far more confusing failure
	// than the bind error we just avoided.
	appPort := portOrFallback(cur["APP_PORT"], 8080)
	corsOrigins := "http://localhost:3000,http://127.0.0.1:3000"
	if appPort != 3000 {
		corsOrigins += fmt.Sprintf(",http://localhost:%d,http://127.0.0.1:%d", appPort, appPort)
	}
	if err := a.setProductionEnvValue("CORS_ORIGINS", corsOrigins); err != nil {
		return err
	}

	// Enforce a pinned image VERSION. This runs on every start/pull, so it
	// self-heals a stale .env.production that an older launcher pinned to
	// "latest" (or left empty) — values that docker compose's ${VERSION:?} and
	// requirePinnedProductionVersion both reject. The canonical pin is the
	// template's VERSION (single source of truth), only applied when the current
	// value is not already a valid pin so user-chosen pins are preserved.
	if !envfile.IsPinnedVersion(cur["VERSION"]) {
		pinned := a.templatePinnedVersion()
		if pinned == "" {
			pinned = defaultPinnedImageVersion
		}
		if err := a.setProductionEnvValue("VERSION", pinned); err != nil {
			return err
		}
	}

	// Pro images are published on their own cadence, so compose resolves them
	// through ${PRO_VERSION:-${VERSION}}. An .env.production written by an older
	// launcher has no PRO_VERSION at all and would silently fall through to
	// VERSION — pulling Pro tags that were never built for a core-only release.
	// Seed it from the template whenever the template pins one and the local file
	// does not, without touching a value the user has chosen.
	if templatePro := a.templateValue("PRO_VERSION"); templatePro != "" && envfile.IsPlaceholder(cur["PRO_VERSION"]) {
		if err := a.setProductionEnvValue("PRO_VERSION", templatePro); err != nil {
			return err
		}
		cur["PRO_VERSION"] = templatePro
	}

	// gpu-short is shared between free and Pro work, so which image that worker
	// runs depends on the current selection rather than on the licence. Resolve
	// it here, on every compose invocation, so enabling a Pro group later starts
	// using the Pro worker and disabling it goes back to the credential-free
	// public one.
	if err := a.syncGPUShortImage(); err != nil {
		return err
	}

	// The template's resource limits describe a multi-GPU workstation. Docker
	// rejects any container whose `cpus` exceeds the daemon's CPU count, so on a
	// smaller machine the stack cannot start at all until these are cut down to
	// size. Runs on every start, so an .env.production carried over from bigger
	// hardware self-heals too.
	return a.fitResourceLimits(cur)
}

// syncGPUShortImage writes or clears LIGANDX_GPU_SHORT_IMAGE to match the groups
// currently selected.
//
// Clearing matters as much as setting: a stale Pro reference left behind after a
// Pro group is deselected would demand registry credentials the user may no
// longer have, and would fail the pull for a selection that is now entirely free.
func (a *App) syncGPUShortImage() error {
	config, err := a.GetLauncherConfig()
	if err != nil {
		// No selection recorded yet (first run) means nothing Pro is running.
		config = LauncherConfig{}
	}
	_, proPrefix := a.productionImageSettings()
	want := gpuShortImageOverride(config.SelectedGroups, proPrefix, a.productionProVersion())

	content, err := a.GetEnvContent("prod")
	if err != nil {
		return err
	}
	if strings.TrimSpace(envfile.Parse(content)["LIGANDX_GPU_SHORT_IMAGE"]) == want {
		return nil
	}
	return a.setProductionEnvValue("LIGANDX_GPU_SHORT_IMAGE", want)
}

// templatePinnedVersion returns the VERSION pinned in .env.production.template,
// or "" if the template is missing or its VERSION is not a concrete pin. This is
// the canonical image tag the bundle was published against.
func (a *App) templatePinnedVersion() string {
	v := a.templateValue("VERSION")
	if !envfile.IsPinnedVersion(v) {
		return ""
	}
	return v
}

// templateValue reads a single key from .env.production.template.
func (a *App) templateValue(key string) string {
	data, err := os.ReadFile(filepath.Join(a.projectPath, ".env.production.template"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(envfile.Parse(string(data))[key])
}

// GetUserSettings returns the user-facing subset of .env.production.
func (a *App) GetUserSettings() (UserSettings, error) {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return UserSettings{}, err
	}
	cur := envfile.Parse(content)

	cpuConc, _ := strconv.Atoi(cur["CPU_WORKER_CONCURRENCY"])
	gpuShort, _ := strconv.Atoi(cur["GPU_SHORT_CONCURRENCY"])
	gpuLong, _ := strconv.Atoi(cur["GPU_LONG_CONCURRENCY"])
	if cpuConc == 0 {
		cpuConc = 4
	}
	if gpuShort == 0 {
		gpuShort = 2
	}
	if gpuLong == 0 {
		gpuLong = 1
	}

	return UserSettings{
		CPUWorkerConcurrency: cpuConc,
		GPUShortConcurrency:  gpuShort,
		GPULongConcurrency:   gpuLong,
		OrcaHostPath:         cur["ORCA_HOST_PATH"],
		BoltzMSAUsername:     cur["BOLTZ_MSA_USERNAME"],
		BoltzMSAPassword:     cur["BOLTZ_MSA_PASSWORD"],
		BoltzMSAApiKey:       cur["MSA_API_KEY_VALUE"],
	}, nil
}

// SaveUserSettings writes user-facing settings back to .env.production.
func (a *App) SaveUserSettings(s UserSettings) error {
	settings := map[string]string{
		"CPU_WORKER_CONCURRENCY": strconv.Itoa(s.CPUWorkerConcurrency),
		"GPU_SHORT_CONCURRENCY":  strconv.Itoa(s.GPUShortConcurrency),
		"GPU_LONG_CONCURRENCY":   strconv.Itoa(s.GPULongConcurrency),
		"ORCA_HOST_PATH":         s.OrcaHostPath,
		"BOLTZ_MSA_USERNAME":     s.BoltzMSAUsername,
		"BOLTZ_MSA_PASSWORD":     s.BoltzMSAPassword,
		"MSA_API_KEY_VALUE":      s.BoltzMSAApiKey,
	}
	for key, val := range settings {
		if err := a.setProductionEnvValue(key, val); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOrcaHostPath reports whether path is a folder that contains an ORCA
// executable. The UI calls this after Browse and before confirming the dialog.
func (a *App) ValidateOrcaHostPath(path string) error {
	return validateOrcaHostPath(path)
}

// OrcaHostPathReady is true when .env.production already points at a real
// ORCA install. The template default /opt/orca does not count unless that
// folder actually contains the binary.
func (a *App) OrcaHostPathReady() bool {
	return validateOrcaHostPath(a.currentOrcaHostPath()) == nil
}

// SetOrcaHostPath validates path and writes ORCA_HOST_PATH without touching
// the rest of user settings.
func (a *App) SetOrcaHostPath(path string) error {
	if err := validateOrcaHostPath(path); err != nil {
		return err
	}
	return a.setProductionEnvValue("ORCA_HOST_PATH", strings.TrimSpace(path))
}

func (a *App) currentOrcaHostPath() string {
	envPath := filepath.Join(a.projectPath, ".env.production")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(envfile.Parse(string(data))["ORCA_HOST_PATH"])
}

// canWriteDir checks whether a directory can be created and written to.
func canWriteDir(dir string) bool {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".write_probe")
	f, err := os.Create(probe)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

func (a *App) DownloadReinventModels() {
	go func() {
		modelsPath := a.getReinventModelsPath()

		if !canWriteDir(modelsPath) {
			// Configured path not writable — fall back to project data dir and persist to .env.
			fallback := filepath.Join(a.projectPath, "data", "reinvent_models")
			if !canWriteDir(fallback) {
				wailsRuntime.EventsEmit(a.ctx, "reinventModelComplete", map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("No write access to %s or fallback %s", modelsPath, fallback),
				})
				return
			}
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   fmt.Sprintf("No write access to %s — using %s and updating .env", modelsPath, fallback),
				Timestamp: time.Now().Format("15:04:05"),
			})
			_ = a.setEnvValue("REINVENT_MODELS_PATH", fallback)
			modelsPath = fallback
		}

		// The concept DOI 10.5281/zenodo.15641296 always resolves to the latest version.
		// The concept record itself (15641296) is a tombstone; the actual published record
		// is 15641297 (and any future versions will be at a new ID). Use the files API on
		// the known latest record ID, which is the DOI target.
		const latestRecordID = "15641297"

		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Fetching REINVENT4 file list from Zenodo record %s ...", latestRecordID),
			Timestamp: time.Now().Format("15:04:05"),
		})

		files, err := resolveZenodoFiles(latestRecordID)
		if err != nil {
			wailsRuntime.EventsEmit(a.ctx, "reinventModelComplete", map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to fetch file list: %v", err),
			})
			return
		}

		// Only download reinvent.prior — the one required by the service.
		var target *zenodoFileEntry
		for i := range files {
			if files[i].Key == "reinvent.prior" {
				target = &files[i]
				break
			}
		}
		if target == nil {
			wailsRuntime.EventsEmit(a.ctx, "reinventModelComplete", map[string]interface{}{
				"success": false,
				"error":   "reinvent.prior not found in Zenodo record " + latestRecordID,
			})
			return
		}

		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Downloading %s (%.1f MB) to %s ...", target.Key, float64(target.Size)/1024/1024, modelsPath),
			Timestamp: time.Now().Format("15:04:05"),
		})

		destPath := filepath.Join(modelsPath, target.Key)
		if err := a.downloadFileWithProgress(target.URL, destPath, target.Key, target.Size); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "reinventModelComplete", map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Download failed: %v", err),
			})
			return
		}

		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   "REINVENT prior downloaded successfully",
			Timestamp: time.Now().Format("15:04:05"),
		})
		wailsRuntime.EventsEmit(a.ctx, "reinventModelComplete", map[string]interface{}{
			"success": true,
		})
	}()
}

func (a *App) downloadFileWithProgress(url, destPath, fileName string, knownSize int64) error {
	httpClient := &http.Client{}
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	totalSize := knownSize
	if totalSize <= 0 {
		totalSize = resp.ContentLength
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	var downloaded int64
	lastEmit := time.Now()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			downloaded += int64(n)

			if time.Since(lastEmit) >= 150*time.Millisecond {
				var pct float64
				if totalSize > 0 {
					pct = float64(downloaded) / float64(totalSize) * 100
				}
				wailsRuntime.EventsEmit(a.ctx, "reinventModelProgress", map[string]interface{}{
					"fileName":   fileName,
					"percent":    pct,
					"bytesDone":  downloaded,
					"bytesTotal": totalSize,
				})
				lastEmit = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	wailsRuntime.EventsEmit(a.ctx, "reinventModelProgress", map[string]interface{}{
		"fileName":   fileName,
		"percent":    float64(100),
		"bytesDone":  downloaded,
		"bytesTotal": downloaded,
	})

	return nil
}

func (a *App) ViewLogs(service string) error {
	a.stopLogStream(service)

	ctx, cancel := context.WithCancel(context.Background())

	a.logStreamsMux.Lock()
	a.logStreams[service] = cancel
	a.logStreamsMux.Unlock()

	go func() {
		args := append([]string{"compose"}, a.devEnvArgs()...)
		args = append(args, "logs", "-f", "--tail", "100")
		if service != "all" {
			args = append(args, service)
		}

		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Dir = a.projectPath

		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			return
		}

		go a.streamOutput(stdout, service)
		go a.streamOutput(stderr, service)

		cmd.Wait()
	}()

	return nil
}

func (a *App) StopLogStream(service string) {
	a.stopLogStream(service)
}

func (a *App) stopLogStream(service string) {
	a.logStreamsMux.Lock()
	defer a.logStreamsMux.Unlock()

	if cancel, ok := a.logStreams[service]; ok {
		cancel()
		delete(a.logStreams, service)
	}
}

func (a *App) stopAllLogStreams() {
	a.logStreamsMux.Lock()
	defer a.logStreamsMux.Unlock()

	for _, cancel := range a.logStreams {
		cancel()
	}
	a.logStreams = make(map[string]context.CancelFunc)
}

func (a *App) pullImageWithProgress(ctx context.Context, image, groupID, groupName string, imageIndex, totalImages int, registryAuth string) error {
	// Track layer-level progress
	type layerState struct {
		status    string
		current   int64
		total     int64
		startTime time.Time
	}
	layers := make(map[string]*layerState)
	var lastEmitPercent float64
	var lastEmitTime time.Time

	// Use Docker API directly for structured JSON stream
	reader, err := a.dockerClient.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: registryAuth})
	if err != nil {
		return fmt.Errorf("failed to pull %s: %v", image, err)
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var msg struct {
			Status         string `json:"status"`
			Error          string `json:"error"`
			ID             string `json:"id"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}

		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue // Skip non-JSON lines
		}

		// Handle errors in stream
		if msg.Error != "" {
			return fmt.Errorf("docker pull error: %s", msg.Error)
		}

		// Update or create layer state
		if msg.ID != "" {
			if _, ok := layers[msg.ID]; !ok {
				layers[msg.ID] = &layerState{startTime: time.Now()}
			}
			layers[msg.ID].status = msg.Status
			if msg.ProgressDetail.Total > 0 {
				layers[msg.ID].current = msg.ProgressDetail.Current
				layers[msg.ID].total = msg.ProgressDetail.Total
			}
		}

		// Calculate per-image progress
		var totalBytes int64
		var downloadedBytes int64
		for _, layer := range layers {
			totalBytes += layer.total
			if layer.status == "Downloading" || layer.status == "Pull complete" {
				downloadedBytes += layer.current
			}
		}

		var imagePercent float64
		if totalBytes > 0 {
			imagePercent = float64(downloadedBytes) / float64(totalBytes) * 100
		}

		overallPercent := (float64(imageIndex) + imagePercent/100) / float64(totalImages) * 100

		// Throttle emissions: only emit if percent changed ≥1% or 500ms elapsed
		shouldEmit := false
		if imagePercent-lastEmitPercent >= 1 {
			shouldEmit = true
		} else if time.Since(lastEmitTime) >= 500*time.Millisecond {
			shouldEmit = true
		}

		if shouldEmit && totalBytes > 0 {
			lastEmitPercent = imagePercent
			lastEmitTime = time.Now()

			progress := PullProgress{
				GroupID:         groupID,
				GroupName:       groupName,
				ImageIndex:      imageIndex,
				TotalImages:     totalImages,
				CurrentImage:    image,
				ImagePercent:    imagePercent,
				OverallPercent:  overallPercent,
				Status:          msg.Status,
				BytesDownloaded: downloadedBytes,
				BytesTotal:      totalBytes,
			}
			wailsRuntime.EventsEmit(a.ctx, "pullProgress", progress)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading pull stream: %v", err)
	}

	return nil
}

func (a *App) PullImages() error {
	config, err := a.GetLauncherConfig()
	if err != nil || config.SelectedGroups == nil || len(config.SelectedGroups) == 0 {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   "No services selected. Configure services in the Services tab first.",
			Timestamp: time.Now().Format("15:04:05"),
		})
		return fmt.Errorf("no services selected; configure in Services tab")
	}

	// Get selected services from groups
	allGroups := a.GetServiceGroups()
	groupMap := make(map[string]ServiceGroup)
	for _, g := range allGroups {
		groupMap[g.ID] = g
	}

	serviceSet := make(map[string]bool)
	for _, groupID := range config.SelectedGroups {
		if group, ok := groupMap[groupID]; ok {
			if group.Locked {
				continue
			}
			for _, svc := range group.Services {
				serviceSet[svc] = true
			}
		}
	}

	var services []string
	for svc := range serviceSet {
		services = append(services, svc)
	}

	// Storage pre-flight (preflight.go), same as the progress-reporting path.
	if warning, err := a.checkDiskSpace(config.SelectedGroups, groupMap, a.CheckImagePresence()); err != nil {
		a.emitAndLog("launcher", err.Error())
		return err
	} else if warning != "" {
		a.emitAndLog("launcher", warning)
	}

	if err := a.dockerLoginForProImages(config.SelectedGroups, groupMap); err != nil {
		return err
	}

	// Pull selected services using docker compose (logs only, no progress bars)
	args := append([]string{"compose"}, a.devEnvArgs()...)
	args = append(args, "pull")
	args = append(args, services...)
	return a.runDockerCompose(args, "Pulling selected services...")
}

func (a *App) CleanDocker() error {
	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   "Cleaning Docker resources...",
		Timestamp: time.Now().Format("15:04:05"),
	})

	cmds := [][]string{
		{"container", "prune", "-f"},
		{"image", "prune", "-f"},
	}

	for _, args := range cmds {
		cmd := exec.Command("docker", args...)
		cmd.Dir = a.projectPath
		if err := cmd.Run(); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   fmt.Sprintf("Warning: %v", err),
				Timestamp: time.Now().Format("15:04:05"),
			})
		}
	}

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   "Cleanup completed",
		Timestamp: time.Now().Format("15:04:05"),
	})

	return nil
}

func (a *App) getConfigPath() (string, error) {
	if configDir := os.Getenv("LIGANDX_LAUNCHER_CONFIG_DIR"); configDir != "" {
		return filepath.Join(configDir, "config.json"), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "ligandx-launcher", "config.json"), nil
}

func coreServicesDescription() string {
	return "Essential services: Proxy, Gateway, Frontend, Structure, Pocket Finder (fpocket / DeepPocket / etc.), and supporting infrastructure"
}

func coreServiceNames() []string {
	return []string{"postgres", "redis", "rabbitmq", "gateway", "frontend", "proxy", "structure", "alignment", "ketcher", "msa", "worker-cpu", "flower", "pocket-finder"}
}

func imageRef(repository, tag string) string {
	return fmt.Sprintf("%s:%s", repository, tag)
}

func (a *App) productionImageSettings() (string, string) {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return "latest", "ghcr.io/kon-218/ligand-x-pro"
	}

	parsed := envfile.Parse(content)
	version := strings.TrimSpace(parsed["VERSION"])
	if version == "" {
		version = "latest"
	}

	proPrefix := strings.TrimSpace(parsed["LIGANDX_PRO_IMAGE_PREFIX"])
	if proPrefix == "" {
		proPrefix = "ghcr.io/kon-218/ligand-x-pro"
	}

	return version, proPrefix
}

func (a *App) requirePinnedProductionVersion() (string, error) {
	if err := a.ensureProductionEnv(); err != nil {
		return "", err
	}

	version, _ := a.productionImageSettings()
	if !envfile.IsPinnedVersion(version) {
		return "", fmt.Errorf("VERSION must be pinned in .env.production (set to a release tag or digest, not 'latest')")
	}
	return version, nil
}

func coreServiceImages(version string) []string {
	images := []string{
		"alpine:3.21",
		imageRef("ghcr.io/kon-218/ligand-x/gateway", version),
		imageRef("ghcr.io/kon-218/ligand-x/frontend", version),
		"nginx:1.27-alpine",
		imageRef("ghcr.io/kon-218/ligand-x/pocket-finder", version),
		imageRef("ghcr.io/kon-218/ligand-x/structure", version),
		imageRef("ghcr.io/kon-218/ligand-x/alignment", version),
		imageRef("ghcr.io/kon-218/ligand-x/ketcher", version),
		imageRef("ghcr.io/kon-218/ligand-x/msa", version),
		imageRef("ghcr.io/kon-218/ligand-x/worker-cpu", version),
		"redis:7-alpine",
		"postgres:16-alpine",
		"rabbitmq:3.13-management-alpine",
		"mher/flower:2.0",
	}
	return images
}

func (a *App) GetServiceGroups() []ServiceGroup {
	licenseStatus := a.GetLicenseStatus()
	version, proPrefix := a.productionImageSettings()
	// Pro images are tagged independently of the public ones; compose resolves
	// them as ${PRO_VERSION:-${VERSION}}. The shared gpu-short worker is pulled
	// here and launched from LIGANDX_GPU_SHORT_IMAGE, so both must name the same
	// tag or the launcher pulls one image and compose runs another.
	proVersion := a.productionProVersion()
	proGPUShortImage := imageRef(proPrefix+"/worker-gpu-short", proVersion)
	groups := []ServiceGroup{
		{
			ID:          "core",
			Name:        "Core Services",
			Description: coreServicesDescription(),
			Services:    coreServiceNames(),
			Images:      coreServiceImages(version),
			SizeMB:      5500,
			Required:    true,
			DefaultOn:   true,
			Edition:     "free",
			Licensed:    true,
		},
		{
			ID:          "docking",
			Name:        "Molecular Docking",
			Description: "AutoDock Vina-based protein-ligand docking calculations",
			Services:    []string{"docking"},
			Images: []string{
				imageRef("ghcr.io/kon-218/ligand-x/docking", version),
			},
			SizeMB:    800,
			Required:  false,
			DefaultOn: true,
			Edition:   "free",
			Licensed:  true,
		},
		{
			ID:          "md",
			Name:        "Molecular Dynamics",
			Description: "MD simulations with OpenMM/OpenFF",
			Services:    []string{"md", "worker-gpu-short"},
			// The public worker image carries md_optimize and workflow_run, which
			// is everything this group submits, and needs no registry
			// credentials -- so a free-tier install can run MD without a licence.
			// Selecting a Pro group that also uses gpu-short swaps in the Pro
			// superset image through LIGANDX_GPU_SHORT_IMAGE; see
			// gpuShortImageOverride.
			Images: []string{
				imageRef("ghcr.io/kon-218/ligand-x/md", version),
				imageRef("ghcr.io/kon-218/ligand-x/worker-gpu-short", version),
			},
			SizeMB:    4500,
			Required:  false,
			DefaultOn: true,
			Edition:   "free",
			Licensed:  true,
		},
		{
			ID:          "admet",
			Name:        "ADMET Prediction",
			Description: "Pro package: predict molecular properties, pharmacokinetics, and toxicity",
			Services:    []string{"admet"},
			// admet_predict runs on the shared gpu-short worker, so this group
			// owns the Pro image for it: the md group ships the public worker,
			// which has no admet module.
			Images: []string{
				imageRef(proPrefix+"/admet", version),
				proGPUShortImage,
			},
			RegistryAuthImages: []string{proGPUShortImage},
			SizeMB:             1500,
			Required:           false,
			DefaultOn:          false,
			Edition:            "pro",
			Entitlement:        "admet",
		},
		{
			ID:          "free-energy",
			Name:        "Binding Free Energy",
			Description: "Pro package: ABFE/RBFE binding free energy calculations",
			Services:    []string{"abfe", "rbfe", "worker-gpu-long"},
			// rbfe_mapping_preview is routed to gpu-short, not gpu-long, so this
			// group needs the Pro shared worker too -- previously it depended on
			// the md group happening to pull it.
			Images: []string{
				imageRef(proPrefix+"/abfe", version),
				imageRef(proPrefix+"/rbfe", version),
				imageRef(proPrefix+"/worker-gpu-long", version),
				proGPUShortImage,
			},
			RegistryAuthImages: []string{proGPUShortImage},
			SizeMB:             5500,
			Required:           false,
			DefaultOn:          false,
			Edition:            "pro",
			Entitlement:        "free-energy",
		},
		{
			ID:          "qc",
			Name:        "Quantum Chemistry",
			Description: "Pro package: ORCA-based quantum chemistry calculations",
			Services:    []string{"qc", "worker-qc"},
			Images: []string{
				imageRef(proPrefix+"/qc", version),
				imageRef(proPrefix+"/worker-qc", version),
			},
			SizeMB:      3000,
			Required:    false,
			DefaultOn:   false,
			Edition:     "pro",
			Entitlement: "qc",
		},
		{
			ID:          "boltz2",
			Name:        "Boltz-2",
			Description: "Pro package: Boltz-2 binding affinity predictions",
			Services:    []string{"boltz2"},
			// boltz_predict and boltz_batch run on the shared gpu-short worker.
			Images: []string{
				imageRef(proPrefix+"/boltz2", version),
				proGPUShortImage,
			},
			RegistryAuthImages: []string{proGPUShortImage},
			SizeMB:             6000,
			Required:           false,
			DefaultOn:          false,
			Edition:            "pro",
			Entitlement:        "boltz2",
		},
		{
			ID:          "reinvent",
			Name:        "De Novo Design",
			Description: "Pro package: generative molecular design with REINVENT4 and DockStream integration",
			Services:    []string{"reinvent", "worker-reinvent"},
			Images: []string{
				imageRef(proPrefix+"/reinvent", version),
				imageRef(proPrefix+"/worker-reinvent", version),
			},
			SizeMB:      5000,
			Required:    false,
			DefaultOn:   false,
			Edition:     "pro",
			Entitlement: "reinvent",
		},
	}
	for i := range groups {
		if groups[i].Edition == "" {
			groups[i].Edition = "free"
		}
		if groups[i].Edition == "pro" {
			groups[i].Licensed = licenseStatus.HasEntitlement(groups[i].Entitlement)
			groups[i].Locked = !groups[i].Licensed
		} else {
			groups[i].Licensed = true
			groups[i].Locked = false
		}
	}
	return groups
}

func (a *App) GetLauncherConfig() (LauncherConfig, error) {
	configPath, err := a.getConfigPath()
	if err != nil {
		return LauncherConfig{ConfigVersion: 1}, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return LauncherConfig{FirstRunDone: false, SelectedGroups: []string{}, ConfigVersion: 1}, nil
		}
		return LauncherConfig{ConfigVersion: 1}, fmt.Errorf("failed to read config: %w", err)
	}

	var config LauncherConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return LauncherConfig{ConfigVersion: 1}, fmt.Errorf("corrupted config file: %w", err)
	}

	return config, nil
}

func (a *App) SaveLauncherConfig(config LauncherConfig) error {
	configPath, err := a.getConfigPath()
	if err != nil {
		return err
	}

	// Create config directory if it doesn't exist
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := envfile.WritePrivate(configPath, data); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

func generateAPIKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validateEnvCredential(label, value string, minLength int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if len(value) < minLength {
		return fmt.Errorf("%s must be at least %d characters", label, minLength)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s cannot contain line breaks", label)
	}
	if strings.ContainsAny(value, " \t") {
		return fmt.Errorf("%s cannot contain spaces", label)
	}
	return nil
}

func (a *App) SaveLocalAccount(username string, email string, password string) (LauncherConfig, error) {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if err := validateEnvCredential("username", username, 1); err != nil {
		return LauncherConfig{}, err
	}
	if err := validateEnvCredential("password", password, 8); err != nil {
		return LauncherConfig{}, err
	}

	// The public launcher only ever runs the production runtime bundle, which
	// ships .env.production / .env.production.template (no dev .env). Ensure the
	// production env exists (GetEnvContent seeds it from the template) and write
	// credentials there.
	if _, err := a.GetEnvContent("prod"); err != nil {
		return LauncherConfig{}, err
	}
	if err := a.setProductionEnvValue("LIGANDX_USERNAME", username); err != nil {
		return LauncherConfig{}, err
	}
	if err := a.setProductionEnvValue("LIGANDX_PASSWORD", password); err != nil {
		return LauncherConfig{}, err
	}
	config, _ := a.GetLauncherConfig()
	config.UserProfile = UserProfile{Username: username, Email: email}
	config.ConfigVersion = 2
	if err := a.SaveLauncherConfig(config); err != nil {
		return LauncherConfig{}, err
	}
	return config, nil
}

// UpdatePassword updates LIGANDX_PASSWORD without touching any other credentials.
func (a *App) UpdatePassword(newPassword string) error {
	if err := validateEnvCredential("password", newPassword, 8); err != nil {
		return err
	}
	if _, err := a.GetEnvContent("prod"); err != nil {
		return err
	}
	return a.setProductionEnvValue("LIGANDX_PASSWORD", newPassword)
}

func (a *App) licensePath() string {
	return filepath.Join(a.projectPath, "data", "license", "ligandx-license.json")
}

func (a *App) GetLicenseStatus() license.Summary {
	status, err := a.readLicenseStatus()
	if err != nil {
		return license.Summary{Edition: "free", Valid: true, Reason: err.Error()}
	}
	return status
}

func (a *App) persistImportedLicense(data []byte) error {
	dest := a.licensePath()
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	// License certificates contain customer and entitlement metadata, so keep the imported copy owner-only.
	return envfile.WritePrivate(dest, data)
}

func (a *App) ImportLicense(path string) (license.Summary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return license.Summary{}, err
	}

	status, err := a.verifyLicenseData(data)
	if err != nil {
		return status, err
	}
	if !status.Valid {
		return status, fmt.Errorf("invalid license: %s", status.Reason)
	}

	if err := a.persistImportedLicense(data); err != nil {
		return status, err
	}

	config, _ := a.GetLauncherConfig()
	config.ConfigVersion = 2
	_ = a.SaveLauncherConfig(config)

	return status, nil
}

func (a *App) SelectLicenseFile() (license.Summary, error) {
	path, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Ligand-X License",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Ligand-X License (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return license.Summary{}, err
	}
	if path == "" {
		return license.Summary{Edition: "free", Valid: true, Reason: "no_license"}, nil
	}
	return a.ImportLicense(path)
}

func (a *App) readLicenseStatus() (license.Summary, error) {
	envfile.EnsurePrivateMode(a.licensePath())
	data, err := os.ReadFile(a.licensePath())
	if err != nil {
		if os.IsNotExist(err) {
			return license.Summary{Edition: "free", Valid: true, Reason: "no_license"}, nil
		}
		return license.Summary{}, err
	}
	return a.verifyLicenseData(data)
}

// verifyLicenseData on the App always uses the embedded public key.
// Allowing a file or env-var override here would let anyone substitute
// their own keypair and forge licenses without modifying the binary.
func (a *App) verifyLicenseData(data []byte) (license.Summary, error) {
	return license.VerifyWithPublicKey(data, []byte(license.PublicKeyPEM))
}

func (a *App) registryCredentialsFromLicense() (license.RegistryCredentials, bool) {
	data, err := os.ReadFile(a.licensePath())
	if err != nil {
		return license.RegistryCredentials{}, false
	}
	return license.RegistryCredentialsFromData(data, []byte(license.PublicKeyPEM))
}

func needsProRegistryAuth(groupIDs []string, groupMap map[string]ServiceGroup) bool {
	for _, groupID := range groupIDs {
		if group, ok := groupMap[groupID]; ok && (group.Edition == "pro" || len(group.RegistryAuthImages) > 0) {
			return true
		}
	}
	return false
}

func selectedProRepositories(groupIDs []string, groupMap map[string]ServiceGroup) []string {
	seen := make(map[string]bool)
	var repos []string
	for _, groupID := range groupIDs {
		group, ok := groupMap[groupID]
		if !ok {
			continue
		}
		images := group.RegistryAuthImages
		if group.Edition == "pro" {
			images = group.Images
		}
		for _, image := range images {
			repo := image
			if at := strings.Index(repo, "@"); at >= 0 {
				repo = repo[:at]
			}
			if colon := strings.LastIndex(repo, ":"); colon > strings.LastIndex(repo, "/") {
				repo = repo[:colon]
			}
			if !seen[repo] {
				seen[repo] = true
				repos = append(repos, repo)
			}
		}
	}
	return repos
}

func machineID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s/%s", goruntime.GOOS, host)
}

func (a *App) registryCredentialsFromBroker(groupIDs []string, groupMap map[string]ServiceGroup) (license.RegistryCredentials, bool, error) {
	tokenURL := strings.TrimSpace(os.Getenv("LIGANDX_REGISTRY_TOKEN_URL"))
	if tokenURL == "" {
		return license.RegistryCredentials{}, false, nil
	}
	parsedTokenURL, parseErr := url.Parse(tokenURL)
	if parseErr != nil || parsedTokenURL.Scheme != "https" || parsedTokenURL.Hostname() == "" || parsedTokenURL.User != nil {
		return license.RegistryCredentials{}, true, fmt.Errorf("LIGANDX_REGISTRY_TOKEN_URL must be an HTTPS URL without embedded credentials")
	}
	accessToken := strings.TrimSpace(os.Getenv("LIGANDX_VENDOR_ACCESS_TOKEN"))
	if accessToken == "" {
		return license.RegistryCredentials{}, true, fmt.Errorf("LIGANDX_VENDOR_ACCESS_TOKEN is required when LIGANDX_REGISTRY_TOKEN_URL is set")
	}
	licenseStatus := a.GetLicenseStatus()
	if !licenseStatus.Valid || licenseStatus.Edition == "free" {
		return license.RegistryCredentials{}, true, fmt.Errorf("valid Pro or Academic license required before requesting registry credentials")
	}
	version, _ := a.productionImageSettings()
	if !envfile.IsPinnedVersion(version) {
		return license.RegistryCredentials{}, true, fmt.Errorf("registry token request requires an immutable VERSION")
	}
	repositories := selectedProRepositories(groupIDs, groupMap)
	reqBody := license.RegistryTokenRequest{
		LicenseID:    licenseStatus.LicenseID,
		Groups:       groupIDs,
		Repositories: repositories,
		Entitlements: licenseStatus.Entitlements,
		MachineID:    machineID(),
		Version:      version,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return license.RegistryCredentials{}, true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return license.RegistryCredentials{}, true, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 || next.URL.Scheme != "https" || next.URL.User != nil {
				return fmt.Errorf("registry broker redirect rejected")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return license.RegistryCredentials{}, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return license.RegistryCredentials{}, true, fmt.Errorf("registry token broker returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}
	var tokenResp license.RegistryTokenResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	if err := decoder.Decode(&tokenResp); err != nil {
		return license.RegistryCredentials{}, true, err
	}
	creds, validationErr := license.ValidateRegistryTokenResponse(tokenResp, repositories)
	if validationErr != nil {
		return license.RegistryCredentials{}, true, validationErr
	}
	return creds, true, nil
}

type bridgeCredentialLoader func() (license.RegistryCredentials, bool)

func (a *App) registryCredentialsForProImagesForBuild(
	groupIDs []string,
	groupMap map[string]ServiceGroup,
	publicBuild bool,
	loadBridge bridgeCredentialLoader,
) (license.RegistryCredentials, bool, error) {
	if !needsProRegistryAuth(groupIDs, groupMap) {
		return license.RegistryCredentials{}, false, nil
	}

	if creds, configured, err := a.registryCredentialsFromBroker(groupIDs, groupMap); configured || err != nil {
		return creds, configured && err == nil, err
	}

	if creds, ok := loadBridge(); ok {
		return creds, true, nil
	}

	if publicBuild {
		return license.RegistryCredentials{}, false, fmt.Errorf("public launcher requires the short-lived registry token broker or signed bridge credentials")
	}
	return license.RegistryCredentials{}, false, fmt.Errorf("Pro image pull requires LIGANDX_REGISTRY_TOKEN_URL/LIGANDX_VENDOR_ACCESS_TOKEN or signed bridge credentials in the license")
}

func (a *App) registryCredentialsForProImages(groupIDs []string, groupMap map[string]ServiceGroup) (license.RegistryCredentials, bool, error) {
	return a.registryCredentialsForProImagesForBuild(
		groupIDs,
		groupMap,
		isPublicBuild,
		a.registryCredentialsFromLicense,
	)
}

func (a *App) dockerLoginForProImages(groupIDs []string, groupMap map[string]ServiceGroup) error {
	creds, ok, err := a.registryCredentialsForProImages(groupIDs, groupMap)
	if err != nil || !ok {
		return err
	}

	cmd := exec.Command("docker", "login", creds.Host, "-u", creds.Username, "--password-stdin")
	cmd.Dir = a.projectPath
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return err
	}
	_, _ = io.WriteString(stdin, creds.Token)
	_ = stdin.Close()
	go a.streamOutput(stdout, "docker")
	go a.streamOutput(stderr, "docker")
	return cmd.Wait()
}

func (a *App) verifyImageSignature(image string) error {
	if strings.ToLower(strings.TrimSpace(os.Getenv("LIGANDX_REQUIRE_IMAGE_SIGNATURES"))) != "true" {
		return nil
	}

	args := []string{"verify"}
	if key := strings.TrimSpace(os.Getenv("LIGANDX_COSIGN_KEY")); key != "" {
		args = append(args, "--key", key)
	} else if identity := strings.TrimSpace(os.Getenv("LIGANDX_COSIGN_CERT_IDENTITY")); identity != "" {
		args = append(args, "--certificate-identity", identity)
		if issuer := strings.TrimSpace(os.Getenv("LIGANDX_COSIGN_OIDC_ISSUER")); issuer != "" {
			args = append(args, "--certificate-oidc-issuer", issuer)
		}
	} else {
		return fmt.Errorf("LIGANDX_REQUIRE_IMAGE_SIGNATURES=true requires LIGANDX_COSIGN_KEY or LIGANDX_COSIGN_CERT_IDENTITY")
	}
	args = append(args, image)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cosign", args...)
	cmd.Dir = a.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// checkGPUForServices returns an error if any service in the list requires
// NVIDIA GPU and the driver is not currently available.
func (a *App) checkGPUForServices(services []string) error {
	var gpuSvcs []string
	for _, svc := range services {
		if gpuRequiredRuntime[svc] {
			gpuSvcs = append(gpuSvcs, svc)
		}
	}
	if len(gpuSvcs) > 0 && !a.CheckGPU() {
		return fmt.Errorf(
			"NVIDIA GPU not available (driver not loaded). Cannot start GPU-only "+
				"services: %s. Deselect the Binding Free Energy, Boltz-2, and "+
				"Kinetics service groups in the Services tab. Molecular Dynamics "+
				"runs on CPU without a GPU (slower).",
			strings.Join(gpuSvcs, ", "),
		)
	}
	return nil
}

func (a *App) CheckGPU() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nvidia-smi")
	err := cmd.Run()
	return err == nil
}

func servicesNeedOrca(services []string) bool {
	for _, svc := range services {
		if svc == "qc" || svc == "worker-qc" {
			return true
		}
	}
	return false
}

// checkOrcaForServices applies both ORCA preflights before any QC start or
// restart: cheap host-folder shape first, then an actual isolated execution in
// the exact pinned worker-qc image.
func (a *App) checkOrcaForServices(services []string) error {
	if !servicesNeedOrca(services) {
		return nil
	}
	path := a.currentOrcaHostPath()
	if err := validateOrcaHostPath(path); err != nil {
		return fmt.Errorf(
			"Quantum Chemistry needs a local ORCA installation. "+
				"Choose the extracted Linux x86-64 ORCA folder that contains a file named 'orca' before starting: %v",
			err,
		)
	}
	return a.probeOrcaRuntime(path)
}

// validateOrcaHostPath requires a directory containing the Linux ORCA binary.
// QC always runs in a Linux container, including under Docker Desktop on
// Windows and macOS, so orca.exe is never a valid host-side shape.
func validateOrcaHostPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("choose the folder that contains the ORCA executable")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("ORCA folder not found: %s", path)
		}
		return fmt.Errorf("cannot read ORCA folder %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("ORCA path is not a folder: %s", path)
	}

	bin := filepath.Join(path, "orca")
	if st, err := os.Stat(bin); err == nil && st.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("no Linux ORCA executable found in %s (expected a regular file named orca)", path)
}

// canonicalImageRef normalizes a tag reference for exact comparisons.
func canonicalImageRef(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i]
	}
	return ref
}

func (a *App) CheckImagePresence() map[string]bool {
	result := make(map[string]bool)

	if a.dockerClient == nil {
		a.initDockerClient()
	}

	if a.dockerClient == nil {
		allGroups := a.GetServiceGroups()
		for _, g := range allGroups {
			result[g.ID] = false
		}
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	imageList, err := a.dockerClient.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		allGroups := a.GetServiceGroups()
		for _, g := range allGroups {
			result[g.ID] = false
		}
		return result
	}

	// Build a list of available image tags
	var availableImages []string
	for _, img := range imageList.Items {
		for _, tag := range img.RepoTags {
			if tag != "<none>:<none>" {
				availableImages = append(availableImages, tag)
			}
		}
	}

	allGroups := a.GetServiceGroups()
	for _, group := range allGroups {
		allPresent := true
		for _, requiredImage := range group.Images {
			found := false

			reqRef := canonicalImageRef(requiredImage)
			for _, availableTag := range availableImages {
				if canonicalImageRef(availableTag) == reqRef {
					found = true
					break
				}
			}

			if !found {
				allPresent = false
				break
			}
		}
		result[group.ID] = allPresent
	}

	return result
}

func (a *App) DeleteServiceGroupImages(groupID string) error {
	if a.dockerClient == nil {
		a.initDockerClient()
	}
	if a.dockerClient == nil {
		return fmt.Errorf("docker client not available")
	}

	allGroups := a.GetServiceGroups()
	var group *ServiceGroup
	for i := range allGroups {
		if allGroups[i].ID == groupID {
			group = &allGroups[i]
			break
		}
	}
	if group == nil {
		return fmt.Errorf("unknown service group: %s", groupID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	imageList, err := a.dockerClient.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return err
	}

	for _, requiredImage := range group.Images {
		parts := strings.Split(requiredImage, "/")
		serviceName := ""
		if len(parts) > 0 {
			lastPart := parts[len(parts)-1]
			serviceName = strings.Split(lastPart, ":")[0]
		}

		for _, img := range imageList.Items {
			for _, tag := range img.RepoTags {
				if tag == "<none>:<none>" {
					continue
				}
				if strings.Contains(tag, requiredImage) || (serviceName != "" && strings.Contains(tag, serviceName)) {
					_, removeErr := a.dockerClient.ImageRemove(ctx, img.ID, client.ImageRemoveOptions{PruneChildren: true})
					if removeErr != nil {
						wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
							Service:   "launcher",
							Message:   fmt.Sprintf("Warning: could not remove image %s: %v", tag, removeErr),
							Timestamp: time.Now().Format("15:04:05"),
						})
					} else {
						wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
							Service:   "launcher",
							Message:   fmt.Sprintf("Removed image: %s", tag),
							Timestamp: time.Now().Format("15:04:05"),
						})
					}
					break
				}
			}
		}
	}

	return nil
}

func (a *App) StopPullServiceGroups() error {
	a.pullCancelMu.Lock()
	cancel := a.activePullCancel
	a.pullCancelMu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

func (a *App) PullServiceGroups(groupIDs []string) {
	go func() {
		pullCtx, pullCancel := context.WithCancel(a.ctx)

		// Store cancel func so the UI can stop an in-progress pull. activePullGen
		// disambiguates which goroutine's cleanup defer is running below — a
		// func value can only be compared to nil, not to another func value.
		a.pullCancelMu.Lock()
		if a.activePullCancel != nil {
			// Interrupt any existing pull before starting a new one.
			a.activePullCancel()
		}
		a.activePullCancel = pullCancel
		a.activePullGen++
		myGen := a.activePullGen
		a.pullCancelMu.Unlock()

		defer func() {
			a.pullCancelMu.Lock()
			if a.activePullGen == myGen {
				a.activePullCancel = nil
			}
			a.pullCancelMu.Unlock()
		}()

		defer func() {
			if r := recover(); r != nil {
				wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
					Service:   "launcher",
					Message:   fmt.Sprintf("Error during pull: %v", r),
					Timestamp: time.Now().Format("15:04:05"),
				})
				wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
					"success":      false,
					"failedGroups": groupIDs,
				})
			}
		}()

		allGroups := a.GetServiceGroups()
		groupMap := make(map[string]ServiceGroup)
		for _, g := range allGroups {
			groupMap[g.ID] = g
		}

		if _, err := a.requirePinnedProductionVersion(); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   err.Error(),
				Timestamp: time.Now().Format("15:04:05"),
			})
			wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
				"success":      false,
				"failedGroups": groupIDs,
				"reason":       "version_not_pinned",
			})
			return
		}

		// Storage pre-flight (preflight.go): running out of space mid-pull costs
		// the user a very long download and leaves a half-populated image store.
		if warning, err := a.checkDiskSpace(groupIDs, groupMap, a.CheckImagePresence()); err != nil {
			a.emitAndLog("launcher", err.Error())
			wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
				"success":      false,
				"failedGroups": groupIDs,
				"reason":       "insufficient_disk",
			})
			return
		} else if warning != "" {
			a.emitAndLog("launcher", warning)
		}

		hasGPUService := false
		for _, groupID := range groupIDs {
			if group, ok := groupMap[groupID]; ok {
				if group.Locked {
					wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
						"success":      false,
						"failedGroups": []string{groupID},
						"reason":       "license_required",
					})
					return
				}
				for _, service := range group.Services {
					if gpuRequiredRuntime[service] {
						hasGPUService = true
						break
					}
				}
			}
		}

		if hasGPUService && !a.CheckGPU() {
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   "NVIDIA GPU not detected. GPU services require NVIDIA Docker runtime.",
				Timestamp: time.Now().Format("15:04:05"),
			})
			wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
				"success":      false,
				"failedGroups": groupIDs,
				"reason":       "gpu_not_found",
			})
			return
		}

		creds, hasRegistryAuth, err := a.registryCredentialsForProImages(groupIDs, groupMap)
		if err != nil {
			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   fmt.Sprintf("Registry login failed: %v", err),
				Timestamp: time.Now().Format("15:04:05"),
			})
			wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
				"success":      false,
				"failedGroups": groupIDs,
				"reason":       "registry_login_failed",
			})
			return
		}
		registryAuth := ""
		if hasRegistryAuth {
			var encodeErr error
			registryAuth, encodeErr = license.EncodeRegistryAuth(creds)
			if encodeErr != nil {
				wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
					Service:   "launcher",
					Message:   fmt.Sprintf("Registry auth failed: %v", encodeErr),
					Timestamp: time.Now().Format("15:04:05"),
				})
				wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
					"success":      false,
					"failedGroups": groupIDs,
					"reason":       "registry_login_failed",
				})
				return
			}
		}

		failedGroups := []string{}

		// Count total images across all selected groups for compounding progress
		totalImagesAll := 0
		for _, groupID := range groupIDs {
			if group, ok := groupMap[groupID]; ok {
				totalImagesAll += len(group.Images)
			}
		}
		globalImgIdx := 0

		for _, groupID := range groupIDs {
			if pullCtx.Err() != nil {
				wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
					"success": false,
					"reason":  "cancelled",
				})
				return
			}

			group, ok := groupMap[groupID]
			if !ok {
				continue
			}

			wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
				Service:   "launcher",
				Message:   fmt.Sprintf("Pulling %s...", group.Name),
				Timestamp: time.Now().Format("15:04:05"),
			})

			groupFailed := false
			for _, image := range group.Images {
				if pullCtx.Err() != nil {
					wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
						"success": false,
						"reason":  "cancelled",
					})
					return
				}

				imgIdx := globalImgIdx

				imageAuth := ""
				if group.Edition == "pro" || slices.Contains(group.RegistryAuthImages, image) {
					imageAuth = registryAuth
				}
				if err := a.verifyImageSignature(image); err != nil {
					wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
						Service:   groupID,
						Message:   fmt.Sprintf("Image signature verification failed for %s: %v", image, err),
						Timestamp: time.Now().Format("15:04:05"),
					})
					groupFailed = true
				} else if err := a.pullImageWithProgress(pullCtx, image, groupID, group.Name, imgIdx, totalImagesAll, imageAuth); err != nil {
					if pullCtx.Err() != nil || errors.Is(err, context.Canceled) {
						groupFailed = false
						// Cancellation: stop immediately and let the caller decide how to
						// interpret the pull lifecycle.
						wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
							"success": false,
							"reason":  "cancelled",
						})
						return
					}

					wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
						Service:   groupID,
						Message:   fmt.Sprintf("Failed to pull %s: %v", image, err),
						Timestamp: time.Now().Format("15:04:05"),
					})
					groupFailed = true
				} else {
					wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
						Service:   groupID,
						Message:   fmt.Sprintf("Pulled image %d/%d: %s", imgIdx+1, totalImagesAll, image),
						Timestamp: time.Now().Format("15:04:05"),
					})
				}

				globalImgIdx++
			}

			if groupFailed {
				failedGroups = append(failedGroups, groupID)
			} else {
				wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
					Service:   groupID,
					Message:   fmt.Sprintf("✓ All images pulled successfully for %s", group.Name),
					Timestamp: time.Now().Format("15:04:05"),
				})
			}
		}

		if len(failedGroups) > 0 {
			wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
				"success":      false,
				"failedGroups": failedGroups,
			})
		} else {
			wailsRuntime.EventsEmit(a.ctx, "pullComplete", map[string]interface{}{
				"success": true,
			})
		}
	}()
}
