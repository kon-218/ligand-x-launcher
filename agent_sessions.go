package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	agentSessionDirName      = ".ligandx-agent-mcp"
	agentSessionsJSONFile    = "sessions.json"
	agentLegacySessionPrefix = "session-"
	agentProofVersion        = "ligand-x-agent-proof-v1"
)

type AgentStorageStatus struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Message   string `json:"message"`
}

type AgentSessionInfo struct {
	SessionID        string `json:"sessionId"`
	CredentialID     string `json:"credentialId"`
	ExpiresAt        string `json:"expiresAt"`
	ExecutionEnabled bool   `json:"executionEnabled"`
	CreatedAt        string `json:"createdAt"`
	SecretPresent    bool   `json:"secretPresent"`
}

type AgentSessionList struct {
	Sessions    []AgentSessionInfo `json:"sessions"`
	LegacyFiles int                `json:"legacyFiles"`
	Storage     AgentStorageStatus `json:"storage"`
}

type AgentSessionHealth struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
}

type agentSessionSecret struct {
	Token        string   `json:"token"`
	SigningKey   string   `json:"signing_key"`
	CredentialID string   `json:"credential_id"`
	Scopes       []string `json:"scopes,omitempty"`
}

type agentSessionMeta struct {
	SessionID        string `json:"session_id"`
	CredentialID     string `json:"credential_id"`
	ExpiresAt        string `json:"expires_at"`
	ExecutionEnabled bool   `json:"execution_enabled"`
	CreatedAt        string `json:"created_at"`
}

type agentSessionsFile struct {
	Sessions []agentSessionMeta `json:"sessions"`
}

func agentSessionDir(runtimeDir string) string {
	return filepath.Join(runtimeDir, agentSessionDirName)
}

func agentSessionMetadataPath(runtimeDir string) string {
	return filepath.Join(agentSessionDir(runtimeDir), agentSessionsJSONFile)
}

func generateAgentSessionID() (string, error) {
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return "", fmt.Errorf("create assistant session identifier: %w", err)
	}
	return hex.EncodeToString(randomID), nil
}

func generateAgentSigningKey() (privateHex, publicHex string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("create assistant signing key: %w", err)
	}
	return hex.EncodeToString(private.Seed()), hex.EncodeToString(public), nil
}

func marshalAgentSessionSecret(secret agentSessionSecret) (string, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func parseAgentSessionSecret(raw string) (agentSessionSecret, error) {
	var secret agentSessionSecret
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &secret); err != nil {
		return agentSessionSecret{}, fmt.Errorf("assistant session credential is invalid")
	}
	if secret.Token == "" || len(secret.Token) > 1024 {
		return agentSessionSecret{}, fmt.Errorf("assistant session credential is invalid")
	}
	seed, err := hex.DecodeString(secret.SigningKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return agentSessionSecret{}, fmt.Errorf("assistant session signing key is invalid")
	}
	if secret.CredentialID == "" {
		return agentSessionSecret{}, fmt.Errorf("assistant session credential is invalid")
	}
	return secret, nil
}

func loadAgentSessionMetadata(runtimeDir string) ([]agentSessionMeta, error) {
	raw, err := os.ReadFile(agentSessionMetadataPath(runtimeDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file agentSessionsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("assistant session metadata is invalid")
	}
	return file.Sessions, nil
}

func saveAgentSessionMetadata(runtimeDir string, sessions []agentSessionMeta) error {
	if err := os.MkdirAll(agentSessionDir(runtimeDir), 0o700); err != nil {
		return fmt.Errorf("create local assistant session directory: %w", err)
	}
	if sessions == nil {
		sessions = []agentSessionMeta{}
	}
	raw, err := json.MarshalIndent(agentSessionsFile{Sessions: sessions}, "", "  ")
	if err != nil {
		return err
	}
	path := agentSessionMetadataPath(runtimeDir)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write assistant session metadata: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func upsertAgentSessionMetadata(runtimeDir string, meta agentSessionMeta) error {
	sessions, err := loadAgentSessionMetadata(runtimeDir)
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range sessions {
		if existing.SessionID == meta.SessionID {
			sessions[i] = meta
			replaced = true
			break
		}
	}
	if !replaced {
		sessions = append(sessions, meta)
	}
	return saveAgentSessionMetadata(runtimeDir, sessions)
}

func removeAgentSessionMetadata(runtimeDir, sessionID string) error {
	sessions, err := loadAgentSessionMetadata(runtimeDir)
	if err != nil {
		return err
	}
	next := sessions[:0]
	for _, existing := range sessions {
		if existing.SessionID != sessionID {
			next = append(next, existing)
		}
	}
	return saveAgentSessionMetadata(runtimeDir, next)
}

func listLegacyAgentTokenFiles(runtimeDir string) []string {
	entries, err := os.ReadDir(agentSessionDir(runtimeDir))
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), agentLegacySessionPrefix) {
			continue
		}
		files = append(files, filepath.Join(agentSessionDir(runtimeDir), entry.Name()))
	}
	return files
}

