package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// The shipped runtime is the authority for which services record an image
// digest and under which variable: the launcher reads it rather than keeping a
// second table that could drift from the snapshot.
func TestComposeImageDigestKeysFromShippedRuntime(t *testing.T) {
	data, err := os.ReadFile("../../runtime/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	got := composeImageDigestKeys(string(data))
	want := map[string]string{
		"gateway":          "LIGANDX_GATEWAY_IMAGE_DIGEST",
		"worker-qc":        "LIGANDX_WORKER_QC_IMAGE_DIGEST",
		"worker-gpu-short": "LIGANDX_WORKER_GPU_SHORT_IMAGE_DIGEST",
		"worker-gpu-long":  "LIGANDX_WORKER_GPU_LONG_IMAGE_DIGEST",
		"worker-cpu":       "LIGANDX_WORKER_CPU_IMAGE_DIGEST",
		"worker-control":   "LIGANDX_WORKER_CONTROL_IMAGE_DIGEST",
		"worker-reinvent":  "LIGANDX_WORKER_REINVENT_IMAGE_DIGEST",
		"worker-proto":     "LIGANDX_WORKER_PROTO_IMAGE_DIGEST",
		"worker-bioemu":    "LIGANDX_WORKER_BIOEMU_IMAGE_DIGEST",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("digest keys = %v\nwant %v", got, want)
	}
}

func TestRepoDigestFor(t *testing.T) {
	const ref = "ghcr.io/kon-218/ligand-x-pro/worker-gpu-short:candidate-c-1"
	repo := "ghcr.io/kon-218/ligand-x-pro/worker-gpu-short"
	cases := []struct {
		name    string
		ref     string
		digests []string
		want    string
		ok      bool
	}{
		{"one match", ref, []string{repo + "@" + digestA}, repo + "@" + digestA, true},
		{"other repositories ignored", ref, []string{"ghcr.io/other/worker@" + digestB, repo + "@" + digestA}, repo + "@" + digestA, true},
		{"duplicates collapse", ref, []string{repo + "@" + digestA, repo + "@" + digestA}, repo + "@" + digestA, true},
		{"two different digests are ambiguous", ref, []string{repo + "@" + digestA, repo + "@" + digestB}, "", false},
		{"locally built image has none", ref, nil, "", false},
		{"only another repository", ref, []string{"ghcr.io/other/worker@" + digestA}, "", false},
		{"registry port is not a tag", "localhost:5000/lx/worker:v1", []string{"localhost:5000/lx/worker@" + digestA}, "localhost:5000/lx/worker@" + digestA, true},
		{"untagged reference", "localhost:5000/lx/worker", []string{"localhost:5000/lx/worker@" + digestA}, "localhost:5000/lx/worker@" + digestA, true},
		{"reference already pinned", repo + "@" + digestB, nil, repo + "@" + digestB, true},
		{"tag and digest", repo + ":v1@" + digestB, nil, repo + "@" + digestB, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := repoDigestFor(tc.ref, tc.digests)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("repoDigestFor(%q) = %q, %v; want %q, %v", tc.ref, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func digestTestApp(t *testing.T, model string, local map[string][]string) (*App, *[]string) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	app := NewApp()
	app.projectPath = tmpDir
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte(strings.Join([]string{
		"services:",
		"  gateway:",
		"    environment:",
		"      - LIGANDX_IMAGE_DIGEST=${LIGANDX_GATEWAY_IMAGE_DIGEST:-}",
		"  worker-gpu-short:",
		"    environment:",
		"      LIGANDX_IMAGE_DIGEST: ${LIGANDX_WORKER_GPU_SHORT_IMAGE_DIGEST:-}",
		"  worker-cpu:",
		"    environment:",
		"      - LIGANDX_IMAGE_DIGEST=${LIGANDX_WORKER_CPU_IMAGE_DIGEST:-}",
		"  postgres:",
		"    image: postgres:16",
	}, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	app.composeConfigFn = func([]string) ([]byte, error) { return []byte(model), nil }
	var inspected []string
	app.imageRepoDigestsFn = func(ref string) ([]string, error) {
		inspected = append(inspected, ref)
		digests, ok := local[ref]
		if !ok {
			return nil, errors.New("No such image: " + ref)
		}
		return digests, nil
	}
	return app, &inspected
}

// A pulled image records the exact build by digest, so a job's record names
// the image that ran it (B-222); without this an installed runtime only ever
// recorded a movable tag and every non-exempt operation failed its identity row.
func TestImageDigestEnvNamesEachServicesLocalImage(t *testing.T) {
	gw := "ghcr.io/kon-218/ligand-x/gateway:v0.1.5"
	gpu := "ghcr.io/kon-218/ligand-x-pro/worker-gpu-short:v0.1.5"
	cpu := "ghcr.io/kon-218/ligand-x/worker-cpu:v0.1.5"
	app, _ := digestTestApp(t, `{"services":{
		"gateway":{"image":"`+gw+`"},
		"worker-gpu-short":{"image":"`+gpu+`"},
		"worker-cpu":{"image":"`+cpu+`"},
		"postgres":{"image":"postgres:16"}}}`,
		map[string][]string{
			gw:  {"ghcr.io/kon-218/ligand-x/gateway@" + digestA},
			gpu: {"ghcr.io/kon-218/ligand-x-pro/worker-gpu-short@" + digestB},
			cpu: nil, // built locally: no registry digest, so the tag stays the record
		})
	got := app.imageDigestEnv([]string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d", "--pull=never"})
	sort.Strings(got)
	want := []string{
		"LIGANDX_GATEWAY_IMAGE_DIGEST=ghcr.io/kon-218/ligand-x/gateway@" + digestA,
		"LIGANDX_WORKER_GPU_SHORT_IMAGE_DIGEST=ghcr.io/kon-218/ligand-x-pro/worker-gpu-short@" + digestB,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imageDigestEnv = %v\nwant %v", got, want)
	}
}

func TestImageDigestEnvSkipsServicesOutsideTheModelAndMissingImages(t *testing.T) {
	gw := "ghcr.io/kon-218/ligand-x/gateway:v0.1.5"
	app, inspected := digestTestApp(t, `{"services":{"gateway":{"image":"`+gw+`"}}}`, map[string][]string{})
	if got := app.imageDigestEnv([]string{"compose", "--env-file", ".env.production", "up", "-d"}); len(got) != 0 {
		t.Fatalf("an image that is not present locally must leave the variable unset, got %v", got)
	}
	if !reflect.DeepEqual(*inspected, []string{gw}) {
		t.Fatalf("inspected %v; only services in the resolved model are looked up", *inspected)
	}
}

func TestImageDigestEnvToleratesAnUnreadableModel(t *testing.T) {
	app, inspected := digestTestApp(t, `not json`, nil)
	if got := app.imageDigestEnv([]string{"compose", "--env-file", ".env.production", "up", "-d"}); len(got) != 0 {
		t.Fatalf("got %v; an unreadable model must not block the start", got)
	}
	if len(*inspected) != 0 {
		t.Fatalf("inspected %v without a model", *inspected)
	}
}

func TestComposeRunEnvAddsDigestsOnlyToProductionUp(t *testing.T) {
	gw := "ghcr.io/kon-218/ligand-x/gateway:v0.1.5"
	app, _ := digestTestApp(t, `{"services":{"gateway":{"image":"`+gw+`"}}}`,
		map[string][]string{gw: {"ghcr.io/kon-218/ligand-x/gateway@" + digestA}})
	want := "LIGANDX_GATEWAY_IMAGE_DIGEST=ghcr.io/kon-218/ligand-x/gateway@" + digestA
	has := func(env []string) bool {
		for _, kv := range env {
			if kv == want {
				return true
			}
		}
		return false
	}
	if !has(app.composeRunEnv([]string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d"})) {
		t.Fatal("production up must carry the resolved digest")
	}
	if has(app.composeRunEnv([]string{"compose", "--env-file", ".env.production", "down"})) {
		t.Fatal("only an up creates containers; other commands keep the plain compose env")
	}
	if has(app.composeRunEnv([]string{"compose", "--env-file", ".env", "up", "-d"})) {
		t.Fatal("a development up builds local images; it is not given registry digests")
	}
}

// The wiring itself: the `up` that creates containers runs with the digest in
// its environment (a fake docker records what it received).
func TestProductionUpRunsComposeWithResolvedDigests(t *testing.T) {
	gw := "ghcr.io/kon-218/ligand-x/gateway:v0.1.5"
	app, _ := digestTestApp(t, `{"services":{"gateway":{"image":"`+gw+`"}}}`,
		map[string][]string{gw: {"ghcr.io/kon-218/ligand-x/gateway@" + digestA}})
	app.hostResourcesFn = func() hostResources { return hostResources{} }
	app.eventSink = func(string, ...interface{}) {}
	if err := os.WriteFile(filepath.Join(app.projectPath, ".env.production"), []byte("VERSION=v0.1.5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "up-env")
	t.Setenv("DIGEST_UP_ENV", log)
	script := `#!/bin/sh
for a in "$@"; do
  if [ "$a" = up ]; then printf '%s\n' "${LIGANDX_GATEWAY_IMAGE_DIGEST:-unset}" >> "$DIGEST_UP_ENV"; break; fi
done
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d", "--pull=never", "gateway"}
	_ = app.runDockerComposeOnce(args, "start")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("compose up was never run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got := lines[len(lines)-1]; got != "ghcr.io/kon-218/ligand-x/gateway@"+digestA {
		t.Fatalf("the container-creating up ran with LIGANDX_GATEWAY_IMAGE_DIGEST=%q", got)
	}
}

// A worker that reports its image (LIGANDX_IMAGE_REF) must also be able to
// report the exact build, or the launcher can never record more than a tag for
// it. Guards a new or regenerated service losing its digest variable.
func TestEveryShippedServiceThatReportsAnImageCanReportItsDigest(t *testing.T) {
	data, err := os.ReadFile("../../runtime/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	keys := composeImageDigestKeys(string(data))
	reporting := map[string]bool{}
	inServices, service := false, ""
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		case indent == 0:
			inServices, service = trimmed == "services:", ""
		case inServices && indent == 2 && strings.HasSuffix(trimmed, ":"):
			service = strings.TrimSuffix(trimmed, ":")
		case inServices && service != "" && strings.Contains(trimmed, "LIGANDX_IMAGE_REF"):
			reporting[service] = true
		}
	}
	if len(reporting) == 0 {
		t.Fatal("no service reports LIGANDX_IMAGE_REF; the parser no longer matches the runtime")
	}
	for service := range reporting {
		if keys[service] == "" {
			t.Errorf("%s reports LIGANDX_IMAGE_REF but has no LIGANDX_IMAGE_DIGEST variable", service)
		}
	}
}
