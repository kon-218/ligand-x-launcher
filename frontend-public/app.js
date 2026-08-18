/* Ligand-X public launcher — guided linear flow.
 *
 * Reuses the same Go backend as the dev launcher via the Wails-injected
 * globals window.go.main.App.* and window.runtime.*. No ES-module imports;
 * the runtime binds these on the window object at startup.
 *
 * Flow:  preflight gate  ->  login  ->  license (optional)  ->  services
 *        ->  pull images  ->  running.
 * Returning users (firstRunDone) skip onboarding and land on the running
 * screen in its "ready to start" state.
 */

const App = () => window.go.main.App;
const RT = () => window.runtime;

// ---------- shared state ----------
const state = {
  config: null,          // LauncherConfig
  license: null,         // LicenseSummary
  groups: [],            // []ServiceGroup
  selected: new Set(),   // selected group IDs (excludes implicit required handling)
  statusTimer: null,
  pulling: false,
  proExpanded: false,    // whether the locked-Pro disclosure is open
  pendingRuntimePull: false,
  releaseOnDone: null,
  releaseInstalled: "",
  _orcaPrompt: null,     // { resolve, confirmed } while the ORCA dialog is open
};

const ONBOARDING_STEPS = ["login", "license", "services", "pull"];
const DOCS_FIRST_LAUNCH_URL = "https://www.ligand-x.com/docs/first-launch/";
const DOCKER_INSTALL_URL = "https://docs.docker.com/get-docker/";

function openExternal(url) {
  try { App().OpenBrowser(url); } catch (e) { /* best-effort */ }
}

// ---------- tiny DOM helpers ----------
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));
const el = (id) => document.getElementById(id);

function showScreen(name) {
  $$(".screen").forEach((s) => s.classList.toggle("active", s.dataset.screen === name));
  const onboarding = ONBOARDING_STEPS.includes(name);
  el("steps").hidden = !onboarding;
  if (onboarding) updateSteps(name);
}

function updateSteps(current) {
  const idx = ONBOARDING_STEPS.indexOf(current);
  $$("#steps .step-dot").forEach((dot, i) => {
    dot.classList.toggle("active", i === idx);
    dot.classList.toggle("done", i < idx);
  });
}

function setEdition(edition) {
  const badge = el("editionBadge");
  const label = edition ? edition.charAt(0).toUpperCase() + edition.slice(1) : "Free";
  badge.textContent = label;
  badge.dataset.edition = (edition || "free").toLowerCase();
}

// ---------- boot ----------
window.addEventListener("DOMContentLoaded", () => {
  wireEvents();
  RT().EventsOn("pullProgress", onPullProgress);
  RT().EventsOn("pullComplete", onPullComplete);
  RT().EventsOn("log", onLog);
  boot();
});

async function boot() {
  await preflight(async () => {
    try {
      state.config = await App().GetLauncherConfig();
    } catch (e) {
      state.config = { firstRunDone: false, selectedGroups: [], userProfile: {} };
    }
    try {
      state.license = await App().GetLicenseStatus();
      setEdition(state.license && state.license.edition);
    } catch (e) { /* free by default */ }

    if (state.config && state.config.firstRunDone && state.pendingRuntimePull) {
      state.pendingRuntimePull = false;
      enterServices(true);
    } else if (state.config && state.config.firstRunDone) {
      enterRunning("idle");
    } else {
      showScreen("login");
      const p = state.config && state.config.userProfile;
      if (p) { el("username").value = p.username || ""; el("email").value = p.email || ""; }
    }
  });
}


function normalizeDockerCheck(res) {
  if (Array.isArray(res)) return { ok: !!res[0], message: res[1] || "" };
  if (typeof res === "boolean") return { ok: res, message: "" };
  if (res && typeof res === "object") {
    return { ok: !!(res.ok ?? res.running ?? res.success), message: res.message || "" };
  }
  if (typeof res === "string") return { ok: false, message: res };
  return { ok: false, message: "" };
}

// ---------- preflight gate (docker + runtime) ----------
// Runs before any start. Calls onReady() once Docker is up and the runtime
// bundle (docker-compose.yml + env templates) is installed.
async function preflight(onReady) {
  showScreen("gate");
  gate({ spinner: true, title: "Checking Docker…", msg: "Making sure the container engine is available." });

  let dockerOk = false, dockerMsg = "";
  try {
    const res = await App().CheckDocker();
    ({ ok: dockerOk, message: dockerMsg } = normalizeDockerCheck(res));
  } catch (e) { dockerMsg = String(e); }

  if (!dockerOk) {
    gate({
      icon: "🐳", title: "Docker isn't running",
      msg: dockerMsg || "Start Docker Desktop (or the Docker engine), then try again. If Docker is not installed yet, use Install Docker.",
      action: { label: "Try again", fn: () => preflight(onReady) },
      secondary: { label: "Install Docker", fn: () => openExternal(DOCKER_INSTALL_URL) },
    });
    return;
  }

  // Runtime bundle
  let dist;
  try {
    dist = await App().GetDistributionStatus();
  } catch (e) {
    gate({ icon: "⚠️", title: "Setup check failed", msg: String(e),
      action: { label: "Try again", fn: () => preflight(onReady) } });
    return;
  }

  if (dist && dist.needsInstall) {
    gate({
      icon: "📦", title: "Set up runtime files",
      msg: dist.message || "Ligand-X needs to download its runtime files (~small) before the first launch.",
      action: { label: "Download runtime", fn: () => installRuntime(onReady) },
      secondary: { label: "Choose version", fn: () => openReleaseSelector(onReady, false) },
    });
    return;
  }

  try {
    const upd = await App().CheckForRuntimeUpdate();
    if (upd && upd.updateAvailable) {
      gate({
        icon: "⬆️", title: "Update available",
        msg: upd.message || ("A newer Ligand-X runtime (" + upd.latestVersion + ") is available."),
        action: { label: "Update now", fn: () => installRuntime(onReady, upd.latestVersion, true) },
        secondary: { label: "Choose version", fn: () => openReleaseSelector(onReady, true) },
        tertiary: upd.updateRequired ? null : { label: "Not now", fn: () => onReady() },
      });
      return;
    }
  } catch (e) {
    // Update checking is best-effort: offline or a rate-limited API must not
    // stop someone launching the runtime they already have installed.
    console.warn("update check failed", e);
  }

  onReady();
}

