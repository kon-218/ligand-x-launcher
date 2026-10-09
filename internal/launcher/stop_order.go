package launcher

import (
	"sort"

	"github.com/moby/moby/api/types/container"
)

// minimumStopTimeoutSeconds is what Stop has always given a container. It
// stays the floor, so a stack created from an older Compose snapshot, whose
// containers state no period of their own, is stopped no more abruptly than
// before.
const minimumStopTimeoutSeconds = 30

// stopBudgetSeconds bounds one Stop: the longest stated period of each wave
// (workers 120s, gateway 30s, stores 60s) plus room for listing and removal.
const stopBudgetSeconds = 300

// stopWave orders a container's stop by what its service still needs running.
// Workers and services stop first: a worker finishing its task still has the
// gateway to report to. The gateway stops next, while the stores it writes to
// are still up. The stores stop last.
func stopWave(service string) int {
	switch service {
	case "postgres", "redis", "rabbitmq":
		return 2
	case "gateway":
		return 1
	default:
		return 0
	}
}

// stopWaves groups the containers of the given compose projects into the
// waves Stop works through, in order. Each wave is sorted by container ID so
// the plan is the same on every run.
func stopWaves(containers []container.Summary, projects map[string]bool) [][]container.Summary {
	waves := make([][]container.Summary, 3)
	for _, c := range containers {
		if !projects[c.Labels["com.docker.compose.project"]] {
			continue
		}
		wave := stopWave(c.Labels["com.docker.compose.service"])
		waves[wave] = append(waves[wave], c)
	}
	plan := make([][]container.Summary, 0, len(waves))
	for _, wave := range waves {
		if len(wave) == 0 {
			continue
		}
		sort.Slice(wave, func(i, j int) bool { return wave[i].ID < wave[j].ID })
		plan = append(plan, wave)
	}
	return plan
}

// stopTimeoutFor decides the timeout Stop passes for one container, given the
// period the container itself states (Compose's stop_grace_period; nil when
// it states none). A stated period at or above the floor is left to Docker by
// passing nil, so the runtime Compose file stays the one place that sets it.
func stopTimeoutFor(configured *int) *int {
	if configured != nil && *configured >= minimumStopTimeoutSeconds {
		return nil
	}
	floor := minimumStopTimeoutSeconds
	return &floor
}
