/* Ligand-X public launcher — pure decision logic.
 *
 * Nothing here touches the DOM or the Go backend, so the rules the screens
 * depend on can be tested with `node --test frontend-public/`. Loaded as a
 * plain script in the launcher (window.LXState) and as a module under Node.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.LXState = api;
})(typeof self !== "undefined" ? self : this, function () {

  // One answer to "what is the stack doing", used by every screen.
  //   busy: "starting" | "restarting" | "stopping" | null — an action this
  //   launcher session kicked off and has not heard back from yet.
  function deriveStack(status, busy) {
    if (busy === "stopping") return "stopping";
    if (busy === "starting" || busy === "restarting") return "starting";
    const services = (status && status.services) || [];
    const running = (status && status.totalRunning) || 0;
    const total = (status && status.totalServices) || services.length;
    if (running === 0) return "stopped";
    if (services.some((s) => s.running && s.health === "starting")) return "starting";
    const unhealthy = services.some((s) => s.running && s.health === "unhealthy");
    if (running >= total && !unhealthy) return "running";
    return "degraded";
  }

  // The dot and label for one service row.
  function serviceState(s) {
    if (!s || !s.running) return { dot: "down", label: (s && s.status) || "stopped" };
    if (s.health === "unhealthy") return { dot: "down", label: "unhealthy" };
    if (s.health && s.health !== "healthy") return { dot: "starting", label: s.health };
    return { dot: "running", label: s.health || "running" };
  }

  const PULL_REASONS = {
    cancelled: "Download cancelled.",
    gpu_not_found: "Some selected modules need an NVIDIA GPU that wasn't found. Go back and deselect them.",
    assets_unavailable: "Protein mutation pilot model assets are unavailable. A healthy worker is not model readiness.",
    license_required: "A selected module requires a license. Add a license, or deselect it.",
    registry_login_failed: "Couldn't authenticate to the image registry. Check your license and connection.",
    insufficient_disk: "There isn't enough free disk space to download the images. Free up space and try again.",
    version_not_pinned: "No runtime version is recorded for this install. Choose a version in Settings, then try again.",
  };

  // What to tell the user when a download ends without success. A message from
  // the backend wins: it knows the actual numbers and paths.
  function pullFailureMessage(res) {
    if (res && res.message) return String(res.message);
    const reason = res && res.reason;
    if (reason && PULL_REASONS[reason]) return PULL_REASONS[reason];
    const failed = (res && res.failedGroups) || [];
    return failed.length ? `Failed to download: ${failed.join(", ")}.` : "Image download failed.";
  }

  // Settings the form does not show (e.g. the MSA API key) must survive a save,
  // so the form's values are laid over what was loaded rather than replacing it.
  function mergeSettings(loaded, form) {
    return Object.assign({}, loaded || {}, form || {});
  }

  // Compares two selections of group IDs regardless of order.
  function sameSelection(a, b) {
    const left = new Set(a || []), right = new Set(b || []);
    if (left.size !== right.size) return false;
    for (const id of left) if (!right.has(id)) return false;
    return true;
  }

  // An optional update is worth a reminder until the user dismisses that
  // version; a required one is never hidden.
  function shouldShowUpdateBanner(update, dismissedVersion) {
    if (!update || !update.updateAvailable) return false;
    if (update.updateRequired) return true;
    return update.latestVersion !== dismissedVersion;
  }

  function formatBytes(bytes) {
    const n = Number(bytes) || 0;
    if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(n >= 10 * (1 << 30) ? 0 : 1)} GB`;
    if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MB`;
    return `${n} B`;
  }

  // The one-line storage summary under the service picker.
  function storageSummary(est) {
    if (!est) return { text: "", level: "" };
    if (est.blocked) return { text: est.blocked, level: "error" };
    if (!est.downloadBytes) {
      return { text: "Everything selected is already downloaded.", level: "" };
    }
    let text = `Download about ${formatBytes(est.downloadBytes)}`;
    if (est.freeKnown) text += ` · ${formatBytes(est.freeBytes)} free`;
    if (est.warning) return { text: `${text}. ${est.warning}`, level: "warn" };
    return { text: `${text}.`, level: "" };
  }

  return {
    deriveStack, serviceState, pullFailureMessage, mergeSettings,
    sameSelection, shouldShowUpdateBanner, formatBytes, storageSummary,
  };
});
