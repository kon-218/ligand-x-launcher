package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
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
	DockerInstalled bool            `json:"dockerInstalled"`
	DockerRunning   bool            `json:"dockerRunning"`
	Services        []ServiceStatus `json:"services"`
	TotalRunning    int             `json:"totalRunning"`
	TotalServices   int             `json:"totalServices"`
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
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Services    []string `json:"services"`
	Images      []string `json:"images"`
	SizeMB      int      `json:"sizeMb"`
	Required    bool     `json:"required"`
	DefaultOn   bool     `json:"defaultOn"`
	Edition     string   `json:"edition"`
	Entitlement string   `json:"entitlement"`
	Licensed    bool     `json:"licensed"`
	Locked      bool     `json:"locked"`
}

type LauncherConfig struct {
	FirstRunDone   bool        `json:"firstRunDone"`
	SelectedGroups []string    `json:"selectedGroups"`
	UserProfile    UserProfile `json:"userProfile"`
	ConfigVersion  int         `json:"configVersion"`
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
	"kinetics":    true,
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
	"worker-kinetics": true,
}

// ligandxServiceSet is every docker-compose service name that belongs to the
// Ligand-X stack. Used to recognize our containers when listing status and when
// stopping the stack.
var ligandxServiceSet = map[string]bool{
	"gateway": true, "frontend": true, "proxy": true, "structure": true,
	"docking": true, "md": true, "admet": true, "boltz2": true,
	"qc": true, "alignment": true, "ketcher": true, "msa": true,
	"abfe": true, "rbfe": true, "reinvent": true, "kinetics": true,
	"pocket-finder": true, "postgres": true, "redis": true, "rabbitmq": true,
	"worker-qc": true, "worker-gpu-short": true, "worker-gpu-long": true,
	"worker-cpu": true, "worker-reinvent": true, "worker-kinetics": true, "flower": true,
}

// isLigandxProject reports whether a compose project name looks like a Ligand-X
// stack (matches the filter used across status detection).
func isLigandxProject(projectName string) bool {
	return strings.Contains(projectName, "ligand") || projectName == "ligandx"
}

const defaultRuntimeBundleURL = "https://github.com/kon-218/ligand-x/releases/latest/download/ligand-x-runtime.zip"

const runtimeBundleAssetName = "ligand-x-runtime.zip"

const latestReleaseAPIURL = "https://api.github.com/repos/kon-218/ligand-x/releases/latest"

const licensePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAcKQKljOJr+vNjOKVewo7sDMaguZUqIJVhYZDgDhnUlE=
-----END PUBLIC KEY-----`

type App struct {
	ctx           context.Context
	dockerClient  *client.Client
	projectPath   string
	logStreams    map[string]context.CancelFunc
	logStreamsMux sync.Mutex

	// Cloudflare tunnel (see tunnel.go)
	tunnelCmd *exec.Cmd
	tunnelMux sync.Mutex
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

// resolveLatestRuntimeBundleURL queries the GitHub releases API to find the
// download URL of the runtime bundle asset attached to the latest release.
// GitHub's /releases/latest/download/<asset> redirect is unreliable on some
// Windows HTTP clients, so we resolve the concrete asset URL explicitly.
func resolveLatestRuntimeBundleURL() (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, latestReleaseAPIURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ligand-x-launcher")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub releases API returned HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", fmt.Errorf("failed to parse GitHub releases response: %w", err)
	}

	for _, asset := range release.Assets {
		if asset.Name == runtimeBundleAssetName && strings.TrimSpace(asset.BrowserDownloadURL) != "" {
			return asset.BrowserDownloadURL, strings.TrimSpace(release.TagName), nil
		}
	}
	return "", "", fmt.Errorf("asset %q not found in latest release %q", runtimeBundleAssetName, release.TagName)
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
	if _, ok := a.findProjectPath(); ok {
		return a.GetDistributionStatus(), nil
	}

	runtimeDir, err := a.defaultRuntimeDir()
	if err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("could not determine runtime directory: %w", err)
	}
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to create runtime directory: %w", err)
	}

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: "Installing Ligand-X runtime files...", Timestamp: time.Now().Format("15:04:05")})

	var bundleURL string
	var releaseTag string
	if override := strings.TrimSpace(os.Getenv("LIGANDX_RUNTIME_BUNDLE_URL")); override != "" {
		bundleURL = override
	} else if resolved, tag, resolveErr := resolveLatestRuntimeBundleURL(); resolveErr == nil {
		bundleURL = resolved
		releaseTag = tag
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Resolved latest runtime bundle: %s", bundleURL), Timestamp: time.Now().Format("15:04:05")})
	} else {
		bundleURL = defaultRuntimeBundleURL
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Could not resolve latest release (%v); falling back to %s", resolveErr, bundleURL), Timestamp: time.Now().Format("15:04:05")})
	}

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Downloading runtime bundle from: %s", bundleURL), Timestamp: time.Now().Format("15:04:05")})

	zipPath := filepath.Join(runtimeDir, "ligand-x-runtime.zip")
	if err := downloadFile(bundleURL, zipPath); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to download runtime bundle from %s: %w", bundleURL, err)
	}
	defer os.Remove(zipPath)

	if err := extractRuntimeBundle(zipPath, runtimeDir); err != nil {
		return a.GetDistributionStatus(), fmt.Errorf("failed to extract runtime bundle: %w", err)
	}

	a.projectPath = runtimeDir
	if err := a.ensureProductionEnv(); err != nil {
		return a.GetDistributionStatus(), err
	}
	if releaseTag != "" {
		content, readErr := a.GetEnvContent("prod")
		if readErr == nil {
			parsed := parseEnvFile(content)
			if isEnvPlaceholder(parsed["VERSION"]) || strings.EqualFold(strings.TrimSpace(parsed["VERSION"]), "latest") {
				if setErr := a.setProductionEnvValue("VERSION", releaseTag); setErr == nil {
					wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Pinned VERSION=%s in .env.production", releaseTag), Timestamp: time.Now().Format("15:04:05")})
				}
			}
		}
	}
	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{Service: "launcher", Message: fmt.Sprintf("Runtime installed at %s", runtimeDir), Timestamp: time.Now().Format("15:04:05")})
	return a.GetDistributionStatus(), nil
}

func downloadFile(sourceURL, dest string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return err
	}
	if parsed.Scheme == "file" || parsed.Scheme == "" {
		path := parsed.Path
		if parsed.Scheme == "" {
			path = sourceURL
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	}

	client := &http.Client{Timeout: 20 * time.Minute}
	resp, err := client.Get(sourceURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, sourceURL)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func extractRuntimeBundle(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
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
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
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
		"docker-compose.yml":       true,
		".env.production.template": true,
		"LICENSE":                  true,
		"README.md":                true,
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
	status := SystemStatus{
		Services: []ServiceStatus{},
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

	var args []string
	var services []string

	// Load launcher config to get selected service groups
	config, err := a.GetLauncherConfig()
	if err != nil || config.SelectedGroups == nil || len(config.SelectedGroups) == 0 {
		// Fallback to legacy mode behavior if config not available
		switch mode {
		case "dev":
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never")
		case "prod":
			if _, err := a.requirePinnedProductionVersion(); err != nil {
				return err
			}
			args = append([]string{"compose"}, a.prodEnvArgs()...)
			args = append(args, "-f", "docker-compose.yml")
			args = append(args, a.gpuComposeArgs()...)
			args = append(args, "up", "-d", "--pull=never")
		case "core":
			coreServices := []string{"postgres", "redis", "rabbitmq", "gateway", "frontend", "proxy", "structure", "flower"}
			if !isPublicBuild {
				coreServices = append(coreServices, "pocket-finder")
			}
			args = append(a.devComposeArgs(), append([]string{"up", "-d", "--pull=never"}, coreServices...)...)
		case "docking":
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never", "postgres", "redis", "rabbitmq", "gateway", "frontend", "structure", "ketcher", "docking", "worker-cpu")
		case "md":
			args = append(a.devComposeArgs(), "up", "-d", "--pull=never", "postgres", "redis", "rabbitmq", "gateway", "frontend", "structure", "ketcher", "md", "worker-gpu-short")
		default:
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

	if err := a.ensureDataDirs(); err != nil {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   fmt.Sprintf("Warning: Could not create data directories: %v", err),
			Timestamp: time.Now().Format("15:04:05"),
		})
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
	ligandProjects := make(map[string]bool)
	for _, c := range containers {
		proj := c.Labels["com.docker.compose.project"]
		if proj == "" || !isLigandxProject(proj) {
			continue
		}
		if ligandxServiceSet[c.Labels["com.docker.compose.service"]] {
			ligandProjects[proj] = true
		}
	}

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

	args := append(a.devComposeArgs(), "up", "-d", "--pull=never")
	args = append(args, services...)
	return a.runDockerCompose(args, fmt.Sprintf("Restarting %d services...", len(services)))
}

func (a *App) RestartServicesCustom(services []string) error {
	if err := a.validateUnlockedServices(services); err != nil {
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
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   "launcher",
			Message:   errMsg,
			Timestamp: time.Now().Format("15:04:05"),
		})
		return fmt.Errorf("%s", errMsg)
	}

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   message,
		Timestamp: time.Now().Format("15:04:05"),
	})

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   fmt.Sprintf("Working directory: %s", a.projectPath),
		Timestamp: time.Now().Format("15:04:05"),
	})

	cmd := exec.Command("docker", args...)
	cmd.Dir = a.projectPath

	uid := os.Getuid()
	gid := os.Getgid()
	if uid < 0 { // os.Getuid() returns -1 on Windows
		uid = 0
	}
	if gid < 0 {
		gid = 0
	}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("UID=%d", uid),
		fmt.Sprintf("GID=%d", gid),
		// Pin the compose project name so up/down/status agree regardless of the
		// install directory's basename. Keep the historical "ligand-x" name so
		// existing named volumes (especially ligand-x_postgres_data) remain visible.
		"COMPOSE_PROJECT_NAME=ligand-x",
	)
	if path, ok := a.proSourcePath(); ok {
		cmd.Env = append(cmd.Env, fmt.Sprintf("LIGANDX_PRO_SRC_PATH=%s", path))
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker compose: %v", err)
	}

	go a.streamOutput(stdout, "docker")
	go a.streamOutput(stderr, "docker")

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker compose failed: %v", err)
	}

	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   "Operation completed successfully",
		Timestamp: time.Now().Format("15:04:05"),
	})

	return nil
}

func (a *App) streamOutput(r io.Reader, service string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
			Service:   service,
			Message:   scanner.Text(),
			Timestamp: time.Now().Format("15:04:05"),
		})
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

func (a *App) OpenFrontend() {
	a.OpenBrowser("http://localhost:8080") // reverse proxy (APP_PORT); single same-origin entry
}

func (a *App) OpenAPI() {
	a.OpenBrowser("http://localhost:8000/docs")
}

func (a *App) OpenFlower() {
	a.OpenBrowser("http://localhost:5555/flower")
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
		return string(data), nil
	}

	// env file doesn't exist — load template and auto-save it as the env file
	templatePath := filepath.Join(a.projectPath, templateFile)
	data, err = os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("no %s file found and could not read %s: %v", envFile, templateFile, err)
	}

	// Write template as the starting env file so docker compose can read it immediately
	_ = os.WriteFile(envPath, data, 0644)

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
	return os.WriteFile(envPath, []byte(content), 0644)
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

func (a *App) setEnvFileValue(fileName, key, value string) error {
	envPath := filepath.Join(a.projectPath, fileName)
	data, _ := os.ReadFile(envPath)
	lines := strings.Split(string(data), "\n")
	prefix := key + "="
	updated := false
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			lines[i] = prefix + value
			updated = true
			break
		}
	}
	if !updated {
		lines = append(lines, prefix+value)
	}
	return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0644)
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
	secretKeys := []string{"POSTGRES_PASSWORD", "RABBITMQ_PASSWORD", "REDIS_PASSWORD", "QC_SECRET_KEY", "LIGANDX_API_KEY", "LIGANDX_PASSWORD", "FLOWER_PASSWORD"}
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
	if err := a.setProductionEnvValue("CORS_ORIGINS", "http://localhost:8080,http://127.0.0.1:8080"); err != nil {
		return err
	}
	return nil
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
	if isPublicBuild {
		return "Essential services: Proxy, Gateway, Frontend, Structure, and supporting infrastructure"
	}
	return "Essential services: Proxy, Gateway, Frontend, Structure, Pocket Finder (fpocket / DeepPocket / etc.), and supporting infrastructure"
}

func coreServiceNames() []string {
	services := []string{"postgres", "redis", "rabbitmq", "gateway", "frontend", "proxy", "structure", "alignment", "ketcher", "msa", "worker-cpu", "flower"}
	if !isPublicBuild {
		services = append(services, "pocket-finder")
	}
	return services
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
		imageRef("ghcr.io/kon-218/ligand-x/gateway", version),
		imageRef("ghcr.io/kon-218/ligand-x/frontend", version),
		"nginx:1.27-alpine",
		imageRef("ghcr.io/kon-218/ligand-x/structure", version),
		imageRef("ghcr.io/kon-218/ligand-x/alignment", version),
		imageRef("ghcr.io/kon-218/ligand-x/ketcher", version),
		imageRef("ghcr.io/kon-218/ligand-x/msa", version),
		imageRef("ghcr.io/kon-218/ligand-x/worker-cpu", version),
		"redis:7-alpine",
		"postgres:16-alpine",
		"rabbitmq:3.13-management-alpine",
	}
	if !isPublicBuild {
		images = append(images[:3], append([]string{imageRef("ghcr.io/kon-218/ligand-x/pocket-finder", version)}, images[3:]...)...)
	}
	return images
}

func (a *App) GetServiceGroups() []ServiceGroup {
	license := a.GetLicenseStatus()
	version, proPrefix := a.productionImageSettings()
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
			Images: []string{
				imageRef(proPrefix+"/admet", version),
			},
			SizeMB:      1500,
			Required:    false,
			DefaultOn:   false,
			Edition:     "pro",
			Entitlement: "admet",
		},
		{
			ID:          "free-energy",
			Name:        "Binding Free Energy",
			Description: "Pro package: ABFE/RBFE binding free energy calculations",
			Services:    []string{"abfe", "rbfe", "worker-gpu-long"},
			Images: []string{
				imageRef(proPrefix+"/abfe", version),
				imageRef(proPrefix+"/rbfe", version),
				imageRef(proPrefix+"/worker-gpu-long", version),
			},
			SizeMB:      5500,
			Required:    false,
			DefaultOn:   false,
			Edition:     "pro",
			Entitlement: "free-energy",
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
			Images: []string{
				imageRef(proPrefix+"/boltz2", version),
			},
			SizeMB:      6000,
			Required:    false,
			DefaultOn:   false,
			Edition:     "pro",
			Entitlement: "boltz2",
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
		{
			ID:          "kinetics",
			Name:        "Kinetics (WESTPA / RAMD)",
			Description: "Pro package: GPU weighted-ensemble unbinding kinetics",
			Services:    []string{"kinetics", "worker-kinetics"},
			Images: []string{
				imageRef(proPrefix+"/kinetics", version),
				imageRef(proPrefix+"/worker-kinetics", version),
			},
			SizeMB:      4000,
			Required:    false,
			DefaultOn:   false,
			Edition:     "pro",
			Entitlement: "kinetics",
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
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
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

	apiKey, err := generateAPIKey()
	if err != nil {
		return LauncherConfig{}, fmt.Errorf("failed to generate API key: %w", err)
	}

	if _, err := a.GetEnvContent("dev"); err != nil {
		return LauncherConfig{}, err
	}
	setters := []func(string, string) error{a.setEnvValue}
	if _, err := a.GetEnvContent("prod"); err == nil {
		setters = append(setters, a.setProductionEnvValue)
	}
	for _, setter := range setters {
		if err := setter("LIGANDX_USERNAME", username); err != nil {
			return LauncherConfig{}, err
		}
		if err := setter("LIGANDX_PASSWORD", password); err != nil {
			return LauncherConfig{}, err
		}
		if err := setter("LIGANDX_API_KEY", apiKey); err != nil {
			return LauncherConfig{}, err
		}
	}

	config, _ := a.GetLauncherConfig()
	config.UserProfile = UserProfile{Username: username, Email: email}
	config.ConfigVersion = 2
	if err := a.SaveLauncherConfig(config); err != nil {
		return LauncherConfig{}, err
	}
	return config, nil
}

// UpdatePassword updates LIGANDX_PASSWORD in both env files without
// touching any other credentials or regenerating the API key.
func (a *App) UpdatePassword(newPassword string) error {
	if err := validateEnvCredential("password", newPassword, 8); err != nil {
		return err
	}
	if _, err := a.GetEnvContent("dev"); err == nil {
		if err := a.setEnvValue("LIGANDX_PASSWORD", newPassword); err != nil {
			return err
		}
	}
	if _, err := a.GetEnvContent("prod"); err == nil {
		if err := a.setProductionEnvValue("LIGANDX_PASSWORD", newPassword); err != nil {
			return err
		}
	}
	return nil
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

	dest := a.licensePath()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return status, err
	}
	// The gateway runs as a non-root container user and reads this file via a
	// read-only bind mount. The license certificate is signed, not a private key;
	// keep it host-readable so Docker UID/GID mappings do not downgrade users to Free.
	if err := os.WriteFile(dest, data, 0644); err != nil {
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
		entitlements = []string{"admet", "boltz2", "free-energy", "kinetics", "qc", "reinvent"}
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

func (a *App) registryCredentialsFromLicense() (registryCredentials, bool) {
	data, err := os.ReadFile(a.licensePath())
	if err != nil {
		return registryCredentials{}, false
	}
	var bundle licenseBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return registryCredentials{}, false
	}
	// Bridge registry credentials embedded in the license are an offline /
	// airgap fallback. They MUST be opt-in via an explicit signed claim so
	// that misconfiguration cannot silently leak long-lived tokens to anyone
	// who exfiltrates the license file.
	if mode := stringValue(bundle.Payload["registry_mode"]); mode != "bridge" {
		return registryCredentials{}, false
	}
	registry, ok := bundle.Payload["registry"].(map[string]interface{})
	if !ok {
		return registryCredentials{}, false
	}
	creds := registryCredentials{
		Host:     stringValue(registry["host"]),
		Username: stringValue(registry["username"]),
		Token:    stringValue(registry["token"]),
	}
	return creds, creds.Host != "" && creds.Username != "" && creds.Token != ""
}

func needsProRegistryAuth(groupIDs []string, groupMap map[string]ServiceGroup) bool {
	for _, groupID := range groupIDs {
		if group, ok := groupMap[groupID]; ok && group.Edition == "pro" {
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
		if !ok || group.Edition != "pro" {
			continue
		}
		for _, image := range group.Images {
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

func (a *App) registryCredentialsFromBroker(groupIDs []string, groupMap map[string]ServiceGroup) (registryCredentials, bool, error) {
	tokenURL := strings.TrimSpace(os.Getenv("LIGANDX_REGISTRY_TOKEN_URL"))
	if tokenURL == "" {
		return registryCredentials{}, false, nil
	}

	accessToken := strings.TrimSpace(os.Getenv("LIGANDX_VENDOR_ACCESS_TOKEN"))
	if accessToken == "" {
		return registryCredentials{}, true, fmt.Errorf("LIGANDX_VENDOR_ACCESS_TOKEN is required when LIGANDX_REGISTRY_TOKEN_URL is set")
	}

	license := a.GetLicenseStatus()
	if !license.Valid || license.Edition == "free" {
		return registryCredentials{}, true, fmt.Errorf("valid Pro or Academic license required before requesting registry credentials")
	}

	reqBody := registryTokenRequest{
		LicenseID:    license.LicenseID,
		Groups:       groupIDs,
		Repositories: selectedProRepositories(groupIDs, groupMap),
		Entitlements: license.Entitlements,
		MachineID:    machineID(),
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return registryCredentials{}, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return registryCredentials{}, true, fmt.Errorf("registry token broker returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}

	var tokenResp registryTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return registryCredentials{}, true, err
	}
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
	if creds.Token == "" {
		return registryCredentials{}, true, fmt.Errorf("registry token broker response did not include a token")
	}
	return creds, true, nil
}

func stringValueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (a *App) registryCredentialsForProImages(groupIDs []string, groupMap map[string]ServiceGroup) (registryCredentials, bool, error) {
	if !needsProRegistryAuth(groupIDs, groupMap) {
		return registryCredentials{}, false, nil
	}

	if creds, configured, err := a.registryCredentialsFromBroker(groupIDs, groupMap); configured || err != nil {
		return creds, configured && err == nil, err
	}

	creds, ok := a.registryCredentialsFromLicense()
	if !ok {
		return registryCredentials{}, false, fmt.Errorf("Pro image pull requires LIGANDX_REGISTRY_TOKEN_URL/LIGANDX_VENDOR_ACCESS_TOKEN or bridge registry credentials in the license")
	}
	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   "launcher",
		Message:   "Warning: using bridge registry credentials embedded in the license. Configure LIGANDX_REGISTRY_TOKEN_URL for production token-broker auth.",
		Timestamp: time.Now().Format("15:04:05"),
	})
	return creds, true, nil
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
				if group.Edition == "pro" {
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
