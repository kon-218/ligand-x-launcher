package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// publishedPort is a host port the stack binds. Each is configurable in
// .env.production, and docker fails the whole `up` with "port is already
// allocated" when one is taken — a partially-started stack the user then has to
// clean up by hand. So the launcher resolves conflicts before starting rather
// than letting compose discover them halfway through.
type publishedPort struct {
	key      string
	fallback int
	label    string
}

// Keep in lockstep with the `ports:` entries in docker-compose.yml.
var publishedPorts = []publishedPort{
	{"APP_PORT", 8080, "app"},
	{"GATEWAY_PORT", 8000, "API"},
	{"FRONTEND_PORT", 3000, "frontend"},
	{"FLOWER_PORT", 5555, "Flower"},
	{"RABBITMQ_MGMT_PORT", 15672, "RabbitMQ management"},
}

// maxPortProbe bounds the search for a free replacement port so a pathological
// host cannot spin here.
const maxPortProbe = 64

// envPort reads a port from .env.production, falling back to the compose
// default. Used by the Open* handlers so the buttons follow a reassigned port
// instead of pointing at a hard-coded one.
func (a *App) envPort(key string, fallback int) int {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return fallback
	}
	return portOrFallback(parseEnvFile(content)[key], fallback)
}

func portOrFallback(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 || v > 65535 {
		return fallback
	}
	return v
}

// fitPublishedPorts moves any conflicting host port to the next free one and
// records it in .env.production. Ports held by our own running containers are
// left alone: without that, restarting a running stack would see its own
// bindings as conflicts and walk every port up by one on each start.
func (a *App) fitPublishedPorts() error {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return err
	}
	cur := parseEnvFile(content)
	bindAddr := strings.TrimSpace(cur["LIGANDX_BIND_ADDRESS"])
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}

	ours := a.portsHeldByOurContainers()
	free := func(port int) bool {
		if ours[port] {
			return true
		}
		return portFree(bindAddr, port)
	}

	updates, notes := fitPorts(cur, free)
	if len(updates) == 0 {
		return nil
	}
	if err := a.setProductionEnvValues(updates); err != nil {
		return err
	}
	a.emitAndLog("launcher", "Port already in use, moved to a free one: "+strings.Join(notes, ", "))
	return nil
}

// fitPorts is the pure core: given the current env and a "is this port usable"
// predicate, return the keys that need rewriting. Exported for tests.
func fitPorts(cur map[string]string, free func(int) bool) (map[string]string, []string) {
	updates := map[string]string{}
	var notes []string
	// Claimed within this pass, so two services cannot be moved onto the same port.
	claimed := map[int]bool{}

	for _, p := range publishedPorts {
		want := portOrFallback(cur[p.key], p.fallback)
		if !claimed[want] && free(want) {
			claimed[want] = true
			continue
		}
		replacement := 0
		for candidate := want + 1; candidate <= want+maxPortProbe && candidate <= 65535; candidate++ {
			if !claimed[candidate] && free(candidate) {
				replacement = candidate
				break
			}
		}
		if replacement == 0 {
			// Nothing free nearby: leave the value alone and let docker report it
			// rather than writing a port we have not verified.
			claimed[want] = true
			continue
		}
		claimed[replacement] = true
		updates[p.key] = strconv.Itoa(replacement)
		notes = append(notes, fmt.Sprintf("%s %d->%d", p.label, want, replacement))
	}

	if len(updates) == 0 {
		return nil, nil
	}
	return updates, notes
}

// portFree reports whether the stack could bind this port right now.
func portFree(bindAddr string, port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(bindAddr, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// --- Storage -------------------------------------------------------------
//
// The images are large (measured: md 8.0 GB, pocket-finder 5.6, docking 4.5,
// gateway 3.8 unpacked), so a pull that runs out of space does so after a very
// long download and leaves a half-populated image store. Checking first turns
// that into an answerable question before anything is downloaded.

// hardFloorFreeBytes is the point below which a pull cannot succeed no matter
// what is selected: no single service image unpacks into less than this.
const hardFloorFreeBytes = 8 << 30

// unpackFactor converts a group's advertised download size into a rough
// on-disk requirement. ServiceGroup.SizeMB describes the *compressed* download,
// while the image store holds the unpacked layers.
//
// NOTE: the SizeMB values are themselves optimistic against measurement (the
// docking group advertises 800 MB but its image is 4.47 GB on disk), so this
// estimate is advisory only — it warns, it never blocks. Only hardFloorFreeBytes
// blocks, because that one is measured rather than derived.
const unpackFactor = 3

// dockerStoragePath returns the best available proxy for "where images land".
// On a native Linux daemon that is DockerRootDir. On Docker Desktop the daemon
// reports a path inside its VM, which does not exist here — the VM's disk image
// grows on the host volume instead, so the project directory is the closer
// answer.
func (a *App) dockerStoragePath() string {
	if root := a.hostResources().DockerRootDir; root != "" {
		if _, err := os.Stat(root); err == nil {
			return root
		}
	}
	return a.projectPath
}

// checkDiskSpace reports whether a pull of the given groups can plausibly fit.
// A returned error blocks the pull; a non-empty warning should be surfaced but
// not acted on.
func (a *App) checkDiskSpace(groupIDs []string, groupMap map[string]ServiceGroup, present map[string]bool) (warning string, err error) {
	path := a.dockerStoragePath()
	free, ok := diskFreeBytes(path)
	if !ok {
		return "", nil // cannot tell; never block on ignorance
	}
	return a.checkDiskSpaceWithFree(groupIDs, groupMap, present, free, path)
}

// checkDiskSpaceWithFree is the decision logic with the measurement injected,
// so the thresholds can be tested without a filesystem of a given size.
func (a *App) checkDiskSpaceWithFree(groupIDs []string, groupMap map[string]ServiceGroup, present map[string]bool, free uint64, path string) (warning string, err error) {
	var requiredMB int
	for _, id := range groupIDs {
		if present[id] {
			continue // already pulled; no new space needed
		}
		if g, ok := groupMap[id]; ok {
			requiredMB += g.SizeMB
		}
	}
	required := uint64(requiredMB) * (1 << 20) * unpackFactor

	if free < hardFloorFreeBytes {
		return "", fmt.Errorf(
			"not enough free disk space to download images: %s available on %s, "+
				"at least %s is needed for even one service. Free up space and try again",
			formatBytes(free), path, formatBytes(hardFloorFreeBytes))
	}
	if required > 0 && free < required {
		return fmt.Sprintf(
			"Low disk space: about %s may be needed for the selected services but only %s is free on %s. "+
				"The download may fail partway — consider freeing space or selecting fewer services.",
			formatBytes(required), formatBytes(free), path), nil
	}
	return "", nil
}

// portsHeldByOurContainers returns host ports currently published by running
// Ligand-X containers.
func (a *App) portsHeldByOurContainers() map[int]bool {
	held := map[int]bool{}
	if a.dockerClient == nil {
		a.initDockerClient()
	}
	if a.dockerClient == nil {
		return held
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	containers, err := a.ligandXContainers(ctx, false)
	if err != nil {
		return held
	}
	for _, c := range containers {
		for _, p := range c.Ports {
			if p.PublicPort > 0 {
				held[int(p.PublicPort)] = true
			}
		}
	}
	return held
}
