/* Ligand-X public launcher.
 *
 * Reuses the same Go backend as the dev launcher via the Wails-injected
 * globals window.go.launcher.App.* and window.runtime.*. No ES-module imports;
 * the runtime binds these on the window object at startup. Pure decision
 * logic lives in state.js (window.LXState) so it can be tested under Node.
 *
 * First run is a wizard:  preflight gate -> login -> license (optional)
 *                         -> services -> pull images.
 * After that the launcher is a small app with four tabs — Home, Services,
 * Settings, Help — so every place is one click from anywhere. One poller keeps
 * state.status current on every tab; renderHome() derives all Home buttons
 * and headlines from it, so no screen keeps its own idea of what is running.
 */

const App = () => window.go.launcher.App;
const RT = () => window.runtime;
const S = () => window.LXState;

// ---------- shared state ----------
const state = {
  mode: "gate",          // "gate" | "onboarding" | "app"
  screen: "gate",        // data-screen currently shown
  config: null,          // LauncherConfig
  license: null,         // LicenseSummary
  info: null,            // AppInfo (versions, paths, address, GPU)
  update: null,          // RuntimeUpdateStatus from the boot check
  groups: [],            // []ServiceGroup
  present: {},           // group ID -> images already downloaded
  selected: new Set(),   // selected group IDs (excludes implicit required handling)
  servicesMode: "onboarding", // "onboarding" | "manage"
  estimateSeq: 0,
  status: null,          // last SystemStatus
  stack: "stopped",      // derived: stopped | starting | running | degraded | stopping
  busy: null,            // "starting" | "restarting" | "stopping" while a call is in flight
  runError: "",
  startNeedsPull: false, // last start failed on missing images
  restartPending: false, // a saved change is waiting for a restart
  fullRestartPending: false, // ...and needs stop + start (a module was removed)
  statusTimer: null,
  statusMarkup: "",
  pulling: false,
  pullCancelTimer: null,
  proExpanded: false,    // whether the locked-Pro disclosure is open
  pendingRuntimePull: false,
  releaseOnDone: null,
  releaseInstalled: "",
  settingsLoaded: null,  // UserSettings as read from the env file
  settingsInitial: null, // what the form showed right after loading
  settingsDirty: false,
  envDirty: false,
  logService: null,      // service whose logs the log dialog is following
  recentLog: [],         // last launcher messages, for diagnostics
  _orcaPrompt: null,     // { resolve, confirmed } while the ORCA dialog is open
};

const ONBOARDING_STEPS = ["login", "license", "services", "pull"];
const TABBED_SCREENS = ["home", "services", "settings", "advanced", "help"];
const DOCS_FIRST_LAUNCH_URL = "https://www.ligand-x.com/docs/first-launch/";
const DOCKER_INSTALL_URL = "https://docs.docker.com/get-docker/";
const SUPPORT_ISSUE_URL = "https://github.com/kon-218/ligand-x-support/issues/new/choose";
const SUPPORT_DISCUSS_URL = "https://github.com/kon-218/ligand-x-support/discussions";

function openExternal(url) {
  try { App().OpenBrowser(url); } catch (e) { /* best-effort */ }
}

// ---------- tiny DOM helpers ----------
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));
const el = (id) => document.getElementById(id);
const cleanError = (e) => String(e).replace(/^Error:\s*/, "");

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; } catch (e) { return false; }
}

// ---------- navigation ----------
function showScreen(name) {
  state.screen = name;
  $$(".screen").forEach((s) => s.classList.toggle("active", s.dataset.screen === name));

  const onboarding = state.mode === "onboarding" && ONBOARDING_STEPS.includes(name);
  el("steps").hidden = !onboarding;
  if (onboarding) updateSteps(name);

  const tabbed = state.mode === "app" && TABBED_SCREENS.includes(name);
  el("tabBar").hidden = !tabbed;
  const activeTab = name === "advanced" ? "settings" : name;
  $$("#tabBar .tab").forEach((tab) => {
    const on = tabbed && tab.dataset.tab === activeTab;
    tab.classList.toggle("active", on);
    tab.setAttribute("aria-selected", on ? "true" : "false");
    tab.tabIndex = on || !tabbed ? 0 : -1;
  });
  $(".screen-host").scrollTop = 0;
}

function updateSteps(current) {
  const idx = ONBOARDING_STEPS.indexOf(current);
  $$("#steps .step-dot").forEach((dot, i) => {
    dot.classList.toggle("active", i === idx);
    dot.classList.toggle("done", i < idx);
  });
}

// Unsaved edits are never dropped silently when the user navigates away.
function canLeaveScreen() {
  if (state.screen === "settings" && state.settingsDirty) {
    if (!window.confirm("Discard your unsaved settings changes?")) return false;
    setSettingsDirty(false);
  }
  if (state.screen === "advanced" && state.envDirty) {
    if (!window.confirm("Discard your unsaved changes to the environment file?")) return false;
    state.envDirty = false;
  }
  if (state.screen === "services" && state.servicesMode === "manage" && servicesChanged()) {
    if (!window.confirm("Discard your service changes?")) return false;
  }
  return true;
}

function showTab(tab) {
  if (state.mode !== "app" || !canLeaveScreen()) return;
  if (tab === "home") enterHome();
  else if (tab === "services") enterServices("manage");
  else if (tab === "settings") enterSettings();
  else if (tab === "help") enterHelp();
}

function setEdition(edition) {
  const badge = el("editionBadge");
  const label = edition ? edition.charAt(0).toUpperCase() + edition.slice(1) : "Free";
  badge.textContent = label;
  badge.dataset.edition = (edition || "free").toLowerCase();
  badge.setAttribute("aria-label", `License: ${label}. View or add a license`);
}

