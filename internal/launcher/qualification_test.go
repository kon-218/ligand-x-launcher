//go:build qualification

package launcher

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ligandx-launcher/internal/envfile"
	"ligandx-launcher/internal/runtimebundle"
)

func qualificationFixture(t *testing.T) (*QualificationApp, string) {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldVersion := runtimeBundlePublicKeyB64, launcherVersion
	t.Cleanup(func() { runtimeBundlePublicKeyB64, launcherVersion = oldKey, oldVersion })
	root := filepath.Join(t.TempDir(), "lx-qualification-test")
	app, err := NewQualificationApp(context.Background(), QualificationConfig{
		RuntimeDir: root, Project: "lx-qualification-test", Owner: "BUILD-03-test",
		PublicKeyB64: base64.StdEncoding.EncodeToString(key), LauncherVersion: "v1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, root
}

func qualificationBundle(t *testing.T, version string) (string, []byte, []byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, body := range map[string]string{"docker-compose.yml": "services: {}\n", ".env.production.template": "VERSION=" + version + "\nPRO_VERSION=" + version + "\n"} {
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, signature := signRuntimeManifestForTest(t, buffer.Bytes(), version, time.Now().Add(time.Hour))
	dir := t.TempDir()
	path := filepath.Join(dir, runtimebundle.AssetName)
	for name, data := range map[string][]byte{runtimebundle.AssetName: buffer.Bytes(), runtimebundle.ManifestAssetName: manifest, runtimebundle.SignatureAssetName: signature} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return path, manifest, signature, runtimeBundlePublicKeyB64
}

func TestQualificationRunsRealInstallerAndPreservesUpgradeState(t *testing.T) {
	app, root := qualificationFixture(t)
	stable, predecessor, predecessorSig, predecessorKey := qualificationBundle(t, "v1.2.3")
	if _, err := app.Install(stable, "v1.2.3", nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	if envfile.Parse(string(before))["POSTGRES_PASSWORD"] == "" {
		t.Fatal("real installer did not generate credentials")
	}
	settings := filepath.Join(root, ".launcher", "config.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"selectedGroups":["core","admet"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, candidateManifest, _, _ := qualificationBundle(t, "candidate-c-0123456789abcdef")
	if _, err := app.Install(candidate, "candidate-c-0123456789abcdef", nil); err == nil {
		t.Fatal("unapproved candidate bypassed production downgrade guard")
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	app.config.AuthorizationPublicKeyB64 = base64.StdEncoding.EncodeToString(public)
	app.config.PredecessorPublicKeyB64 = predecessorKey
	plan := []byte(`{"candidate_id":"c-0123456789abcdef","baseline_version":"v1.2.3"}`)
	var cm runtimebundle.Manifest
	if err := json.Unmarshal(candidateManifest, &cm); err != nil {
		t.Fatal(err)
	}
	auth := QualificationAuthorization{Schema: "ligandx-qualification-transition/1", CandidateID: "c-0123456789abcdef", PlanSHA256: qualificationDigest(plan), PredecessorVersion: "v1.2.3", PredecessorManifestSHA256: qualificationDigest(predecessor), TargetBundleSHA256: cm.SHA256, Project: app.config.Project, Owner: app.config.Owner, IssuedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(auth)
	upgrade := &QualificationUpgrade{Plan: plan, Authorization: raw, Signature: []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))), PredecessorManifest: predecessor, PredecessorSignature: predecessorSig}
	if _, err := app.Install(candidate, "candidate-c-0123456789abcdef", upgrade); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".env.production"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"POSTGRES_PASSWORD", "RABBITMQ_PASSWORD", "LIGANDX_PASSWORD", "INTERNAL_WORKER_SECRET"} {
		if envfile.Parse(string(before))[key] != envfile.Parse(string(after))[key] {
			t.Fatalf("upgrade rotated %s", key)
		}
	}
	if envfile.Parse(string(after))["VERSION"] != "candidate-c-0123456789abcdef" || runtimebundle.InstalledVersion(root) != "candidate-c-0123456789abcdef" {
		t.Fatal("candidate identity was not activated")
	}
	if stored, _ := os.ReadFile(settings); string(stored) != `{"selectedGroups":["core","admet"]}` {
		t.Fatal("upgrade changed user selection")
	}
	if _, err := app.Install(candidate, "candidate-c-0123456789abcdef", upgrade); err == nil {
		t.Fatal("replayed predecessor authorization")
	}
	if app.EventCounts()["log"] == 0 {
		t.Fatal("installer emitted no headless events")
	}
}

func TestQualificationTamperedBundleCannotActivate(t *testing.T) {
	app, root := qualificationFixture(t)
	path, _, _, _ := qualificationBundle(t, "v1.2.3")
	if err := os.WriteFile(path, []byte("tampered archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Install(path, "v1.2.3", nil); err == nil {
		t.Fatal("tampered archive accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "docker-compose.yml")); !os.IsNotExist(err) {
		t.Fatal("tampered bundle changed runtime")
	}
}

func TestQualificationStartRejectsSkippedOrUnknownGroups(t *testing.T) {
	app, _ := qualificationFixture(t)
	for _, groups := range [][]string{nil, {"does-not-exist"}, {"admet"}} {
		if err := app.Start(groups); err == nil || !strings.Contains(err.Error(), "group") {
			t.Fatalf("groups=%v err=%v", groups, err)
		}
	}
}

func TestQualificationDockerQueriesUseExactOwnedProject(t *testing.T) {
	app, _ := qualificationFixture(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BUILD03_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUILD03_ARGS", output)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := app.app.activeProductionWorkerServices(); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(output)
	if !strings.Contains(string(args), "label=com.docker.compose.project="+app.config.Project+"\n") {
		t.Fatalf("wrong project query: %s", args)
	}
	if !app.app.acceptsComposeProject(app.config.Project) || app.app.acceptsComposeProject("ligand-x") || app.app.acceptsComposeProject("lx-qualification-other") {
		t.Fatal("qualification scope accepts unrelated stacks")
	}
}

func TestQualificationTransitionVerifiesExactCandidateAndPredecessor(t *testing.T) {
	app, root := qualificationFixture(t)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	key := base64.StdEncoding.EncodeToString(public)
	app.config.AuthorizationPublicKeyB64 = key
	app.config.PredecessorPublicKeyB64 = key
	runtimeBundlePublicKeyB64 = key
	plan := []byte(`{"candidate_id":"c-0123456789abcdef","baseline_version":"v1.2.3"}`)
	manifest := func(version, digest string) []byte {
		data, err := json.Marshal(runtimebundle.Manifest{
			Schema: "ligandx-runtime-manifest/1", Version: version, Asset: runtimebundle.AssetName,
			SHA256: digest, Size: 5, IssuedAt: time.Now().UTC().Format(time.RFC3339),
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), GitCommit: strings.Repeat("a", 40),
			Artifacts: map[string]runtimebundle.Artifact{runtimebundle.AssetName: {SHA256: digest, Size: 5}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	predecessor := manifest("v1.2.3", strings.Repeat("b", 64))
	candidate := manifest("candidate-c-0123456789abcdef", strings.Repeat("c", 64))
	sign := func(data []byte) []byte {
		return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, data)))
	}
	digest := func(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
	base := QualificationAuthorization{
		Schema: "ligandx-qualification-transition/1", CandidateID: "c-0123456789abcdef",
		PlanSHA256: digest(plan), PredecessorVersion: "v1.2.3", PredecessorManifestSHA256: digest(predecessor),
		TargetBundleSHA256: strings.Repeat("c", 64), Project: app.config.Project, Owner: app.config.Owner,
		IssuedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
	}
	if err := os.WriteFile(filepath.Join(root, ".ligandx-runtime-version"), []byte("v1.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*QualificationAuthorization)
		valid  bool
	}{
		{"exact", func(a *QualificationAuthorization) {}, true},
		{"other candidate", func(a *QualificationAuthorization) { a.CandidateID = "c-fedcba9876543210" }, false},
		{"other plan", func(a *QualificationAuthorization) { a.PlanSHA256 = strings.Repeat("d", 64) }, false},
		{"other predecessor", func(a *QualificationAuthorization) { a.PredecessorVersion = "v1.2.2" }, false},
		{"other predecessor manifest", func(a *QualificationAuthorization) { a.PredecessorManifestSHA256 = strings.Repeat("d", 64) }, false},
		{"other bundle", func(a *QualificationAuthorization) { a.TargetBundleSHA256 = strings.Repeat("d", 64) }, false},
		{"other owner", func(a *QualificationAuthorization) { a.Owner = "someone-else" }, false},
		{"other project", func(a *QualificationAuthorization) { a.Project = "lx-qualification-other" }, false},
		{"expired", func(a *QualificationAuthorization) {
			a.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		}, false},
		{"future", func(a *QualificationAuthorization) { a.IssuedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }, false},
		{"unbounded", func(a *QualificationAuthorization) {
			a.ExpiresAt = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := base
			test.change(&a)
			raw, _ := json.Marshal(a)
			_, err := app.verifyTransition(raw, sign(raw), plan, predecessor, sign(predecessor), candidate, sign(candidate))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	raw, _ := json.Marshal(base)
	if _, err := app.verifyTransition(raw, sign([]byte("forged")), plan, predecessor, sign(predecessor), candidate, sign(candidate)); err == nil {
		t.Fatal("accepted forged authorization")
	}
	if err := os.WriteFile(filepath.Join(root, ".ligandx-runtime-version"), []byte("v1.2.4"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.verifyTransition(raw, sign(raw), plan, predecessor, sign(predecessor), candidate, sign(candidate)); err == nil {
		t.Fatal("accepted wrong installed predecessor")
	}
}

func TestQualificationAppUsesOwnedPathsAndComposeNames(t *testing.T) {
	app, root := qualificationFixture(t)
	t.Setenv("LIGANDX_INFRA_CONTAINER_PREFIX", "someone-else")
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	runtime, err := app.app.defaultRuntimeDir()
	if err != nil || runtime != root {
		t.Fatalf("runtime=%q err=%v", runtime, err)
	}
	config, err := app.app.getConfigPath()
	if err != nil || !strings.HasPrefix(config, root+string(os.PathSeparator)) {
		t.Fatalf("config=%q err=%v", config, err)
	}
	if !strings.HasPrefix(app.app.composeLogPath(), root+string(os.PathSeparator)) {
		t.Fatal("logs escaped owned root")
	}
	if got := app.app.infrastructureContainerName("postgres"); got != "lx-qualification-test-postgres" {
		t.Fatal(got)
	}
	env := strings.Join(app.app.composeEnv(), "\n")
	if !strings.Contains(env, "COMPOSE_PROJECT_NAME=lx-qualification-test") || !strings.HasSuffix(env, "LIGANDX_INFRA_CONTAINER_PREFIX=lx-qualification-test") {
		t.Fatal("compose namespace is not forced")
	}
	app.app.emit("log", "headless event")
	if app.EventCounts()["log"] != 1 {
		t.Fatal("event sink not used")
	}
}

func TestQualificationAppRefusesForeignRootAndOwner(t *testing.T) {
	app, root := qualificationFixture(t)
	config := app.config
	config.Owner = "different-session"
	if _, err := NewQualificationApp(context.Background(), config); err == nil {
		t.Fatal("adopted another owner")
	}
	config.RuntimeDir = t.TempDir()
	config.Project = "ligand-x"
	if _, err := NewQualificationApp(context.Background(), config); err == nil {
		t.Fatal("accepted production namespace")
	}
	if _, err := os.Stat(filepath.Join(root, ".qualification-owner")); err != nil {
		t.Fatal(err)
	}
}