async function installRuntime(onReady, version = "", pullAfter = false) {
  gate({ spinner: true, title: "Downloading runtime…", msg: "This runs once.", log: true });
  clearGateLog();
  try {
    if (version) await App().InstallRuntimeBundleVersion(version);
    else await App().InstallRuntimeBundle();
    state.pendingRuntimePull = pullAfter;
    onReady();
  } catch (e) {
    gate({ icon: "⚠️", title: "Download failed", msg: String(e), log: true,
      action: { label: "Retry", fn: () => installRuntime(onReady, version, pullAfter) } });
  }
}

async function openReleaseSelector(onDone, pullAfter = true) {
  state.releaseOnDone = { fn: onDone, pullAfter };
  const modal = el("releaseModal");
  const list = el("releaseList");
  const install = el("releaseInstall");
  list.textContent = "Loading signed stable releases…";
  el("releaseError").textContent = "";
  install.disabled = true;
  install.dataset.version = "";
  modal.showModal();
  try {
    try {
      const distribution = await App().GetDistributionStatus();
      state.releaseInstalled = distribution.installedVersion || "";
    } catch (e) { state.releaseInstalled = ""; }
    const releases = await App().ListRuntimeReleases();
    list.textContent = "";
    releases.forEach((release) => {
      const label = document.createElement("label");
      label.className = "release-option";
      label.dataset.compatible = release.compatible ? "true" : "false";
      const radio = document.createElement("input");
      radio.type = "radio";
      radio.name = "runtimeRelease";
      radio.value = release.version;
      radio.disabled = !release.compatible;
      radio.onchange = () => {
        install.dataset.version = release.version;
        install.dataset.rollback = compareRuntimeVersions(release.version, state.releaseInstalled) < 0 ? "true" : "false";
        install.disabled = false;
        install.textContent = release.recommended ? "Update to recommended version" : "Use selected version";
      };
      const body = document.createElement("span");
      body.className = "release-option-body";
      const title = document.createElement("strong");
      title.textContent = `${release.version}${release.recommended ? " · Recommended" : ""}`;
      const summary = document.createElement("span");
      summary.textContent = release.summary || release.compatibility || "Supported stable release";
      const meta = document.createElement("small");
      const size = release.downloadBytes ? ` · runtime ${(release.downloadBytes / 1048576).toFixed(1)} MB` : "";
      const rebuilt = release.rebuiltComponents && release.rebuiltComponents.length ? ` · ${release.rebuiltComponents.length} rebuilt component${release.rebuiltComponents.length === 1 ? "" : "s"}` : "";
      meta.textContent = `${release.status || "supported"}${size}${rebuilt}${release.compatible ? "" : " · " + release.compatibility}`;
      body.append(title, summary, meta);
      label.append(radio, body);
      list.appendChild(label);
    });
  } catch (e) {
    list.textContent = "";
    el("releaseError").textContent = String(e).replace(/^Error:\s*/, "");
  }
}

async function installSelectedRelease() {
  const version = el("releaseInstall").dataset.version;
  if (!version) return;
  if (el("releaseInstall").dataset.rollback === "true") {
    const confirmed = window.confirm("Roll back to " + version + "? Ligand-X will refuse if jobs are active and will create a database and scientific-artifact backup before changing runtime files.");
    if (!confirmed) return;
  }
  const continuation = state.releaseOnDone || { fn: () => enterRunning("idle"), pullAfter: true };
  el("releaseModal").close();
  await installRuntime(continuation.fn, version, continuation.pullAfter);
}

function compareRuntimeVersions(left, right) {
  const parse = (value) => String(value || "").replace(/^v/, "").split("-")[0].split(".").map(Number);
  const a = parse(left), b = parse(right);
  if (a.length !== 3 || b.length !== 3 || a.some(Number.isNaN) || b.some(Number.isNaN)) return 0;
  for (let i = 0; i < 3; i++) { if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1; }
  return 0;
}

