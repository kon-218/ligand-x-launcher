package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// uninstallConfirmPhrase must be echoed back by the caller before anything is
// deleted. Wails binds every exported method on App, so an accidental or
// injected call to Uninstall() would otherwise destroy the user's data with no
// interaction at all. The UI collects this by making the user type it.
const uninstallConfirmPhrase = "UNINSTALL"

// sharedBaseImages are third-party images the stack pulls but does not own.
// They are deliberately NOT removed: `postgres:16-alpine` or `nginx` may well be
// backing something else on this machine, and silently deleting them would make
// uninstalling Ligand-X break an unrelated project. They are reported instead so
// the user can prune them by hand if they want to.
var sharedBaseImages = []string{
	"alpine:3.21",
	"mher/flower:2.0",
	"nginx:1.27-alpine",
	"postgres:16-alpine",
	"rabbitmq:3.13-management-alpine",
	"redis:7-alpine",
}

// UninstallOptions selects how much of the install to remove. The zero value is
// the full "as if it had never been installed" removal; each flag opts out of
// one part.
type UninstallOptions struct {
	// Confirm must equal uninstallConfirmPhrase.
	Confirm string `json:"confirm"`
	// KeepImages leaves the downloaded Ligand-X images in place, so a later
	// reinstall does not have to re-pull tens of GB.
	KeepImages bool `json:"keepImages"`
	// KeepData leaves the Docker volumes — every docking result, MD trajectory
	// and database row — in place.
	KeepData bool `json:"keepData"`
	// KeepLauncher leaves the launcher executable on disk.
	KeepLauncher bool `json:"keepLauncher"`
}

// UninstallStep is one line of the report shown to the user.
type UninstallStep struct {
	Name   string `json:"name"`
	Status string `json:"status"` // done | skipped | failed
	Detail string `json:"detail"`
}

// UninstallReport is the outcome. Uninstall is best-effort by design: a failure
// to remove one image must not abandon the runtime directory half-deleted, so
// every step runs and records its own result rather than returning early.
type UninstallReport struct {
	Steps       []UninstallStep `json:"steps"`
	ManualSteps []string        `json:"manualSteps"`
	Complete    bool            `json:"complete"`
}

func (r *UninstallReport) add(name, status, detail string) {
	r.Steps = append(r.Steps, UninstallStep{Name: name, Status: status, Detail: detail})
}

// ligandxComposeProjects returns the compose projects that own at least one
// known Ligand-X service. Membership is decided by a service we recognize, not
// by the project name alone, so a user project that merely has "ligand" in its
// name is never swept up.
func ligandxComposeProjects(containers []container.Summary) map[string]bool {
	projects := make(map[string]bool)
	for _, c := range containers {
		proj := c.Labels["com.docker.compose.project"]
		if proj == "" || !isLigandxProject(proj) {
			continue
		}
		if ligandxServiceSet[c.Labels["com.docker.compose.service"]] {
			projects[proj] = true
		}
	}
	return projects
}

