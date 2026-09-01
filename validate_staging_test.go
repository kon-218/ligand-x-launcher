package main

import (
	"os"
	"strings"
	"testing"
)

// rabbitmq/redis/postgres/flower/proxy pin container_name so a local override
// stack can share them with the real install. That defeats
// `docker compose --project-name` isolation, which is exactly what candidate
// validation (and `make validate-staging`) needs when a production stack is
// already running. The generated snapshot must keep the prefix overridable,
// and the staging script must actually set it from COMPOSE_PROJECT_NAME.
func TestValidateStagingIsolatesInfraContainerNames(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rabbitmq", "redis", "postgres", "flower", "proxy"} {
		want := "${LIGANDX_INFRA_CONTAINER_PREFIX:-ligandx}-" + name
		if !strings.Contains(string(compose), "container_name: "+want) {
			t.Errorf("docker-compose.yml: %s must use overridable container_name %s", name, want)
		}
		if strings.Contains(string(compose), "container_name: ligandx-"+name+"\n") {
			t.Errorf("docker-compose.yml: %s still has a hardcoded ligandx-%s container_name", name, name)
		}
	}

	script, err := os.ReadFile("scripts/validate-staging-startup.sh")
	if err != nil {
		t.Fatal(err)
	}
	const exportLine = `export LIGANDX_INFRA_CONTAINER_PREFIX="${LIGANDX_INFRA_CONTAINER_PREFIX:-$COMPOSE_PROJECT_NAME}"`
	if !strings.Contains(string(script), exportLine) {
		t.Fatal("validate-staging-startup.sh must export LIGANDX_INFRA_CONTAINER_PREFIX from COMPOSE_PROJECT_NAME so an isolated stack can start next to a running install")
	}
}
