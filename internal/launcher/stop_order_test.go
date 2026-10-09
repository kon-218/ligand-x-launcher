package launcher

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func stackContainer(id, project, service string) container.Summary {
	return container.Summary{ID: id, Labels: map[string]string{
		"com.docker.compose.project": project,
		"com.docker.compose.service": service,
	}}
}

func waveServices(wave []container.Summary) []string {
	names := make([]string, 0, len(wave))
	for _, c := range wave {
		names = append(names, c.Labels["com.docker.compose.service"])
	}
	return names
}

func TestStopWavesStopWorkersThenGatewayThenStores(t *testing.T) {
	containers := []container.Summary{
		stackContainer("7", "ligandx", "postgres"),
		stackContainer("3", "ligandx", "gateway"),
		stackContainer("5", "ligandx", "worker-cpu"),
		stackContainer("1", "ligandx", "redis"),
		stackContainer("4", "ligandx", "celery-beat"),
		stackContainer("2", "ligandx", "rabbitmq"),
		stackContainer("6", "ligandx", "docking"),
		stackContainer("9", "someone-elses", "postgres"),
	}
	plan := stopWaves(containers, map[string]bool{"ligandx": true})
	if len(plan) != 3 {
		t.Fatalf("want 3 waves, got %d: %v", len(plan), plan)
	}
	want := [][]string{
		{"celery-beat", "worker-cpu", "docking"},
		{"gateway"},
		{"redis", "rabbitmq", "postgres"},
	}
	for i, wave := range plan {
		got := waveServices(wave)
		if len(got) != len(want[i]) {
			t.Fatalf("wave %d: want %v, got %v", i, want[i], got)
		}
		for j := range got {
			if got[j] != want[i][j] {
				t.Fatalf("wave %d: want %v, got %v", i, want[i], got)
			}
		}
	}
}

func TestStopWavesLeaveOtherProjectsAlone(t *testing.T) {
	plan := stopWaves(
		[]container.Summary{stackContainer("1", "someone-elses", "postgres")},
		map[string]bool{"ligandx": true},
	)
	if len(plan) != 0 {
		t.Fatalf("a container outside our projects was planned for stopping: %v", plan)
	}
}

func TestStopWavesOmitEmptyWaves(t *testing.T) {
	plan := stopWaves(
		[]container.Summary{stackContainer("1", "ligandx", "postgres")},
		map[string]bool{"ligandx": true},
	)
	if len(plan) != 1 || waveServices(plan[0])[0] != "postgres" {
		t.Fatalf("want one wave holding postgres, got %v", plan)
	}
}

func TestStopTimeoutLeavesAStatedPeriodToDocker(t *testing.T) {
	for _, stated := range []int{30, 60, 120} {
		if got := stopTimeoutFor(&stated); got != nil {
			t.Fatalf("stated %ds: want nil so Docker uses the container's own period, got %d", stated, *got)
		}
	}
}

func TestStopTimeoutKeepsTheFloorForOlderStacks(t *testing.T) {
	short := 10
	for name, stated := range map[string]*int{"unset": nil, "below the floor": &short} {
		got := stopTimeoutFor(stated)
		if got == nil || *got != minimumStopTimeoutSeconds {
			t.Fatalf("%s: want the %ds floor, got %v", name, minimumStopTimeoutSeconds, got)
		}
	}
}

// The shipped Compose snapshot must state the periods Stop relies on, and one
// Stop must fit inside its budget when every wave runs to its limit.
func TestRuntimeSnapshotStatesStopGraceWithinTheBudget(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "runtime", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	service := regexp.MustCompile(`^  ([a-z0-9-]+):\s*$`)
	grace := regexp.MustCompile(`^    stop_grace_period: (\d+)s\s*$`)
	stated := map[string]int{}
	current := ""
	for _, line := range regexp.MustCompile(`\r?\n`).Split(string(raw), -1) {
		if m := service.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if m := grace.FindStringSubmatch(line); m != nil && current != "" {
			seconds, _ := strconv.Atoi(m[1])
			stated[current] = seconds
		}
	}
	longest := make([]int, 3)
	for _, name := range []string{"gateway", "postgres", "rabbitmq", "redis", "worker-cpu", "worker-control"} {
		seconds, ok := stated[name]
		if !ok {
			t.Fatalf("%s states no stop_grace_period in the runtime snapshot", name)
		}
		if seconds < minimumStopTimeoutSeconds {
			t.Fatalf("%s states %ds, below the %ds Stop has always given", name, seconds, minimumStopTimeoutSeconds)
		}
	}
	for name, seconds := range stated {
		if wave := stopWave(name); seconds > longest[wave] {
			longest[wave] = seconds
		}
	}
	if total := longest[0] + longest[1] + longest[2]; total >= stopBudgetSeconds {
		t.Fatalf("waves can take %ds, which does not fit the %ds Stop budget", total, stopBudgetSeconds)
	}
}
