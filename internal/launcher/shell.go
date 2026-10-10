package launcher

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ligandx-launcher/internal/envfile"
	"ligandx-launcher/internal/hostmetrics"

	"github.com/moby/moby/client"
)

// StorageEstimate is what the service picker shows before a download: how much
// the not-yet-present selection needs and how much the Docker volume has free.
type StorageEstimate struct {
	DownloadBytes uint64 `json:"downloadBytes"`
	RequiredBytes uint64 `json:"requiredBytes"`
	FreeBytes     uint64 `json:"freeBytes"`
	FreeKnown     bool   `json:"freeKnown"`
	Path          string `json:"path"`
	Warning       string `json:"warning"`
	Blocked       string `json:"blocked"`
}

// GetStorageEstimate applies the same thresholds the pull pre-flight enforces,
// so the picker can say "this will not fit" before the user starts a download.
func (a *App) GetStorageEstimate(groupIDs []string) StorageEstimate {
	groupMap := make(map[string]ServiceGroup)
	for _, g := range a.GetServiceGroups() {
		groupMap[g.ID] = g
	}
	present := a.CheckImagePresence()

	var downloadMB int
	for _, id := range groupIDs {
		if present[id] {
			continue
		}
		if g, ok := groupMap[id]; ok {
			downloadMB += g.SizeMB
		}
	}
	estimate := StorageEstimate{
		DownloadBytes: uint64(downloadMB) << 20,
		RequiredBytes: (uint64(downloadMB) << 20) * unpackFactor,
		Path:          a.dockerStoragePath(),
	}
	free, ok := hostmetrics.DiskFree(estimate.Path)
	if !ok {
		return estimate // cannot tell; the picker shows the download size alone
	}
	estimate.FreeBytes, estimate.FreeKnown = free, true
	if downloadMB == 0 {
		return estimate // nothing to download, so free space is not a concern
	}
	warning, err := a.checkDiskSpaceWithFree(groupIDs, groupMap, present, free, estimate.Path)
	if err != nil {
		estimate.Blocked = err.Error()
	}
	estimate.Warning = warning
	return estimate
}

const activeJobQuery = "SELECT count(*) FROM jobs WHERE status IN ('pending','queued','running','processing','preparing');"

// activeJobCount asks the running stack how many jobs would be interrupted by a
// stop. It needs postgres up, so an error means "unknown", not "none".
func (a *App) activeJobCount(ctx context.Context) (int, error) {
	args := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml",
		"exec", "-T", "postgres", "psql", "-U", "ligandx", "-d", "ligandx", "-Atc", activeJobQuery}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = a.projectPath
	cmd.Env = a.composeEnv()
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("invalid active-job count")
	}
	return count, nil
}

// ActiveJobCount lets the UI warn before Stop or Restart interrupts running
// work. The short timeout keeps a wedged database from blocking the button.
func (a *App) ActiveJobCount() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return a.activeJobCount(ctx)
}

// AppInfo is the "about this install" block: what a user needs to find their
// files, reach the application, or describe their setup in a support request.
type AppInfo struct {
	LauncherVersion string `json:"launcherVersion"`
	RuntimeVersion  string `json:"runtimeVersion"`
	ProjectPath     string `json:"projectPath"`
	LogPath         string `json:"logPath"`
	AppURL          string `json:"appUrl"`
	Username        string `json:"username"`
	GPUAvailable    bool   `json:"gpuAvailable"`
}

func (a *App) GetAppInfo() AppInfo {
	info := AppInfo{
		LauncherVersion: launcherVersion,
		RuntimeVersion:  a.GetDistributionStatus().InstalledVersion,
		ProjectPath:     a.projectPath,
		LogPath:         a.composeLogPath(),
		AppURL:          fmt.Sprintf("http://localhost:%d", a.envPort("APP_PORT", 8080)),
		GPUAvailable:    a.CheckGPU(),
	}
	if content, err := a.GetEnvContent("prod"); err == nil {
		info.Username = strings.TrimSpace(envfile.Parse(content)["LIGANDX_USERNAME"])
	}
	return info
}

// OpenProjectFolder shows the install directory (runtime files, data, logs) in
// the system file manager.
func (a *App) OpenProjectFolder() {
	if a.projectPath != "" {
		a.OpenBrowser(a.projectPath)
	}
}

// unusedGroupImages returns the image references that belong to the removed
// groups and to no group the user is keeping. Required groups always count as
// kept, so a shared or core image can never be selected for deletion.
func unusedGroupImages(groups []ServiceGroup, removedIDs, keptIDs []string) []string {
	kept := make(map[string]bool)
	for _, id := range keptIDs {
		kept[id] = true
	}
	removed := make(map[string]bool)
	for _, id := range removedIDs {
		removed[id] = true
	}
	stillNeeded := make(map[string]bool)
	for _, g := range groups {
		if g.Required || kept[g.ID] {
			for _, image := range g.Images {
				stillNeeded[image] = true
			}
		}
	}
	var unused []string
	seen := make(map[string]bool)
	for _, g := range groups {
		if !removed[g.ID] || g.Required || kept[g.ID] {
			continue
		}
		for _, image := range g.Images {
			if !stillNeeded[image] && !seen[image] {
				seen[image] = true
				unused = append(unused, image)
			}
		}
	}
	return unused
}

// RemoveUnusedGroupImages frees the disk space of modules the user deselected.
// It removes images by exact reference only; Docker itself refuses to remove an
// image a container still uses, which is reported rather than forced.
func (a *App) RemoveUnusedGroupImages(removedIDs, keptIDs []string) (int, error) {
	if a.dockerClient == nil {
		a.initDockerClient()
	}
	if a.dockerClient == nil {
		return 0, fmt.Errorf("docker client not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var removedCount int
	var failures []string
	for _, image := range unusedGroupImages(a.GetServiceGroups(), removedIDs, keptIDs) {
		if _, err := a.dockerClient.ImageRemove(ctx, image, client.ImageRemoveOptions{PruneChildren: true}); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such image") {
				continue // never downloaded; nothing to free
			}
			a.emitAndLog("launcher", fmt.Sprintf("Could not remove image %s: %v", image, err))
			failures = append(failures, image)
			continue
		}
		removedCount++
	}
	if len(failures) > 0 {
		return removedCount, fmt.Errorf("%d image(s) are still in use and were kept. Stop services, then try again", len(failures))
	}
	return removedCount, nil
}

// RestartService bounces one running service, for the "this one is unhealthy"
// case. Unlike `compose up -d`, which leaves an unchanged container alone,
// `compose restart` always stops and starts it.
func (a *App) RestartService(service string) error {
	if !ligandxServiceSet[service] {
		return fmt.Errorf("unknown service: %s", service)
	}
	if err := a.validateUnlockedServices([]string{service}); err != nil {
		return err
	}
	args := append([]string{"compose"}, a.prodEnvArgs()...)
	args = append(args, "-f", "docker-compose.yml")
	args = append(args, a.gpuComposeArgs()...)
	args = append(args, "restart", service)
	return a.runDockerCompose(args, fmt.Sprintf("Restarting %s...", service))
}
