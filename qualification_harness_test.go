//go:build qualification

package main

// Drives the same verified installation, lease upgrade and grouped-start code
// used by the desktop. This tag is deliberately excluded from normal unit runs.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLauncherPathAcceptance(t *testing.T) {
	if os.Getenv("LIGANDX_QUALIFICATION_OPT_IN") != "I_ACCEPT_ISOLATED_RUNTIME_CHANGES" {
		t.Fatal("qualification requires explicit isolated-runtime opt-in")
	}
	project := os.Getenv("LIGANDX_QUALIFICATION_PROJECT")
	rt := os.Getenv("LIGANDX_RUNTIME_DIR")
	config := os.Getenv("LIGANDX_LAUNCHER_CONFIG_DIR")
	url, version, key := os.Getenv("LX_BUNDLE_URL"), os.Getenv("LX_VERSION"), os.Getenv("LX_PUBLIC_KEY")
	if !strings.HasPrefix(project, "lx-qualification-") || rt == "" || config == "" || url == "" || version == "" || key == "" {
		t.Fatal("missing isolated project/runtime/config or signed bundle inputs")
	}
	if config == rt {
		t.Fatal("config and runtime directories must differ")
	}
	// A managed installation must not auto-discover an adjacent source checkout.
	t.Chdir(t.TempDir())
	a := NewApp()
	a.composeProjectName = project
	a.projectPath = rt
	runtimeBundlePublicKeyB64 = key
	launcherVersion = os.Getenv("LX_LAUNCHER_VERSION")
	if launcherVersion == "" {
		t.Fatal("LX_LAUNCHER_VERSION required for signed manifest compatibility")
	}
	t.Setenv("LIGANDX_PROJECT_PATH", rt)
	if _, err := a.installRuntimeBundleSelected(url, version, false); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(filepath.Join(rt, ".ligandx-runtime-version"))
	if err != nil || strings.TrimSpace(string(installed)) != version {
		t.Fatal("managed runtime did not install the requested version")
	}
	// Copy candidate pins/isolated ports while retaining generated installation
	// credentials and all on-disk database/output data across upgrades.
	if source := os.Getenv("LX_ENV_FILE"); source != "" {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
				values[k] = v
			}
		}
		if err := a.setProductionEnvValues(values); err != nil {
			t.Fatal(err)
		}
	}
	if lic := os.Getenv("LX_LICENSE"); lic != "" {
		data, err := os.ReadFile(lic)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.persistImportedLicense(data); err != nil {
			t.Fatal(err)
		}
	}
	groups := strings.Split(os.Getenv("LX_GROUPS"), ",")
	if groups[0] == "" {
		t.Fatal("LX_GROUPS required (core plus selected workload groups)")
	}
	if err := a.SaveLauncherConfig(LauncherConfig{SelectedGroups: groups}); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureProductionEnv(); err != nil {
		t.Fatal(err)
	}
	digestFile := os.Getenv("LX_DIGESTS_FILE")
	if digestFile == "" {
		t.Fatal("LX_DIGESTS_FILE required; tags are not qualification identity")
	}
	digestBytes, err := os.ReadFile(digestFile)
	if err != nil {
		t.Fatal(err)
	}
	digests := map[string]string{}
	if err = json.Unmarshal(digestBytes, &digests); err != nil {
		t.Fatal(err)
	}
	modelCmd := exec.Command("docker", "compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "config", "--format", "json")
	modelCmd.Dir = rt
	modelCmd.Env = a.composeEnv()
	modelBytes, err := modelCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		Services map[string]struct{ Image string }
	}
	if err = json.Unmarshal(modelBytes, &model); err != nil {
		t.Fatal(err)
	}
	pins := map[string]map[string]string{}
	publicPrefix := os.Getenv("LIGANDX_PUBLIC_IMAGE_PREFIX")
	if publicPrefix == "" {
		publicPrefix = "ghcr.io/kon-218/ligand-x"
	}
	proPrefix := os.Getenv("LIGANDX_PRO_IMAGE_PREFIX")
	if proPrefix == "" {
		proPrefix = "ghcr.io/kon-218/ligand-x-pro"
	}
	for service, item := range model.Services {
		repo := strings.Split(item.Image, "@")[0]
		if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
			repo = repo[:i]
		}
		imageKey := ""
		for _, prefix := range []struct {
			name string
			path string
		}{{"pro", proPrefix}, {"public", publicPrefix}} {
			if strings.HasPrefix(repo, prefix.path+"/") {
				imageKey = prefix.name + "/" + strings.TrimPrefix(repo, prefix.path+"/")
				break
			}
		}
		if imageKey != "" {
			digest := digests[imageKey]
			if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
				t.Fatal("missing immutable image digest for " + imageKey)
			}
			pins[service] = map[string]string{"image": repo + "@" + digest}
		}
	}
	if len(pins) == 0 {
		t.Fatal("no first-party images resolved")
	}
	overlay := filepath.Join(rt, "qualification-images.json")
	overlayBytes, _ := json.Marshal(map[string]interface{}{"services": pins})
	if err = os.WriteFile(overlay, overlayBytes, 0600); err != nil {
		t.Fatal(err)
	}
	a.composeOverrideFiles = []string{overlay}
	if os.Getenv("LX_BASELINE") == "1" {
		// Historical bundles can predate control/beat. Start only services
		// declared by that verified model through the launcher's public API.
		allowed := launcherAllowedServices(a.GetServiceGroups(), groups)
		var available []string
		for _, service := range allowed {
			if _, exists := model.Services[service]; exists {
				available = append(available, service)
			}
		}
		if err = a.StartServicesCustom("prod", available); err != nil {
			t.Fatal(err)
		}
	} else if err = a.StartServiceGroups("prod", groups); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("pinned %d application services to immutable digests", len(pins)))
	if a.leaseActivationPending() {
		t.Fatal("upgrade lease activation is still pending")
	}
	// Inspect via Compose with the exact App project/environment. A skipped or
	// created service cannot satisfy the gate.
	cmd := exec.CommandContext(context.Background(), "docker", a.scopedComposeArgs([]string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "ps", "--all", "--format", "json"})...)
	cmd.Dir = rt
	cmd.Env = a.composeEnv()
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Service string
		State   string
		Health  string
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var row struct {
				Service string
				State   string
				Health  string
			}
			if err = json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
	}
	running := map[string]bool{}
	for _, row := range rows {
		running[row.Service] = row.State == "running" && row.Health != "unhealthy"
	}
	requiredServices := []string{"worker-control", "celery-beat", "gateway"}
	if os.Getenv("LX_BASELINE") == "1" {
		requiredServices = []string{"gateway"}
	}
	for _, service := range requiredServices {
		if !running[service] {
			t.Fatalf("%s not running: %s", service, raw)
		}
	}
	receipt := map[string]interface{}{"schema": "ligandx-launcher-path-phase/1", "status": "passed", "project": project, "version": version, "entry_point": "installRuntimeBundleSelected → StartServiceGroups", "services": rows, "runtime": rt}
	path := os.Getenv("LX_PHASE_OUTPUT")
	if path == "" {
		t.Fatal("LX_PHASE_OUTPUT required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
}
