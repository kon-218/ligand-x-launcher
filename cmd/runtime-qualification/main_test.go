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