// ---------- boot ----------
window.addEventListener("DOMContentLoaded", () => {
  wireEvents();
  RT().EventsOn("pullProgress", onPullProgress);
  RT().EventsOn("pullComplete", onPullComplete);
  RT().EventsOn("log", onLog);
  boot();
});

function boot() {
  return preflight(afterRuntimeReady, { checkUpdate: true });
}

// Docker is up and the runtime files are installed: decide where the user lands.
// Also the continuation after any runtime install or version change.
async function afterRuntimeReady() {
  try {
    state.config = await App().GetLauncherConfig();
  } catch (e) {
    state.config = { firstRunDone: false, selectedGroups: [], userProfile: {} };
  }
  try {
    state.license = await App().GetLicenseStatus();
    setEdition(state.license && state.license.edition);
  } catch (e) { /* free by default */ }
  refreshAppInfo();

  if (!(state.config && state.config.firstRunDone)) {
    state.mode = "onboarding";
    showScreen("login");
    const p = state.config && state.config.userProfile;
    if (p) { el("username").value = p.username || ""; el("email").value = p.email || ""; }
    return;
  }

  state.mode = "app";
  startStatusPolling();
  if (state.pendingRuntimePull) {
    // A new runtime version pins new images: fetch whatever the saved
    // selection is now missing, and remind the user to restart onto them.
    state.pendingRuntimePull = false;
    markRestartNeeded(true);
    const ids = savedGroups();
    let present = {};
    try { present = await App().CheckImagePresence(); } catch (e) { /* pull all */ }
    const missing = ids.filter((id) => present[id] !== true);
    if (missing.length) {
      state.servicesMode = "manage";
      state.selected = new Set(ids);
      startPull(missing);
      return;
    }
  }
  enterHome();
}

async function refreshAppInfo() {
  try {
    state.info = await App().GetAppInfo();
  } catch (e) { /* keep what we had */ }
  if (state.screen === "home") renderHome();
  if (state.screen === "help") renderAbout();
  if (state.screen === "services") renderServices();
}

