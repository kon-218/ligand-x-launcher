const test = require("node:test");
const assert = require("node:assert/strict");
const S = require("../../frontend-public/state.js");

const svc = (name, running, health) => ({ name, running, health, status: running ? "running" : "exited" });

test("deriveStack: nothing running is stopped", () => {
  assert.equal(S.deriveStack({ services: [], totalRunning: 0, totalServices: 5 }, null), "stopped");
  assert.equal(S.deriveStack(null, null), "stopped");
});

test("deriveStack: an in-flight action wins over the container snapshot", () => {
  const up = { services: [svc("gateway", true, "healthy")], totalRunning: 1, totalServices: 1 };
  assert.equal(S.deriveStack(up, "stopping"), "stopping");
  assert.equal(S.deriveStack({ totalRunning: 0, totalServices: 3 }, "starting"), "starting");
  assert.equal(S.deriveStack(up, "restarting"), "starting");
});

test("deriveStack: a stack found already running is running, not 'ready to start'", () => {
  const status = { services: [svc("gateway", true, "healthy"), svc("redis", true, "")], totalRunning: 2, totalServices: 2 };
  assert.equal(S.deriveStack(status, null), "running");
});

test("deriveStack: health checks still warming up is starting", () => {
  const status = { services: [svc("gateway", true, "starting"), svc("redis", true, "healthy")], totalRunning: 2, totalServices: 2 };
  assert.equal(S.deriveStack(status, null), "starting");
});

test("deriveStack: a missing or unhealthy service is degraded", () => {
  const missing = { services: [svc("gateway", true, "healthy"), svc("md", false, "")], totalRunning: 1, totalServices: 2 };
  assert.equal(S.deriveStack(missing, null), "degraded");
  const sick = { services: [svc("gateway", true, "unhealthy")], totalRunning: 1, totalServices: 1 };
  assert.equal(S.deriveStack(sick, null), "degraded");
});

test("serviceState: unhealthy shows as down, not as running", () => {
  assert.deepEqual(S.serviceState(svc("gateway", true, "unhealthy")), { dot: "down", label: "unhealthy" });
  assert.deepEqual(S.serviceState(svc("gateway", true, "healthy")), { dot: "running", label: "healthy" });
  assert.deepEqual(S.serviceState(svc("gateway", true, "")), { dot: "running", label: "running" });
  assert.deepEqual(S.serviceState(svc("gateway", false, "")), { dot: "down", label: "exited" });
});

test("pullFailureMessage: every backend reason has a specific message", () => {
  for (const reason of ["cancelled", "gpu_not_found", "assets_unavailable", "license_required",
    "registry_login_failed", "insufficient_disk", "version_not_pinned"]) {
    const msg = S.pullFailureMessage({ reason, failedGroups: ["core"] });
    assert.ok(!msg.startsWith("Failed to download"), `${reason} fell through to the generic message`);
  }
});

test("pullFailureMessage: a backend message wins, unknown reasons name the groups", () => {
  assert.equal(S.pullFailureMessage({ reason: "insufficient_disk", message: "only 2 GB free on /var" }), "only 2 GB free on /var");
  assert.equal(S.pullFailureMessage({ failedGroups: ["md", "qc"] }), "Failed to download: md, qc.");
  assert.equal(S.pullFailureMessage(null), "Image download failed.");
});

test("mergeSettings: a field the form does not show survives a save", () => {
  const loaded = { cpuWorkerConcurrency: 4, boltzMsaApiKey: "secret-key", boltzMsaUsername: "" };
  const merged = S.mergeSettings(loaded, { cpuWorkerConcurrency: 8, boltzMsaUsername: "me" });
  assert.equal(merged.boltzMsaApiKey, "secret-key");
  assert.equal(merged.cpuWorkerConcurrency, 8);
  assert.equal(merged.boltzMsaUsername, "me");
  assert.equal(loaded.cpuWorkerConcurrency, 4, "the loaded object is not mutated");
});

test("sameSelection ignores order", () => {
  assert.ok(S.sameSelection(["core", "md"], ["md", "core"]));
  assert.ok(!S.sameSelection(["core"], ["core", "md"]));
  assert.ok(S.sameSelection(null, []));
});

test("shouldShowUpdateBanner: dismissed optional updates stay quiet until a newer one", () => {
  const upd = { updateAvailable: true, updateRequired: false, latestVersion: "v1.2.0" };
  assert.ok(S.shouldShowUpdateBanner(upd, ""));
  assert.ok(!S.shouldShowUpdateBanner(upd, "v1.2.0"));
  assert.ok(S.shouldShowUpdateBanner(Object.assign({}, upd, { latestVersion: "v1.3.0" }), "v1.2.0"));
  assert.ok(S.shouldShowUpdateBanner(Object.assign({}, upd, { updateRequired: true }), "v1.2.0"));
  assert.ok(!S.shouldShowUpdateBanner({ updateAvailable: false }, ""));
  assert.ok(!S.shouldShowUpdateBanner(null, ""));
});

test("storageSummary: nothing to download, a fit, a warning and a block", () => {
  assert.equal(S.storageSummary({ downloadBytes: 0 }).text, "Everything selected is already downloaded.");
  const fit = S.storageSummary({ downloadBytes: 14 * 2 ** 30, freeKnown: true, freeBytes: 93 * 2 ** 30 });
  assert.equal(fit.text, "Download about 14 GB · 93 GB free.");
  assert.equal(fit.level, "");
  assert.equal(S.storageSummary({ downloadBytes: 2 ** 30, freeKnown: false }).text, "Download about 1.0 GB.");
  assert.equal(S.storageSummary({ downloadBytes: 2 ** 30, warning: "Low disk space" }).level, "warn");
  assert.deepEqual(S.storageSummary({ downloadBytes: 2 ** 30, blocked: "not enough free disk space" }),
    { text: "not enough free disk space", level: "error" });
});
