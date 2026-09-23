package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ligandx-launcher/internal/agentsession"
	"ligandx-launcher/internal/secretstore"
)

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

func TestAgentMCPSessionUsesProtectedStoreNotFiles(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	store := secretstore.NewMemory()
	app.secretStore = store
	sessionID := "0123456789abcdef0123456789abcdef"
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	secret := agentsession.Secret{
		Token:        "workspace-token-value",
		SigningKey:   hex.EncodeToString(private.Seed()),
		CredentialID: "11111111-2222-3333-4444-555555555555",
		Scopes:       []string{"projects:read", "jobs:read", "jobs:plan"},
	}
	meta := agentsession.Meta{
		SessionID:        sessionID,
		CredentialID:     secret.CredentialID,
		ExpiresAt:        "2026-09-02T00:00:00Z",
		ExecutionEnabled: false,
		CreatedAt:        "2026-09-02T00:00:00Z",
	}
	if err := agentsession.Persist(store, app.projectPath, meta, secret); err != nil {
		t.Fatalf("store session: %v", err)
	}
	cmd := agentsession.Command(app.projectPath, secret, nil, nil, nil)
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
	raw, err := os.ReadFile(filepath.Join(app.projectPath, ".ligandx-agent-mcp", "sessions.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(string(raw), "workspace-token-value") || strings.Contains(string(raw), secret.SigningKey) {
		t.Fatalf("session metadata must remain non-secret")
	}
	if _, err := os.Stat(filepath.Join(app.projectPath, ".ligandx-agent-mcp", "session-"+sessionID)); !os.IsNotExist(err) {
		t.Fatalf("legacy token files must not be created")
	}
	copied, err := app.CopyAgentSessionConfig(sessionID)
	if err != nil {
		t.Fatalf("copy config: %v", err)
	}
	if strings.Contains(copied, "workspace-token-value") || strings.Contains(copied, secret.SigningKey) {
		t.Fatalf("copied MCP config must not contain secrets")
	}
	if err := agentsession.Delete(store, app.projectPath, sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := store.Get(sessionID); err == nil {
		t.Fatalf("revoked session must disappear from protected storage")
	}
}

func TestLinuxWithoutSecretServiceDoesNotBreakNonMCPLauncher(t *testing.T) {
	app := NewApp()
	store := secretstore.NewMemory()
	store.SetUnavailable(secretstore.ErrUnavailable)
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
