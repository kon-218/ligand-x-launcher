package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Image identity for an installed runtime (B-222).
//
// Every job record names the image that ran it. The runtime's compose file
// passes each worker LIGANDX_IMAGE_DIGEST from a per-service variable
// (LIGANDX_<SERVICE>_IMAGE_DIGEST) and falls back to LIGANDX_IMAGE_REF, a
// movable tag, when that variable is unset. The candidate controller sets the
// variables for its own stack; an installed runtime never did, so every record
// on a user's machine named only a tag and a new operation version failed its
// identity check on the install scenarios. Before each production `up` the
// launcher now resolves the image compose will run for each such service to the
// registry digest it was pulled by, and passes that in compose's environment.
// Nothing is written to .env.production, so a later pull can never leave a
// stale digest behind: the value is recomputed from the local image every time
// containers are created.

const imageDigestInspectTimeout = 30 * time.Second

// composeImageDigestKeys maps each service to the variable its
// LIGANDX_IMAGE_DIGEST reads, from the runtime's own compose file.
func composeImageDigestKeys(composeYAML string) map[string]string {
	keys := map[string]string{}
	inServices := false
	service := ""
	for _, line := range strings.Split(composeYAML, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inServices = trimmed == "services:"
			service = ""
			continue
		}
		if !inServices {
			continue
		}
		if indent == 2 && strings.HasSuffix(trimmed, ":") {
			service = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if service == "" {
			continue
		}
		entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		for _, sep := range []string{"=", ":"} {
			if value, ok := strings.CutPrefix(entry, "LIGANDX_IMAGE_DIGEST"+sep); ok {
				if key := envVarNameIn(value); key != "" {
					keys[service] = key
				}
				break
			}
		}
	}
	return keys
}

// resolvedServiceImages reads each service's image from `docker compose config`.
func resolvedServiceImages(configJSON []byte) (map[string]string, error) {
	var model struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &model); err != nil {
		return nil, err
	}
	images := map[string]string{}
	for name, svc := range model.Services {
		if image := strings.TrimSpace(svc.Image); image != "" {
			images[name] = image
		}
	}
	return images, nil
}

// imageRepository strips the tag and digest from an image reference.
// "localhost:5000/lx/worker:v1" -> "localhost:5000/lx/worker".
func imageRepository(ref string) string {
	repo, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo
}

// repoDigestFor picks the registry digest that identifies ref's image from the
// local image's RepoDigests. A reference that is already pinned is its own
// answer. It refuses (ok=false) when the image has no digest for ref's
// repository (a local build) or more than one distinct digest for it.
func repoDigestFor(ref string, repoDigests []string) (string, bool) {
	repo := imageRepository(ref)
	if _, digest, pinned := strings.Cut(ref, "@"); pinned && strings.HasPrefix(digest, "sha256:") {
		return repo + "@" + digest, true
	}
	found := ""
	for _, candidate := range repoDigests {
		candidate = strings.TrimSpace(candidate)
		name, digest, ok := strings.Cut(candidate, "@")
		if !ok || name != repo || !strings.HasPrefix(digest, "sha256:") {
			continue
		}
		if found != "" && found != candidate {
			return "", false
		}
		found = candidate
	}
	return found, found != ""
}

// localRepoDigests returns the RepoDigests of the local image named by ref.
func (a *App) localRepoDigests(ref string) ([]string, error) {
	if a.imageRepoDigestsFn != nil {
		return a.imageRepoDigestsFn(ref)
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageDigestInspectTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", ref).Output()
	if err != nil {
		return nil, err
	}
	var digests []string
	if err := json.Unmarshal(out, &digests); err != nil {
		return nil, err
	}
	return digests, nil
}

// imageDigestEnv returns KEY=repository@sha256:... for every service in the
// resolved model whose image identity can be read from its local image. A
// service it cannot resolve is left unset, so its record falls back to the tag
// (which the identity check reports as a tag, never as a digest). Failures are
// logged, never fatal: identity is evidence about a run, not a reason to stop one.
func (a *App) imageDigestEnv(upArgs []string) []string {
	composeYAML, err := os.ReadFile(filepath.Join(a.projectPath, "docker-compose.yml"))
	if err != nil {
		return nil
	}
	keys := composeImageDigestKeys(string(composeYAML))
	if len(keys) == 0 {
		return nil
	}
	out, err := a.runComposeConfig(composeConfigArgs(upArgs))
	if err != nil {
		a.emitAndLog("launcher", fmt.Sprintf("Could not resolve image digests (docker compose config failed: %v); job records will name image tags", err))
		return nil
	}
	images, err := resolvedServiceImages(out)
	if err != nil {
		a.emitAndLog("launcher", fmt.Sprintf("Could not resolve image digests (%v); job records will name image tags", err))
		return nil
	}
	services := make([]string, 0, len(keys))
	for service := range keys {
		if _, ok := images[service]; ok {
			services = append(services, service)
		}
	}
	sort.Strings(services)
	var env, unresolved []string
	for _, service := range services {
		ref := images[service]
		digests, err := a.localRepoDigests(ref)
		if err != nil {
			unresolved = append(unresolved, service)
			continue
		}
		if pinned, ok := repoDigestFor(ref, digests); ok {
			env = append(env, keys[service]+"="+pinned)
		} else {
			unresolved = append(unresolved, service)
		}
	}
	if len(env) > 0 {
		a.emitAndLog("launcher", fmt.Sprintf("Recording exact image digests for %d service(s)", len(env)))
	}
	if len(unresolved) > 0 {
		a.emitAndLog("launcher", fmt.Sprintf("No registry digest for %s; their job records will name image tags", strings.Join(unresolved, ", ")))
	}
	return env
}

// composeRunEnv is the environment for one compose command: composeEnv, plus
// the resolved image digests when the command creates production containers.
func (a *App) composeRunEnv(args []string) []string {
	env := a.composeEnv()
	if isProductionUpCommand(args) {
		env = append(env, a.imageDigestEnv(args)...)
	}
	return env
}
