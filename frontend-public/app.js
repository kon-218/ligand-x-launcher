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
};

const ONBOARDING_STEPS = ["login", "license", "services", "pull"];

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

    if (state.config && state.config.firstRunDone) {
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
      msg: dockerMsg || "Start Docker Desktop (or the Docker engine), then try again.",
      action: { label: "Try again", fn: () => preflight(onReady) },
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
    });
    return;
  }

  onReady();
}

async function installRuntime(onReady) {
  gate({ spinner: true, title: "Downloading runtime…", msg: "This runs once.", log: true });
  clearGateLog();
  try {
    await App().InstallRuntimeBundle();
    onReady();
  } catch (e) {
    gate({ icon: "⚠️", title: "Download failed", msg: String(e), log: true,
      action: { label: "Retry", fn: () => installRuntime(onReady) } });
  }
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
    item.onclick = () => {
      if (state.selected.has(g.id)) state.selected.delete(g.id);
      else state.selected.add(g.id);
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
    setRunHeader("○", "", "Ready to start", "Your services are installed.");
    el("startBtn").hidden = false;
    el("stopBtn").hidden = true;
    el("openApp").disabled = true;
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
  await preflight(async () => {
    showScreen("running");
    const groupIds = (state.config && state.config.selectedGroups && state.config.selectedGroups.length)
      ? state.config.selectedGroups : ["core"];
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
  } else if (running > 0) {
    setRunHeader("◐", "starting", "Starting services…", `${running} of ${total} services up.`);
    el("stopBtn").hidden = false;
  } else if (!state.statusTimer) {
    // idle state with nothing running — leave the "ready to start" header.
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

// ---------- license modal ----------
function openLicenseModal() {
  el("licenseModalError").textContent = "";
  renderLicenseModal(state.license);
  el("licenseModal").showModal();
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
    if (lic) {
      if (lic.valid === false && lic.reason) {
        el("licenseModalError").textContent = lic.reason;
      } else {
        state.license = lic;
        setEdition(lic.edition);
        renderLicenseModal(lic);
      }
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

  try {
    await App().SaveUserSettings(s);
    el("settingsSaved").hidden = false;
  } catch (e) {
    el("settingsError").textContent = String(e).replace(/^Error:\s*/, "");
  } finally {
    btn.disabled = false;
    btn.textContent = "Save changes";
  }
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
  el("editionBadge").onclick = openLicenseModal;
  el("licenseModalClose").onclick = () => el("licenseModal").close();
  el("licenseModal").addEventListener("click", (e) => { if (e.target === el("licenseModal")) el("licenseModal").close(); });
  el("licenseModalImport").onclick = importLicenseFromModal;
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
  el("openSettings").onclick = enterSettings;
  el("settingsBack").onclick = () => enterRunning("idle");
  el("settingsSave").onclick = saveSettings;
  el("browseOrca").onclick = async () => {
    try {
      const p = await App().BrowseForFolder("Select ORCA Installation Folder");
      if (p) el("orcaPath").value = p;
    } catch (e) { /* cancelled */ }
  };
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