// selectProjectContainers returns every container belonging to one of projects —
// including ones whose service we do not recognize, such as celery-beat, since
// the project as a whole is ours once it has been identified.
func selectProjectContainers(containers []container.Summary, projects map[string]bool) []string {
	var ids []string
	for _, c := range containers {
		if projects[c.Labels["com.docker.compose.project"]] {
			ids = append(ids, c.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// selectLigandxVolumes picks the volumes to destroy. A compose-created volume
// carries its project label, which is the reliable signal. The name-prefix
// fallback covers a volume whose label is missing (older compose versions), and
// is deliberately restricted to projects already confirmed via a container —
// matching bare "ligand" against volume names would happily delete a user's own
// `my-ligand-data`.
func selectLigandxVolumes(volumes []volume.Volume, projects map[string]bool) []string {
	var names []string
	for _, v := range volumes {
		if proj := v.Labels["com.docker.compose.project"]; proj != "" {
			if isLigandxProject(proj) {
				names = append(names, v.Name)
			}
			continue
		}
		for proj := range projects {
			if strings.HasPrefix(v.Name, proj+"_") {
				names = append(names, v.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

// selectLigandxNetworks mirrors selectLigandxVolumes for compose networks.
func selectLigandxNetworks(networks []network.Summary, projects map[string]bool) []string {
	var ids []string
	for _, n := range networks {
		if proj := n.Labels["com.docker.compose.project"]; proj != "" {
			if isLigandxProject(proj) {
				ids = append(ids, n.ID)
			}
			continue
		}
		for proj := range projects {
			if strings.HasPrefix(n.Name, proj+"_") {
				ids = append(ids, n.ID)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// ligandxImageRepoPrefixes are the repositories whose images belong to this
// product. Images are matched on repository prefix and nothing else: a substring
// match on "ligand" would take a user's own `ligand-tools:dev` with it, and the
// label-based approach used for containers is unavailable because images carry
// no compose project.
func ligandxImageRepoPrefixes(proPrefix string) []string {
	prefixes := []string{
		"ghcr.io/kon-218/ligand-x/",
		"ghcr.io/kon-218/ligand-x-pro/",
	}
	if p := strings.TrimSuffix(strings.TrimSpace(proPrefix), "/"); p != "" {
		prefixes = append(prefixes, p+"/")
	}
	sort.Strings(prefixes)
	return slicesCompact(prefixes)
}

func slicesCompact(in []string) []string {
	out := in[:0]
	var last string
	for i, v := range in {
		if i == 0 || v != last {
			out = append(out, v)
		}
		last = v
	}
	return out
}

// selectLigandxImages returns the image IDs to remove, de-duplicated: one image
// can carry several of our tags.
func selectLigandxImages(images []image.Summary, prefixes []string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == "" || tag == "<none>:<none>" {
				continue
			}
			matched := false
			for _, p := range prefixes {
				if strings.HasPrefix(tag, p) {
					matched = true
					break
				}
			}
			if matched && !seen[img.ID] {
				seen[img.ID] = true
				ids = append(ids, img.ID)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// launcherOwnedPaths lists the on-disk trees the launcher created: the whole
// <UserConfigDir>/ligandx-launcher directory (runtime bundle, config.json and
// logs all live under it) plus a runtime directory relocated via
// LIGANDX_RUNTIME_DIR, which sits outside that tree.
//
// Paths are filtered through safeToRemoveTree so a misconfigured environment
// cannot turn uninstall into `rm -rf /`.
func (a *App) launcherOwnedPaths() []string {
	var paths []string
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		paths = append(paths, filepath.Join(base, "ligandx-launcher"))
	}
	if runtimeDir, err := a.defaultRuntimeDir(); err == nil {
		if !pathWithin(runtimeDir, paths) {
			paths = append(paths, runtimeDir)
		}
	}
	var safe []string
	for _, p := range paths {
		if safeToRemoveTree(p) {
			safe = append(safe, p)
		}
	}
	return safe
}

func pathWithin(candidate string, parents []string) bool {
	for _, parent := range parents {
		rel, err := filepath.Rel(parent, candidate)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// safeToRemoveTree rejects anything that is not a plausible per-user data
// directory: a relative path, a filesystem root, or a path so shallow that
// deleting it would take a home directory with it.
func safeToRemoveTree(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && filepath.Clean(home) == clean {
		return false
	}
	// Require at least two path segments below the root, so "/ligandx" or "C:\x"
	// is refused while "~/.config/ligandx-launcher" is allowed.
	trimmed := strings.Trim(strings.TrimPrefix(clean, filepath.VolumeName(clean)), string(filepath.Separator))
	return len(strings.Split(trimmed, string(filepath.Separator))) >= 2
}

// Uninstall removes the Ligand-X stack from this machine: containers, networks,
// optionally volumes and images, the runtime bundle, launcher config and logs,
// and optionally the launcher executable itself.
//
// It is deliberately independent of docker-compose.yml. Uninstall has to work
// for the user whose install is broken — that is usually why they are
// uninstalling — so targets are discovered from the daemon's own labels rather
// than by interpolating a compose model that may not resolve.
func (a *App) Uninstall(opts UninstallOptions) (UninstallReport, error) {
	report := UninstallReport{}
	if strings.TrimSpace(opts.Confirm) != uninstallConfirmPhrase {
		return report, fmt.Errorf("uninstall not confirmed: expected %q", uninstallConfirmPhrase)
	}

	a.emitAndLog("launcher", "Uninstalling Ligand-X…")

	// The tunnel is a child process, not a container, so nothing below would
	// reach it — it would outlive the uninstall and keep publishing a hostname
	// for a stack that no longer exists.
	a.shutdownTunnel()

	if a.dockerClient == nil {
		a.initDockerClient()
	}
	if a.dockerClient == nil {
		report.add("Docker cleanup", "skipped",
			"Docker is not reachable; containers, volumes and images were left in place")
		report.ManualSteps = append(report.ManualSteps,
			"Start Docker and run: docker compose -p ligand-x down -v --remove-orphans")
	} else {
		a.uninstallDockerObjects(&report, opts)
	}

	a.uninstallLocalFiles(&report)

	if opts.KeepLauncher {
		report.add("Launcher application", "skipped", "left in place at your request")
	} else {
		a.uninstallLauncherBinary(&report)
	}

	report.ManualSteps = append(report.ManualSteps,
		"Shared base images were left in place because other projects may use them. "+
			"To remove them: docker rmi "+strings.Join(sharedBaseImages, " "))

	report.Complete = true
	a.emitAndLog("launcher", "Uninstall finished.")
	return report, nil
}

// uninstallDockerObjects tears down everything the daemon holds for us. Each
// class of object is a separate step so a permission error on one does not hide
// the others.
func (a *App) uninstallDockerObjects(report *UninstallReport, opts UninstallOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	listResult, err := a.dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		report.add("Containers", "failed", err.Error())
		return
	}
	projects := ligandxComposeProjects(listResult.Items)
	if len(projects) == 0 {
		report.add("Containers", "skipped", "no Ligand-X containers found")
	} else {
		ids := selectProjectContainers(listResult.Items, projects)
		removed, failed := 0, 0
		for _, id := range ids {
			if _, err := a.dockerClient.ContainerRemove(ctx, id, client.ContainerRemoveOptions{
				Force: true, RemoveVolumes: !opts.KeepData,
			}); err != nil {
				failed++
				continue
			}
			removed++
		}
		detail := fmt.Sprintf("removed %d container(s) from project(s): %s", removed, strings.Join(sortedSet(projects), ", "))
		if failed > 0 {
			detail += fmt.Sprintf("; %d could not be removed", failed)
		}
		report.add("Containers", statusFor(failed), detail)
	}

	if netList, err := a.dockerClient.NetworkList(ctx, client.NetworkListOptions{}); err == nil {
		ids := selectLigandxNetworks(netList.Items, projects)
		failed := 0
		for _, id := range ids {
			if _, err := a.dockerClient.NetworkRemove(ctx, id, client.NetworkRemoveOptions{}); err != nil {
				failed++
			}
		}
		report.add("Networks", statusFor(failed), fmt.Sprintf("removed %d network(s)", len(ids)-failed))
	} else {
		report.add("Networks", "failed", err.Error())
	}

	if opts.KeepData {
		report.add("Data volumes", "skipped", "your results and database were kept at your request")
	} else if volList, err := a.dockerClient.VolumeList(ctx, client.VolumeListOptions{}); err == nil {
		names := selectLigandxVolumes(volList.Items, projects)
		failed := 0
		for _, name := range names {
			if _, err := a.dockerClient.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true}); err != nil {
				failed++
			}
		}
		report.add("Data volumes", statusFor(failed), fmt.Sprintf("removed %d volume(s)", len(names)-failed))
	} else {
		report.add("Data volumes", "failed", err.Error())
	}

	if opts.KeepImages {
		report.add("Images", "skipped", "downloaded images were kept at your request")
		return
	}
	_, proPrefix := a.productionImageSettings()
	if imgList, err := a.dockerClient.ImageList(ctx, client.ImageListOptions{}); err == nil {
		ids := selectLigandxImages(imgList.Items, ligandxImageRepoPrefixes(proPrefix))
		failed := 0
		for _, id := range ids {
			if _, err := a.dockerClient.ImageRemove(ctx, id, client.ImageRemoveOptions{
				Force: true, PruneChildren: true,
			}); err != nil {
				failed++
			}
		}
		report.add("Images", statusFor(failed), fmt.Sprintf("removed %d Ligand-X image(s)", len(ids)-failed))
	} else {
		report.add("Images", "failed", err.Error())
	}
}

// uninstallLocalFiles deletes the runtime bundle, generated .env.production,
// launcher config and logs.
func (a *App) uninstallLocalFiles(report *UninstallReport) {
	paths := a.launcherOwnedPaths()
	if len(paths) == 0 {
		report.add("Launcher files", "skipped", "nothing found to remove")
		return
	}
	var removed []string
	failed := 0
	for _, p := range paths {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			failed++
			report.ManualSteps = append(report.ManualSteps, "Delete by hand: "+p)
			continue
		}
		removed = append(removed, p)
	}
	if len(removed) == 0 && failed == 0 {
		report.add("Launcher files", "skipped", "nothing found to remove")
		return
	}
	report.add("Launcher files", statusFor(failed), "removed "+strings.Join(removed, ", "))
}

// uninstallLauncherBinary removes the running executable. On Unix the file can
// be unlinked while running — the kernel keeps the inode alive until exit. On
// Windows a running image is locked, so the delete is handed to a detached
// script that waits for this process to exit first.
func (a *App) uninstallLauncherBinary(report *UninstallReport) {
	exe, err := os.Executable()
	if err != nil {
		report.add("Launcher application", "failed", "could not locate the executable: "+err.Error())
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	target := exe
	// A macOS .app is a directory; deleting the inner binary would leave a
	// broken bundle in /Applications.
	if goruntime.GOOS == "darwin" {
		if idx := strings.Index(exe, ".app"+string(filepath.Separator)); idx >= 0 {
			target = exe[:idx+len(".app")]
		}
	}

	if goruntime.GOOS == "windows" {
		if err := scheduleWindowsSelfDelete(target); err != nil {
			report.add("Launcher application", "failed", err.Error())
			report.ManualSteps = append(report.ManualSteps, "Delete by hand: "+target)
			return
		}
		report.add("Launcher application", "done", "will be deleted when you close the launcher: "+target)
		return
	}

	if err := os.RemoveAll(target); err != nil {
		report.add("Launcher application", "failed", err.Error())
		report.ManualSteps = append(report.ManualSteps, "Delete by hand: "+target)
		return
	}
	report.add("Launcher application", "done", "removed "+target)
}

// scheduleWindowsSelfDelete writes a batch file that retries the delete until
// the executable is unlocked, then removes itself, and launches it detached.
func scheduleWindowsSelfDelete(target string) error {
	script := filepath.Join(os.TempDir(), "ligandx-uninstall.bat")
	body := "@echo off\r\n" +
		":retry\r\n" +
		"del /f /q \"" + target + "\" >nul 2>&1\r\n" +
		"if exist \"" + target + "\" (\r\n" +
		"  ping -n 2 127.0.0.1 >nul\r\n" +
		"  goto retry\r\n" +
		")\r\n" +
		"del /f /q \"%~f0\" >nul 2>&1\r\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return fmt.Errorf("could not stage the removal script: %w", err)
	}
	cmd := exec.Command("cmd", "/c", "start", "/min", "", "cmd", "/c", script)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start the removal script: %w", err)
	}
	return cmd.Process.Release()
}

func statusFor(failed int) string {
	if failed > 0 {
		return "failed"
	}
	return "done"
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
