//go:build qualification

// This adapter is compiled only for isolated qualification, never desktop builds.
package launcher

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"ligandx-launcher/internal/runtimebundle"
)

type QualificationConfig struct {
	RuntimeDir                string `json:"runtime_dir"`
	Project                   string `json:"project"`
	Owner                     string `json:"owner"`
	PublicKeyB64              string `json:"public_key_b64"`
	LauncherVersion           string `json:"launcher_version"`
	CPUOnly                   bool   `json:"cpu_only"`
	AuthorizationPublicKeyB64 string `json:"authorization_public_key_b64"`
	PredecessorPublicKeyB64   string `json:"predecessor_public_key_b64"`
}

// QualificationAuthorization is a short-lived assertion from the candidate controller.
// That controller verifies the canonical candidate ID before signing the raw plan hash.
// Its key is an explicit qualification trust root, never a shipping release key.
type QualificationAuthorization struct {
	Schema                    string `json:"schema"`
	CandidateID               string `json:"candidate_id"`
	PlanSHA256                string `json:"plan_sha256"`
	PredecessorVersion        string `json:"predecessor_version"`
	PredecessorManifestSHA256 string `json:"predecessor_manifest_sha256"`
	TargetBundleSHA256        string `json:"target_bundle_sha256"`
	Project                   string `json:"project"`
	Owner                     string `json:"owner"`
	IssuedAt                  string `json:"issued_at"`
	ExpiresAt                 string `json:"expires_at"`
}

func qualificationDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (q *QualificationApp) verifyTransition(raw, signature, plan, predecessor, predecessorSig, candidate, candidateSig []byte) (QualificationAuthorization, error) {
	var auth QualificationAuthorization
	key, err := runtimebundle.DecodePublicKey(q.config.AuthorizationPublicKeyB64)
	if err != nil {
		return auth, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || !ed25519.Verify(key, raw, sig) {
		return auth, fmt.Errorf("invalid qualification transition signature")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&auth); err != nil {
		return auth, err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return auth, fmt.Errorf("trailing qualification authorization data")
	}
	issued, err := time.Parse(time.RFC3339, auth.IssuedAt)
	if err != nil {
		return auth, err
	}
	expires, err := time.Parse(time.RFC3339, auth.ExpiresAt)
	now := time.Now().UTC()
	if err != nil || issued.After(now.Add(time.Minute)) || !expires.After(now) || !expires.After(issued) || expires.Sub(issued) > 30*time.Minute {
		return auth, fmt.Errorf("invalid or expired qualification authorization interval")
	}
	if auth.Schema != "ligandx-qualification-transition/1" || auth.Project != q.config.Project || auth.Owner != q.config.Owner || !regexp.MustCompile(`^c-[a-f0-9]{16}$`).MatchString(auth.CandidateID) {
		return auth, fmt.Errorf("qualification transition scope mismatch")
	}
	var identity struct {
		CandidateID     string `json:"candidate_id"`
		BaselineVersion string `json:"baseline_version"`
	}
	if err := json.Unmarshal(plan, &identity); err != nil {
		return auth, err
	}
	if auth.PlanSHA256 != qualificationDigest(plan) || identity.CandidateID != auth.CandidateID || identity.BaselineVersion != auth.PredecessorVersion || runtimebundle.InstalledVersion(q.config.RuntimeDir) != auth.PredecessorVersion {
		return auth, fmt.Errorf("qualification candidate plan or installed predecessor mismatch")
	}
	// The predecessor must be an authenticated semantic release, not another
	// qualification candidate or an invented version used to defeat ordering.
	if _, comparable := runtimebundle.CompareVersions(auth.PredecessorVersion, auth.PredecessorVersion); !comparable {
		return auth, fmt.Errorf("predecessor is not a semantic release")
	}
	predecessorPolicy := runtimePolicy()
	predecessorPolicy.PublicKeyB64 = q.config.PredecessorPublicKeyB64
	if _, err := predecessorPolicy.VerifyManifest(predecessor, predecessorSig, auth.PredecessorVersion); err != nil {
		return auth, err
	}
	if auth.PredecessorManifestSHA256 != qualificationDigest(predecessor) {
		return auth, fmt.Errorf("predecessor manifest mismatch")
	}
	manifest, err := runtimePolicy().VerifyManifest(candidate, candidateSig, "candidate-"+auth.CandidateID)
	if err != nil {
		return auth, err
	}
	if manifest.SHA256 != auth.TargetBundleSHA256 {
		return auth, fmt.Errorf("qualification target bundle mismatch")
	}
	return auth, nil
}

type QualificationApp struct {
	app    *App
	config QualificationConfig
	events map[string]int
	mutex  sync.Mutex
}

func NewQualificationApp(ctx context.Context, config QualificationConfig) (*QualificationApp, error) {
	if isPublicBuild {
		return nil, fmt.Errorf("qualification adapter cannot run in a public build")
	}
	if !regexp.MustCompile(`^lx-qualification-[a-z0-9][a-z0-9-]{0,42}$`).MatchString(config.Project) || strings.TrimSpace(config.Owner) == "" {
		return nil, fmt.Errorf("qualification requires a named owner and isolated project namespace")
	}
	if _, err := runtimebundle.DecodePublicKey(config.PublicKeyB64); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(config.RuntimeDir)
	if err != nil || filepath.Base(root) != config.Project {
		return nil, fmt.Errorf("runtime root must be named for its qualification project")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, fmt.Errorf("qualification root must not traverse symlinks")
	}
	marker := filepath.Join(root, ".qualification-owner")
	stored, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		files, readErr := os.ReadDir(root)
		if readErr != nil || len(files) != 0 {
			return nil, fmt.Errorf("refusing unowned nonempty qualification root")
		}
		file, openErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return nil, openErr
		}
		_, err = file.WriteString(config.Owner + "\n")
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil || strings.TrimSpace(string(stored)) != config.Owner {
		return nil, fmt.Errorf("qualification root belongs to a different owner")
	}
	config.RuntimeDir = root
	runtimeBundlePublicKeyB64 = config.PublicKeyB64
	launcherVersion = config.LauncherVersion
	qa := &QualificationApp{config: config, events: make(map[string]int)}
	qa.app = NewApp()
	qa.app.ctx = ctx
	qa.app.projectPath = root
	qa.app.qualificationProject = config.Project
	qa.app.qualificationCPUOnly = config.CPUOnly
	qa.app.eventSink = func(event string, _ ...interface{}) { qa.mutex.Lock(); defer qa.mutex.Unlock(); qa.events[event]++ }
	return qa, nil
}

func (q *QualificationApp) EventCounts() map[string]int {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	copy := make(map[string]int, len(q.events))
	for name, count := range q.events {
		copy[name] = count
	}
	return copy
}

type QualificationUpgrade struct {
	Plan                 []byte
	Authorization        []byte
	Signature            []byte
	PredecessorManifest  []byte
	PredecessorSignature []byte
}

func qualificationRead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("qualification input exceeds size limit")
	}
	return data, nil
}