// gate({ spinner?, icon?, title, msg, action?:{label,fn}, log? })
function gate(opts) {
  el("gateSpinner").hidden = !opts.spinner;
  el("gateIcon").hidden = !opts.icon;
  if (opts.icon) el("gateIcon").textContent = opts.icon;
  el("gateTitle").textContent = opts.title || "";
  el("gateMsg").textContent = opts.msg || "";
  const btn = el("gateAction");
  if (opts.action) {
    btn.hidden = false;
    btn.textContent = opts.action.label;
    btn.onclick = opts.action.fn;
  } else {
    btn.hidden = true;
    btn.onclick = null;
  }
  const secondary = el("gateSecondary");
  if (opts.secondary) {
    secondary.hidden = false;
    secondary.textContent = opts.secondary.label;
    secondary.onclick = opts.secondary.fn;
  } else {
    secondary.hidden = true;
    secondary.onclick = null;
  }
  const tertiary = el("gateTertiary");
  if (opts.tertiary) {
    tertiary.hidden = false;
    tertiary.textContent = opts.tertiary.label;
    tertiary.onclick = opts.tertiary.fn;
  } else {
    tertiary.hidden = true;
    tertiary.onclick = null;
  }
  el("gateLog").hidden = !opts.log;
}
function clearGateLog() { el("gateLog").textContent = ""; }

// ---------- login ----------
async function handleLogin() {
  const username = el("username").value.trim();
  const email = el("email").value.trim();
  const password = el("password").value;
  const errBox = el("loginError");
  errBox.textContent = "";

  if (!username) { errBox.textContent = "Username is required."; return; }
  if (password.length < 8) { errBox.textContent = "Password must be at least 8 characters."; return; }

  const btn = el("loginNext");
  btn.disabled = true; btn.textContent = "Saving…";
  try {
    state.config = await App().SaveLocalAccount(username, email, password);
    enterLicense();
  } catch (e) {
    errBox.textContent = String(e).replace(/^Error:\s*/, "");
  } finally {
    btn.disabled = false; btn.textContent = "Continue";
  }
}

// ---------- license (optional) ----------
async function enterLicense() {
  showScreen("license");
  el("licenseError").textContent = "";
  try {
    state.license = await App().GetLicenseStatus();
  } catch (e) { /* keep prior */ }
  renderLicense();
}

function renderLicense() {
  const lic = state.license || { edition: "free", valid: true };
  setEdition(lic.edition);
  el("licEdition").textContent = (lic.edition || "free").replace(/^\w/, (c) => c.toUpperCase());
  const isLicensed = lic.edition && lic.edition !== "free";
  el("licenseCard").classList.toggle("is-licensed", !!isLicensed);

  const extra = [];
  if (lic.customerName) extra.push(["Licensed to", lic.customerName]);
  if (lic.expiresAt) extra.push(["Expires", lic.expiresAt.slice(0, 10)]);
  if (lic.entitlements && lic.entitlements.length) extra.push(["Modules", lic.entitlements.join(", ")]);
  el("licExtraRows").innerHTML = extra
    .map(([k, v]) => `<div class="license-row"><dt>${esc(k)}</dt><dd>${esc(v)}</dd></div>`)
    .join("");

  el("licenseNext").textContent = isLicensed ? "Continue" : "Continue with Free";
}

async function addLicense() {
  el("licenseError").textContent = "";
  const btn = el("addLicense");
  btn.disabled = true;
  try {
    const lic = await App().SelectLicenseFile();
    if (lic) {
      state.license = lic;
      if (lic.valid === false && lic.reason) {
        el("licenseError").textContent = lic.reason;
      }
      renderLicense();
    }
  } catch (e) {
    const msg = String(e).replace(/^Error:\s*/, "");
    // Ignore plain cancellations from the file dialog.
    if (!/cancel|no file|no such file/i.test(msg)) el("licenseError").textContent = msg;
  } finally {
    btn.disabled = false;
  }
}

// ---------- services ----------
async function enterServices(fromChange) {
  showScreen("services");
  state.proExpanded = false;
  el("svcNext").textContent = "Continue";
  try {
    state.groups = await App().GetServiceGroups();
  } catch (e) {
    state.groups = [];
  }

  // Initialise selection.
  state.selected = new Set();
  const saved = (state.config && state.config.selectedGroups) || [];
  state.groups.forEach((g) => {
    const wasSaved = fromChange && saved.includes(g.id);
    if (g.required || g.defaultOn || wasSaved) {
      if (!g.locked || g.required) state.selected.add(g.id);
    }
  });
  renderServices();
}

// Build one selectable/locked service card.
function svcCard(g) {
  const selected = state.selected.has(g.id) || g.required;
  const item = document.createElement("div");
  item.className = "svc-item" + (selected ? " selected" : "") + (g.locked ? " locked" : "");

  let tag = "";
  if (g.required) tag = `<span class="svc-tag required">Required</span>`;
  else if (g.edition && g.edition !== "free") tag = `<span class="svc-tag pro">${esc(g.edition)}</span>`;

  const sizeTxt = g.sizeMb ? `${(g.sizeMb / 1024).toFixed(g.sizeMb >= 1024 ? 1 : 2)} GB download` : "";
  const unlock = g.locked ? `<div class="svc-unlock">Add a license to unlock</div>` : "";

  item.innerHTML = `
    <div class="svc-check">${selected ? "✓" : ""}</div>
    <div class="svc-body">
      <div class="svc-name">${esc(g.name)}${tag}</div>
      <div class="svc-desc">${esc(g.description || "")}</div>
      ${sizeTxt ? `<div class="svc-size">${sizeTxt}</div>` : ""}
      ${unlock}
    </div>`;

  if (!g.locked && !g.required) {
    item.onclick = async () => {
      if (state.selected.has(g.id)) {
        state.selected.delete(g.id);
        renderServices();
        return;
      }
      if (g.id === "qc" && !(await ensureOrcaForQC())) return;
      state.selected.add(g.id);
      renderServices();
    };
  } else if (g.locked) {
    item.onclick = () => { enterLicense(); };
  }
  return item;
}