func deleteLegacyAgentTokenFiles(runtimeDir string) {
	for _, path := range listLegacyAgentTokenFiles(runtimeDir) {
		_ = os.Remove(path)
	}
}

func storeAgentSession(store SecretStore, runtimeDir string, meta agentSessionMeta, secret agentSessionSecret) error {
	if store == nil {
		return fmt.Errorf("%w: secure storage is not configured", ErrSecureStorageUnavailable)
	}
	if err := store.Available(); err != nil {
		return err
	}
	payload, err := marshalAgentSessionSecret(secret)
	if err != nil {
		return err
	}
	if err := store.Set(meta.SessionID, payload); err != nil {
		return err
	}
	if err := upsertAgentSessionMetadata(runtimeDir, meta); err != nil {
		_ = store.Delete(meta.SessionID)
		return err
	}
	return nil
}

func loadAgentSessionSecret(store SecretStore, sessionID string) (agentSessionSecret, error) {
	if store == nil {
		return agentSessionSecret{}, fmt.Errorf("%w: secure storage is not configured", ErrSecureStorageUnavailable)
	}
	raw, err := store.Get(sessionID)
	if err != nil {
		return agentSessionSecret{}, err
	}
	return parseAgentSessionSecret(raw)
}

func deleteStoredAgentSession(store SecretStore, runtimeDir, sessionID string) error {
	if store != nil {
		_ = store.Delete(sessionID)
	}
	return removeAgentSessionMetadata(runtimeDir, sessionID)
}

func mcpConfigJSON(executable, runtimeDir, sessionID string) ([]byte, error) {
	return json.MarshalIndent(map[string]interface{}{"mcpServers": map[string]interface{}{
		"ligand-x": map[string]interface{}{
			"command": executable,
			"args":    []string{"agent-mcp", "--runtime-dir", runtimeDir, "--session-id", sessionID},
		},
	}}, "", "  ")
}

func signAgentProof(privateKeyHex, method, path, bodySHA256 string, timestamp int64, nonce string) (string, error) {
	seed, err := hex.DecodeString(privateKeyHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("assistant session signing key is invalid")
	}
	message := strings.Join([]string{
		agentProofVersion,
		strings.ToUpper(method),
		path,
		"", // launcher health checks have no query parameters
		strings.ToLower(bodySHA256),
		fmt.Sprintf("%d", timestamp),
		strings.ToLower(nonce),
	}, "\n")
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(message))
	return hex.EncodeToString(signature), nil
}

func contentSHA256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func newAgentNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func agentProofHeaders(secret agentSessionSecret, method, path string, body []byte) (map[string]string, error) {
	nonce, err := newAgentNonce()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().Unix()
	digest := contentSHA256Hex(body)
	signature, err := signAgentProof(secret.SigningKey, method, path, digest, timestamp, nonce)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Authorization":            "Bearer " + secret.Token,
		"X-LigandX-Credential-Id":  secret.CredentialID,
		"X-LigandX-Timestamp":      fmt.Sprintf("%d", timestamp),
		"X-LigandX-Nonce":          nonce,
		"X-LigandX-Content-SHA256": digest,
		"X-LigandX-Signature":      signature,
	}, nil
}