// Install freezes the authenticated local inputs before calling the desktop
// installer's actual download, verification, extraction and activation path.
func (q *QualificationApp) Install(bundlePath, version string, upgrade *QualificationUpgrade) (DistributionStatus, error) {
	fail := func(err error) (DistributionStatus, error) { return q.app.GetDistributionStatus(), err }
	if strings.TrimSpace(version) == "" {
		return fail(fmt.Errorf("explicit runtime version required"))
	}
	bundlePath, err := filepath.Abs(bundlePath)
	if err != nil {
		return fail(err)
	}
	manifest, err := qualificationRead(filepath.Join(filepath.Dir(bundlePath), runtimebundle.ManifestAssetName), 64*1024)
	if err != nil {
		return fail(err)
	}
	signature, err := qualificationRead(filepath.Join(filepath.Dir(bundlePath), runtimebundle.SignatureAssetName), 4096)
	if err != nil {
		return fail(err)
	}
	verified, err := runtimePolicy().VerifyManifest(manifest, signature, version)
	if err != nil {
		return fail(err)
	}
	if upgrade != nil {
		auth, verifyErr := q.verifyTransition(upgrade.Authorization, upgrade.Signature, upgrade.Plan, upgrade.PredecessorManifest, upgrade.PredecessorSignature, manifest, signature)
		if verifyErr != nil {
			return fail(verifyErr)
		}
		if version != "candidate-"+auth.CandidateID {
			return fail(fmt.Errorf("authorized candidate version mismatch"))
		}
	}
	stage, err := os.MkdirTemp("", "ligandx-qualification-input-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(stage)
	for name, data := range map[string][]byte{runtimebundle.ManifestAssetName: manifest, runtimebundle.SignatureAssetName: signature} {
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			return fail(err)
		}
	}
	source, err := os.Open(bundlePath)
	if err != nil {
		return fail(err)
	}
	defer source.Close()
	frozenPath := filepath.Join(stage, runtimebundle.AssetName)
	dest, err := os.OpenFile(frozenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fail(err)
	}
	n, copyErr := io.Copy(dest, io.LimitReader(source, verified.Size+1))
	closeErr := dest.Close()
	if copyErr != nil {
		return fail(copyErr)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	if n != verified.Size {
		return fail(fmt.Errorf("qualification bundle size mismatch"))
	}
	if err := runtimebundle.VerifyFile(frozenPath, verified); err != nil {
		return fail(err)
	}
	// Only the verified qualification transition authorizes non-semver ordering.
	// The production stable-index and downgrade methods are unchanged.
	return q.app.installRuntimeBundleSelected(frozenPath, version, upgrade != nil)
}

// Start refuses a silent partial selection: the desktop method intentionally
// skips locked groups, but a qualification case must exercise every requested one.
func (q *QualificationApp) Start(groups []string) error {
	if _, err := q.ServicePlan(groups); err != nil {
		return err
	}
	return q.app.StartServiceGroups("prod", groups)
}

// ServicePlan records intent independently of which containers happen to start.
func (q *QualificationApp) ServicePlan(groups []string) ([]string, error) {
	if len(groups) == 0 {
		return nil, fmt.Errorf("qualification requires explicit service groups")
	}
	known := make(map[string]ServiceGroup)
	for _, group := range q.app.GetServiceGroups() {
		known[group.ID] = group
	}
	for _, id := range groups {
		group, ok := known[id]
		if !ok || group.Locked {
			return nil, fmt.Errorf("qualification service group %q is unknown or locked", id)
		}
	}
	return launcherAllowedServices(q.app.GetServiceGroups(), groups), nil
}

// ImagePlan names the images PullServiceGroups would fetch for these groups, so a
// harness can stage an installed predecessor's images without starting anything.
func (q *QualificationApp) ImagePlan(groups []string) ([]string, error) {
	if _, err := q.ServicePlan(groups); err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(groups))
	for _, id := range groups {
		selected[id] = true
	}
	var images []string
	for _, group := range q.app.GetServiceGroups() {
		if selected[group.ID] {
			images = append(images, group.Images...)
		}
	}
	return sortedUnique(images), nil
}

func (q *QualificationApp) Configure(envPath, licensePath string, groups []string) error {
	if envPath != "" {
		content, err := qualificationRead(envPath, 1024*1024)
		if err != nil {
			return err
		}
		if err := q.app.SaveEnvContent("prod", string(content)); err != nil {
			return err
		}
	}
	if licensePath != "" {
		if _, err := q.app.ImportLicense(licensePath); err != nil {
			return err
		}
	}
	if groups != nil {
		config, err := q.app.GetLauncherConfig()
		if err != nil {
			return err
		}
		config.SelectedGroups = groups
		if err := q.app.SaveLauncherConfig(config); err != nil {
			return err
		}
	}
	return nil
}

func (q *QualificationApp) Status() DistributionStatus { return q.app.GetDistributionStatus() }
func (q *QualificationApp) Close()                     { q.app.shutdown(q.app.ctx) }