function renderServices() {
  const list = el("svcList");
  list.innerHTML = "";

  // Free or already-licensed groups stay in the main list; locked Pro modules
  // are tucked behind a collapsed disclosure so the list stays short.
  const visible = state.groups.filter((g) => !g.locked);
  const lockedPro = state.groups.filter((g) => g.locked);

  visible.forEach((g) => list.appendChild(svcCard(g)));

  if (lockedPro.length) {
    const disc = document.createElement("button");
    disc.type = "button";
    disc.className = "svc-disclosure" + (state.proExpanded ? " open" : "");
    disc.innerHTML = `<span class="svc-disclosure-caret">▸</span>
      <span>Pro modules (${lockedPro.length}) — add a license to unlock</span>`;
    disc.onclick = () => { state.proExpanded = !state.proExpanded; renderServices(); };
    list.appendChild(disc);

    if (state.proExpanded) {
      lockedPro.forEach((g) => list.appendChild(svcCard(g)));
    }
  }
}

function selectedGroupIds() {
  const ids = new Set(state.selected);
  state.groups.forEach((g) => { if (g.required) ids.add(g.id); });
  // Guarantee at least core.
  if (ids.size === 0) ids.add("core");
  return Array.from(ids);
}

// Confirm the service selection: download only the groups whose images aren't
// already present, then land on the running screen in "ready to start" state.
// If everything is already downloaded we skip the pull screen entirely — this
// is what makes "Change services" cheap when nothing new was added.
async function confirmServices() {
  if (selectedGroupIds().includes("qc") && !(await ensureOrcaForQC())) return;

  const ids = selectedGroupIds();
  const btn = el("svcNext");

  let present = {};
  try {
    present = await App().CheckImagePresence();
  } catch (e) { /* treat as nothing present -> pull all */ }

  const missing = ids.filter((id) => present[id] !== true);
  btn.textContent = missing.length ? "Download & continue" : "Continue";

  if (missing.length) {
    startPull(missing); // full selection is persisted on completion
    return;
  }
  await persistSelection();
  enterRunning("idle");
}

// ---------- pull ----------
function startPull(groupIds) {
  showScreen("pull");
  state.pulling = true;
  state._pullGroups = groupIds;
  el("pullError").textContent = "";
  el("pullActions").hidden = true;
  el("pullLog").textContent = "";
  el("pullFill").style.width = "0%";
  el("pullGroup").textContent = "Preparing…";
  el("pullCounter").textContent = "";
  el("pullCaption").textContent = "";
  try {
    App().PullServiceGroups(groupIds); // async on the Go side; progress via events
  } catch (e) {
    pullFailed(String(e));
  }
}

function onPullProgress(p) {
  if (!state.pulling) return;
  if (p.groupName) el("pullGroup").textContent = `Downloading ${p.groupName}`;
  if (p.totalImages) el("pullCounter").textContent = `${p.imageIndex || 0}/${p.totalImages}`;
  const pct = Math.max(0, Math.min(100, p.overallPercent || 0));
  el("pullFill").style.width = pct + "%";
  if (p.currentImage) el("pullCaption").textContent = p.currentImage;
}

async function onPullComplete(res) {
  if (!state.pulling) return;
  state.pulling = false;

  if (res && res.success) {
    el("pullFill").style.width = "100%";
    el("pullGroup").textContent = "Download complete";
    await persistSelection();
    // Land on "ready to start"; the user clicks Start, which routes through the
    // working startFromRunning() -> StartServiceGroups() path.
    enterRunning("idle");
    return;
  }

  // Failure paths.
  const reason = res && res.reason;
  if (reason === "gpu_not_found") {
    // Drop GPU-requiring groups and bounce back to selection.
    pullFailed("Some selected modules need an NVIDIA GPU that wasn't found. Remove them or continue with the rest.");
    return;
  }
  if (reason === "license_required") {
    pullFailed("A selected module requires a license. Add a license, or deselect it.");
    return;
  }
  if (reason === "registry_login_failed") {
    pullFailed("Couldn't authenticate to the image registry. Check your license and connection.");
    return;
  }
  const failed = (res && res.failedGroups) || [];
  pullFailed(failed.length ? `Failed to download: ${failed.join(", ")}.` : "Image download failed.");
}

function pullFailed(msg) {
  state.pulling = false;
  el("pullError").textContent = msg;
  el("pullActions").hidden = false;
}

async function persistSelection() {
  const groupIds = selectedGroupIds();
  try {
    const cfg = Object.assign({}, state.config, {
      firstRunDone: true,
      selectedGroups: groupIds,
    });
    await App().SaveLauncherConfig(cfg);
    state.config = cfg;
  } catch (e) { /* non-fatal: start can still proceed */ }
}

// ---------- running ----------
// sub: "idle" (ready to start), "starting" (just kicked off / starting up).
async function enterRunning(sub) {
  showScreen("running");
  el("runError").textContent = "";
  stopStatusPolling();

  if (sub === "idle") {
    setRunHeader("○", "", "Ready to start", "Click Start services, then Open Ligand-X.");
    el("startBtn").hidden = false;
    el("stopBtn").hidden = true;
    el("openApp").disabled = true;
    el("runTip").hidden = false;
    renderStatusList([]);
    // Reflect any already-running stack.
    refreshStatus();
    return;
  }

  // "starting": services were just (or are being) started.
  el("startBtn").hidden = true;
  el("stopBtn").hidden = false;
  setRunHeader("◐", "starting", "Starting services…", "This can take a minute on first run.");
  startStatusPolling();
}

