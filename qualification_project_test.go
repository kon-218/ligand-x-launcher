package main

import (
	"strings"
	"testing"
)

func TestQualificationProjectIsolation(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "other-session")
	a := NewApp()
	if a.composeProject() != "ligand-x" {
		t.Fatal("production default changed")
	}
	a.composeProjectName = "lx-qualification-123"
	env := strings.Join(a.composeEnv(), "\n")
	if strings.Count(env, "COMPOSE_PROJECT_NAME=") != 1 || !strings.Contains(env, "COMPOSE_PROJECT_NAME=lx-qualification-123") {
		t.Fatal(env)
	}
	if !strings.Contains(env, "LIGANDX_INFRA_CONTAINER_PREFIX=lx-qualification-123") {
		t.Fatal("infrastructure names must be isolated")
	}
	if !strings.Contains(a.composeContextLine(), "COMPOSE_PROJECT_NAME=lx-qualification-123") {
		t.Fatal(a.composeContextLine())
	}
	if a.infraContainerName("postgres") != "lx-qualification-123-postgres" {
		t.Fatal("credential repair can reach another installation")
	}
	if !a.ownsComposeProject("lx-qualification-123") || a.ownsComposeProject("ligand-x") {
		t.Fatal("container/status queries must be scoped")
	}
}

func TestQualificationComposeOverridePinsEveryCommand(t *testing.T) {
	a := NewApp()
	a.composeOverrideFiles = []string{"qualification.json"}
	for _, verb := range []string{"up", "stop", "down", "config", "ps", "restart"} {
		args := a.scopedComposeArgs([]string{"compose", "-f", "docker-compose.yml", verb})
		if strings.Join(args, " ") != "compose -f docker-compose.yml -f qualification.json "+verb {
			t.Fatal(args)
		}
		if len(a.scopedComposeArgs(args)) != len(args) {
			t.Fatal("override duplicated")
		}
	}
}

func TestQualificationUninstallOwnedPaths(t *testing.T) {
	a := NewApp()
	a.composeProjectName = "lx-qualification-test"
	root := t.TempDir()
	config := root + "/config"
	runtime := root + "/runtime"
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", config)
	t.Setenv("LIGANDX_RUNTIME_DIR", runtime)
	paths := a.launcherOwnedPaths()
	if len(paths) != 2 || paths[0] != config || paths[1] != runtime {
		t.Fatalf("uninstall can reach production files: %v", paths)
	}
}
