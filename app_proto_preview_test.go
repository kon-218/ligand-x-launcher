package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ligandx-launcher/internal/runtimebundle"
)

func TestSelectedPreviewRuntimeSurvivesBundleExtraction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, body := range map[string]string{
		"docker-compose.yml":       "# Preview bundles: proto-mutation\nservices:\n  gateway:\n    image: pinned\n  worker-proto:\n    image: pinned\n",
		".env.production.template": "VERSION=v2026.10.01-test\nLIGANDX_PROTO_PILOT_ENABLED=1\n",
		"docker-compose.gpu.yml":   "services:\n  worker-proto:\n    devices: [nvidia.com/gpu=all]\n",
	} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(dir, "installed")
	if err := runtimebundle.Extract(path, extracted); err != nil {
		t.Fatal(err)
	}
	if err := verifySelectedRuntimePreview(extracted, "proto-mutation"); err != nil {
		t.Fatal(err)
	}
	if err := verifySelectedRuntimePreview(extracted, ""); err == nil {
		t.Fatal("stable launcher silently accepted preview runtime")
	}
	if err := verifySelectedRuntimePreview(extracted, "unreviewed"); err == nil {
		t.Fatal("unknown preview accepted")
	}
	if err := os.WriteFile(filepath.Join(extracted, "docker-compose.yml"), []byte("services:\n  gateway:\n    image: pinned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySelectedRuntimePreview(extracted, "proto-mutation"); err == nil {
		t.Fatal("preview selection accepted a stable installed bundle")
	}
}

func TestPreparedProteinRuntimeChecksCanonicalInventoryAndBytes(t *testing.T) {
	dir := t.TempDir()
	home, cache := filepath.Join(dir, "proto"), filepath.Join(dir, "models")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("prepared bytes")
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(home, "python"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	weight := filepath.Join(cache, "weight.bin")
	if err := os.WriteFile(weight, payload, 0600); err != nil {
		t.Fatal(err)
	}
	row := map[string]any{
		"interpreter": "/opt/proto/python", "identity": map[string]string{"proto_commit": "reviewed", "interpreter_sha256": hash},
		"files": []map[string]string{{"path": "/models/proto/weight.bin", "sha256": hash}},
	}
	document := map[string]any{"schema": "protein_tools_assets/v1", "esm2": map[string]any{"esm2_t6_8M_UR50D": row, "esm2_t33_650M_UR50D": row}, "fampnn": map[string]any{"0.3_cath": row}}
	manifest, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "assets.json")
	approvedPath := manifestPath + ".sha256"
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifest)
	if err := os.WriteFile(approvedPath, []byte(hex.EncodeToString(manifestDigest[:])), 0600); err != nil {
		t.Fatal(err)
	}
	controls := map[string]string{"PROTO_HOME_HOST": home, "PROTO_MODEL_CACHE_HOST": cache, "PROTO_RUNTIME_MANIFEST_HOST": manifestPath, "PROTO_RUNTIME_MANIFEST_SHA256_HOST": approvedPath}
	if err := preparedProteinRuntimeAvailable(controls); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(weight, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preparedProteinRuntimeAvailable(controls); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("tampered weights accepted: %v", err)
	}
	if err := os.WriteFile(approvedPath, []byte(strings.Repeat("0", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preparedProteinRuntimeAvailable(controls); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("unapproved manifest accepted: %v", err)
	}
}