async function startFromRunning() {
  const groupIds = (state.config && state.config.selectedGroups && state.config.selectedGroups.length)
    ? state.config.selectedGroups : ["core"];
  if (groupIds.includes("qc") && !(await ensureOrcaForQC())) return;

  await preflight(async () => {
    showScreen("running");
    el("startBtn").hidden = true;
    el("stopBtn").hidden = false;
    el("runError").textContent = "";
    setRunHeader("◐", "starting", "Starting services…", "This can take a minute.");
    startStatusPolling();
    try {
      await App().StartServiceGroups("prod", groupIds);
    } catch (e) {
      stopStatusPolling();
      handleStartError(String(e), groupIds);
    }
  });
}

function handleStartError(msg, groupIds) {
  const clean = msg.replace(/^Error:\s*/, "");
  el("runError").textContent = clean;
  setRunHeader("○", "", "Couldn't start", "");
  el("stopBtn").hidden = true;
  // Missing images? Offer a re-download.
  if (/pull|not found|no such image|manifest/i.test(clean)) {
    el("startBtn").hidden = false;
    el("startBtn").textContent = "Re-download images";
    el("startBtn").onclick = () => startPull(groupIds || selectedFallback());
  } else {
    el("startBtn").hidden = false;
    el("startBtn").textContent = "Start services";
    el("startBtn").onclick = startFromRunning;
  }
}

function selectedFallback() {
  return (state.config && state.config.selectedGroups && state.config.selectedGroups.length)
    ? state.config.selectedGroups : ["core"];
}

function setRunHeader(glyph, cls, headline, subline) {
  const pulse = el("runPulse");
  pulse.textContent = glyph;
  pulse.className = "run-pulse" + (cls ? " " + cls : "");
  el("runHeadline").textContent = headline;
  el("runSubline").textContent = subline;
}

function startStatusPolling() {
  stopStatusPolling();
  refreshStatus();
  state.statusTimer = setInterval(refreshStatus, 4000);
}
function stopStatusPolling() {
  if (state.statusTimer) { clearInterval(state.statusTimer); state.statusTimer = null; }
}

async function refreshStatus() {
  let status;
  try {
    status = await App().GetSystemStatus();
  } catch (e) { return; }

  const services = (status && status.services) || [];
  renderStatusList(services);

  const running = (status && status.totalRunning) || 0;
  const total = (status && status.totalServices) || services.length;

  el("openApp").disabled = running === 0;

  if (running > 0 && running >= total && total > 0) {
    setRunHeader("●", "up", "Ligand-X is running", `${running} of ${total} services up.`);
    el("startBtn").hidden = true;
    el("stopBtn").hidden = false;
    el("runTip").hidden = false;
  } else if (running > 0) {
    setRunHeader("◐", "starting", "Starting services…", `${running} of ${total} services up.`);
    el("stopBtn").hidden = false;
    el("runTip").hidden = true;
  } else if (!state.statusTimer) {
    // idle state with nothing running — leave the "ready to start" header.
    el("runTip").hidden = false;
  }
}

function renderStatusList(services) {
  const list = el("svcStatusList");
  if (!services.length) { list.innerHTML = ""; return; }
  list.innerHTML = services.map((s) => {
    let dot = "down", label = s.status || "stopped";
    if (s.running && (s.health === "healthy" || !s.health)) { dot = "running"; label = s.health || "running"; }
    else if (s.running) { dot = "starting"; label = s.health || "starting"; }
    return `<div class="svc-status">
      <span class="dot ${dot}"></span>
      <span class="svc-status-name">${esc(s.name)}</span>
      <span class="svc-status-state">${esc(label)}</span>
    </div>`;
  }).join("");
}

async function stopServices() {
  el("stopBtn").disabled = true;
  el("runError").textContent = "";
  let stopErr = "";
  try {
    await App().StopServices();
  } catch (e) {
    stopErr = String(e).replace(/^Error:\s*/, "");
  } finally {
    el("stopBtn").disabled = false;
  }
  stopStatusPolling();

  if (stopErr) {
    // Don't transition to idle (that clears #runError) — surface the failure and
    // re-reflect the real container state, which likely shows services still up.
    el("runError").textContent = stopErr;
    refreshStatus();
    return;
  }
  enterRunning("idle");
}

// ---------- user modal ----------
function openUserModal() {
  const profile = (state.config && state.config.userProfile) || {};
  const body = el("userModalBody");
  body.textContent = "";

  const rows = [];
  if (profile.username) rows.push(["Username", profile.username]);
  if (profile.email) rows.push(["Email", profile.email]);
  if (!rows.length) rows.push(["Account", "Not set up yet"]);

  const lic = state.license || {};
  const edition = (lic.edition || "free").replace(/^\w/, (c) => c.toUpperCase());
  rows.push(["License", edition + (lic.customerName ? ` — ${lic.customerName}` : "")]);
  if (lic.expiresAt && lic.edition && lic.edition !== "free") {
    rows.push(["Expires", lic.expiresAt.slice(0, 10)]);
  }

  rows.forEach(([k, v]) => {
    const row = document.createElement("div");
    row.className = "license-row";
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    row.appendChild(dt); row.appendChild(dd);
    body.appendChild(row);
  });

  el("changePasswordForm").hidden = true;
  el("changePasswordToggle").textContent = "Change password";
  el("newPassword").value = "";
  el("changePasswordError").textContent = "";
  el("changePasswordSaved").hidden = true;

  const modal = el("userModal");
  modal.showModal();
  modal.focus();
}

