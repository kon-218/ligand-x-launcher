package launcher

import (
	"ligandx-launcher/internal/envfile"
	"os"
	"strconv"
	"strings"
	"testing"
)

// B-251: a manual install is opened through the bundled reverse proxy on
// APP_PORT, and the template's explicit CORS_ORIGINS overrides the gateway's
// narrower code default. Both loopback aliases for the default port have to be
// listed — a browser at http://127.0.0.1:8080 is a different origin from
// http://localhost:8080 as far as the WebSocket gate is concerned.

const defaultManualAPPPort = 8080

func readShippedTemplate(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../runtime/.env.production.template")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestManualRuntimeTemplateAllowsBothDefaultLoopbackOrigins(t *testing.T) {
	origins := splitOrigins(t, envfile.Parse(readShippedTemplate(t))["CORS_ORIGINS"])

	for _, want := range []string{
		"http://localhost:" + strconv.Itoa(defaultManualAPPPort),
		"http://127.0.0.1:" + strconv.Itoa(defaultManualAPPPort),
	} {
		if !hasOrigin(origins, want) {
			t.Errorf("CORS_ORIGINS is missing %s; the shipped list is %v", want, origins)
		}
	}
}

// TestManualRuntimeTemplateKeepsItsOtherOrigins guards the other half of the
// change: adding the missing alias must not drop or rewrite an existing origin.
func TestManualRuntimeTemplateKeepsItsOtherOrigins(t *testing.T) {
	origins := splitOrigins(t, envfile.Parse(readShippedTemplate(t))["CORS_ORIGINS"])

	for _, want := range []string{"http://localhost:3000", "http://127.0.0.1:3000"} {
		if !hasOrigin(origins, want) {
			t.Errorf("CORS_ORIGINS lost %s; the shipped list is %v", want, origins)
		}
	}
	for _, origin := range origins {
		if strings.Contains(origin, "*") {
			t.Errorf("CORS_ORIGINS carries a wildcard origin %q", origin)
		}
	}
}

func splitOrigins(t *testing.T, raw string) []string {
	t.Helper()
	if raw == "" {
		t.Fatal("the shipped template has no CORS_ORIGINS value")
	}
	var origins []string
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			origins = append(origins, entry)
		}
	}
	return origins
}

func hasOrigin(origins []string, want string) bool {
	for _, origin := range origins {
		if origin == want {
			return true
		}
	}
	return false
}
