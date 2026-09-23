// Package agentsession manages the launcher-issued MCP sessions that let a coding
// assistant reach the local Ligand-X gateway: session metadata on disk, secrets
// in the OS credential store, request signing, and the agent-mcp connector.
package agentsession

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"ligandx-launcher/internal/secretstore"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	sessionDirName      = ".ligandx-agent-mcp"
	sessionsJSONFile    = "sessions.json"
	legacySessionPrefix = "session-"
	proofVersion        = "ligand-x-agent-proof-v1"
)

type StorageStatus struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Message   string `json:"message"`
}

type Info struct {
	SessionID        string `json:"sessionId"`
	CredentialID     string `json:"credentialId"`
	ExpiresAt        string `json:"expiresAt"`
	ExecutionEnabled bool   `json:"executionEnabled"`
	CreatedAt        string `json:"createdAt"`
	SecretPresent    bool   `json:"secretPresent"`
}

type List struct {
	Sessions    []Info        `json:"sessions"`
	LegacyFiles int           `json:"legacyFiles"`
	Storage     StorageStatus `json:"storage"`
}

type Health struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
}

type Secret struct {
	Token        string   `json:"token"`
	SigningKey   string   `json:"signing_key"`
	CredentialID string   `json:"credential_id"`
	Scopes       []string `json:"scopes,omitempty"`
}

type Meta struct {
	SessionID        string `json:"session_id"`
	CredentialID     string `json:"credential_id"`
	ExpiresAt        string `json:"expires_at"`
	ExecutionEnabled bool   `json:"execution_enabled"`
	CreatedAt        string `json:"created_at"`
}

type sessionsFile struct {
	Sessions []Meta `json:"sessions"`
}

func sessionDir(runtimeDir string) string {
	return filepath.Join(runtimeDir, sessionDirName)
}

func metadataPath(runtimeDir string) string {
	return filepath.Join(sessionDir(runtimeDir), sessionsJSONFile)
}

func NewSessionID() (string, error) {
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return "", fmt.Errorf("create assistant session identifier: %w", err)
	}
	return hex.EncodeToString(randomID), nil
}

func NewSigningKey() (privateHex, publicHex string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("create assistant signing key: %w", err)
	}
	return hex.EncodeToString(private.Seed()), hex.EncodeToString(public), nil
}

func marshalSecret(secret Secret) (string, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func parseSecret(raw string) (Secret, error) {
	var secret Secret
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &secret); err != nil {
		return Secret{}, fmt.Errorf("assistant session credential is invalid")
	}
	if secret.Token == "" || len(secret.Token) > 1024 {
		return Secret{}, fmt.Errorf("assistant session credential is invalid")
	}
	seed, err := hex.DecodeString(secret.SigningKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return Secret{}, fmt.Errorf("assistant session signing key is invalid")
	}
	if secret.CredentialID == "" {
		return Secret{}, fmt.Errorf("assistant session credential is invalid")
	}
	return secret, nil
}

func LoadMetadata(runtimeDir string) ([]Meta, error) {
	raw, err := os.ReadFile(metadataPath(runtimeDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file sessionsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("assistant session metadata is invalid")
	}
	return file.Sessions, nil
}

func saveMetadata(runtimeDir string, sessions []Meta) error {
	if err := os.MkdirAll(sessionDir(runtimeDir), 0o700); err != nil {
		return fmt.Errorf("create local assistant session directory: %w", err)
	}
	if sessions == nil {
		sessions = []Meta{}
	}
	raw, err := json.MarshalIndent(sessionsFile{Sessions: sessions}, "", "  ")
	if err != nil {
		return err
	}
	path := metadataPath(runtimeDir)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write assistant session metadata: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func upsertMetadata(runtimeDir string, meta Meta) error {
	sessions, err := LoadMetadata(runtimeDir)
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
	return saveMetadata(runtimeDir, sessions)
}

func removeMetadata(runtimeDir, sessionID string) error {
	sessions, err := LoadMetadata(runtimeDir)
	if err != nil {
		return err
	}
	next := sessions[:0]
	for _, existing := range sessions {
		if existing.SessionID != sessionID {
			next = append(next, existing)
		}
	}
	return saveMetadata(runtimeDir, next)
}

func ListLegacyTokenFiles(runtimeDir string) []string {
	entries, err := os.ReadDir(sessionDir(runtimeDir))
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), legacySessionPrefix) {
			continue
		}
		files = append(files, filepath.Join(sessionDir(runtimeDir), entry.Name()))
	}
	return files
}

func DeleteLegacyTokenFiles(runtimeDir string) {
	for _, path := range ListLegacyTokenFiles(runtimeDir) {
		_ = os.Remove(path)
	}
}

func Persist(store secretstore.Store, runtimeDir string, meta Meta, secret Secret) error {
	if store == nil {
		return fmt.Errorf("%w: secure storage is not configured", secretstore.ErrUnavailable)
	}
	if err := store.Available(); err != nil {
		return err
	}
	payload, err := marshalSecret(secret)
	if err != nil {
		return err
	}
	if err := store.Set(meta.SessionID, payload); err != nil {
		return err
	}
	if err := upsertMetadata(runtimeDir, meta); err != nil {
		_ = store.Delete(meta.SessionID)
		return err
	}
	return nil
}

func LoadSecret(store secretstore.Store, sessionID string) (Secret, error) {
	if store == nil {
		return Secret{}, fmt.Errorf("%w: secure storage is not configured", secretstore.ErrUnavailable)
	}
	raw, err := store.Get(sessionID)
	if err != nil {
		return Secret{}, err
	}
	return parseSecret(raw)
}

func Delete(store secretstore.Store, runtimeDir, sessionID string) error {
	if store != nil {
		_ = store.Delete(sessionID)
	}
	return removeMetadata(runtimeDir, sessionID)
}

func MCPConfigJSON(executable, runtimeDir, sessionID string) ([]byte, error) {
	return json.MarshalIndent(map[string]interface{}{"mcpServers": map[string]interface{}{
		"ligand-x": map[string]interface{}{
			"command": executable,
			"args":    []string{"agent-mcp", "--runtime-dir", runtimeDir, "--session-id", sessionID},
		},
	}}, "", "  ")
}

func signProof(privateKeyHex, method, path, bodySHA256 string, timestamp int64, nonce string) (string, error) {
	seed, err := hex.DecodeString(privateKeyHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("assistant session signing key is invalid")
	}
	message := strings.Join([]string{
		proofVersion,
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

func newNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func ProofHeaders(secret Secret, method, path string, body []byte) (map[string]string, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().Unix()
	digest := contentSHA256Hex(body)
	signature, err := signProof(secret.SigningKey, method, path, digest, timestamp, nonce)
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

// Status reports whether the protected store can hold assistant sessions.
func Status(store secretstore.Store) StorageStatus {
	if store == nil {
		store = secretstore.Default()
	}
	status := StorageStatus{Backend: store.Name(), Available: true}
	if err := store.Available(); err != nil {
		status.Available = false
		status.Message = fmt.Sprintf(
			"%s is unavailable or locked. Ligand-X itself still works; reconnecting an AI assistant needs an unlocked %s.",
			store.Name(), store.Name(),
		)
		return status
	}
	status.Message = store.Name() + " is ready for assistant sessions."
	return status
}