async function changePassword() {
  const pwd = el("newPassword").value;
  el("changePasswordError").textContent = "";
  el("changePasswordSaved").hidden = true;
  if (pwd.length < 8) {
    el("changePasswordError").textContent = "Password must be at least 8 characters.";
    return;
  }
  const btn = el("changePasswordSave");
  btn.disabled = true; btn.textContent = "Saving…";
  try {
    await App().UpdatePassword(pwd);
    el("newPassword").value = "";
    el("changePasswordSaved").hidden = false;
  } catch (e) {
    el("changePasswordError").textContent = String(e).replace(/^Error:\s*/, "");
  } finally {
    btn.disabled = false; btn.textContent = "Save new password";
  }
}

// ---------- license modal ----------
function openLicenseModal() {
  el("licenseModalError").textContent = "";
  renderLicenseModal(state.license);
  const modal = el("licenseModal");
  modal.showModal();
  modal.focus();
}

function renderLicenseModal(lic) {
  const l = lic || { edition: "free", valid: true };
  const edition = (l.edition || "free").replace(/^\w/, (c) => c.toUpperCase());
  const rows = [["Edition", edition]];
  if (l.customerName) rows.push(["Licensed to", l.customerName]);
  if (l.licenseId) rows.push(["License ID", l.licenseId]);
  if (l.expiresAt) rows.push(["Expires", l.expiresAt.slice(0, 10)]);
  if (l.graceUntil) rows.push(["Grace until", l.graceUntil.slice(0, 10)]);
  if (l.entitlements && l.entitlements.length) rows.push(["Modules", l.entitlements.join(", ")]);
  if (l.valid === false && l.reason) rows.push(["Status", l.reason]);

  const body = el("licenseModalBody");
  body.textContent = "";
  rows.forEach(([k, v]) => {
    const row = document.createElement("div");
    row.className = "license-row";
    const dt = document.createElement("dt");
    dt.textContent = k;
    const dd = document.createElement("dd");
    dd.textContent = v;
    row.appendChild(dt);
    row.appendChild(dd);
    body.appendChild(row);
  });
}

async function importLicenseFromModal() {
  el("licenseModalError").textContent = "";
  const btn = el("licenseModalImport");
  btn.disabled = true;
  try {
    const lic = await App().SelectLicenseFile();
    // "no_license" reason means the file picker was dismissed — keep previous license.
    if (!lic || lic.reason === "no_license") return;
    if (lic.valid === false && lic.reason) {
      el("licenseModalError").textContent = lic.reason;
    } else {
      state.license = lic;
      setEdition(lic.edition);
      renderLicenseModal(lic);
    }
  } catch (e) {
    const msg = String(e).replace(/^Error:\s*/, "");
    if (!/cancel|no file|no such file/i.test(msg)) el("licenseModalError").textContent = msg;
  } finally {
    btn.disabled = false;
  }
}

// ---------- settings ----------
async function enterSettings() {
  showScreen("settings");
  el("settingsError").textContent = "";
  el("settingsSaved").hidden = true;
  await loadSettings();
  try {
    const dist = await App().GetDistributionStatus();
    el("installedRuntimeVersion").textContent = dist.installedVersion ? `Installed: ${dist.installedVersion}` : "No runtime version is recorded.";
  } catch (e) {
    el("installedRuntimeVersion").textContent = "Installed version could not be read.";
  }
  updateSettingsSections();
}

async function loadSettings() {
  let s;
  try {
    s = await App().GetUserSettings();
  } catch (e) {
    s = { cpuWorkerConcurrency: 4, gpuShortConcurrency: 2, gpuLongConcurrency: 1, orcaHostPath: "", boltzMsaUsername: "", boltzMsaPassword: "", boltzMsaApiKey: "" };
  }
  setSelectValue("cpuConcurrency", s.cpuWorkerConcurrency);
  setSelectValue("gpuShortConcurrency", s.gpuShortConcurrency);
  setSelectValue("gpuLongConcurrency", s.gpuLongConcurrency);
  el("orcaPath").value = s.orcaHostPath || "";
  el("boltzMsaUser").value = s.boltzMsaUsername || "";
  el("boltzMsaPass").value = s.boltzMsaPassword || "";
}

function setSelectValue(id, val) {
  const sel = el(id);
  const str = String(val);
  let best = sel.options[0];
  for (const opt of sel.options) {
    if (opt.value === str) { best = opt; break; }
    if (Math.abs(Number(opt.value) - Number(str)) < Math.abs(Number(best.value) - Number(str))) best = opt;
  }
  sel.value = best.value;
}

function updateSettingsSections() {
  const selected = (state.config && state.config.selectedGroups) || [];
  const hasGPU = selected.some((id) => ["md", "boltz2", "free-energy", "kinetics"].includes(id));
  el("gpuShortField").hidden = !hasGPU;
  el("gpuLongField").hidden = !selected.includes("free-energy");
  el("qcSettings").hidden = !selected.includes("qc");
  el("boltzSettings").hidden = !selected.includes("boltz2");
}

