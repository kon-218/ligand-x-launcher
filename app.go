package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
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

// RuntimeRelease is a launcher-safe entry from the signed stable release
// index. The backend re-resolves the selected version from the authenticated
// index; it never accepts a URL or image tag supplied by the UI.
type RuntimeRelease struct {
	Version           string   `json:"version"`
	PublishedAt       string   `json:"publishedAt"`
	Status            string   `json:"status"`
	Summary           string   `json:"summary"`
	Recommended       bool     `json:"recommended"`
	Compatible        bool     `json:"compatible"`
	Compatibility     string   `json:"compatibility"`
	DownloadBytes     int64    `json:"downloadBytes"`
	RebuiltComponents []string `json:"rebuiltComponents"`
	RollbackSafeFrom  []string `json:"rollbackSafeFrom"`
	MinimumLauncher   string   `json:"minimumLauncherVersion"`
	BundleURL         string   `json:"bundleUrl"`
}

type runtimeReleaseIndex struct {
	Schema    string           `json:"schema"`
	IssuedAt  string           `json:"issued_at"`
	ExpiresAt string           `json:"expires_at"`
	Releases  []RuntimeRelease `json:"releases"`

	// Warning carries a non-fatal defect found while validating the index, for
	// display alongside the release list. Never serialised: it describes this
	// verification, not the signed document.
	Warning string `json:"-"`
}

