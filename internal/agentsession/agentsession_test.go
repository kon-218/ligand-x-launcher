package agentsession

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ligandx-launcher/internal/secretstore"
)

func TestBuildAgentSetupInstructionsOmitsPassword(t *testing.T) {
	configJSON := `{"mcpServers":{"ligand-x":{"command":"docker"}}}`
	text := SetupInstructions(configJSON, "2026-08-30T00:00:00Z", "gpu-short note", false)
	if !strings.Contains(text, configJSON) {
		t.Fatalf("instructions should include MCP config JSON")
	}
	if strings.Contains(strings.ToLower(text), "password") {
		t.Fatalf("instructions must not mention passwords")
	}
	if !strings.Contains(text, "ligandx_capabilities") || !strings.Contains(text, "ligandx_create_plan_from_template") {
		t.Fatalf("instructions should mention template workflow tools")
	}
	if strings.Contains(text, "ligandx_run_") {
		t.Fatalf("instructions must not advertise generic run tools")
	}
	if strings.Contains(text, "ligandx_wait_for_job") {
		t.Fatalf("instructions must not advertise blocking wait tools")
	}
	if !strings.Contains(text, "lethal trifecta") {
		t.Fatalf("instructions must warn about the lethal trifecta")
	}
	if !strings.Contains(text, "Claude Code") || !strings.Contains(text, "Codex") || !strings.Contains(text, ".cursor/mcp.json") {
		t.Fatalf("instructions should name Claude Code, Codex, and Cursor config targets")
	}
}

func TestAgentSetupMCPConfigShape(t *testing.T) {
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"ligand-x": map[string]interface{}{
				"command": "/Applications/Ligand-X Launcher",
				"args":    []string{"agent-mcp", "--runtime-dir", "/runtime", "--session-id", "0123456789abcdef0123456789abcdef"},
			},
		},
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := SetupInstructions(string(raw), "2026-08-30T00:00:00Z", "gpu-short note", true)
	if !strings.Contains(text, "agent-mcp") {
		t.Fatalf("expected launcher connector mode in instructions")
	}
	if strings.Contains(text, "docker") || strings.Contains(text, "LIGANDX_AGENT_TOKEN") {
		t.Fatalf("MCP config must not expose Docker or credential details")
	}
}

func testSessionSecret(t *testing.T) Secret {
	t.Helper()
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return Secret{
		Token:        "workspace-token-value",
		SigningKey:   hex.EncodeToString(private.Seed()),
		CredentialID: "11111111-2222-3333-4444-555555555555",
		Scopes:       []string{"projects:read", "jobs:read", "jobs:plan"},
	}
}

func TestAgentMCPConnectorRejectsMalformedSessionAndUnavailableStore(t *testing.T) {
	runtimeDir := t.TempDir()
	for _, filename := range []string{"docker-compose.yml", ".env.production"} {
		if err := os.WriteFile(filepath.Join(runtimeDir, filename), []byte("# test\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
	}
	store := secretstore.NewMemory()
	if err := runConnector([]string{"--runtime-dir", runtimeDir, "--session-id", "not-hex"}, store); err == nil {
		t.Fatal("malformed session id must be rejected")
	}
	sessionID := "0123456789abcdef0123456789abcdef"
	if err := runConnector([]string{"--runtime-dir", runtimeDir, "--session-id", sessionID}, store); err == nil {
		t.Fatal("missing protected secret must be rejected")
	}
	store.SetUnavailable(secretstore.ErrUnavailable)
	err := runConnector([]string{"--runtime-dir", runtimeDir, "--session-id", sessionID}, store)
	if err == nil {
		t.Fatal("unavailable secure storage must fail closed for MCP")
	}
	if !strings.Contains(err.Error(), "secure credential storage") && !strings.Contains(err.Error(), "Unlock") {
		t.Fatalf("MCP error should tell the user to unlock storage, got %v", err)
	}
}

func TestLegacyTokenFilesAreDeletedOnlyAfterSecureStore(t *testing.T) {
	runtimeDir := t.TempDir()
	legacy := filepath.Join(sessionDir(runtimeDir), "session-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("old-file-token\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if got := ListLegacyTokenFiles(runtimeDir); len(got) != 1 {
		t.Fatalf("expected one legacy file, got %v", got)
	}
	store := secretstore.NewMemory()
	secret := testSessionSecret(t)
	meta := Meta{SessionID: "0123456789abcdef0123456789abcdef", CredentialID: secret.CredentialID}
	if err := Persist(store, runtimeDir, meta, secret); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy file must remain until the caller deletes it after a successful store: %v", err)
	}
	DeleteLegacyTokenFiles(runtimeDir)
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy file should be gone after successful secure creation")
	}
}
