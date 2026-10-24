//go:build qualification

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestRefusesUnknownActionsAndTrailingInput(t *testing.T) {
	for _, raw := range []string{`{"action":"uninstall"}`, `{"action":"status"} {}`, `{"action":"status","unknown":true}`} {
		if err := run(strings.NewReader(raw), new(bytes.Buffer)); err == nil {
			t.Fatal("accepted malformed/unsupported request")
		}
	}
}

func TestConfigureAndStatusDoNotExposeCredentials(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	root := filepath.Join(t.TempDir(), "lx-qualification-cli")
	envPath := filepath.Join(t.TempDir(), "private.env")
	if err := os.WriteFile(envPath, []byte("POSTGRES_PASSWORD=private-test-value\nVERSION=v1.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := map[string]interface{}{"action": "configure", "config": map[string]interface{}{"runtime_dir": root, "project": "lx-qualification-cli", "owner": "BUILD-03-test", "public_key_b64": base64.StdEncoding.EncodeToString(public), "launcher_version": "v1.0.0"}, "env_path": envPath, "groups": []string{"core"}}
	raw, _ := json.Marshal(request)
	var output bytes.Buffer
	if err := run(bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-test-value") {
		t.Fatal("response leaked credentials")
	}
	var result map[string]interface{}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true || result["qualification_only"] != true {
		t.Fatalf("unexpected result: %v", result)
	}
	if _, err := os.Stat(filepath.Join(root, ".env.production")); err != nil {
		t.Fatal("configuration did not use real launcher path:", err)
	}
}

func TestPlanNamesImagesWithoutStartingServices(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	root := filepath.Join(t.TempDir(), "lx-qualification-plan")
	envPath := filepath.Join(t.TempDir(), "private.env")
	if err := os.WriteFile(envPath, []byte("VERSION=v1.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := map[string]interface{}{"runtime_dir": root, "project": "lx-qualification-plan", "owner": "BUILD-03-test", "public_key_b64": base64.StdEncoding.EncodeToString(public), "launcher_version": "v1.0.0"}
	for _, request := range []map[string]interface{}{
		{"action": "configure", "config": config, "env_path": envPath, "groups": []string{"core"}},
		{"action": "plan", "config": config, "groups": []string{"core"}},
	} {
		raw, _ := json.Marshal(request)
		var output bytes.Buffer
		if err := run(bytes.NewReader(raw), &output); err != nil {
			t.Fatal(err)
		}
		if request["action"] != "plan" {
			continue
		}
		var result struct {
			OK       bool                   `json:"ok"`
			Services []string               `json:"requested_services"`
			Images   []string               `json:"requested_images"`
			Events   map[string]interface{} `json:"events"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.OK || len(result.Services) == 0 || len(result.Images) == 0 {
			t.Fatalf("plan omitted services or images: %s", output.String())
		}
		for _, image := range result.Images {
			if !strings.HasSuffix(image, ":v1.2.3") && !strings.Contains(image, "@sha256:") {
				t.Fatalf("plan image is not pinned to the installed version: %s", image)
			}
		}
		if len(result.Events) != 0 {
			t.Fatalf("plan acted on the runtime: %v", result.Events)
		}
	}
}