type LicenseSummary struct {
	Edition      string   `json:"edition"`
	LicenseID    string   `json:"licenseId"`
	CustomerName string   `json:"customerName"`
	ExpiresAt    string   `json:"expiresAt"`
	GraceUntil   string   `json:"graceUntil"`
	Entitlements []string `json:"entitlements"`
	Valid        bool     `json:"valid"`
	Reason       string   `json:"reason"`
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

type licenseBundle struct {
	Schema    string                 `json:"schema"`
	Algorithm string                 `json:"algorithm"`
	Payload   map[string]interface{} `json:"payload"`
	Signature string                 `json:"signature"`
}

type registryCredentials struct {
	Host     string
	Username string
	Token    string
}

type registryTokenRequest struct {
	LicenseID    string   `json:"license_id"`
	Groups       []string `json:"groups"`
	Repositories []string `json:"repositories"`
	Entitlements []string `json:"entitlements"`
	MachineID    string   `json:"machine_id"`
	Version      string   `json:"version"`
}

type registryTokenResponse struct {
	Host          string   `json:"host"`
	Username      string   `json:"username"`
	Token         string   `json:"token"`
	IdentityToken string   `json:"identity_token"`
	RegistryToken string   `json:"registry_token"`
	ExpiresAt     string   `json:"expires_at"`
	Repositories  []string `json:"repositories"`
}

var proEntitlements = map[string]bool{
	"admet":       true,
	"qc":          true,
	"boltz2":      true,
	"free-energy": true,
	"reinvent":    true,
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

const defaultRuntimeBundleURL = "https://github.com/kon-218/ligand-x-launcher/releases/latest/download/ligand-x-runtime.zip"

const runtimeBundleAssetName = "ligand-x-runtime.zip"

const latestReleaseAPIURL = "https://api.github.com/repos/kon-218/ligand-x-launcher/releases/latest"

// The listing endpoint, unlike /releases/latest, returns pre-releases and does
// not depend on GitHub's "Latest" pointer -- which has been wrong here before.
const releasesListAPIURL = "https://api.github.com/repos/kon-218/ligand-x-launcher/releases?per_page=50"

const runtimeBundleManifestAssetName = "ligand-x-runtime-manifest.json"
const runtimeBundleSignatureAssetName = "ligand-x-runtime-manifest.sig"
const runtimeReleaseIndexAssetName = "ligand-x-release-index.json"
const runtimeReleaseIndexSignatureAssetName = "ligand-x-release-index.sig"
const runtimeBundleMaxDownloadBytes int64 = 256 * 1024 * 1024
const runtimeBundleMaxExpandedBytes uint64 = 1024 * 1024 * 1024
const runtimeBundleMaxFiles = 128

// Injected into public builds with: -ldflags "-X main.runtimeBundlePublicKeyB64=<base64 raw Ed25519 public key>".
// A public build without a trust root fails closed before downloading a runtime bundle.
var runtimeBundlePublicKeyB64 string

// Injected by production builds. The fallback matches the last runtime known
// to older build scripts, while CI always supplies the product release.
var launcherVersion = defaultPinnedImageVersion

type runtimeReleaseArtifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type runtimeWindowsSigning struct {
	Authenticode bool   `json:"authenticode"`
	Evidence     string `json:"evidence"`
}

type runtimeMacOSSigning struct {
	DeveloperID bool   `json:"developer_id"`
	Notarized   bool   `json:"notarized"`
	Evidence    string `json:"evidence"`
}

type runtimePlatformSigning struct {
	Windows runtimeWindowsSigning `json:"windows"`
	MacOS   runtimeMacOSSigning   `json:"macos"`
}

type runtimeBundleManifest struct {
	Schema          string                            `json:"schema"`
	Version         string                            `json:"version"`
	Asset           string                            `json:"asset"`
	SHA256          string                            `json:"sha256"`
	Size            int64                             `json:"size"`
	IssuedAt        string                            `json:"issued_at"`
	ExpiresAt       string                            `json:"expires_at"`
	GitCommit       string                            `json:"git_commit"`
	Artifacts       map[string]runtimeReleaseArtifact `json:"artifacts,omitempty"`
	PlatformSigning runtimePlatformSigning            `json:"platform_signing,omitempty"`
}

// defaultPinnedImageVersion is the image tag this launcher build was published
// against. It is the last-resort fallback for VERSION self-healing when the
// on-disk .env.production.template is missing or stale (e.g. an older runtime
// dir whose template still says CHANGE_ME). Keep in sync with the published
// core image tag and .env.production.template's VERSION.
const defaultPinnedImageVersion = "v2026.08.05"

const licensePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAcKQKljOJr+vNjOKVewo7sDMaguZUqIJVhYZDgDhnUlE=
-----END PUBLIC KEY-----`

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

	// Cloudflare tunnel (see tunnel.go)
	tunnelCmd *exec.Cmd
	tunnelMux sync.Mutex

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
}

func NewApp() *App {
	return &App{
		logStreams: make(map[string]context.CancelFunc),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Don't initialize Docker client here - do it lazily in CheckDocker() to avoid blocking on startup
	a.detectProjectPath()
}

func (a *App) shutdown(ctx context.Context) {
	a.stopAllLogStreams()
	a.shutdownTunnel()
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
	return defaultRuntimeBundleURL
}

// fetchReleaseListing queries the GitHub releases API to find the
// download URL of the runtime bundle asset attached to the latest release.
// GitHub's /releases/latest/download/<asset> redirect is unreliable on some
// Windows HTTP clients, so we resolve the concrete asset URL explicitly.
func fetchReleaseListing() ([]githubRelease, error) {
	req, err := http.NewRequest(http.MethodGet, releasesListAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ligand-x-launcher")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases API returned HTTP %d", resp.StatusCode)
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub releases response: %w", err)
	}
	return releases, nil
}

// resolveReleaseAssets finds the newest release on the requested channel that
// carries every wanted asset.
func resolveReleaseAssets(includePrereleases bool, wanted ...string) (map[string]string, string, error) {
	releases, err := fetchReleaseListing()
	if err != nil {
		return nil, "", err
	}
	return selectReleaseAssets(releases, includePrereleases, wanted)
}

func resolveRuntimeBundleURLForChannel(includePrereleases bool) (string, string, error) {
	assets, tag, err := resolveReleaseAssets(includePrereleases, runtimeBundleAssetName)
	if err != nil {
		return "", "", err
	}
	return assets[runtimeBundleAssetName], tag, nil
}

func companionRuntimeAssetURL(bundleURL, assetName string) (string, error) {
	parsed, err := url.Parse(bundleURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		return filepath.Join(filepath.Dir(bundleURL), assetName), nil
	}
	parsed.Path = pathpkg.Join(pathpkg.Dir(parsed.Path), assetName)
	parsed.RawPath = ""
	return parsed.String(), nil
}

func decodeRuntimeBundlePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
	}
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid runtime bundle public key")
	}
	return ed25519.PublicKey(raw), nil
}

func verifyRuntimeBundleManifest(manifestBytes, signatureBytes []byte, expectedTag string) (runtimeBundleManifest, error) {
	publicKey, err := decodeRuntimeBundlePublicKey(runtimeBundlePublicKeyB64)
	if err != nil {
		return runtimeBundleManifest{}, fmt.Errorf("runtime release trust root is not configured: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureBytes)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime manifest signature encoding")
	}
	if !ed25519.Verify(publicKey, manifestBytes, signature) {
		return runtimeBundleManifest{}, fmt.Errorf("runtime manifest signature verification failed")
	}
	var manifest runtimeBundleManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime manifest: %w", err)
	}
	if manifest.Schema != "ligandx-runtime-manifest/1" || manifest.Asset != runtimeBundleAssetName {
		return runtimeBundleManifest{}, fmt.Errorf("unsupported runtime manifest")
	}
	if !isPinnedImageVersion(manifest.Version) || (expectedTag != "" && manifest.Version != expectedTag) {
		return runtimeBundleManifest{}, fmt.Errorf("runtime manifest version mismatch")
	}
	if manifest.Size <= 0 || manifest.Size > runtimeBundleMaxDownloadBytes {
		return runtimeBundleManifest{}, fmt.Errorf("runtime bundle size is outside allowed bounds")
	}
	runtimeArtifact, ok := manifest.Artifacts[runtimeBundleAssetName]
	if !ok || runtimeArtifact.SHA256 != manifest.SHA256 || runtimeArtifact.Size != manifest.Size {
		return runtimeBundleManifest{}, fmt.Errorf("runtime artifact is missing from signed release manifest")
	}
	for name, artifact := range manifest.Artifacts {
		if name == "" || artifact.Size <= 0 || len(artifact.SHA256) != 64 {
			return runtimeBundleManifest{}, fmt.Errorf("invalid signed artifact metadata")
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			return runtimeBundleManifest{}, fmt.Errorf("invalid signed artifact digest")
		}
	}
	if len(manifest.SHA256) != 64 {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime bundle digest")
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime bundle digest")
	}
	issuedAt, err := time.Parse(time.RFC3339, manifest.IssuedAt)
	if err != nil || issuedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime manifest issuance time")
	}
	expiresAt, err := time.Parse(time.RFC3339, manifest.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || time.Now().UTC().After(expiresAt) {
		return runtimeBundleManifest{}, fmt.Errorf("runtime manifest is expired or has invalid expiry")
	}
	if len(manifest.GitCommit) != 40 {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime manifest source commit")
	}
	if _, err := hex.DecodeString(manifest.GitCommit); err != nil {
		return runtimeBundleManifest{}, fmt.Errorf("invalid runtime manifest source commit")
	}
	return manifest, nil
}

func verifyRuntimeReleaseIndex(indexBytes, signatureBytes []byte) (runtimeReleaseIndex, error) {
	publicKey, err := decodeRuntimeBundlePublicKey(runtimeBundlePublicKeyB64)
	if err != nil {
		return runtimeReleaseIndex{}, fmt.Errorf("runtime release trust root is not configured: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureBytes)))
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, indexBytes, signature) {
		return runtimeReleaseIndex{}, fmt.Errorf("release index signature verification failed")
	}
	var index runtimeReleaseIndex
	decoder := json.NewDecoder(bytes.NewReader(indexBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return runtimeReleaseIndex{}, fmt.Errorf("invalid release index: %w", err)
	}
	if index.Schema != "ligandx-release-index/1" || len(index.Releases) == 0 {
		return runtimeReleaseIndex{}, fmt.Errorf("unsupported or empty release index")
	}
	issuedAt, err := time.Parse(time.RFC3339, index.IssuedAt)
	if err != nil || issuedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return runtimeReleaseIndex{}, fmt.Errorf("invalid release index issuance time")
	}
	expiresAt, err := time.Parse(time.RFC3339, index.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || time.Now().UTC().After(expiresAt) {
		return runtimeReleaseIndex{}, fmt.Errorf("release index is expired or has invalid expiry")
	}
	seen := map[string]bool{}
	recommended := 0
	for i := range index.Releases {
		release := &index.Releases[i]
		if !isPinnedImageVersion(release.Version) || seen[release.Version] {
			return runtimeReleaseIndex{}, fmt.Errorf("invalid or duplicate release version")
		}
		seen[release.Version] = true
		if release.Status != "supported" && release.Status != "deprecated" && release.Status != "revoked" {
			return runtimeReleaseIndex{}, fmt.Errorf("invalid status for release %s", release.Version)
		}
		if _, err := approvedRuntimeDownloadURL(release.BundleURL); err != nil {
			return runtimeReleaseIndex{}, fmt.Errorf("release %s has invalid bundle URL: %w", release.Version, err)
		}
		if release.DownloadBytes <= 0 || release.DownloadBytes > runtimeBundleMaxDownloadBytes {
			return runtimeReleaseIndex{}, fmt.Errorf("release %s has invalid download size", release.Version)
		}
		for _, sourceVersion := range release.RollbackSafeFrom {
			if !isPinnedImageVersion(sourceVersion) {
				return runtimeReleaseIndex{}, fmt.Errorf("release %s has invalid rollback source", release.Version)
			}
		}
		if release.Recommended {
			recommended++
		}
		compatible := release.Status != "revoked"
		message := "Compatible"
		if release.MinimumLauncher != "" {
			comparison, comparable := compareReleaseVersions(launcherVersion, release.MinimumLauncher)
			if !comparable || comparison < 0 {
				compatible = false
				message = "Requires launcher " + release.MinimumLauncher + " or newer"
			}
		}
		if release.Status == "revoked" {
			compatible = false
			message = "This release has been revoked"
		}
		release.Compatible = compatible
		release.Compatibility = message
	}
	// A wrong recommendation count is a presentation defect, not an authenticity
	// one: the signature above already proved the document is ours. Failing here
	// used to hide every release and leave no way to install anything, which is
	// exactly what v2026.08.15-rc.8 did to the version picker by shipping an
	// index with no recommendation and being pinned as "Latest". Clear the
	// ambiguous flags, say so, and still show the list.
	switch {
	case recommended == 0:
		index.Warning = "This release index does not mark a recommended version. " +
			"Choose one explicitly, or check for a newer release."
	case recommended > 1:
		for i := range index.Releases {
			index.Releases[i].Recommended = false
		}
		index.Warning = "This release index marks more than one version as recommended, " +
			"so none is shown as recommended. Choose one explicitly."
	}
	return index, nil
}

// ListRuntimeReleases returns authenticated stable release choices. Older
// servers without an index retain the latest-only behavior; the selected
// runtime manifest is still signature-verified during installation.
func (a *App) ListRuntimeReleases() ([]RuntimeRelease, error) {
	// A user already running a pre-release must keep seeing it even with the
	// toggle off, or the picker hides the version they are on.
	channel := a.includePrereleases() || isPrereleaseVersion(installedRuntimeVersion(a.projectPath))
	assets, tag, err := resolveReleaseAssets(channel, runtimeReleaseIndexAssetName, runtimeReleaseIndexSignatureAssetName)
	if err != nil {
		bundleURL, latest, latestErr := resolveRuntimeBundleURLForChannel(channel)
		if latestErr != nil {
			return nil, err
		}
		version := releaseVersionFromTag(latest)
		summary := "Latest stable release"
		if isPrereleaseVersion(version) {
			summary = "Latest release candidate"
		}
		return []RuntimeRelease{{Version: version, Status: "supported", Summary: summary, Recommended: true, Compatible: true, Compatibility: "Compatible", BundleURL: bundleURL}}, nil
	}
	tempDir, err := os.MkdirTemp("", "ligandx-release-index-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	indexPath := filepath.Join(tempDir, runtimeReleaseIndexAssetName)
	signaturePath := filepath.Join(tempDir, runtimeReleaseIndexSignatureAssetName)
	if err := downloadFileLimited(assets[runtimeReleaseIndexAssetName], indexPath, 2*1024*1024); err != nil {
		return nil, err
	}
	if err := downloadFileLimited(assets[runtimeReleaseIndexSignatureAssetName], signaturePath, 4096); err != nil {
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
	index, err := verifyRuntimeReleaseIndex(indexBytes, signatureBytes)
	if err != nil {
		return nil, err
	}
	a.lastReleaseIndexWarning = index.Warning
	slices.SortFunc(index.Releases, compareReleasesNewestFirst)
	_ = tag
	return filterReleasesForChannel(index.Releases, channel, installedRuntimeVersion(a.projectPath)), nil
}

// ReleaseOptions is what the version picker renders: the releases available on
// the current channel, plus any non-fatal defect found in the signed index.
type ReleaseOptions struct {
	Releases        []RuntimeRelease `json:"releases"`
	Warning         string           `json:"warning"`
	ShowPrereleases bool             `json:"showPrereleases"`
	Installed       string           `json:"installed"`
}

// ListRuntimeReleaseOptions is the picker-facing wrapper around
// ListRuntimeReleases. It exists so the UI can show why an index looks odd --
// no recommendation, or several -- instead of the list silently going empty.
func (a *App) ListRuntimeReleaseOptions() (ReleaseOptions, error) {
	options := ReleaseOptions{
		ShowPrereleases: a.includePrereleases(),
		Installed:       installedRuntimeVersion(a.projectPath),
	}
	releases, err := a.ListRuntimeReleases()
	if err != nil {
		return options, err
	}
	options.Releases = releases
	options.Warning = a.lastReleaseIndexWarning
	return options, nil
}

func verifyRuntimeBundleFile(path string, manifest runtimeBundleManifest) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != manifest.Size {
		return fmt.Errorf("runtime bundle size mismatch")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256) {
		return fmt.Errorf("runtime bundle digest mismatch")
	}
	return nil
}

func numericReleaseVersion(version string) ([3]int, bool) {
	var parsed [3]int
	base := strings.TrimPrefix(strings.TrimSpace(version), "v")
	base = strings.SplitN(base, "-", 2)[0]
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return parsed, false
	}
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return parsed, false
		}
		parsed[index] = value
	}
	return parsed, true
}

func compareReleaseVersions(left, right string) (int, bool) {
	leftParts, leftOK := numericReleaseVersion(left)
	rightParts, rightOK := numericReleaseVersion(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1, true
		}
		if leftParts[index] > rightParts[index] {
			return 1, true
		}
	}
	return 0, true
}

// shouldAdvanceVersion decides whether installing releaseTag should re-pin
// VERSION in .env.production.
//
// The old rule only rewrote a broken value (empty/CHANGE_ME/latest), which meant
// a valid-but-old pin was indistinguishable from a deliberate choice and
// survived forever — so an existing install could take a new launcher AND a new
// runtime bundle and still run the previous release's images. Installing a
// newer runtime is an explicit act by the user, so it advances the pin; a pin
// that is already ahead of, or equal to, the installed runtime is left alone,
// and an unparseable one is treated as deliberate.
func shouldAdvanceVersion(current, releaseTag string) bool {
	if isEnvPlaceholder(current) || strings.EqualFold(current, "latest") {
		return true
	}
	comparison, comparable := compareReleaseVersions(releaseTag, current)
	return comparable && comparison > 0
}

// installedRuntimeVersion reads the release tag recorded when the runtime
// bundle was installed, or "" when the marker is absent (a pre-marker install,
// or none at all).
func installedRuntimeVersion(runtimeDir string) string {
	data, err := os.ReadFile(filepath.Join(runtimeDir, ".ligandx-runtime-version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// CheckForRuntimeUpdate reports whether a newer runtime release than the
// installed one is published, so the UI can prompt. Costs a GitHub API call.
func (a *App) CheckForRuntimeUpdate() RuntimeUpdateStatus {
	current := installedRuntimeVersion(a.projectPath)
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
	comparison, comparable := compareReleaseVersions(latest, current)
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

func enforceRuntimeRollbackPolicy(runtimeDir, candidate string) error {
	current := installedRuntimeVersion(runtimeDir)
	if current == "" {
		return nil
	}
	if comparison, comparable := compareReleaseVersions(candidate, current); !comparable || comparison < 0 {
		return fmt.Errorf("runtime downgrade rejected: installed=%s candidate=%s", current, candidate)
	}
	return nil
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
		InstalledVersion: installedRuntimeVersion(a.projectPath),
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
		current := installedRuntimeVersion(a.projectPath)
		if comparison, comparable := compareReleaseVersions(version, current); current != "" && comparable && comparison < 0 {
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
	if err := writePrivateFile(filepath.Join(backupDir, "manifest.json"), append(payload, '\n')); err != nil {
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
	} else if resolved, tag, resolveErr := resolveRuntimeBundleURLForChannel(a.includePrereleases()); resolveErr == nil {
		bundleURL = resolved
		releaseTag = tag
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Resolved latest runtime bundle: %s", bundleURL), Timestamp: time.Now().Format("15:04:05")})
	} else {
		bundleURL = defaultRuntimeBundleURL
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Could not resolve latest release (%v); falling back to %s", resolveErr, bundleURL), Timestamp: time.Now().Format("15:04:05")})
	}

	manifestURL, err := companionRuntimeAssetURL(bundleURL, runtimeBundleManifestAssetName)
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to resolve runtime manifest URL: %w", err)
	}
	signatureURL, err := companionRuntimeAssetURL(bundleURL, runtimeBundleSignatureAssetName)
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to resolve runtime signature URL: %w", err)
	}
	manifestPath := filepath.Join(stageRoot, runtimeBundleManifestAssetName)
	signaturePath := filepath.Join(stageRoot, runtimeBundleSignatureAssetName)
	if err := downloadFileLimited(manifestURL, manifestPath, 64*1024); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to download signed runtime manifest: %w", err)
	}
	defer os.Remove(manifestPath)
	if err := downloadFileLimited(signatureURL, signaturePath, 4*1024); err != nil {
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
	manifest, err := verifyRuntimeBundleManifest(manifestBytes, signatureBytes, expectedVersion)
	if err != nil {
		return a.GetDistributionStatus(), err
	}
	if !allowRollback {
		if err := enforceRuntimeRollbackPolicy(runtimeDir, manifest.Version); err != nil {
			return a.GetDistributionStatus(), err
		}
	}
	releaseTag = manifest.Version

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Downloading verified runtime bundle %s", releaseTag), Timestamp: time.Now().Format("15:04:05")})
	zipPath := filepath.Join(stageRoot, runtimeBundleAssetName)
	if err := downloadFileLimited(bundleURL, zipPath, manifest.Size); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to download runtime bundle from %s: %w", bundleURL, err)
	}
	defer os.Remove(zipPath)
	if err := verifyRuntimeBundleFile(zipPath, manifest); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("runtime bundle verification failed: %w", err)
	}
	extractedDir := filepath.Join(stageRoot, "extracted")
	if err := os.MkdirAll(extractedDir, 0755); err != nil {
		return a.GetDistributionStatus(), err
	}
	if err := extractRuntimeBundle(zipPath, extractedDir); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to extract runtime bundle: %w", err)
	}
	rollback, err := activateRuntimeStage(extractedDir, runtimeDir, filepath.Join(stageRoot, "backup"))
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to activate staged runtime: %w", err)
	}
	envPath := filepath.Join(runtimeDir, ".env.production")
	oldEnv, oldEnvErr := os.ReadFile(envPath)
	oldEnvMode := os.FileMode(0600)
	if info, statErr := os.Stat(envPath); statErr == nil {
		oldEnvMode = info.Mode().Perm()
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollback()
		if oldEnvErr == nil {
			_ = os.WriteFile(envPath, oldEnv, oldEnvMode)
		} else if os.IsNotExist(oldEnvErr) {
			_ = os.Remove(envPath)
		}
	}()

	a.projectPath = runtimeDir
	if err := a.ensureProductionEnv(); err != nil {
		return a.GetDistributionStatus(), err
	}
	if releaseTag != "" {
		content, readErr := a.GetEnvContent("prod")
		if readErr == nil {
			current := strings.TrimSpace(parseEnvFile(content)["VERSION"])
			if allowRollback || shouldAdvanceVersion(current, releaseTag) {
				if setErr := a.setProductionEnvValues(map[string]string{"VERSION": releaseTag, "PRO_VERSION": releaseTag}); setErr == nil {
					wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Pinned product images to %s in .env.production", releaseTag), Timestamp: time.Now().Format("15:04:05")})
				}
			}
		}
	}
	if err := writePrivateFile(filepath.Join(runtimeDir, ".ligandx-runtime-version"), []byte(releaseTag+"\n")); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to persist runtime version: %w", err)
	}
	committed = true
	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Runtime installed at %s", runtimeDir), Timestamp: time.Now().Format("15:04:05")})
	return a.GetDistributionStatus(), nil
}

func approvedRuntimeDownloadURL(sourceURL string) (*url.URL, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Scheme == "file" {
		if isPublicBuild {
			return nil, fmt.Errorf("local runtime bundle URLs are disabled in public builds")
		}
		return parsed, nil
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("runtime bundle URL must use HTTPS without embedded credentials")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("runtime bundle URL uses an unapproved port")
	}
	host := strings.ToLower(parsed.Hostname())
	approved := host == "github.com" || host == "api.github.com" ||
		host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
	if !approved {
		return nil, fmt.Errorf("runtime bundle host is not approved: %s", host)
	}
	return parsed, nil
}

func downloadFileLimited(sourceURL, dest string, maxBytes int64) error {
	parsed, err := approvedRuntimeDownloadURL(sourceURL)
	if err != nil {
		return err
	}
	var reader io.ReadCloser
	if parsed.Scheme == "file" || parsed.Scheme == "" {
		path := parsed.Path
		if parsed.Scheme == "" {
			path = sourceURL
		}
		reader, err = os.Open(path)
		if err != nil {
			return err
		}
	} else {
		client := &http.Client{
			Timeout: 20 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many runtime bundle redirects")
				}
				_, err := approvedRuntimeDownloadURL(req.URL.String())
				return err
			},
		}
		resp, requestErr := client.Get(sourceURL)
		if requestErr != nil {
			return requestErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fmt.Errorf("HTTP %d from %s", resp.StatusCode, sourceURL)
		}
		if resp.ContentLength > maxBytes {
			resp.Body.Close()
			return fmt.Errorf("download exceeds maximum size")
		}
		reader = resp.Body
	}
	defer reader.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := out.Name()
	defer os.Remove(tmpPath)
	if err := out.Chmod(0600); err != nil {
		out.Close()
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(reader, maxBytes+1))
	if copyErr == nil && written > maxBytes {
		copyErr = fmt.Errorf("download exceeds maximum size")
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		if removeErr := os.Remove(dest); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if retryErr := os.Rename(tmpPath, dest); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func rejectRuntimeSymlinkPath(baseDir, target string) error {
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("runtime bundle target escapes destination")
	}
	current := baseAbs
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime bundle target traverses a symbolic link: %s", current)
		}
	}
	return nil
}

func extractRuntimeBundle(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	if len(zr.File) > runtimeBundleMaxFiles {
		return fmt.Errorf("runtime bundle contains too many entries")
	}
	var expandedBytes uint64
	required := map[string]bool{
		"docker-compose.yml":       false,
		".env.production.template": false,
	}

	for _, f := range zr.File {
		if f.UncompressedSize64 > runtimeBundleMaxExpandedBytes-expandedBytes {
			return fmt.Errorf("runtime bundle exceeds expanded-size limit")
		}
		expandedBytes += f.UncompressedSize64
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime bundle contains a symbolic link: %s", f.Name)
		}
		name := normalizedRuntimeEntryName(f.Name)
		if name == "" || !runtimeEntryAllowed(name) {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		cleanDest, _ := filepath.Abs(destDir)
		cleanTarget, _ := filepath.Abs(target)
		if cleanTarget != cleanDest && !strings.HasPrefix(cleanTarget, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in runtime bundle: %s", f.Name)
		}
		if err := rejectRuntimeSymlinkPath(destDir, target); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		// Self-heal stale installs: an earlier run with a missing bundle source
		// could leave a directory where Docker auto-created the bind-mount source
		// (e.g. docker/nginx/ligandx.conf as a dir). We're about to write a file
		// here, so remove a colliding directory first or os.OpenFile will fail
		// with "is a directory".
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			rc.Close()
			return err
		}
		entrySize := int64(f.UncompressedSize64) // #nosec G115 -- bounded by runtimeBundleMaxExpandedBytes above.
		written, copyErr := io.Copy(out, io.LimitReader(rc, entrySize+1))
		if copyErr == nil && written != entrySize {
			copyErr = fmt.Errorf("runtime bundle entry size mismatch: %s", f.Name)
		}
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if _, needed := required[name]; needed {
			required[name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("runtime bundle is missing required file: %s", name)
		}
	}
	return nil
}

// activateRuntimeStage copies a fully verified/extracted bundle into the
// managed runtime one file at a time, retaining originals until the caller
// commits. It never touches .env.production, Docker volumes, or user results.
func activateRuntimeStage(stageDir, destDir, backupDir string) (func(), error) {
	type activatedFile struct {
		target  string
		backup  string
		existed bool
	}
	activated := []activatedFile{}
	rollback := func() {
		for index := len(activated) - 1; index >= 0; index-- {
			item := activated[index]
			if item.existed {
				_ = copyFileAtomic(item.backup, item.target, 0644)
			} else {
				_ = os.Remove(item.target)
			}
		}
	}
	err := filepath.Walk(stageDir, func(source string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(stageDir, source)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, relative)
		backup := filepath.Join(backupDir, relative)
		item := activatedFile{target: target, backup: backup}
		if current, statErr := os.Stat(target); statErr == nil {
			if current.IsDir() {
				return fmt.Errorf("runtime file collides with a directory: %s", relative)
			}
			item.existed = true
			if err := copyFileAtomic(target, backup, current.Mode().Perm()); err != nil {
				return err
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		activated = append(activated, item)
		if err := copyFileAtomic(source, target, info.Mode().Perm()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		rollback()
		return func() {}, err
	}
	return rollback, nil
}

func copyFileAtomic(source, target string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	temporary := target + ".ligandx-new"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func normalizedRuntimeEntryName(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	parts := strings.Split(name, "/")
	for len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	if len(parts) > 1 && strings.HasPrefix(parts[0], "ligand-x") {
		parts = parts[1:]
	}
	name = strings.Join(parts, "/")
	if name == "." || strings.Contains(name, "..") {
		return ""
	}
	return name
}

func runtimeEntryAllowed(name string) bool {
	allowedFiles := map[string]bool{
		"docker-compose.yml":        true,
		".env.production.template":  true,
		"LICENSE":                   true,
		"README.md":                 true,
		"docker/nginx/ligandx.conf": true,
		"config/rabbitmq.conf":      true,
		"config/flower_config.py":   true,
	}
	if allowedFiles[name] {
		return true
	}
	for _, prefix := range []string{"data/license/", "opt/deeppocket_models/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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
	metrics.CPUPercent, metrics.LoadAverage = readHostCPU()
	metrics.MemoryUsedBytes, metrics.MemoryTotalBytes, metrics.MemoryPercent = readHostMemory()
	metrics.NetRxBytes, metrics.NetTxBytes = readHostNetwork()
	metrics.DiskUsedBytes, metrics.DiskTotalBytes = readDiskUsage(a.projectPath)
	metrics.GPUPercent, metrics.GPUMemoryUsedMB, metrics.GPUMemoryTotalMB = readNvidiaGPU()

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
					metric.MemoryText = fmt.Sprintf("%s / %s", formatBytes(metric.MemoryBytes), formatBytes(metric.MemoryLimit))
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

func readHostCPU() (float64, string) {
	idle1, total1, ok1 := readProcStat()
	time.Sleep(120 * time.Millisecond)
	idle2, total2, ok2 := readProcStat()
	load := "-"
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			load = strings.Join(fields[:3], " ")
		}
	}
	if !ok1 || !ok2 || total2 <= total1 {
		return 0, load
	}
	idleDelta := float64(idle2 - idle1)
	totalDelta := float64(total2 - total1)
	return (1 - idleDelta/totalDelta) * 100, load
}

func readProcStat() (uint64, uint64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total uint64
	var values []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		values = append(values, v)
		total += v
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return idle, total, true
}

func readHostMemory() (uint64, uint64, float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		values[key] = v * 1024
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 || available > total {
		return 0, total, 0
	}
	used := total - available
	return used, total, float64(used) / float64(total) * 100
}

func readHostNetwork() (uint64, uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func readDiskUsage(path string) (uint64, uint64) {
	if goruntime.GOOS == "windows" {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "df", "-k", path).Output()
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, 0
	}
	totalKB, _ := strconv.ParseUint(fields[1], 10, 64)
	usedKB, _ := strconv.ParseUint(fields[2], 10, 64)
	return usedKB * 1024, totalKB * 1024
}

func readNvidiaGPU() (float64, uint64, uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, 0, 0
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.Split(line, ",")
	if len(parts) < 3 {
		return 0, 0, 0
	}
	util, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	used, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
	total, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
	return util, used, total
}

func formatBytes(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
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
		if v := strings.TrimSpace(parseEnvFile(content)["VERSION"]); v != "" {
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
	cur := parseEnvFile(content)
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
	ensurePrivateFileMode(a.composeLogPath())
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

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(path, 0600)
}

func ensurePrivateFileMode(path string) {
	_ = os.Chmod(path, 0600)
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
		ensurePrivateFileMode(envPath)
		return string(data), nil
	}

	// env file doesn't exist — load template and auto-save it as the env file
	templatePath := filepath.Join(a.projectPath, templateFile)
	data, err = os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("no %s file found and could not read %s: %v", envFile, templateFile, err)
	}

	// Write template as the starting env file so docker compose can read it immediately
	_ = writePrivateFile(envPath, data)

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
	return writePrivateFile(envPath, []byte(content))
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

// supersededEnvComment marks an earlier duplicate definition the launcher has
// retired. The line is commented rather than deleted so a user who added an
// override can see where it went, and why it was not the one taking effect.
const supersededEnvComment = "# [ligand-x] superseded — the definition below is the one Docker Compose reads: "

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
		if key := envKeyOnLine(l); key != "" {
			if _, ok := values[key]; ok {
				last[key] = i
			}
		}
	}
	// Pass 2: write it, and comment out every earlier definition of the same key
	// so exactly one live definition survives. Leaving them would let the file
	// keep a value that contradicts the one we just wrote.
	for i, l := range lines {
		key := envKeyOnLine(l)
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
			lines[i] = supersededEnvComment + strings.TrimSpace(l)
		}
	}
	// Append anything the file didn't already declare, in a stable order.
	for _, k := range sortedKeys(values) {
		if _, ok := last[k]; !ok {
			lines = append(lines, k+"="+values[k])
		}
	}
	return writePrivateFile(envPath, []byte(strings.Join(lines, "\n")))
}

// productionEnvPath is the absolute path of the file compose reads through
// --env-file. Worth naming in full in any message about it: on Windows it sits
// under %AppData% where a user is unlikely to look, and "the .env.production
// you edited" is only actionable if they know which one that is.
func (a *App) productionEnvPath() string {
	return filepath.Join(a.projectPath, ".env.production")
}

// envKeyOnLine returns the key a line defines, or "" for blanks, comments and
// non-assignments. It mirrors parseEnvFile and compose's dotenv parser: the key
// is whatever precedes the first '=', trimmed, so `KEY = value` defines KEY
// exactly as `KEY=value` does.
func envKeyOnLine(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return ""
	}
	i := strings.Index(t, "=")
	if i <= 0 {
		return ""
	}
	return strings.TrimSpace(t[:i])
}

// duplicateEnvKeys returns, sorted, every key with more than one live
// definition. A duplicate is invisible in an editor but decisive at runtime —
// compose takes the last one — so an override inserted above the original
// silently does nothing. Worth naming in the log for every key, not just the
// resource limits: it applies equally to VERSION and POSTGRES_PASSWORD.
func duplicateEnvKeys(content string) []string {
	counts := map[string]int{}
	for _, line := range strings.Split(content, "\n") {
		if key := envKeyOnLine(line); key != "" {
			counts[key]++
		}
	}
	var dups []string
	for k, n := range counts {
		if n > 1 {
			dups = append(dups, k)
		}
	}
	slices.Sort(dups)
	return dups
}

// parseEnvFile parses KEY=VALUE lines (ignoring comments/blanks) into a map.
func parseEnvFile(content string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			out[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	return out
}

// isEnvPlaceholder reports whether a value still needs generating: empty, a
// template CHANGE_ME marker, or an unresolved compose/env substitution.
func isEnvPlaceholder(v string) bool {
	return v == "" || strings.Contains(v, "CHANGE_ME") || strings.Contains(v, "${")
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
	cur := parseEnvFile(content)

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

	if dups := duplicateEnvKeys(content); len(dups) > 0 {
		a.emitAndLog("launcher", fmt.Sprintf(
			"Warning: %s defines these keys more than once, and only the last definition of each takes effect: %s",
			a.productionEnvPath(), strings.Join(dups, ", ")))
	}

	// setIfPlaceholder writes only when the existing value is empty/CHANGE_ME,
	// and keeps cur in sync so derived URLs can reference fresh secrets.
	setIfPlaceholder := func(key, value string) error {
		if isEnvPlaceholder(cur[key]) {
			cur[key] = value
			return a.setProductionEnvValue(key, value)
		}
		return nil
	}

	// Generate any missing secrets.
	secretKeys := []string{"POSTGRES_PASSWORD", "RABBITMQ_PASSWORD", "REDIS_PASSWORD", "QC_SECRET_KEY", "LIGANDX_PASSWORD", "FLOWER_PASSWORD", "INTERNAL_WORKER_SECRET"}
	for _, key := range secretKeys {
		if isEnvPlaceholder(cur[key]) {
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
	if !isPinnedImageVersion(cur["VERSION"]) {
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
	if templatePro := a.templateValue("PRO_VERSION"); templatePro != "" && isEnvPlaceholder(cur["PRO_VERSION"]) {
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
	if strings.TrimSpace(parseEnvFile(content)["LIGANDX_GPU_SHORT_IMAGE"]) == want {
		return nil
	}
	return a.setProductionEnvValue("LIGANDX_GPU_SHORT_IMAGE", want)
}

// templatePinnedVersion returns the VERSION pinned in .env.production.template,
// or "" if the template is missing or its VERSION is not a concrete pin. This is
// the canonical image tag the bundle was published against.
func (a *App) templatePinnedVersion() string {
	v := a.templateValue("VERSION")
	if !isPinnedImageVersion(v) {
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
	return strings.TrimSpace(parseEnvFile(string(data))[key])
}

// GetUserSettings returns the user-facing subset of .env.production.
func (a *App) GetUserSettings() (UserSettings, error) {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return UserSettings{}, err
	}
	cur := parseEnvFile(content)

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
	return strings.TrimSpace(parseEnvFile(string(data))["ORCA_HOST_PATH"])
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

	parsed := parseEnvFile(content)
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

func isPinnedImageVersion(version string) bool {
	v := strings.TrimSpace(version)
	return v != "" && !isEnvPlaceholder(v) && !strings.EqualFold(v, "latest")
}

func (a *App) requirePinnedProductionVersion() (string, error) {
	if err := a.ensureProductionEnv(); err != nil {
		return "", err
	}

	version, _ := a.productionImageSettings()
	if !isPinnedImageVersion(version) {
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
	license := a.GetLicenseStatus()
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
			groups[i].Licensed = license.HasEntitlement(groups[i].Entitlement)
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

	if err := writePrivateFile(configPath, data); err != nil {
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

func (a *App) GetLicenseStatus() LicenseSummary {
	status, err := a.readLicenseStatus()
	if err != nil {
		return LicenseSummary{Edition: "free", Valid: true, Reason: err.Error()}
	}
	return status
}

func (a *App) persistImportedLicense(data []byte) error {
	dest := a.licensePath()
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	// License certificates contain customer and entitlement metadata, so keep the imported copy owner-only.
	return writePrivateFile(dest, data)
}

func (a *App) ImportLicense(path string) (LicenseSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LicenseSummary{}, err
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

func (a *App) SelectLicenseFile() (LicenseSummary, error) {
	path, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Ligand-X License",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Ligand-X License (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return LicenseSummary{}, err
	}
	if path == "" {
		return LicenseSummary{Edition: "free", Valid: true, Reason: "no_license"}, nil
	}
	return a.ImportLicense(path)
}

func (a *App) readLicenseStatus() (LicenseSummary, error) {
	ensurePrivateFileMode(a.licensePath())
	data, err := os.ReadFile(a.licensePath())
	if err != nil {
		if os.IsNotExist(err) {
			return LicenseSummary{Edition: "free", Valid: true, Reason: "no_license"}, nil
		}
		return LicenseSummary{}, err
	}
	return a.verifyLicenseData(data)
}

func verifyLicenseData(data []byte) (LicenseSummary, error) {
	return verifyLicenseDataWithPublicKey(data, []byte(licensePublicKeyPEM))
}

// verifyLicenseData on the App always uses the embedded public key.
// Allowing a file or env-var override here would let anyone substitute
// their own keypair and forge licenses without modifying the binary.
func (a *App) verifyLicenseData(data []byte) (LicenseSummary, error) {
	return verifyLicenseDataWithPublicKey(data, []byte(licensePublicKeyPEM))
}

func canonicalLicensePayload(payload map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func verifyLicenseDataWithPublicKey(data []byte, publicKeyPEM []byte) (LicenseSummary, error) {
	var bundle licenseBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_license_json"}, err
	}
	if bundle.Algorithm != "Ed25519" {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "unsupported_algorithm"}, nil
	}

	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_public_key"}, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_public_key"}, err
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_public_key_type"}, nil
	}

	canonical, err := canonicalLicensePayload(bundle.Payload)
	if err != nil {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_payload"}, err
	}
	signature, err := base64.StdEncoding.DecodeString(bundle.Signature)
	if err != nil {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_signature_encoding"}, err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return LicenseSummary{Edition: "free", Valid: false, Reason: "invalid_signature"}, nil
	}

	return summarizeLicensePayload(bundle.Payload), nil
}

func summarizeLicensePayload(payload map[string]interface{}) LicenseSummary {
	edition, _ := payload["edition"].(string)
	entitlements := stringSlice(payload["entitlements"])
	if edition == "academic" {
		entitlements = []string{"admet", "boltz2", "free-energy", "qc", "reinvent"}
	}

	status := LicenseSummary{
		Edition:      edition,
		LicenseID:    stringValue(payload["license_id"]),
		ExpiresAt:    stringValue(payload["expires_at"]),
		GraceUntil:   stringValue(payload["grace_until"]),
		Entitlements: entitlements,
		Valid:        true,
		Reason:       "ok",
	}
	if customer, ok := payload["customer"].(map[string]interface{}); ok {
		status.CustomerName = stringValue(customer["name"])
	}
	if edition != "academic" && edition != "pro" {
		status.Edition = "free"
		status.Valid = false
		status.Reason = "invalid_edition"
		return status
	}
	if edition == "pro" && len(entitlements) == 0 {
		status.Edition = "free"
		status.Valid = false
		status.Reason = "pro_license_requires_entitlements"
		return status
	}
	for _, entitlement := range entitlements {
		if !proEntitlements[entitlement] {
			status.Edition = "free"
			status.Valid = false
			status.Reason = "unknown_entitlement"
			return status
		}
	}

	now := time.Now().UTC()
	if status.ExpiresAt != "" {
		if expiresAt, err := time.Parse(time.RFC3339, status.ExpiresAt); err == nil && now.After(expiresAt) {
			if status.GraceUntil == "" {
				status.Edition = "free"
				status.Valid = false
				status.Reason = "license_expired"
				return status
			}
			if graceUntil, err := time.Parse(time.RFC3339, status.GraceUntil); err != nil || now.After(graceUntil) {
				status.Edition = "free"
				status.Valid = false
				status.Reason = "license_expired"
				return status
			}
		}
	}

	return status
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func stringSlice(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func (s LicenseSummary) HasEntitlement(entitlement string) bool {
	if entitlement == "" {
		return true
	}
	if s.Valid && s.Edition == "academic" {
		return true
	}
	if !s.Valid || s.Edition != "pro" {
		return false
	}
	for _, candidate := range s.Entitlements {
		if candidate == entitlement {
			return true
		}
	}
	return false
}

func registryCredentialsFromLicenseData(data, publicKeyPEM []byte) (registryCredentials, bool) {
	status, err := verifyLicenseDataWithPublicKey(data, publicKeyPEM)
	if err != nil || !status.Valid {
		return registryCredentials{}, false
	}

	var bundle licenseBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return registryCredentials{}, false
	}
	// Bridge registry credentials embedded in the license are an offline /
	// airgap fallback. They MUST be opt-in via an explicit signed claim and
	// are accepted only after the complete certificate verifies above.
	if mode := stringValue(bundle.Payload["registry_mode"]); mode != "bridge" {
		return registryCredentials{}, false
	}
	registry, ok := bundle.Payload["registry"].(map[string]interface{})
	if !ok {
		return registryCredentials{}, false
	}
	creds := registryCredentials{
		Host:     strings.ToLower(strings.TrimSpace(stringValue(registry["host"]))),
		Username: strings.TrimSpace(stringValue(registry["username"])),
		Token:    strings.TrimSpace(stringValue(registry["token"])),
	}
	if creds.Host != "ghcr.io" || creds.Username == "" || creds.Token == "" {
		return registryCredentials{}, false
	}
	return creds, true
}

func (a *App) registryCredentialsFromLicense() (registryCredentials, bool) {
	data, err := os.ReadFile(a.licensePath())
	if err != nil {
		return registryCredentials{}, false
	}
	return registryCredentialsFromLicenseData(data, []byte(licensePublicKeyPEM))
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

func validateRegistryTokenResponse(tokenResp registryTokenResponse, repositories []string) (registryCredentials, error) {
	secret := tokenResp.Token
	if secret == "" {
		secret = tokenResp.IdentityToken
	}
	if secret == "" {
		secret = tokenResp.RegistryToken
	}
	creds := registryCredentials{
		Host:     stringValueOrDefault(tokenResp.Host, "ghcr.io"),
		Username: stringValueOrDefault(tokenResp.Username, "oauth2"),
		Token:    secret,
	}
	expiresAt, expiryErr := time.Parse(time.RFC3339, tokenResp.ExpiresAt)
	now := time.Now().UTC()
	if expiryErr != nil || now.After(expiresAt) || expiresAt.After(now.Add(15*time.Minute+30*time.Second)) {
		return registryCredentials{}, fmt.Errorf("registry token broker returned an invalid expiry")
	}
	if !slices.Equal(tokenResp.Repositories, repositories) {
		return registryCredentials{}, fmt.Errorf("registry token broker returned an unexpected repository scope")
	}
	if creds.Host != "ghcr.io" || creds.Token == "" {
		return registryCredentials{}, fmt.Errorf("registry token broker response did not include valid GHCR credentials")
	}
	return creds, nil
}

func (a *App) registryCredentialsFromBroker(groupIDs []string, groupMap map[string]ServiceGroup) (registryCredentials, bool, error) {
	tokenURL := strings.TrimSpace(os.Getenv("LIGANDX_REGISTRY_TOKEN_URL"))
	if tokenURL == "" {
		return registryCredentials{}, false, nil
	}
	parsedTokenURL, parseErr := url.Parse(tokenURL)
	if parseErr != nil || parsedTokenURL.Scheme != "https" || parsedTokenURL.Hostname() == "" || parsedTokenURL.User != nil {
		return registryCredentials{}, true, fmt.Errorf("LIGANDX_REGISTRY_TOKEN_URL must be an HTTPS URL without embedded credentials")
	}
	accessToken := strings.TrimSpace(os.Getenv("LIGANDX_VENDOR_ACCESS_TOKEN"))
	if accessToken == "" {
		return registryCredentials{}, true, fmt.Errorf("LIGANDX_VENDOR_ACCESS_TOKEN is required when LIGANDX_REGISTRY_TOKEN_URL is set")
	}
	license := a.GetLicenseStatus()
	if !license.Valid || license.Edition == "free" {
		return registryCredentials{}, true, fmt.Errorf("valid Pro or Academic license required before requesting registry credentials")
	}
	version, _ := a.productionImageSettings()
	if !isPinnedImageVersion(version) {
		return registryCredentials{}, true, fmt.Errorf("registry token request requires an immutable VERSION")
	}
	repositories := selectedProRepositories(groupIDs, groupMap)
	reqBody := registryTokenRequest{
		LicenseID:    license.LicenseID,
		Groups:       groupIDs,
		Repositories: repositories,
		Entitlements: license.Entitlements,
		MachineID:    machineID(),
		Version:      version,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return registryCredentials{}, true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return registryCredentials{}, true, err
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
		return registryCredentials{}, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return registryCredentials{}, true, fmt.Errorf("registry token broker returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}
	var tokenResp registryTokenResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	if err := decoder.Decode(&tokenResp); err != nil {
		return registryCredentials{}, true, err
	}
	creds, validationErr := validateRegistryTokenResponse(tokenResp, repositories)
	if validationErr != nil {
		return registryCredentials{}, true, validationErr
	}
	return creds, true, nil
}

func stringValueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type bridgeCredentialLoader func() (registryCredentials, bool)

func (a *App) registryCredentialsForProImagesForBuild(
	groupIDs []string,
	groupMap map[string]ServiceGroup,
	publicBuild bool,
	loadBridge bridgeCredentialLoader,
) (registryCredentials, bool, error) {
	if !needsProRegistryAuth(groupIDs, groupMap) {
		return registryCredentials{}, false, nil
	}

	if creds, configured, err := a.registryCredentialsFromBroker(groupIDs, groupMap); configured || err != nil {
		return creds, configured && err == nil, err
	}

	if creds, ok := loadBridge(); ok {
		return creds, true, nil
	}

	if publicBuild {
		return registryCredentials{}, false, fmt.Errorf("public launcher requires the short-lived registry token broker or signed bridge credentials")
	}
	return registryCredentials{}, false, fmt.Errorf("Pro image pull requires LIGANDX_REGISTRY_TOKEN_URL/LIGANDX_VENDOR_ACCESS_TOKEN or signed bridge credentials in the license")
}

func (a *App) registryCredentialsForProImages(groupIDs []string, groupMap map[string]ServiceGroup) (registryCredentials, bool, error) {
	return a.registryCredentialsForProImagesForBuild(
		groupIDs,
		groupMap,
		isPublicBuild,
		a.registryCredentialsFromLicense,
	)
}

func encodeRegistryAuth(creds registryCredentials) (string, error) {
	if creds.Host == "" || creds.Token == "" {
		return "", nil
	}
	payload := map[string]string{
		"username":      creds.Username,
		"password":      creds.Token,
		"serveraddress": creds.Host,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
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

func (a *App) PullServiceGroups(groupIDs []string) {
	go func() {
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
			registryAuth, encodeErr = encodeRegistryAuth(creds)
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
				imgIdx := globalImgIdx
				ctx, cancel := context.WithCancel(a.ctx)

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
				} else if err := a.pullImageWithProgress(ctx, image, groupID, group.Name, imgIdx, totalImagesAll, imageAuth); err != nil {
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

				cancel()
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