function savedGroups() {
  return (state.config && state.config.selectedGroups && state.config.selectedGroups.length)
    ? state.config.selectedGroups : ["core"];
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
// Runs at boot and before every start. Calls onReady() once Docker is up and
// the runtime bundle (docker-compose.yml + env templates) is installed.
//   opts.checkUpdate  ask the release index for a newer runtime (boot only, so
//                     an optional update never stands between the user and Start)
//   opts.quiet        stay on the current screen unless something needs attention
async function preflight(onReady, opts = {}) {
  const retry = () => preflight(onReady, Object.assign({}, opts, { quiet: false }));
  const backHome = state.mode === "app" ? { label: "Back to Home", fn: () => enterHome() } : null;
  if (!opts.quiet) {
    showScreen("gate");
    gate({ spinner: true, title: "Checking Docker…", msg: "Making sure the container engine is available." });
  }

  let dockerOk = false, dockerMsg = "";
  try {
    const res = await App().CheckDocker();
    ({ ok: dockerOk, message: dockerMsg } = normalizeDockerCheck(res));
  } catch (e) { dockerMsg = String(e); }

  if (!dockerOk) {
    showScreen("gate");
    gate({
      icon: "🐳", title: "Docker isn't running",
      msg: dockerMsg || "Start Docker Desktop (or the Docker engine), then try again. If Docker is not installed yet, use Install Docker.",
      action: { label: "Try again", fn: retry },
      secondary: { label: "Install Docker", fn: () => openExternal(DOCKER_INSTALL_URL) },
      tertiary: backHome,
    });
    return;
  }

  // Runtime bundle
  let dist;
  try {
    dist = await App().GetDistributionStatus();
  } catch (e) {
    showScreen("gate");
    gate({ icon: "⚠️", title: "Setup check failed", msg: String(e),
      action: { label: "Try again", fn: retry }, tertiary: backHome });
    return;
  }

  if (dist && dist.needsInstall) {
    showScreen("gate");
    gate({
      icon: "📦", title: "Set up runtime files",
      msg: dist.message || "Ligand-X needs to download its runtime files (~small) before the first launch.",
      action: { label: "Download runtime", fn: () => installRuntime(afterRuntimeReady) },
      secondary: { label: "Choose version", fn: () => openReleaseSelector(afterRuntimeReady, false) },
    });
    return;
  }

  if (opts.checkUpdate) {
    try {
      state.update = await App().CheckForRuntimeUpdate();
    } catch (e) {
      // Update checking is best-effort: offline or a rate-limited API must not
      // stop someone launching the runtime they already have installed.
      console.warn("update check failed", e);
    }
  }
  // Only an update the release index marks as required may block. An optional
  // one is offered as a banner on Home instead (see renderHome).
  const upd = state.update;
  if (upd && upd.updateAvailable && upd.updateRequired) {
    showScreen("gate");
    gate({
      icon: "⬆️", title: "Update required",
      msg: upd.message || ("Ligand-X runtime " + upd.latestVersion + " must be installed before services can start."),
      action: { label: "Update now", fn: () => installRuntime(afterRuntimeReady, upd.latestVersion, true) },
      secondary: { label: "Choose version", fn: () => openReleaseSelector(afterRuntimeReady, true) },
      tertiary: backHome,
    });
    return;
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
    state.update = null; // re-checked at next launch
    onReady();
  } catch (e) {
    gate({ icon: "⚠️", title: "Download failed", msg: String(e), log: true,
      action: { label: "Retry", fn: () => installRuntime(onReady, version, pullAfter) } });
  }
}

async function openReleaseSelector(onDone, pullAfter = true) {
  state.releaseOnDone = { fn: onDone, pullAfter };
  const modal = el("releaseModal");
  modal.showModal();
  await loadReleaseList();
}

// Split out from opening the modal so toggling the channel can refresh the list
// in place, without closing and losing what the user was doing.
async function loadReleaseList() {
  const list = el("releaseList");
  const install = el("releaseInstall");
  const warning = el("releaseWarning");
  const toggle = el("releasePrereleaseToggle");
  list.textContent = "Loading signed releases…";
  el("releaseError").textContent = "";
  warning.hidden = true;
  warning.textContent = "";
  install.disabled = true;
  install.dataset.version = "";
  try {
    const options = await App().ListRuntimeReleaseOptions();
    const releases = options.releases || [];
    state.releaseInstalled = options.installed || "";
    toggle.checked = !!options.showPrereleases;
    if (options.warning) {
      warning.textContent = options.warning;
      warning.hidden = false;
    }
    list.textContent = "";
    if (!releases.length) {
      list.textContent = options.showPrereleases
        ? "No signed releases were found."
        : "No stable releases were found. Turn on release candidates to see pre-release builds.";
      return;
    }
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
      const tags = [];
      if (release.recommended) tags.push("Recommended");
      if (release.version.includes("-")) tags.push("Release candidate");
      if (release.version === state.releaseInstalled) tags.push("Installed");
      title.textContent = `${release.version}${tags.length ? " · " + tags.join(" · ") : ""}`;
      const summary = document.createElement("span");
      summary.textContent = release.summary || release.compatibility || "Signed, supported release";
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
  const continuation = state.releaseOnDone || { fn: afterRuntimeReady, pullAfter: true };
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
// One panel, two uses: a wizard step on first run ("onboarding") and the
// Services tab afterwards ("manage"). keepSelection re-shows what the user had
// picked (e.g. coming back from a failed download) instead of resetting it.
async function enterServices(mode, keepSelection) {
  state.servicesMode = mode;
  showScreen("services");
  state.proExpanded = false;
  el("svcError").textContent = "";
  el("svcNotice").hidden = true;
  const manage = mode === "manage";
  el("svcTitle").textContent = manage ? "Services" : "Choose services";
  el("svcSub").textContent = manage
    ? "Add or remove modules. Core services are required and always included."
    : "Pick the modules to install. Core services are required and always included.";

  try {
    state.groups = await App().GetServiceGroups();
  } catch (e) {
    state.groups = [];
  }
  try {
    state.present = await App().CheckImagePresence();
  } catch (e) { state.present = {}; }

  if (!(keepSelection && state.selected.size)) {
    state.selected = new Set();
    const saved = (state.config && state.config.selectedGroups) || [];
    state.groups.forEach((g) => {
      const wanted = manage ? saved.includes(g.id) : g.defaultOn;
      if (g.required || (wanted && !g.locked)) state.selected.add(g.id);
    });
  }
  renderServices();
}

// Whether the picker differs from what is saved (manage mode only).
function servicesChanged() {
  if (!state.groups.length) return false;
  return !S().sameSelection(selectedGroupIds(), withRequired(savedGroups()));
}

function withRequired(ids) {
  const all = new Set(ids);
  state.groups.forEach((g) => { if (g.required) all.add(g.id); });
  return Array.from(all);
}

// A GPU-only module on a machine without one would download and then refuse
// to start, so it cannot be newly selected. info is null until the check ran.
function gpuMissingFor(g) {
  return !!(g.requiresGpu && state.info && state.info.gpuAvailable === false);
}

async function toggleGroup(g) {
  if (state.selected.has(g.id)) {
    state.selected.delete(g.id);
    renderServices();
    return;
  }
  if (gpuMissingFor(g)) return;
  if (g.id === "qc" && !(await ensureOrcaForQC())) return;
  state.selected.add(g.id);
  renderServices();
}

// Build one selectable/locked service card.
function svcCard(g) {
  const selected = state.selected.has(g.id) || g.required;
  const noGpu = gpuMissingFor(g) && !selected;
  const item = document.createElement("div");
  item.className = "svc-item" + (selected ? " selected" : "") + (g.locked ? " locked" : "") + (noGpu ? " unavailable" : "");
  item.tabIndex = 0;

  let tag = "";
  if (g.required) tag = `<span class="svc-tag required">Required</span>`;
  else if (g.edition && g.edition !== "free") tag = `<span class="svc-tag pro">${esc(g.edition)}</span>`;
  if (g.requiresGpu) tag += `<span class="svc-tag gpu">NVIDIA GPU</span>`;

  const downloaded = state.present[g.id] === true;
  let sizeTxt = g.sizeMb ? `${(g.sizeMb / 1024).toFixed(g.sizeMb >= 1024 ? 1 : 2)} GB download` : "";
  if (downloaded) sizeTxt = sizeTxt ? `Downloaded · ${sizeTxt.replace(" download", "")}` : "Downloaded";
  const unlock = g.locked ? `<div class="svc-unlock">Add a license to unlock</div>` : "";
  const gpuNote = noGpu
    ? `<div class="svc-warn">No NVIDIA GPU was detected on this computer, so this module can't run here.</div>`
    : "";
  const pilotNote = g.id === "protein-mutation"
    ? `<div class="svc-size">Protein mutation pilot opt-in. Sets LIGANDX_ENABLE_PREVIEW_MODULES and LIGANDX_PROTO_PILOT_ENABLED. A healthy worker is not model readiness.</div>`
    : "";

  item.innerHTML = `
    <div class="svc-check" aria-hidden="true">${selected ? "✓" : ""}</div>
    <div class="svc-body">
      <div class="svc-name">${esc(g.name)}${tag}</div>
      <div class="svc-desc">${esc(g.description || "")}</div>
      ${sizeTxt ? `<div class="svc-size">${sizeTxt}</div>` : ""}
      ${pilotNote}
      ${gpuNote}
      ${unlock}
    </div>`;

  let activate = null;
  if (g.locked) {
    item.setAttribute("role", "button");
    item.setAttribute("aria-label", `${g.name}: add a license to unlock`);
    activate = () => openLicenseModal();
  } else {
    item.setAttribute("role", "checkbox");
    item.setAttribute("aria-checked", selected ? "true" : "false");
    item.setAttribute("aria-label", g.name);
    if (g.required || noGpu) item.setAttribute("aria-disabled", "true");
    if (!g.required) activate = () => toggleGroup(g);
  }
  if (activate) {
    item.onclick = (e) => { if (!e.target.closest("button")) activate(); };
    item.onkeydown = (e) => {
      if (e.target !== item) return;
      if (e.key === " " || e.key === "Enter") { e.preventDefault(); activate(); }
    };
  }

  // Images of a module the user no longer runs keep taking disk space until
  // they are removed; offer that right where the module is listed.
  const savedIds = withRequired(savedGroups());
  if (state.servicesMode === "manage" && downloaded && !selected && !g.locked && !savedIds.includes(g.id)) {
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "link-btn svc-remove";
    remove.textContent = "Remove downloaded images";
    remove.onclick = () => removeGroupImages(g, remove);
    item.querySelector(".svc-body").appendChild(remove);
  }
  return item;
}

async function removeGroupImages(g, button) {
  if (!window.confirm(`Remove the downloaded images for ${g.name}? They will need to be downloaded again if you add it back.`)) return;
  el("svcError").textContent = "";
  button.disabled = true;
  button.textContent = "Removing…";
  try {
    const kept = Array.from(new Set([...withRequired(savedGroups()), ...selectedGroupIds()]));
    await App().RemoveUnusedGroupImages([g.id], kept);
  } catch (e) {
    el("svcError").textContent = cleanError(e);
  }
  try { state.present = await App().CheckImagePresence(); } catch (e) { /* keep */ }
  renderServices();
}

function renderServices() {
  const list = el("svcList");
  const focused = document.activeElement && list.contains(document.activeElement)
    ? Array.from(list.children).indexOf(document.activeElement) : -1;
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
    disc.setAttribute("aria-expanded", state.proExpanded ? "true" : "false");
    disc.innerHTML = `<span class="svc-disclosure-caret" aria-hidden="true">▸</span>
      <span>Pro modules (${lockedPro.length}) — add a license to unlock</span>`;
    disc.onclick = () => { state.proExpanded = !state.proExpanded; renderServices(); };
    list.appendChild(disc);

    if (state.proExpanded) {
      lockedPro.forEach((g) => list.appendChild(svcCard(g)));
    }
  }
  // Re-rendering replaced the card that had keyboard focus; give it back.
  if (focused >= 0 && list.children[focused]) list.children[focused].focus();
  updateServicesFooter();
}

// Size-versus-free-space line and the two buttons, which say exactly what
// pressing them will do.
async function updateServicesFooter() {
  const manage = state.servicesMode === "manage";
  const changed = manage && servicesChanged();
  const back = el("svcBack");
  const next = el("svcNext");
  back.textContent = manage ? "Discard changes" : "Back";
  back.disabled = manage && !changed;

  const seq = ++state.estimateSeq;
  let est = null;
  try {
    est = await App().GetStorageEstimate(selectedGroupIds());
  } catch (e) { /* no estimate; the pull pre-flight still checks */ }
  if (seq !== state.estimateSeq) return; // a newer selection is already being sized

  const summary = S().storageSummary(est);
  const line = el("svcSummary");
  line.textContent = summary.text;
  line.dataset.level = summary.level;

  const download = !!(est && est.downloadBytes);
  if (manage) {
    next.textContent = download ? "Download & apply" : (changed ? "Apply" : "No changes");
    next.disabled = !!(est && est.blocked) || (!changed && !download);
  } else {
    next.textContent = download ? "Download & continue" : "Continue";
    next.disabled = !!(est && est.blocked);
  }
}

function selectedGroupIds() {
  const ids = new Set(state.selected);
  state.groups.forEach((g) => { if (g.required) ids.add(g.id); });
  // Guarantee at least core.
  if (ids.size === 0) ids.add("core");
  return Array.from(ids);
}

function leaveServices() {
  if (state.servicesMode === "manage") {
    // "Discard changes": back to what is saved, staying on the tab.
    enterServices("manage");
    return;
  }
  enterLicense();
}

// Confirm the service selection: download only the groups whose images aren't
// already present, then land on Home. If everything is already downloaded we
// skip the pull screen entirely — this is what makes changing services cheap
// when nothing new was added.
async function confirmServices() {
  const ids = selectedGroupIds();
  if (ids.includes("qc") && !(await ensureOrcaForQC())) return;

  let present = {};
  try {
    present = await App().CheckImagePresence();
  } catch (e) { /* treat as nothing present -> pull all */ }

  const missing = ids.filter((id) => present[id] !== true);
  if (missing.length) {
    startPull(missing); // full selection is persisted on completion
    return;
  }
  await finishSelection();
}

// The selection is downloaded: save it and go Home. Shared by the picker and
// by a completed download.
async function finishSelection() {
  const before = withRequired(savedGroups());
  const after = selectedGroupIds();
  const wasSetUp = !!(state.config && state.config.firstRunDone);
  await persistSelection();
  if (wasSetUp && !S().sameSelection(before, after)) {
    // `up -d` starts added modules but leaves removed ones running, so a
    // removal needs a full stop + start to take effect.
    markRestartNeeded(before.some((id) => !after.includes(id)));
  }
  if (state.mode !== "app") {
    state.mode = "app";
    startStatusPolling();
  }
  enterHome();
}

// ---------- pull ----------
function startPull(groupIds) {
  showScreen("pull");
  state.pulling = true;
  state._pullGroups = groupIds;
  el("pullError").textContent = "";
  el("pullActions").hidden = true;
  el("pullRunningActions").hidden = false;
  el("pullCancel").disabled = false;
  el("pullCancel").textContent = "Cancel download";
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

async function cancelPull() {
  if (!state.pulling) return;
  const btn = el("pullCancel");
  btn.disabled = true;
  btn.textContent = "Cancelling…";
  try { await App().StopPullServiceGroups(); } catch (e) { /* fall through to the timer */ }
  // The backend confirms with a pullComplete("cancelled") event. If the pull
  // was between steps and never reports back, do not leave the user waiting.
  clearTimeout(state.pullCancelTimer);
  state.pullCancelTimer = setTimeout(() => {
    if (state.pulling) pullFailed(S().pullFailureMessage({ reason: "cancelled" }));
  }, 15000);
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
  clearTimeout(state.pullCancelTimer);

  if (res && res.success) {
    el("pullFill").style.width = "100%";
    el("pullGroup").textContent = "Download complete";
    state.startNeedsPull = false;
    await finishSelection();
    return;
  }
  pullFailed(S().pullFailureMessage(res));
}

function pullFailed(msg) {
  state.pulling = false;
  clearTimeout(state.pullCancelTimer);
  el("pullError").textContent = msg;
  el("pullRunningActions").hidden = true;
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

// ---------- home ----------
function enterHome() {
  showScreen("home");
  renderHome();
  refreshStatus();
}

// One poller for the whole app: it keeps running on every tab, so Home is
// already current when the user comes back to it.
function startStatusPolling() {
  if (state.statusTimer) return;
  refreshStatus();
  state.statusTimer = setInterval(refreshStatus, 4000);
}

async function refreshStatus() {
  if (state.mode !== "app") return;
  try {
    state.status = await App().GetSystemStatus();
  } catch (e) { return; }
  renderHome();
}

// A saved change needs the stack restarted before it applies. Harmless when
// nothing is running: the next start picks the change up and clears the flag.
function markRestartNeeded(full) {
  state.restartPending = true;
  if (full) state.fullRestartPending = true;
  renderHome();
}

const STACK_HEADERS = {
  stopped:  ["○", "", "Ready to start", "Start services, then open Ligand-X in your browser."],
  starting: ["◐", "starting", "Starting services…", "This can take a minute on first run."],
  running:  ["●", "up", "Ligand-X is running", ""],
  degraded: ["◐", "starting", "Some services need attention", ""],
  stopping: ["◐", "starting", "Stopping services…", "Waiting for running work to shut down cleanly."],
};

// Everything on Home is a function of state; nothing else shows or hides its
// buttons. Safe to call from any screen.
function renderHome() {
  const status = state.status;
  const services = (status && status.services) || [];
  const running = (status && status.totalRunning) || 0;
  const total = (status && status.totalServices) || services.length;
  const stack = S().deriveStack(status, state.busy);
  state.stack = stack;

  let [glyph, cls, headline, subline] = STACK_HEADERS[stack];
  const counts = `${running} of ${total} services up.`;
  if (stack === "starting" && state.busy === "restarting") headline = "Restarting services…";
  if (stack === "starting" && running > 0) subline = counts;
  if (stack === "running") subline = counts;
  if (stack === "degraded") subline = `${counts} Select a service below to see why.`;
  if (stack === "stopped" && state.runError) headline = "Couldn't start";
  const pulse = el("runPulse");
  pulse.textContent = glyph;
  pulse.className = "run-pulse" + (cls ? " " + cls : "");
  el("runHeadline").textContent = headline;
  el("runSubline").textContent = subline;
  el("runError").textContent = state.runError;

  const up = stack !== "stopped";
  const busy = !!state.busy;
  el("startBtn").hidden = up;
  el("startBtn").disabled = busy;
  el("startBtn").textContent = state.startNeedsPull ? "Re-download images" : "Start services";
  el("openApp").hidden = !up;
  el("openApp").disabled = running === 0 || stack === "stopping";
  el("restartBtn").hidden = !up;
  el("stopBtn").hidden = !up;
  el("restartBtn").disabled = busy;
  el("stopBtn").disabled = busy;

  // Where the application lives and who to sign in as.
  const info = state.info;
  el("runAddress").hidden = !(up && info && info.appUrl);
  if (info && info.appUrl) {
    el("runAddressText").textContent = info.username
      ? `${info.appUrl} — sign in as ${info.username}`
      : info.appUrl;
  }

  // Banners.
  el("restartBanner").hidden = !(state.restartPending && up && !busy);
  const dismissed = (state.config && state.config.dismissedUpdateVersion) || "";
  const showUpdate = S().shouldShowUpdateBanner(state.update, dismissed);
  el("updateBanner").hidden = !showUpdate;
  if (showUpdate) {
    el("updateBannerText").textContent = `Ligand-X ${state.update.latestVersion} is available.`;
  }

  renderStatusList(services);
}

function renderStatusList(services) {
  el("svcStatusTitle").hidden = !services.length;
  const markup = services.map((s) => {
    const { dot, label } = S().serviceState(s);
    return `<button type="button" class="svc-status" data-service="${esc(s.name)}" aria-label="${esc(s.name)}: ${esc(label)}. Show logs">
      <span class="dot ${dot}" aria-hidden="true"></span>
      <span class="svc-status-name">${esc(s.name)}</span>
      <span class="svc-status-state">${esc(label)}</span>
    </button>`;
  }).join("");
  // Re-rendering identical rows every poll would steal keyboard focus.
  if (markup === state.statusMarkup) return;
  state.statusMarkup = markup;
  const list = el("svcStatusList");
  const focused = document.activeElement && list.contains(document.activeElement)
    ? document.activeElement.dataset.service : null;
  list.innerHTML = markup;
  if (focused) {
    const again = Array.from(list.children).find((row) => row.dataset.service === focused);
    if (again) again.focus();
  }
}

// Stop and Restart interrupt whatever is running. Ask first when jobs are
// active; when the count cannot be read (database down) there is nothing to
// lose by proceeding.
async function confirmInterrupt(verb) {
  let count = 0;
  try { count = await App().ActiveJobCount(); } catch (e) { return true; }
  if (!count) return true;
  const jobs = count === 1 ? "1 job is" : `${count} jobs are`;
  return window.confirm(`${jobs} still running or queued. ${verb} anyway? Running work will be interrupted.`);
}

async function startServices() {
  if (state.busy) return;
  const groupIds = savedGroups();
  if (state.startNeedsPull) {
    // The last start failed on missing images: fetch them, then come back.
    state.runError = "";
    state.servicesMode = "manage";
    state.selected = new Set(groupIds);
    startPull(groupIds);
    return;
  }
  if (groupIds.includes("qc") && !(await ensureOrcaForQC())) return;

  state.runError = "";
  await preflight(async () => {
    if (state.screen !== "home") showScreen("home");
    state.busy = "starting";
    renderHome();
    try {
      await App().StartServiceGroups("prod", groupIds);
      state.restartPending = false;
      state.fullRestartPending = false;
    } catch (e) {
      state.runError = cleanError(e);
      state.startNeedsPull = /pull|not found|no such image|manifest/i.test(state.runError);
    } finally {
      state.busy = null;
      await refreshStatus();
      renderHome();
    }
  }, { quiet: true });
}

async function stopServices() {
  if (state.busy) return;
  if (!(await confirmInterrupt("Stop"))) return;
  state.runError = "";
  state.busy = "stopping";
  renderHome();
  try {
    await App().StopServices();
  } catch (e) {
    // Surface the failure; the next status refresh shows what is still up.
    state.runError = cleanError(e);
  } finally {
    state.busy = null;
    await refreshStatus();
    renderHome();
  }
}

async function restartServices() {
  if (state.busy) return;
  const groupIds = savedGroups();
  if (groupIds.includes("qc") && !(await ensureOrcaForQC())) return;
  if (!(await confirmInterrupt("Restart"))) return;

  state.runError = "";
  state.busy = "restarting";
  renderHome();
  try {
    if (state.fullRestartPending) {
      await App().StopServices();
      await App().StartServiceGroups("prod", groupIds);
    } else {
      await App().RestartServiceGroups(groupIds);
    }
    state.restartPending = false;
    state.fullRestartPending = false;
  } catch (e) {
    state.runError = cleanError(e);
  } finally {
    state.busy = null;
    await refreshStatus();
    renderHome();
  }
}

async function dismissUpdate() {
  if (!state.update) return;
  const cfg = Object.assign({}, state.config, { dismissedUpdateVersion: state.update.latestVersion });
  state.config = cfg;
  renderHome();
  try { await App().SaveLauncherConfig(cfg); } catch (e) { /* asks again next launch */ }
}

function installUpdate() {
  if (!state.update || !canLeaveScreen()) return;
  installRuntime(afterRuntimeReady, state.update.latestVersion, true);
}

// ---------- service logs ----------
async function openServiceLogs(name) {
  state.logService = name;
  el("logModalTitle").textContent = `${name} — logs`;
  el("logModalBox").textContent = "";
  el("logModalError").textContent = "";
  const modal = el("logModal");
  modal.showModal();
  modal.focus();
  try {
    await App().ViewLogs(name);
  } catch (e) {
    el("logModalError").textContent = cleanError(e);
  }
}

function onLogModalClose() {
  const name = state.logService;
  state.logService = null;
  if (name) { try { App().StopLogStream(name); } catch (e) { /* already gone */ } }
}

async function restartOneService() {
  const name = state.logService;
  if (!name) return;
  if (!(await confirmInterrupt(`Restart ${name}`))) return;
  const btn = el("logModalRestart");
  el("logModalError").textContent = "";
  btn.disabled = true;
  btn.textContent = "Restarting…";
  try {
    await App().RestartService(name);
    refreshStatus();
  } catch (e) {
    el("logModalError").textContent = cleanError(e);
  } finally {
    btn.disabled = false;
    btn.textContent = "Restart this service";
  }
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
    markRestartNeeded();
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
      // A new license can unlock modules: refresh whichever screen shows them.
      if (state.screen === "services") enterServices(state.servicesMode, true);
      if (state.screen === "license") renderLicense();
      if (state.screen === "help") renderAbout();
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
  el("resetResourcesMsg").hidden = true;
  await loadSettings();
  try {
    const dist = await App().GetDistributionStatus();
    el("installedRuntimeVersion").textContent = dist.installedVersion ? `Installed: ${dist.installedVersion}` : "No runtime version is recorded.";
  } catch (e) {
    el("installedRuntimeVersion").textContent = "Installed version could not be read.";
  }
  updateSettingsSections();
}

function setSettingsDirty(dirty) {
  state.settingsDirty = dirty;
  el("settingsSave").disabled = !dirty;
  if (dirty) el("settingsSaved").hidden = true;
}

function readSettingsForm() {
  return {
    cpuWorkerConcurrency: Number(el("cpuConcurrency").value),
    gpuShortConcurrency:  Number(el("gpuShortConcurrency").value),
    gpuLongConcurrency:   Number(el("gpuLongConcurrency").value),
    orcaHostPath:         el("orcaPath").value.trim(),
    boltzMsaUsername:     el("boltzMsaUser").value.trim(),
    boltzMsaPassword:     el("boltzMsaPass").value,
  };
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
  state.settingsLoaded = s;
  // What the form shows can differ from the file (a select snaps a
  // hand-edited value to its nearest option). Remember it so a save writes
  // only what the user actually changed.
  state.settingsInitial = readSettingsForm();
  setSettingsDirty(false);
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

  const form = readSettingsForm();
  const initial = state.settingsInitial || {};
  const changed = {};
  Object.keys(form).forEach((key) => { if (form[key] !== initial[key]) changed[key] = form[key]; });
  const merged = S().mergeSettings(state.settingsLoaded, changed);

  const selected = (state.config && state.config.selectedGroups) || [];
  try {
    if (selected.includes("qc")) {
      await App().ValidateOrcaHostPath(merged.orcaHostPath);
    }
    await App().SaveUserSettings(merged);
    state.settingsLoaded = merged;
    state.settingsInitial = form;
    setSettingsDirty(false);
    el("settingsSaved").hidden = false;
    markRestartNeeded();
  } catch (e) {
    el("settingsError").textContent = cleanError(e);
    btn.disabled = false;
  } finally {
    btn.textContent = "Save changes";
  }
}

// ---------- advanced: raw environment file ----------
// Kept on its own screen and reloaded on every visit, as Settings is, so the
// form and the editor can never hold two different copies of the same file.
function enterAdvanced() {
  if (!canLeaveScreen()) return;
  showScreen("advanced");
  loadEnvEditor();
}

async function loadEnvEditor() {
  el("envEditorError").textContent = "";
  el("envEditorSaved").hidden = true;
  try {
    el("envEditor").value = await App().GetEnvContent("prod");
    state.envDirty = false;
  } catch (e) {
    el("envEditorError").textContent = cleanError(e);
  }
}

async function saveEnvEditor() {
  el("envEditorError").textContent = "";
  el("envEditorSaved").hidden = true;
  const btn = el("envEditorSave");
  btn.disabled = true;
  try {
    await App().SaveEnvContent("prod", el("envEditor").value);
    state.envDirty = false;
    el("envEditorSaved").hidden = false;
    markRestartNeeded();
    refreshAppInfo(); // the port or username may have changed
  } catch (e) {
    el("envEditorError").textContent = cleanError(e);
  } finally {
    btn.disabled = false;
  }
}

// ---------- help ----------
function enterHelp() {
  showScreen("help");
  el("diagnosticsCopied").hidden = true;
  renderAbout();
  refreshAppInfo();
}

function aboutRows() {
  const info = state.info || {};
  const lic = state.license || {};
  const edition = (lic.edition || "free").replace(/^\w/, (c) => c.toUpperCase());
  return [
    ["Launcher", info.launcherVersion || "unknown"],
    ["Runtime", info.runtimeVersion || "not recorded"],
    ["Edition", edition],
    ["Address", info.appUrl || "—"],
    ["Signed in as", info.username || "—"],
    ["NVIDIA GPU", state.info ? (info.gpuAvailable ? "Detected" : "Not detected") : "Checking…"],
    ["Install folder", info.projectPath || "—"],
    ["Launcher log", info.logPath || "—"],
  ];
}

function renderAbout() {
  const box = el("aboutRows");
  box.textContent = "";
  aboutRows().forEach(([k, v]) => {
    const row = document.createElement("div");
    row.className = "license-row about-row";
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    row.append(dt, dd);
    box.appendChild(row);
  });
}

// Plain text a user can paste into a support request. Deliberately built from
// what is already on screen: no password, license contents or env file.
function diagnosticsText() {
  const lines = ["Ligand-X launcher diagnostics", ""];
  aboutRows().forEach(([k, v]) => lines.push(`${k}: ${v}`));
  lines.push(`Platform: ${navigator.platform || "unknown"}`);
  lines.push(`Selected modules: ${savedGroups().join(", ")}`);
  lines.push(`Stack: ${state.stack}`);
  const services = (state.status && state.status.services) || [];
  if (services.length) {
    lines.push("", "Services:");
    services.forEach((s) => lines.push(`  ${s.name}: ${S().serviceState(s).label}`));
  }
  if (state.runError) lines.push("", `Last error: ${state.runError}`);
  if (state.recentLog.length) {
    lines.push("", "Recent launcher messages:");
    state.recentLog.forEach((l) => lines.push(`  ${l}`));
  }
  return lines.join("\n");
}

async function copyDiagnostics() {
  const ok = await copyText(diagnosticsText());
  const note = el("diagnosticsCopied");
  note.textContent = ok ? "Copied. Paste it into your support request." : "Couldn't reach the clipboard.";
  note.hidden = false;
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

// ---------- logs (gate + pull + service log dialog) ----------
function onLog(entry) {
  const line = `${entry.timestamp || ""} ${entry.message || ""}`.trim();
  if (entry.service === "launcher") {
    state.recentLog.push(line);
    if (state.recentLog.length > 30) state.recentLog.shift();
  }
  if (state.logService && entry.service === state.logService) {
    appendLog(el("logModalBox"), line);
    return;
  }
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
function closeOnBackdrop(id) {
  el(id).addEventListener("click", (e) => { if (e.target === el(id)) el(id).close(); });
}

function wireEvents() {
  // Tabs. Arrow keys move between them, as the tablist role promises.
  const tabs = $$("#tabBar .tab");
  tabs.forEach((tab, i) => {
    tab.onclick = () => showTab(tab.dataset.tab);
    tab.onkeydown = (e) => {
      if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
      const next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
      next.focus();
      showTab(next.dataset.tab);
    };
  });

  // Header.
  el("helpBtn").onclick = () => {
    if (state.mode === "app") showTab("help");
    else openExternal(DOCS_FIRST_LAUNCH_URL);
  };
  el("userBtn").onclick = openUserModal;
  el("editionBadge").onclick = openLicenseModal;

  // Release selector.
  el("releaseModalClose").onclick = () => el("releaseModal").close();
  closeOnBackdrop("releaseModal");
  el("releaseInstall").onclick = installSelectedRelease;
  el("releasePrereleaseToggle").onchange = async (event) => {
    const toggle = event.target;
    toggle.disabled = true;
    try {
      await App().SetShowPrereleases(toggle.checked);
      await loadReleaseList();
    } catch (e) {
      // Put the checkbox back where it was: the channel did not actually change.
      toggle.checked = !toggle.checked;
      el("releaseError").textContent = cleanError(e);
    } finally {
      toggle.disabled = false;
    }
  };

  // License + account dialogs.
  el("licenseModalClose").onclick = () => el("licenseModal").close();
  closeOnBackdrop("licenseModal");
  el("licenseModalImport").onclick = importLicenseFromModal;
  el("userModalClose").onclick = () => el("userModal").close();
  closeOnBackdrop("userModal");
  el("accountManageLicense").onclick = () => { el("userModal").close(); openLicenseModal(); };
  el("changePasswordToggle").onclick = () => {
    const form = el("changePasswordForm");
    form.hidden = !form.hidden;
    el("changePasswordToggle").textContent = form.hidden ? "Change password" : "Cancel";
    if (!form.hidden) el("newPassword").focus();
  };
  el("changePasswordSave").onclick = changePassword;
  el("newPassword").addEventListener("keydown", (e) => { if (e.key === "Enter") changePassword(); });

  // ORCA dialog.
  el("orcaModalClose").onclick = () => el("orcaModal").close();
  closeOnBackdrop("orcaModal");
  el("orcaModal").addEventListener("close", onOrcaModalClose);
  el("orcaModalBrowse").onclick = browseOrcaModal;
  el("orcaModalConfirm").onclick = confirmOrcaModal;

  // Onboarding.
  el("loginForm").addEventListener("submit", (e) => { e.preventDefault(); handleLogin(); });
  el("showPassword").onchange = (e) => { el("password").type = e.target.checked ? "text" : "password"; };
  el("addLicense").onclick = addLicense;
  el("licenseNext").onclick = () => enterServices("onboarding");

  // Services + download.
  el("svcBack").onclick = leaveServices;
  el("svcNext").onclick = confirmServices;
  el("pullCancel").onclick = cancelPull;
  el("pullBack").onclick = () => enterServices(state.servicesMode, true);
  el("pullRetry").onclick = () => startPull(state._pullGroups || selectedGroupIds());

  // Home.
  el("openApp").onclick = () => { try { App().OpenFrontend(); } catch (e) {} };
  el("startBtn").onclick = startServices;
  el("restartBtn").onclick = restartServices;
  el("restartBannerBtn").onclick = restartServices;
  el("stopBtn").onclick = stopServices;
  el("updateNow").onclick = installUpdate;
  el("updateLater").onclick = dismissUpdate;
  el("copyAddress").onclick = async () => {
    const ok = state.info && await copyText(state.info.appUrl);
    el("copyAddress").textContent = ok ? "Copied" : "Copy";
    setTimeout(() => { el("copyAddress").textContent = "Copy"; }, 1500);
  };
  el("svcStatusList").addEventListener("click", (e) => {
    const row = e.target.closest(".svc-status");
    if (row) openServiceLogs(row.dataset.service);
  });

  // Service log dialog.
  el("logModalClose").onclick = () => el("logModal").close();
  closeOnBackdrop("logModal");
  el("logModal").addEventListener("close", onLogModalClose);
  el("logModalRestart").onclick = restartOneService;
  el("logModalCopy").onclick = async () => {
    const ok = await copyText(el("logModalBox").innerText);
    el("logModalCopy").textContent = ok ? "Copied" : "Copy";
    setTimeout(() => { el("logModalCopy").textContent = "Copy"; }, 1500);
  };

  // Settings.
  el("settingsForm").addEventListener("submit", (e) => { e.preventDefault(); saveSettings(); });
  el("settingsForm").addEventListener("input", () => setSettingsDirty(true));
  el("settingsForm").addEventListener("change", () => setSettingsDirty(true));
  el("browseOrca").onclick = async () => {
    try {
      const p = await App().BrowseForFolder("Select ORCA Installation Folder");
      if (p) { el("orcaPath").value = p; setSettingsDirty(true); }
    } catch (e) { /* cancelled */ }
  };
  el("chooseRuntimeVersion").onclick = () => {
    if (canLeaveScreen()) openReleaseSelector(afterRuntimeReady, true);
  };
  el("resetResources").onclick = resetResourceLimits;
  el("openAdvanced").onclick = enterAdvanced;
  el("advancedBack").onclick = () => { if (canLeaveScreen()) enterSettings(); };
  el("envEditor").addEventListener("input", () => { state.envDirty = true; el("envEditorSaved").hidden = true; });
  el("envEditorReload").onclick = loadEnvEditor;
  el("envEditorSave").onclick = saveEnvEditor;

  // Uninstall.
  el("openUninstall").onclick = openUninstallModal;
  el("uninstallModalClose").onclick = () => el("uninstallModal").close();
  // Typing the phrase is the only thing that arms the button, mirroring the
  // check the Go side enforces independently.
  el("uninstallConfirm").oninput = () => {
    el("uninstallGo").disabled = el("uninstallConfirm").value.trim() !== "UNINSTALL";
  };
  el("uninstallGo").onclick = runUninstall;
  el("uninstallQuit").onclick = () => { try { window.runtime.Quit(); } catch (e) {} };

  // Help.
  el("helpDocs").onclick = () => openExternal(DOCS_FIRST_LAUNCH_URL);
  el("helpSupport").onclick = () => openExternal(SUPPORT_ISSUE_URL);
  el("helpDiscuss").onclick = () => openExternal(SUPPORT_DISCUSS_URL);
  el("openFolder").onclick = () => { try { App().OpenProjectFolder(); } catch (e) {} };
  el("copyDiagnostics").onclick = copyDiagnostics;
  el("helpAccount").onclick = openUserModal;
  el("helpLicense").onclick = openLicenseModal;
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
    markRestartNeeded();
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
