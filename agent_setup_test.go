package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildAgentSetupInstructionsOmitsPassword(t *testing.T) {
	configJSON := `{"mcpServers":{"ligand-x":{"command":"docker"}}}`
	text := buildAgentSetupInstructions(configJSON, "2026-08-30T00:00:00Z", []string{"core"}, false)
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

func TestAgentSetupGPUShortWarning(t *testing.T) {
	withPro := agentSetupGPUShortWarning([]string{"core", "admet"})
	if !strings.Contains(withPro, "can run") {
		t.Fatalf("expected positive gpu-short note, got %q", withPro)
	}
	withoutPro := agentSetupGPUShortWarning([]string{"core", "docking"})
	if !strings.Contains(withoutPro, "WARNING") {
		t.Fatalf("expected warning when Pro gpu-short groups absent, got %q", withoutPro)
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
	text := buildAgentSetupInstructions(string(raw), "2026-08-30T00:00:00Z", []string{"boltz2"}, true)
	if !strings.Contains(text, "agent-mcp") {
		t.Fatalf("expected launcher connector mode in instructions")
	}
	if strings.Contains(text, "docker") || strings.Contains(text, "LIGANDX_AGENT_TOKEN") {
		t.Fatalf("MCP config must not expose Docker or credential details")
	}
}

func testSessionSecret(t *testing.T) agentSessionSecret {
	t.Helper()
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return agentSessionSecret{
		Token:        "workspace-token-value",
		SigningKey:   hex.EncodeToString(private.Seed()),
		CredentialID: "11111111-2222-3333-4444-555555555555",
		Scopes:       []string{"projects:read", "jobs:read", "jobs:plan"},
	}
}

func TestAgentMCPSessionUsesProtectedStoreNotFiles(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	store := NewMemorySecretStore()
	app.secretStore = store
	sessionID := "0123456789abcdef0123456789abcdef"
	secret := testSessionSecret(t)
	meta := agentSessionMeta{
		SessionID:        sessionID,
		CredentialID:     secret.CredentialID,
		ExpiresAt:        "2026-09-02T00:00:00Z",
		ExecutionEnabled: false,
		CreatedAt:        "2026-09-02T00:00:00Z",
	}
	if err := storeAgentSession(store, app.projectPath, meta, secret); err != nil {
		t.Fatalf("store session: %v", err)
	}
	cmd := agentMCPCommand(app.projectPath, secret, nil, nil, nil)
	joinedArgs := strings.Join(cmd.Args, " ")
	joinedEnv := strings.Join(cmd.Env, "\n")
	if strings.Contains(joinedArgs, "workspace-token-value") || strings.Contains(joinedArgs, secret.SigningKey) {
		t.Fatalf("connector arguments must not contain secrets")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_TOKEN=workspace-token-value") {
		t.Fatalf("connector must forward the bearer token only through its private environment")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_CREDENTIAL_ID="+secret.CredentialID) {
		t.Fatalf("connector must inject the credential id into the child environment")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_SIGNING_KEY="+secret.SigningKey) {
		t.Fatalf("connector must inject the signing key into the child environment")
	}
	if !strings.Contains(joinedEnv, "LIGANDX_AGENT_SCOPES=projects:read,jobs:read,jobs:plan") {
		t.Fatalf("connector must inject workspace scopes into the child environment")
	}
	raw, err := os.ReadFile(agentSessionMetadataPath(app.projectPath))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(string(raw), "workspace-token-value") || strings.Contains(string(raw), secret.SigningKey) {
		t.Fatalf("session metadata must remain non-secret")
	}
	if _, err := os.Stat(filepath.Join(agentSessionDir(app.projectPath), "session-"+sessionID)); !os.IsNotExist(err) {
		t.Fatalf("legacy token files must not be created")
	}
	copied, err := app.CopyAgentSessionConfig(sessionID)
	if err != nil {
		t.Fatalf("copy config: %v", err)
	}
	if strings.Contains(copied, "workspace-token-value") || strings.Contains(copied, secret.SigningKey) {
		t.Fatalf("copied MCP config must not contain secrets")
	}
	if err := deleteStoredAgentSession(store, app.projectPath, sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := store.Get(sessionID); err == nil {
		t.Fatalf("revoked session must disappear from protected storage")
	}
}

func TestAgentMCPConnectorRejectsMalformedSessionAndUnavailableStore(t *testing.T) {
	runtimeDir := t.TempDir()
	for _, filename := range []string{"docker-compose.yml", ".env.production"} {
		if err := os.WriteFile(filepath.Join(runtimeDir, filename), []byte("# test\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
	}
	store := NewMemorySecretStore()
	if err := runAgentMCPConnectorWithStore([]string{"--runtime-dir", runtimeDir, "--session-id", "not-hex"}, store); err == nil {
		t.Fatal("malformed session id must be rejected")
	}
	sessionID := "0123456789abcdef0123456789abcdef"
	if err := runAgentMCPConnectorWithStore([]string{"--runtime-dir", runtimeDir, "--session-id", sessionID}, store); err == nil {
		t.Fatal("missing protected secret must be rejected")
	}
	store.SetUnavailable(ErrSecureStorageUnavailable)
	err := runAgentMCPConnectorWithStore([]string{"--runtime-dir", runtimeDir, "--session-id", sessionID}, store)
	if err == nil {
		t.Fatal("unavailable secure storage must fail closed for MCP")
	}
	if !strings.Contains(err.Error(), "secure credential storage") && !strings.Contains(err.Error(), "Unlock") {
		t.Fatalf("MCP error should tell the user to unlock storage, got %v", err)
	}
}

func TestLegacyTokenFilesAreDeletedOnlyAfterSecureStore(t *testing.T) {
	runtimeDir := t.TempDir()
	legacy := filepath.Join(agentSessionDir(runtimeDir), "session-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("old-file-token\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if got := listLegacyAgentTokenFiles(runtimeDir); len(got) != 1 {
		t.Fatalf("expected one legacy file, got %v", got)
	}
	store := NewMemorySecretStore()
	secret := testSessionSecret(t)
	meta := agentSessionMeta{SessionID: "0123456789abcdef0123456789abcdef", CredentialID: secret.CredentialID}
	if err := storeAgentSession(store, runtimeDir, meta, secret); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy file must remain until the caller deletes it after a successful store: %v", err)
	}
	deleteLegacyAgentTokenFiles(runtimeDir)
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy file should be gone after successful secure creation")
	}
}

func TestNativeSecretStoreRoundTripOrFailClosed(t *testing.T) {
	store := defaultSecretStore()
	account := "ligandx-test-" + strings.Repeat("ab", 8)
	if err := store.Available(); err != nil {
		if runtime.GOOS == "linux" && strings.Contains(strings.ToLower(err.Error()), "secret service") {
			return
		}
		t.Skipf("native store unavailable: %v", err)
	}
	secret := `{"token":"t","signing_key":"` + strings.Repeat("0", 64) + `","credential_id":"id"}`
	if err := store.Set(account, secret); err != nil {
		t.Fatalf("set: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(account) })
	got, err := store.Get(account)
	if err != nil || got != secret {
		t.Fatalf("get mismatch: %q %v", got, err)
	}
}

func TestLinuxWithoutSecretServiceDoesNotBreakNonMCPLauncher(t *testing.T) {
	app := NewApp()
	store := NewMemorySecretStore()
	store.SetUnavailable(ErrSecureStorageUnavailable)
	app.secretStore = store
	status := app.GetAgentStorageStatus()
	if status.Available {
		t.Fatal("storage should report unavailable")
	}
	if status.Message == "" {
		t.Fatal("unavailable storage must include an actionable message")
	}
	if app.logStreams == nil {
		t.Fatal("core launcher state must still initialize when assistant storage is locked")
	}
}
