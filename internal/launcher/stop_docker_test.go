package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// TestStopProjectContainersOnRealDocker drives the launcher's stop against the
// Docker daemon, with stand-in containers that record when they were told to
// stop and when they finished. It is opt-in (LIGANDX_DOCKER_TEST=1) because it
// needs a daemon and a local alpine image and takes about 40 seconds.
//
// The stand-ins live in their own compose project, passed explicitly, so a
// real Ligand-X stack on the same machine is never touched.
func TestStopProjectContainersOnRealDocker(t *testing.T) {
	if os.Getenv("LIGANDX_DOCKER_TEST") != "1" {
		t.Skip("set LIGANDX_DOCKER_TEST=1 to run against a Docker daemon")
	}
	image := os.Getenv("LIGANDX_DOCKER_TEST_IMAGE")
	if image == "" {
		image = "alpine:3.21"
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	project := fmt.Sprintf("lxstoptest%d", os.Getpid())
	out := t.TempDir()
	if err := os.Chmod(out, 0o777); err != nil {
		t.Fatal(err)
	}
	stated := func(seconds int) *int { return &seconds }
	// service -> how long it keeps working after SIGTERM, and the period it states.
	standIns := []struct {
		service string
		busy    int
		stop    *int
	}{
		{"worker-gpu-long", 35, stated(45)}, // longer than the 30s floor: only its own period saves it
		{"worker-cpu", 15, nil},             // longer than Docker's 10s default: only the floor saves it
		{"gateway", 0, stated(30)},
		{"postgres", 0, stated(60)},
		{"redis", 0, stated(30)},
	}
	var ids []string
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = cli.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
		}
	})
	for _, s := range standIns {
		script := fmt.Sprintf(
			`trap 'date +%%s > /out/%[1]s.asked; sleep %[2]d; date +%%s > /out/%[1]s.done; exit 0' TERM; touch /out/%[1]s.up; while :; do sleep 1; done`,
			s.service, s.busy,
		)
		created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
			Name: project + "-" + s.service,
			Config: &container.Config{
				Image: image, Entrypoint: []string{"sh", "-c", script}, StopTimeout: s.stop,
				Labels: map[string]string{
					"com.docker.compose.project": project,
					"com.docker.compose.service": s.service,
				},
			},
			HostConfig: &container.HostConfig{Binds: []string{out + ":/out"}},
		})
		if err != nil {
			t.Fatalf("create %s: %v", s.service, err)
		}
		ids = append(ids, created.ID)
		if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatalf("start %s: %v", s.service, err)
		}
	}
	for _, s := range standIns {
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(out, s.service+".up")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never came up", s.service)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	failed := stopProjectContainers(ctx, cli, listed.Items, map[string]bool{project: true}, func(msg string) { t.Log(msg) })
	took := time.Since(began)
	if len(failed) != 0 {
		t.Fatalf("could not stop %v", failed)
	}

	stamp := func(service, kind string) int64 {
		raw, err := os.ReadFile(filepath.Join(out, service+"."+kind))
		if err != nil {
			t.Fatalf("%s was killed before it finished (%s not written): %v", service, kind, err)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	// Every stand-in finished its work: none was killed.
	workersDone := stamp("worker-gpu-long", "done")
	if cpu := stamp("worker-cpu", "done"); cpu > workersDone {
		workersDone = cpu
	}
	// The gateway is asked only once the workers are done, the stores only
	// once the gateway is.
	if asked := stamp("gateway", "asked"); asked < workersDone {
		t.Fatalf("gateway asked to stop at %d, before the workers finished at %d", asked, workersDone)
	}
	gatewayDone := stamp("gateway", "done")
	for _, store := range []string{"postgres", "redis"} {
		if asked := stamp(store, "asked"); asked < gatewayDone {
			t.Fatalf("%s asked to stop at %d, before the gateway finished at %d", store, asked, gatewayDone)
		}
		stamp(store, "done")
	}
	// The two workers stopped together (35s), not one after the other (50s).
	if took > 48*time.Second {
		t.Fatalf("stop took %s: the workers were not stopped together", took.Round(time.Second))
	}
	remaining, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range remaining.Items {
		if c.Labels["com.docker.compose.project"] == project {
			t.Fatalf("%v was left behind", c.Names)
		}
	}
	t.Logf("stopped five containers in %s", took.Round(time.Second))
}

// TestStopNamedProjectOnRealDocker stops one existing compose project, named
// by LIGANDX_DOCKER_TEST_PROJECT, with the launcher's own stop. It is for
// running the stop against a throwaway copy of the real product stack; the
// order and exit codes are read from `docker events` by whoever runs it.
func TestStopNamedProjectOnRealDocker(t *testing.T) {
	project := os.Getenv("LIGANDX_DOCKER_TEST_PROJECT")
	if project == "" {
		t.Skip("set LIGANDX_DOCKER_TEST_PROJECT to the compose project to stop")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopBudgetSeconds*time.Second)
	defer cancel()
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	projects := map[string]bool{project: true}
	if len(stopWaves(listed.Items, projects)) == 0 {
		t.Fatalf("no containers found for project %q", project)
	}
	began := time.Now()
	if failed := stopProjectContainers(ctx, cli, listed.Items, projects, func(msg string) { t.Log(msg) }); len(failed) != 0 {
		t.Fatalf("could not stop %v", failed)
	}
	remaining, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range remaining.Items {
		if c.Labels["com.docker.compose.project"] == project {
			t.Fatalf("%v was left behind", c.Names)
		}
	}
	t.Logf("stopped project %s in %s", project, time.Since(began).Round(time.Second))
}