async function saveSettings() {
  el("settingsError").textContent = "";
  el("settingsSaved").hidden = true;
  const btn = el("settingsSave");
  btn.disabled = true;
  btn.textContent = "Saving…";

  const s = {
    cpuWorkerConcurrency: Number(el("cpuConcurrency").value),
    gpuShortConcurrency:  Number(el("gpuShortConcurrency").value),
    gpuLongConcurrency:   Number(el("gpuLongConcurrency").value),
    orcaHostPath:         el("orcaPath").value.trim(),
    boltzMsaUsername:     el("boltzMsaUser").value.trim(),
    boltzMsaPassword:     el("boltzMsaPass").value,
    boltzMsaApiKey:       "",
  };

  const selected = (state.config && state.config.selectedGroups) || [];
  try {
    if (selected.includes("qc")) {
      await App().ValidateOrcaHostPath(s.orcaHostPath);
    }
    await App().SaveUserSettings(s);
    el("settingsSaved").hidden = false;
  } catch (e) {
    el("settingsError").textContent = String(e).replace(/^Error:\s*/, "");
  } finally {
    btn.disabled = false;
    btn.textContent = "Save changes";
  }
}

// ---------- ORCA path prompt ----------
// QC bind-mounts ORCA_HOST_PATH into the container. Skip the dialog when that
// path already contains an orca binary; otherwise require the user to pick one.
async function ensureOrcaForQC() {
  try {
    if (await App().OrcaHostPathReady()) return true;
  } catch (e) { /* treat as not ready */ }
  if (state._orcaPrompt || el("orcaModal").open) return false;
  return promptOrcaPath();
}

function promptOrcaPath() {
  return new Promise((resolve) => {
    state._orcaPrompt = { resolve, confirmed: false };
    el("orcaModalError").textContent = "";
    el("orcaModalPath").value = "";
    (async () => {
      try {
        const s = await App().GetUserSettings();
        el("orcaModalPath").value = s.orcaHostPath || "";
      } catch (e) { /* leave blank */ }
      try {
        const modal = el("orcaModal");
        modal.showModal();
        modal.focus();
      } catch (e) {
        finishOrcaPrompt(false);
      }
    })();
  });
}

function finishOrcaPrompt(ok) {
  const pending = state._orcaPrompt;
  state._orcaPrompt = null;
  if (pending && pending.resolve) pending.resolve(ok);
}

async function browseOrcaModal() {
  el("orcaModalError").textContent = "";
  try {
    const p = await App().BrowseForFolder("Select ORCA Installation Folder");
    if (!p) return;
    el("orcaModalPath").value = p;
    try {
      await App().ValidateOrcaHostPath(p);
    } catch (e) {
      el("orcaModalError").textContent = String(e).replace(/^Error:\s*/, "");
    }
  } catch (e) { /* cancelled */ }
}

async function confirmOrcaModal() {
  const path = el("orcaModalPath").value.trim();
  el("orcaModalError").textContent = "";
  const btn = el("orcaModalConfirm");
  btn.disabled = true;
  try {
    await App().SetOrcaHostPath(path);
    if (el("orcaPath")) el("orcaPath").value = path;
    if (state._orcaPrompt) state._orcaPrompt.confirmed = true;
    el("orcaModal").close();
  } catch (e) {
    el("orcaModalError").textContent = String(e).replace(/^Error:\s*/, "");
  } finally {
    btn.disabled = false;
  }
}

function onOrcaModalClose() {
  const pending = state._orcaPrompt;
  finishOrcaPrompt(!!(pending && pending.confirmed));
}

// ---------- logs (gate + pull) ----------
function onLog(entry) {
  const line = `${entry.timestamp || ""} ${entry.message || ""}`.trim();
  if (state.pulling) appendLog(el("pullLog"), line);
  else if (!el("gateLog").hidden) appendLog(el("gateLog"), line);
}
function appendLog(box, line) {
  const div = document.createElement("div");
  div.className = "log-line";
  div.textContent = line;
  box.appendChild(div);
  while (box.childElementCount > 200) box.removeChild(box.firstChild);
  box.scrollTop = box.scrollHeight;
}

