package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// REL-05 S5a: lease activation fails the pre-lease jobs its stopped workers
// were running, after the worker health barrier, without blocking activation.

func activationHarness(t *testing.T) (string, []string, *[]string, func(bool) error, func([]string, string) error) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), leaseActivationMarkerName)
	if err := os.WriteFile(marker, []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var phases []string
	names := []string{"stop", "gateway-disabled", "workers", "gateway-enabled", "final"}
	calls := 0
	run := func(args []string, _ string) error {
		name := "extra"
		if calls < len(names) {
			name = names[calls]
		}
		calls++
		phases = append(phases, name)
		return nil
	}
	setFlag := func(enabled bool) error {
		phases = append(phases, fmt.Sprintf("leases=%t", enabled))
		return nil
	}
	startArgs := []string{"compose", "--env-file", ".env.production", "-f", "docker-compose.yml", "up", "-d", "--pull=never", "worker-cpu"}
	return marker, startArgs, &phases, setFlag, run
}

func TestActivationFailsInterruptedJobsAfterTheWorkerBarrierWithTheStopTime(t *testing.T) {
	marker, startArgs, phases, setFlag, run := activationHarness(t)
	var cutoff time.Time
	var callPhase []string
	stopDone := time.Time{}
	wrappedRun := func(args []string, message string) error {
		err := run(args, message)
		if len(*phases) > 0 && (*phases)[len(*phases)-1] == "stop" {
			stopDone = time.Now().UTC()
		}
		return err
	}
	before := time.Now().UTC()
	fail := func(at time.Time) error {
		cutoff = at
		callPhase = append([]string(nil), *phases...)
		return nil
	}
	if err := runLeaseActivationSequence(marker, startArgs, startArgs, []string{"worker-cpu"}, []string{"worker-cpu"}, setFlag, wrappedRun, fail, "Starting services..."); err != nil {
		t.Fatal(err)
	}
	if cutoff.IsZero() {
		t.Fatal("interrupted jobs were never failed")
	}
	if cutoff.Before(before) || cutoff.After(stopDone.Add(time.Second)) {
		t.Fatalf("cutoff %v is not the moment the old workers stopped (%v..%v)", cutoff, before, stopDone)
	}
	last := callPhase[len(callPhase)-1]
	if last != "workers" {
		t.Fatalf("interrupted jobs were failed after %q; want right after the worker health barrier (%v)", last, callPhase)
	}
	for _, phase := range callPhase {
		if phase == "leases=true" {
			t.Fatalf("leases were enabled before interrupted jobs were failed: %v", callPhase)
		}
	}
}

func TestActivationContinuesWhenRecoveringInterruptedJobsFails(t *testing.T) {
	marker, startArgs, phases, setFlag, run := activationHarness(t)
	fail := func(time.Time) error { return errors.New("gateway unreachable") }
	if err := runLeaseActivationSequence(marker, startArgs, startArgs, []string{"worker-cpu"}, []string{"worker-cpu"}, setFlag, run, fail, "Starting services..."); err != nil {
		t.Fatalf("a recovery failure must not abort lease activation: %v", err)
	}
	if !strings.Contains(strings.Join(*phases, ","), "leases=true") {
		t.Fatalf("leases were not enabled: %v", *phases)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("activation marker was not cleared")
	}
}

func TestActivationDoesNotFailJobsWhenWorkersNeverBecomeHealthy(t *testing.T) {
	marker, startArgs, phases, setFlag, _ := activationHarness(t)
	called := false
	calls := 0
	run := func(_ []string, _ string) error {
		calls++
		*phases = append(*phases, "call")
		if calls == 3 {
			return errors.New("unhealthy")
		}
		return nil
	}
	fail := func(time.Time) error { called = true; return nil }
	if err := runLeaseActivationSequence(marker, startArgs, startArgs, []string{"worker-cpu"}, []string{"worker-cpu"}, setFlag, run, fail, "Starting services..."); err == nil {
		t.Fatal("expected the unhealthy worker phase to abort activation")
	}
	if called {
		t.Fatal("jobs were failed although the replacement workers never became healthy")
	}
}

func gatewayApp(t *testing.T, handler http.HandlerFunc, secret string) *App {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := fmt.Sprintf("GATEWAY_PORT=%s\nINTERNAL_WORKER_SECRET=%s\n", u.Port(), secret)
	if err := os.WriteFile(filepath.Join(dir, ".env.production"), []byte(env), 0600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.projectPath = dir
	return app
}

func TestFailUpgradeInterruptedJobsPostsTheCutoffWithTheWorkerSecret(t *testing.T) {
	stoppedAt := time.Date(2026, 9, 26, 12, 0, 0, 123, time.UTC)
	var gotPath, gotKey string
	var gotBody map[string]string
	app := gatewayApp(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.Method+" "+r.URL.Path, r.Header.Get("X-Internal-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"failed_job_ids":["a","b"]}`))
	}, "s3cret")
	if err := app.failUpgradeInterruptedJobs(stoppedAt); err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST "+upgradeInterruptedRoute {
		t.Fatalf("called %q", gotPath)
	}
	if gotKey != "s3cret" {
		t.Fatalf("X-Internal-Key = %q", gotKey)
	}
	parsed, err := time.Parse(time.RFC3339Nano, gotBody["workers_replaced_at"])
	if err != nil || !parsed.Equal(stoppedAt) {
		t.Fatalf("workers_replaced_at = %q (%v)", gotBody["workers_replaced_at"], err)
	}
}

func TestFailUpgradeInterruptedJobsToleratesAGatewayWithoutTheRoute(t *testing.T) {
	app := gatewayApp(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, "s3cret")
	if err := app.failUpgradeInterruptedJobs(time.Now()); err != nil {
		t.Fatalf("an older gateway without the route is not an error: %v", err)
	}
}

func TestFailUpgradeInterruptedJobsReportsOtherFailures(t *testing.T) {
	app := gatewayApp(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, "s3cret")
	if err := app.failUpgradeInterruptedJobs(time.Now()); err == nil {
		t.Fatal("a 401 must be reported")
	}
	missing := gatewayApp(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not call the gateway without a worker secret")
	}, "")
	if err := missing.failUpgradeInterruptedJobs(time.Now()); err == nil {
		t.Fatal("a missing worker secret must be reported")
	}
}
