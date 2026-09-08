package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func agentMCPCommand(runtimeDir string, secret agentSessionSecret, stdin io.Reader, stdout, stderr io.Writer) *exec.Cmd {
	args := []string{
		"compose", "--env-file", filepath.Join(runtimeDir, ".env.production"),
		"-f", filepath.Join(runtimeDir, "docker-compose.yml"),
		"exec", "-T",
		"-e", "LIGANDX_AGENT_BASE_URL=http://gateway:8000",
		"-e", "LIGANDX_AGENT_TOKEN",
		"-e", "LIGANDX_AGENT_CREDENTIAL_ID",
		"-e", "LIGANDX_AGENT_SIGNING_KEY",
		"-e", "LIGANDX_AGENT_SCOPES",
		"gateway", "python", "-m", "gateway.agent_mcp",
	}
	cmd := exec.Command("docker", args...)
	cmd.Dir = runtimeDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	env := withoutEnvironmentKey(os.Environ(), "LIGANDX_AGENT_TOKEN")
	env = withoutEnvironmentKey(env, "LIGANDX_AGENT_CREDENTIAL_ID")
	env = withoutEnvironmentKey(env, "LIGANDX_AGENT_SIGNING_KEY")
	env = withoutEnvironmentKey(env, "LIGANDX_AGENT_SCOPES")
	cmd.Env = append(env,
		"LIGANDX_AGENT_TOKEN="+secret.Token,
		"LIGANDX_AGENT_CREDENTIAL_ID="+secret.CredentialID,
		"LIGANDX_AGENT_SIGNING_KEY="+secret.SigningKey,
		"LIGANDX_AGENT_SCOPES="+strings.Join(secret.Scopes, ","),
	)
	return cmd
}

func withoutEnvironmentKey(values []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func runAgentMCPConnector(args []string) error {
	return runAgentMCPConnectorWithStore(args, defaultSecretStore())
}

func runAgentMCPConnectorWithStore(args []string, store SecretStore) error {
	flags := flag.NewFlagSet("agent-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runtimeDirArg := flags.String("runtime-dir", "", "Ligand-X runtime directory")
	sessionID := flags.String("session-id", "", "opaque launcher-issued session identifier")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*runtimeDirArg) == "" {
		return fmt.Errorf("expected --runtime-dir and --session-id only")
	}
	decodedSessionID, err := hex.DecodeString(*sessionID)
	if err != nil || len(decodedSessionID) != 16 {
		return fmt.Errorf("assistant session identifier is invalid")
	}
	runtimeDir, err := filepath.Abs(*runtimeDirArg)
	if err != nil {
		return fmt.Errorf("resolve runtime directory: %w", err)
	}
	for _, required := range []string{"docker-compose.yml", ".env.production"} {
		if info, statErr := os.Stat(filepath.Join(runtimeDir, required)); statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("runtime is missing required %s", required)
		}
	}
	if err := store.Available(); err != nil {
		return fmt.Errorf("%s. Unlock it, then reconnect the assistant from Ligand-X Launcher", err.Error())
	}
	secret, err := loadAgentSessionSecret(store, *sessionID)
	if err != nil {
		return err
	}
	return agentMCPCommand(runtimeDir, secret, os.Stdin, os.Stdout, os.Stderr).Run()
}