// ---------- wiring ----------
function wireEvents() {
  el("releaseModalClose").onclick = () => el("releaseModal").close();
  el("releaseModal").addEventListener("click", (e) => { if (e.target === el("releaseModal")) el("releaseModal").close(); });
  el("releaseInstall").onclick = installSelectedRelease;
  el("editionBadge").onclick = openLicenseModal;
  el("licenseModalClose").onclick = () => el("licenseModal").close();
  el("licenseModal").addEventListener("click", (e) => { if (e.target === el("licenseModal")) el("licenseModal").close(); });
  el("licenseModalImport").onclick = importLicenseFromModal;
  el("orcaModalClose").onclick = () => el("orcaModal").close();
  el("orcaModal").addEventListener("click", (e) => { if (e.target === el("orcaModal")) el("orcaModal").close(); });
  el("orcaModal").addEventListener("close", onOrcaModalClose);
  el("orcaModalBrowse").onclick = browseOrcaModal;
  el("orcaModalConfirm").onclick = confirmOrcaModal;
  el("loginNext").onclick = handleLogin;
  el("password").addEventListener("keydown", (e) => { if (e.key === "Enter") handleLogin(); });

  el("addLicense").onclick = addLicense;
  el("licenseNext").onclick = () => enterServices(false);

  el("svcBack").onclick = () => enterLicense();
  el("svcNext").onclick = confirmServices;

  el("pullBack").onclick = () => enterServices(true);
  el("pullRetry").onclick = () => startPull(state._pullGroups || selectedGroupIds());

  el("openApp").onclick = () => { try { App().OpenFrontend(); } catch (e) {} };
  el("startBtn").onclick = startFromRunning;
  el("stopBtn").onclick = stopServices;
  el("changeServices").onclick = () => enterServices(true);
  el("helpBtn").onclick = () => openExternal(DOCS_FIRST_LAUNCH_URL);
  el("openDocs").onclick = () => openExternal(DOCS_FIRST_LAUNCH_URL);
  el("userBtn").onclick = openUserModal;
  el("userModalClose").onclick = () => el("userModal").close();
  el("userModal").addEventListener("click", (e) => { if (e.target === el("userModal")) el("userModal").close(); });
  el("changePasswordToggle").onclick = () => {
    const form = el("changePasswordForm");
    form.hidden = !form.hidden;
    el("changePasswordToggle").textContent = form.hidden ? "Change password" : "Cancel";
  };
  el("changePasswordSave").onclick = changePassword;
  el("newPassword").addEventListener("keydown", (e) => { if (e.key === "Enter") changePassword(); });
  el("openSettings").onclick = enterSettings;
  el("settingsBackTop").onclick = () => enterRunning("idle");
  el("settingsSave").onclick = saveSettings;
  el("chooseRuntimeVersion").onclick = () => openReleaseSelector(() => enterServices(true), true);
  el("resetResources").onclick = resetResourceLimits;
  el("openUninstall").onclick = openUninstallModal;
  el("uninstallModalClose").onclick = () => el("uninstallModal").close();
  // Typing the phrase is the only thing that arms the button, mirroring the
  // check the Go side enforces independently.
  el("uninstallConfirm").oninput = () => {
    el("uninstallGo").disabled = el("uninstallConfirm").value.trim() !== "UNINSTALL";
  };
  el("uninstallGo").onclick = runUninstall;
  el("uninstallQuit").onclick = () => { try { window.runtime.Quit(); } catch (e) {} };
  el("browseOrca").onclick = async () => {
    try {
      const p = await App().BrowseForFolder("Select ORCA Installation Folder");
      if (p) el("orcaPath").value = p;
    } catch (e) { /* cancelled */ }
  };
}

// ---------- troubleshooting ----------
async function resetResourceLimits() {
  const btn = el("resetResources");
  el("settingsError").textContent = "";
  el("resetResourcesMsg").hidden = true;
  btn.disabled = true;
  try {
    const summary = await App().ResetResourceLimits();
    el("resetResourcesMsg").textContent = summary || "Resource limits reset.";
    el("resetResourcesMsg").hidden = false;
  } catch (e) {
    el("settingsError").textContent = String(e).replace(/^Error:\s*/, "");
  } finally {
    btn.disabled = false;
  }
}

// ---------- uninstall ----------
function openUninstallModal() {
  el("uninstallPrompt").hidden = false;
  el("uninstallResult").hidden = true;
  el("uninstallError").textContent = "";
  el("uninstallConfirm").value = "";
  el("uninstallGo").hidden = false;
  el("uninstallGo").disabled = true;
  el("uninstallGo").textContent = "Uninstall";
  el("uninstallQuit").hidden = true;
  for (const id of ["uninstallKeepData", "uninstallKeepImages", "uninstallKeepLauncher"]) {
    el(id).checked = false;
  }
  el("uninstallModal").showModal();
}

async function runUninstall() {
  const btn = el("uninstallGo");
  el("uninstallError").textContent = "";
  btn.disabled = true;
  btn.textContent = "Uninstalling…";
  el("uninstallModalClose").hidden = true;

  try {
    const report = await App().Uninstall({
      confirm: el("uninstallConfirm").value.trim(),
      keepData: el("uninstallKeepData").checked,
      keepImages: el("uninstallKeepImages").checked,
      keepLauncher: el("uninstallKeepLauncher").checked,
    });
    renderUninstallReport(report);
  } catch (e) {
    el("uninstallError").textContent = String(e).replace(/^Error:\s*/, "");
    btn.disabled = false;
    btn.textContent = "Uninstall";
    el("uninstallModalClose").hidden = false;
  }
}

function renderUninstallReport(report) {
  const icons = { done: "✓", skipped: "–", failed: "✕" };
  const steps = (report && report.steps) || [];
  el("uninstallSteps").innerHTML = steps.map((s) =>
    `<div class="uninstall-step" data-status="${esc(s.status)}">` +
    `<span class="uninstall-step-icon">${icons[s.status] || "•"}</span>` +
    `<span><strong>${esc(s.name)}</strong> — ${esc(s.detail)}</span></div>`).join("");

  const manual = (report && report.manualSteps) || [];
  el("uninstallManual").innerHTML = manual.length
    ? "<p style='margin-top:14px'>Left for you to finish:</p><ul>" +
      manual.map((m) => `<li>${esc(m)}</li>`).join("") + "</ul>"
    : "";

  el("uninstallPrompt").hidden = true;
  el("uninstallResult").hidden = false;
  el("uninstallGo").hidden = true;
  el("uninstallQuit").hidden = false;
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
