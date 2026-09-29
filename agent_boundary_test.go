package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Both desktop variants must remain installers/service managers; connection
// credentials and the MCP process belong to the application gateway.
func TestLauncherDoesNotOwnAgentConnections(t *testing.T) {
	appType := reflect.TypeOf((*App)(nil))
	for _, method := range []string{"CreateAgentSetup", "CreateAgentSetupWithExecution", "ListAgentSessions", "GetAgentStorageStatus", "CopyAgentSessionConfig", "CheckAgentSessionHealth", "RevokeAgentSession", "RevokeAgentAccess"} {
		if _, exists := appType.MethodByName(method); exists {
			t.Errorf("launcher still exposes agent method %s", method)
		}
	}
	for _, file := range []string{"frontend-public/index.html", "frontend-public/app.js", "frontend/index.html", "frontend/app.js", "main.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"connectAssistant", "revokeAssistant", "assistantSession", "CreateAgentSetup", "ListAgentSessions", "agent-mcp", "internal/agentsession"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s still contains %s", file, forbidden)
			}
		}
	}
}
