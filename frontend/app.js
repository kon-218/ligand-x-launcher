// ============================================================
// Ligand-X Launcher - Frontend Application
// ============================================================

let statusInterval = null;
const MAX_LOGS = 500;
let logs = [];

// Services tab selection state (mirrors config.selectedGroups)
let serviceTabSelection = [];

const CORE_SERVICES = ['postgres', 'redis', 'rabbitmq', 'gateway', 'frontend', 'structure'];

// Initialize on load — wait for Wails runtime to be injected before calling backend
document.addEventListener('DOMContentLoaded', () => {
    // Poll until window.go and window.runtime are available (Wails injects them async)
    const waitForWails = setInterval(() => {
        if (window.go && window.runtime) {
            clearInterval(waitForWails);
            init();
        }
    }, 50);
    // Timeout after 10 seconds to avoid hanging forever
    setTimeout(() => clearInterval(waitForWails), 10000);
});

async function init() {
    // Setup tab switching first — sync, no backend needed
    setupTabSwitching();
    initMenuBar();
    initDrawerActions();
    document.addEventListener('keydown', e => { if (e.key === 'Escape') closeLicenseModal() })

    // Run all backend initialization in parallel — none of these should block the UI
    checkDocker();
    updateStatus();
    checkTunnel();
    updateProjectPath();
    initializeWizard();
    renderServicesTab();

    // Start polling for status updates
    statusInterval = setInterval(updateStatus, 5000);

    // Subscribe to log events (these don't depend on initialization)
    window.runtime.EventsOn('log', handleLogEvent);
    window.runtime.EventsOn('pullProgress', handlePullProgress);
    window.runtime.EventsOn('pullComplete', handlePullComplete);
    window.runtime.EventsOn('reinventModelProgress', handleReinventModelProgress);
    window.runtime.EventsOn('reinventModelComplete', handleReinventModelComplete);
    window.runtime.EventsOn('tunnel-status', updateTunnelUI);

    // Start streaming logs for the default selection (All Services)
    changeLogService();
}

// ============================================================
// Tab Switching
// ============================================================

function setupTabSwitching() {
    const tabButtons = document.querySelectorAll('.tab-button');

    tabButtons.forEach(button => {
        button.addEventListener('click', async () => {
            const tabName = button.getAttribute('data-tab');

            // Remove active from all buttons
            tabButtons.forEach(btn => btn.classList.remove('active'));
            button.classList.add('active');

            // Remove active from all content panels
            document.querySelectorAll('.tab-content').forEach(content => {
                content.classList.remove('active');
            });

            // Add active to selected content panel
            const activeContent = document.querySelector(`.tab-content[data-tab="${tabName}"]`);
            if (activeContent) {
                activeContent.classList.add('active');
            }

            // Render Services tab if clicked
            if (tabName === 'services') {
                await renderServicesTab();
            }

            // Load env config if clicked
            if (tabName === 'config') {
                await loadEnvConfig();
            }
        });
    });
}

// ============================================================
// Docker & Status
// ============================================================

async function checkDocker() {
    try {
        // Add a 3-second timeout for Docker check
        const checkPromise = window.go.main.App.CheckDocker();
        const timeoutPromise = new Promise((_, reject) =>
            setTimeout(() => reject(new Error('Docker check timed out')), 3000)
        );
        const [ok, message] = await Promise.race([checkPromise, timeoutPromise]);
        updateDockerStatus(ok, message);
        return ok;
    } catch (err) {
        updateDockerStatus(false, err.message || err);
        return false;
    }
}

function updateDockerStatus(running, message) {
    const indicator = document.getElementById('dockerStatus');
    const dot = indicator.querySelector('.status-dot');
    const text = indicator.querySelector('.status-text');
    
    dot.classList.remove('running', 'stopped');
    dot.classList.add(running ? 'running' : 'stopped');
    text.textContent = running ? 'Docker Running' : 'Docker Not Running';
    
    // Enable/disable controls based on Docker status
    const buttons = ['startBtn', 'stopBtn', 'restartBtn'];
    buttons.forEach(id => {
        document.getElementById(id).disabled = !running;
    });
}

async function updateStatus() {
    try {
        const status = await window.go.main.App.GetSystemStatus();
        
        // Update docker status
        updateDockerStatus(status.dockerRunning, '');
        
        // Update count
        document.getElementById('runningCount').textContent = status.totalRunning;
        
        // Update services grid
        const grid = document.getElementById('servicesGrid');
        if (status.services && status.services.length > 0) {
            grid.innerHTML = status.services.map(svc => `
                <div class="service-badge ${svc.running ? 'running' : 'stopped'}">
                    <span class="dot"></span>
                    <span>${svc.name}</span>
                </div>
            `).join('');
        } else {
            grid.innerHTML = '<span style="color: var(--text-muted); font-size: 12px;">No services running</span>';
        }
        
    } catch (err) {
        console.error('Failed to update status:', err);
    }
}

// ── Tunnel / Remote Access ──────────────────────────────────────────────────

let _tunnelRunning = false; // cached so toggleTunnel knows which action to take

async function checkTunnel() {
    try {
        const status = await window.go.main.App.GetTunnelStatus();
        updateTunnelUI(status);
    } catch (err) {
        // Backend not ready yet or non-devtunnel build — hide the row silently.
        const row = document.getElementById('tunnelRow');
        if (row) row.style.display = 'none';
    }
}

function updateTunnelUI(status) {
    const row = document.getElementById('tunnelRow');
    if (!row) return;

    // Non-devtunnel builds: status.enabled is false — hide the row entirely.
    if (!status.enabled) {
        row.style.display = 'none';
        return;
    }
    row.style.display = '';

    const dot = document.getElementById('tunnelDot');
    const text = document.getElementById('tunnelStatusText');
    const openBtn = document.getElementById('tunnelOpenBtn');
    const toggleBtn = document.getElementById('tunnelToggleBtn');

    _tunnelRunning = status.running;

    dot.classList.remove('running', 'external', 'stopped', 'error');
    if (status.running && status.managed) {
        dot.classList.add('running');
    } else if (status.running && !status.managed) {
        dot.classList.add('external');
    } else {
        dot.classList.add('stopped');
    }

    text.textContent = status.message || (status.running ? 'Tunnel running' : 'Tunnel stopped');

    if (openBtn) openBtn.disabled = !status.running;
    if (toggleBtn) {
        toggleBtn.textContent = status.running ? 'Stop Tunnel' : 'Start Tunnel';
        toggleBtn.disabled = false;
    }
}

async function toggleTunnel() {
    const toggleBtn = document.getElementById('tunnelToggleBtn');
    if (toggleBtn) toggleBtn.disabled = true;
    try {
        if (_tunnelRunning) {
            await window.go.main.App.StopTunnel();
        } else {
            await window.go.main.App.StartTunnel();
        }
    } catch (err) {
        console.error('Tunnel toggle failed:', err);
    }
    // Status update arrives via tunnel-status event; re-enable button on error path.
    await checkTunnel();
}

async function openTunnelURL() {
    await window.go.main.App.OpenTunnelURL();
}

// ───────────────────────────────────────────────────────────────────────────

async function updateProjectPath() {
    try {
        const path = await window.go.main.App.GetProjectPath();
        document.getElementById('projectPath').textContent = path || 'Not set';
    } catch (err) {
        document.getElementById('projectPath').textContent = 'Error';
    }
}

// ============================================================
// Service Controls
// ============================================================

function setControlButtonsLoading(activeIcon) {
    ['startBtn', 'stopBtn', 'restartBtn'].forEach(id => {
        document.getElementById(id).disabled = true;
    });
    activeIcon.innerHTML = '<circle cx="12" cy="12" r="9" stroke-dasharray="28 29" stroke-linecap="round" fill="none"/>';
    activeIcon.style.animation = 'spin 0.8s linear infinite';
}

function clearControlButtonLoading(activeIcon, originalIconHtml) {
    activeIcon.innerHTML = originalIconHtml;
    activeIcon.style.animation = '';
}

async function startServices() {
    const env = document.getElementById('envMode').value;
    const btn = document.getElementById('startBtn');
    const icon = btn.querySelector('svg');
    const originalIcon = icon.innerHTML;
    setControlButtonsLoading(icon);

    try {
        if (serviceTabSelection.length > 0) {
            await window.go.main.App.StartServiceGroups(env, serviceTabSelection);
            addLog('launcher', `Services started in ${env} mode (${serviceTabSelection.length} groups selected)`);
        } else {
            await window.go.main.App.StartServices(env);
            addLog('launcher', `Services started in ${env} mode`);
        }
        await updateStatus();
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    } finally {
        clearControlButtonLoading(icon, originalIcon);
        await updateStatus();
    }
}

async function stopServices() {
    const btn = document.getElementById('stopBtn');
    const icon = btn.querySelector('svg');
    const originalIcon = icon.innerHTML;
    setControlButtonsLoading(icon);

    try {
        await window.go.main.App.StopServices();
        await updateStatus();
        addLog('launcher', 'Services stopped');
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    } finally {
        clearControlButtonLoading(icon, originalIcon);
        await updateStatus();
    }
}

async function restartServices() {
    const btn = document.getElementById('restartBtn');
    const icon = btn.querySelector('svg');
    const originalIcon = icon.innerHTML;
    setControlButtonsLoading(icon);

    try {
        if (serviceTabSelection.length > 0) {
            await window.go.main.App.RestartServiceGroups(serviceTabSelection);
            addLog('launcher', `Services restarted (${serviceTabSelection.length} groups selected)`);
        } else {
            await window.go.main.App.RestartServices();
            addLog('launcher', 'Services restarted');
        }
        await updateStatus();
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    } finally {
        clearControlButtonLoading(icon, originalIcon);
        await updateStatus();
    }
}

async function pullImages() {
    const btn = document.getElementById('pullBtn');
    const icon = document.getElementById('pullIcon');
    btn.disabled = true;
    icon.style.animation = 'spin 0.8s linear infinite';

    try {
        if (serviceTabSelection.length === 0) {
            addLog('launcher', 'No services selected. Please select services in the Services tab.');
            return;
        }

        addLog('launcher', `Pulling services: ${serviceTabSelection.join(', ')}...`);
        window.go.main.App.PullServiceGroups(serviceTabSelection);
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    } finally {
        btn.disabled = false;
        icon.style.animation = '';
    }
}

// ============================================================
// Quick Links
// ============================================================

async function openFrontend() {
    await window.go.main.App.OpenFrontend();
}

async function openAPI() {
    await window.go.main.App.OpenAPI();
}

async function openFlower() {
    await window.go.main.App.OpenFlower();
}

// ============================================================
// Project & Maintenance
// ============================================================

async function selectProjectFolder() {
    try {
        const path = await window.go.main.App.SelectProjectFolder();
        if (path) {
            document.getElementById('projectPath').textContent = path;
            addLog('launcher', `Runtime path set to: ${path}`);
            distributionStatus = await window.go.main.App.GetDistributionStatus();
        }
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    }
}

async function cleanDocker() {
    const btn = document.querySelector('.footer-btn[onclick="cleanDocker()"]');
    const icon = btn.querySelector('svg');
    const originalIcon = icon.innerHTML;
    btn.disabled = true;
    icon.innerHTML = '<circle cx="12" cy="12" r="9" stroke-dasharray="28 29" stroke-linecap="round" fill="none"/>';
    icon.style.animation = 'spin 0.8s linear infinite';

    try {
        await window.go.main.App.CleanDocker();
        addLog('launcher', 'Docker cleanup completed');
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    } finally {
        btn.disabled = false;
        icon.innerHTML = originalIcon;
        icon.style.animation = '';
    }
}

// ============================================================
// Logs
// ============================================================

function handleLogEvent(entry) {
    addLog(entry.service, entry.message);

    // Also pipe logs into the wizard terminal while it's visible
    const wizard = document.getElementById('firstRunWizard');
    const logsContainer = document.getElementById('wizardLogsContainer');
    if (wizard && !wizard.classList.contains('hidden') && logsContainer) {
        const line = document.createElement('div');
        line.className = 'log-entry';
        line.style.color = entry.service === 'launcher' ? 'var(--text-muted)' : 'var(--text-primary)';
        line.textContent = `[${entry.service}] ${entry.message}`;
        logsContainer.appendChild(line);
        logsContainer.scrollTop = logsContainer.scrollHeight;
    }
}

function handlePullProgress(data) {
    // Update wizard progress bar if visible (only show progress for wizard, not for re-pulls)
    if (!document.getElementById('firstRunWizard').classList.contains('hidden')) {
        document.getElementById('pullOverallBar').style.width = data.overallPercent.toFixed(1) + '%';
        document.getElementById('pullGroupLabel').textContent = data.groupName ? ('Downloading ' + data.groupName) : 'Downloading...';
        document.getElementById('pullImageCounter').textContent = (data.imageIndex + 1) + ' / ' + data.totalImages;
    }
}

function addLog(service, message, type = 'info') {
    const container = document.getElementById('logsContainer');

    // If we're pulling, only show logs for the group being pulled
    if (window.isPulling && service !== window.currentPullingGroup && service !== 'launcher') {
        return;
    }

    const placeholder = container.querySelector('.log-placeholder');
    if (placeholder) {
        placeholder.remove();
    }

    const timestamp = new Date().toLocaleTimeString('en-US', { hour12: false });

    logs.push({ timestamp, service, message, type });

    // Trim logs if too many
    if (logs.length > MAX_LOGS) {
        logs = logs.slice(-MAX_LOGS);
    }

    const entry = document.createElement('div');
    entry.className = `log-entry ${type} fade-in`;
    entry.innerHTML = `
        <span class="log-time">${timestamp}</span>
        <span class="log-service">[${service}]</span>
        <span class="log-message">${escapeHtml(message)}</span>
    `;

    container.appendChild(entry);
    
    // Auto-scroll to bottom
    container.scrollTop = container.scrollHeight;
    
    // Remove old entries from DOM if too many
    while (container.children.length > MAX_LOGS) {
        container.removeChild(container.firstChild);
    }
}

function clearLogs() {
    logs = [];
    const container = document.getElementById('logsContainer');
    container.innerHTML = '<div class="log-placeholder">Logs will appear here...</div>';
}

async function changeLogService() {
    const service = document.getElementById('logService').value;
    try {
        await window.go.main.App.ViewLogs(service);
        addLog('launcher', `Now viewing logs for: ${service}`);
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    }
}

// ============================================================
// Utilities
// ============================================================

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// ============================================================
// License Badge
// ============================================================

function renderLicenseBadge(license) {
    const badge = document.getElementById('licenseBadge')
    if (!badge) return

    const edition = (license && license.edition) || 'free'
    const styles = {
        academic: { label: 'Academic', dotColor: 'var(--accent-warning)' },
        pro:      { label: 'Pro',      dotColor: 'var(--accent-primary)' },
        free:     { label: 'Free',     dotColor: 'var(--text-dim)'       },
    }
    const s = styles[edition] || styles.free

    const dot = document.getElementById('licenseBadgeDot')
    const label = document.getElementById('licenseBadgeLabel')
    if (dot)   dot.style.background = s.dotColor
    if (label) label.textContent = s.label

    badge.style.opacity = '1'
    badge.style.pointerEvents = 'auto'
}

function openLicenseModal() {
    renderLicenseModalContent(wizardLicenseStatus || { edition: 'free', valid: true, entitlements: [] })
    document.getElementById('licenseModal').classList.remove('hidden')
}

function closeLicenseModal() {
    document.getElementById('licenseModal').classList.add('hidden')
}

function renderLicenseModalContent(license) {
    const container = document.getElementById('licenseModalContent')
    if (!container) return

    while (container.firstChild) container.removeChild(container.firstChild)

    const edition = (license && license.edition) || 'free'
    const editionColors = {
        academic: { color: 'var(--accent-warning)', bg: 'rgba(245,158,11,0.12)',   border: 'rgba(245,158,11,0.3)' },
        pro:      { color: 'var(--accent-primary)', bg: 'rgba(6,182,212,0.12)',    border: 'rgba(6,182,212,0.3)'  },
        free:     { color: 'var(--text-dim)',        bg: 'rgba(107,114,128,0.12)', border: '#374151'              },
    }
    const ec = editionColors[edition] || editionColors.free

    const ENTITLEMENT_LABELS = {
        admet:         'ADMET',
        qc:            'Quantum Chem',
        boltz2:        'Boltz-2',
        'free-energy': 'Free Energy',
        reinvent:      'De Novo Design',
        kinetics:      'Kinetics',
    }

    function expiryColor(iso) {
        if (!iso) return 'var(--text-muted)'
        const days = (new Date(iso) - Date.now()) / 86400000
        if (days > 60) return 'var(--accent-success)'
        if (days > 0)  return 'var(--accent-warning)'
        return 'var(--accent-danger)'
    }

    const expiresAt  = (license && license.expiresAt)  || ''
    const graceUntil = (license && license.graceUntil) || ''
    const isExpired  = expiresAt && new Date(expiresAt) < new Date()
    const inGrace    = isExpired && graceUntil && new Date(graceUntil) > new Date()
    const entitlements = Array.isArray(license && license.entitlements) ? license.entitlements : []

    // --- Edition + customer row ---
    const topRow = document.createElement('div')
    topRow.style.cssText = 'display:flex;align-items:center;gap:12px;margin-bottom:16px;'

    const editionPill = document.createElement('span')
    editionPill.textContent = edition.toUpperCase()
    editionPill.style.cssText = 'padding:6px 14px;border-radius:16px;font-size:12px;font-weight:700;letter-spacing:0.5px;flex-shrink:0;'
    editionPill.style.color = ec.color
    editionPill.style.background = ec.bg
    editionPill.style.border = '1px solid ' + ec.border
    topRow.appendChild(editionPill)

    if (license && license.customerName) {
        const nameEl = document.createElement('p')
        nameEl.textContent = license.customerName
        nameEl.style.cssText = 'margin:0;font-size:13px;font-weight:500;color:var(--text-primary);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;'
        topRow.appendChild(nameEl)
    }
    container.appendChild(topRow)

    // --- Details grid ---
    const details = []
    if (license && license.licenseId) {
        details.push({ label: 'License ID', value: license.licenseId, color: 'var(--text-secondary)', mono: true })
    }
    if (expiresAt) {
        details.push({
            label: 'Expires',
            value: expiresAt.slice(0, 10) + (inGrace ? ' (grace period)' : ''),
            color: expiryColor(expiresAt),
            mono: false,
        })
    }
    if (inGrace && graceUntil) {
        details.push({ label: 'Grace until', value: graceUntil.slice(0, 10), color: 'var(--accent-warning)', mono: false })
    }

    if (details.length > 0) {
        const grid = document.createElement('div')
        grid.style.cssText = 'display:grid;grid-template-columns:auto 1fr;gap:6px 16px;font-size:12px;margin-bottom:16px;align-items:baseline;'
        details.forEach(({ label, value, color, mono }) => {
            const labelEl = document.createElement('span')
            labelEl.textContent = label
            labelEl.style.cssText = 'color:var(--text-muted);white-space:nowrap;'

            const valueEl = document.createElement('span')
            valueEl.textContent = value
            valueEl.style.color = color
            if (mono) valueEl.style.fontFamily = 'monospace'
            valueEl.style.overflow = 'hidden'
            valueEl.style.textOverflow = 'ellipsis'

            grid.appendChild(labelEl)
            grid.appendChild(valueEl)
        })
        container.appendChild(grid)
    }

    // --- Entitlements ---
    const entSection = document.createElement('div')
    entSection.style.marginBottom = '16px'

    const entTitle = document.createElement('p')
    entTitle.textContent = 'Pro Modules'
    entTitle.style.cssText = 'font-size:11px;font-weight:600;color:var(--text-muted);text-transform:uppercase;letter-spacing:0.5px;margin-bottom:8px;'
    entSection.appendChild(entTitle)

    if (entitlements.length > 0) {
        const chipsRow = document.createElement('div')
        chipsRow.style.cssText = 'display:flex;flex-wrap:wrap;gap:6px;'
        entitlements.forEach(e => {
            const chip = document.createElement('span')
            chip.textContent = ENTITLEMENT_LABELS[e] || e
            chip.style.cssText = 'padding:3px 10px;border-radius:10px;font-size:11px;font-weight:600;color:var(--accent-primary);background:rgba(6,182,212,0.1);border:1px solid rgba(6,182,212,0.25);'
            chipsRow.appendChild(chip)
        })
        entSection.appendChild(chipsRow)
    } else {
        const note = document.createElement('p')
        note.textContent = 'Free edition — no Pro entitlements'
        note.style.cssText = 'font-size:12px;color:var(--text-dim);'
        entSection.appendChild(note)
    }
    container.appendChild(entSection)

    // --- Import button ---
    const importBtn = document.createElement('button')
    importBtn.textContent = 'Import New License'
    importBtn.className = 'btn btn-sm btn-secondary'
    importBtn.style.width = '100%'
    importBtn.addEventListener('click', importLicenseFromModal)
    container.appendChild(importBtn)
}

async function importLicenseFromModal() {
    try {
        const status = await window.go.main.App.SelectLicenseFile()
        wizardLicenseStatus = status
        renderLicenseBadge(status)
        renderLicenseModalContent(status)
        addLog('launcher', status.valid && status.edition !== 'free'
            ? 'License imported: ' + status.edition
            : 'No license imported')
        // Refresh wizard service groups if the wizard is currently visible
        if (!document.getElementById('firstRunWizard').classList.contains('hidden')) {
            renderWizardLicenseSummary()
            await refreshWizardLicenseAndGroups()
        }
    } catch (err) {
        addLog('launcher', 'License import failed: ' + (err.message || err), 'error')
    }
}

// ============================================================
// First-Run Wizard
// ============================================================

let wizardServiceGroups = [];
let wizardSelectedGroups = [];
let failedPullGroups = [];
let wizardImageStatus = {};
let wizardLicenseStatus = null;
let wizardAccountSaved = false;
let distributionStatus = null;

async function initializeWizard() {
    try {
        const [config, groups, imageStatus, license, distro] = await Promise.all([
            window.go.main.App.GetLauncherConfig(),
            window.go.main.App.GetServiceGroups(),
            window.go.main.App.CheckImagePresence(),
            window.go.main.App.GetLicenseStatus(),
            window.go.main.App.GetDistributionStatus(),
        ]);

        distributionStatus = distro;
        wizardServiceGroups = groups;
        wizardImageStatus = imageStatus || {};
        wizardLicenseStatus = license;
        renderLicenseBadge(license);
        wizardAccountSaved = !!(config.userProfile && config.userProfile.username);

        // Pre-select saved groups if available, filtering locked ones; otherwise default groups
        if (config.selectedGroups && config.selectedGroups.length > 0) {
            wizardSelectedGroups = config.selectedGroups.filter(id => {
                const g = groups.find(grp => grp.id === id);
                return g && !g.locked;
            });
        } else {
            wizardSelectedGroups = groups.filter(g => !g.locked && (g.defaultOn || g.required)).map(g => g.id);
        }

        // Always force-include required groups that aren't downloaded yet — they must be pulled
        for (const g of groups) {
            if (g.required && !g.locked && !wizardImageStatus[g.id] && !wizardSelectedGroups.includes(g.id)) {
                wizardSelectedGroups.push(g.id);
            }
        }

        showWizard();
        populateWizardAccount(config, license);
        renderWizardLicenseSummary();
    } catch (err) {
        console.error('Failed to initialize wizard:', err);
    }
}

function showWizard() {
    document.getElementById('firstRunWizard').classList.remove('hidden');
    document.getElementById('pullProgressContainer').classList.add('hidden');
    document.getElementById('pullSetupBtn').style.display = '';
    document.getElementById('skipPullBtn').style.display = 'none';
    document.getElementById('pullErrorBanner').classList.add('hidden');

    renderWizardServiceCards();
    updateEstimatedSize();
    renderWizardLicenseSummary();
}

function populateWizardAccount(config, license) {
    const username = document.getElementById('wizardUsername');
    const email = document.getElementById('wizardEmail');
    if (!username || !email) return;

    if (config.userProfile && config.userProfile.username) {
        username.value = config.userProfile.username;
        email.value = config.userProfile.email || '';
        return;
    }

    username.value = 'admin';
    if (license && license.valid && license.customerName) {
        email.value = '';
    }
}

function renderWizardLicenseSummary() {
    const summary = document.getElementById('wizardLicenseSummary');
    if (!summary) return;
    if (distributionStatus && distributionStatus.needsInstall) {
        summary.textContent = 'First run will install the Ligand-X runtime files, then download selected services.';
        return;
    }
    const license = wizardLicenseStatus;
    if (!license || !license.valid || license.edition === 'free') {
        summary.textContent = 'Free edition. Import an Academic or Pro license to unlock Pro services.';
        return;
    }
    const owner = license.customerName ? `${license.customerName} · ` : '';
    const expiry = license.expiresAt ? ` · Expires ${license.expiresAt.slice(0, 10)}` : '';
    const count = Array.isArray(license.entitlements) ? license.entitlements.length : 0;
    summary.textContent = `${owner}${license.edition.toUpperCase()} · ${count} entitlement${count === 1 ? '' : 's'}${expiry}`;
}

async function refreshWizardLicenseAndGroups() {
    const [groups, license] = await Promise.all([
        window.go.main.App.GetServiceGroups(),
        window.go.main.App.GetLicenseStatus(),
    ]);
    wizardServiceGroups = groups;
    wizardLicenseStatus = license;
    wizardSelectedGroups = wizardSelectedGroups.filter(id => {
        const group = groups.find(g => g.id === id);
        return group && !group.locked;
    });
    for (const group of groups) {
        if (!group.locked && (group.required || group.defaultOn) && !wizardSelectedGroups.includes(group.id)) {
            wizardSelectedGroups.push(group.id);
        }
    }
    renderWizardLicenseSummary();
    renderWizardServiceCards();
    updateEstimatedSize();
}

async function importWizardLicense() {
    try {
        const status = await window.go.main.App.SelectLicenseFile();
        wizardLicenseStatus = status;
        renderLicenseBadge(status);
        renderWizardLicenseSummary();
        await refreshWizardLicenseAndGroups();
        addLog('launcher', status.valid && status.edition !== 'free' ? `License imported: ${status.edition}` : 'No license imported');
    } catch (err) {
        setWizardSetupError(`License import failed: ${err.message || err}`);
    }
}

function setWizardSetupError(message) {
    const errorBanner = document.getElementById('pullErrorBanner');
    const errorMsg = document.getElementById('errorMessage');
    errorMsg.textContent = message;
    errorBanner.classList.remove('hidden');
}

async function ensureWizardAccount() {
    const username = document.getElementById('wizardUsername').value.trim();
    const email = document.getElementById('wizardEmail').value.trim();
    const password = document.getElementById('wizardPassword').value;
    const confirm = document.getElementById('wizardPasswordConfirm').value;

    if (wizardAccountSaved && !password && !confirm) {
        return await window.go.main.App.GetLauncherConfig();
    }
    if (!username) {
        throw new Error('Enter a username.');
    }
    if (password.length < 8) {
        throw new Error('Password must be at least 8 characters.');
    }
    if (password !== confirm) {
        throw new Error('Passwords do not match.');
    }

    const config = await window.go.main.App.SaveLocalAccount(username, email, password);
    wizardAccountSaved = true;
    document.getElementById('wizardPassword').value = '';
    document.getElementById('wizardPasswordConfirm').value = '';
    return config;
}

function dismissWizard() {
    document.getElementById('firstRunWizard').classList.add('hidden');
    wizardSelectedGroups = [];
}

function renderWizardServiceCards() {
    const container = document.getElementById('wizardServiceCards');
    while (container.firstChild) {
        container.removeChild(container.firstChild);
    }

    wizardServiceGroups.forEach(group => {
        const isPresent = !!wizardImageStatus[group.id];
        const isLocked = !!group.locked;
        const isDisabled = isLocked || (group.required && !isPresent);
        const isSelected = wizardSelectedGroups.includes(group.id);

        const card = document.createElement('div');
        card.className = `wizard-card ${isDisabled ? 'disabled' : ''}`;
        if (!isDisabled) {
            card.onclick = () => toggleWizardGroup(group.id);
        }

        const toggle = document.createElement('div');
        toggle.className = `wizard-card-toggle ${isSelected ? 'checked' : ''} ${isDisabled ? 'disabled' : ''}`;

        const info = document.createElement('div');
        info.className = 'wizard-card-info';

        const nameRow = document.createElement('div');
        nameRow.style.cssText = 'display:flex;align-items:center;gap:6px;min-width:0;';

        const name = document.createElement('span');
        name.className = 'wizard-card-name';
        name.textContent = group.name;
        nameRow.appendChild(name);

        if (isLocked) {
            const badge = document.createElement('span');
            badge.textContent = 'Pro';
            badge.style.cssText = 'font-size:10px;font-weight:600;color:var(--accent-warning);background:rgba(245,158,11,0.12);border:1px solid rgba(245,158,11,0.3);border-radius:4px;padding:1px 6px;white-space:nowrap;flex-shrink:0;';
            nameRow.appendChild(badge);
        } else if (isPresent) {
            const badge = document.createElement('span');
            badge.textContent = 'on disk';
            badge.style.cssText = 'font-size:10px;font-weight:600;color:var(--accent-success);background:rgba(34,197,94,0.12);border:1px solid rgba(34,197,94,0.3);border-radius:4px;padding:1px 6px;white-space:nowrap;flex-shrink:0;';
            nameRow.appendChild(badge);
        }

        // Spacer pushes size to the right
        const spacer = document.createElement('span');
        spacer.style.flex = '1';
        nameRow.appendChild(spacer);

        if (!isPresent && !isLocked && group.sizeMb) {
            const sizeEl = document.createElement('span');
            sizeEl.style.cssText = 'font-size:11px;color:var(--text-muted);font-family:var(--font-mono);white-space:nowrap;flex-shrink:0;';
            sizeEl.textContent = '~' + (group.sizeMb / 1000).toFixed(1) + ' GB';
            nameRow.appendChild(sizeEl);
        }

        const desc = document.createElement('p');
        desc.className = 'wizard-card-desc';
        desc.textContent = group.description;

        info.appendChild(nameRow);
        info.appendChild(desc);

        card.appendChild(toggle);
        card.appendChild(info);
        container.appendChild(card);
    });
}

function toggleWizardGroup(groupId) {
    const idx = wizardSelectedGroups.indexOf(groupId);
    if (idx > -1) {
        wizardSelectedGroups.splice(idx, 1);
    } else {
        wizardSelectedGroups.push(groupId);
    }
    renderWizardServiceCards();
    updateEstimatedSize();
}

function applyWizardPreset(preset) {
    const available = wizardServiceGroups.filter(g => !g.locked).map(g => g.id);
    const required  = wizardServiceGroups.filter(g => g.required && !g.locked).map(g => g.id);
    const include = (...ids) => [...new Set([...required, ...ids])].filter(id => available.includes(id));

    switch (preset) {
        case 'minimal':
            wizardSelectedGroups = [...required];
            break;
        case 'docking-md':
            wizardSelectedGroups = include('docking', 'md');
            break;
        case 'discovery':
            wizardSelectedGroups = include('docking', 'md', 'admet', 'boltz2');
            break;
        case 'everything':
            wizardSelectedGroups = [...available];
            break;
    }
    renderWizardServiceCards();
    updateEstimatedSize();
}

function updateEstimatedSize() {
    // Determine which selected groups still need downloading
    const needDownload = wizardSelectedGroups.filter(id => !wizardImageStatus[id]);
    let total = 0;
    wizardServiceGroups.forEach(group => {
        if (needDownload.includes(group.id)) {
            total += group.sizeMb;
        }
    });
    const allReady = needDownload.length === 0 && wizardSelectedGroups.length > 0;

    const gb = (total / 1000).toFixed(1);
    document.getElementById('estimatedSize').textContent = gb;
    document.getElementById('downloadSizeInfo').style.display = allReady ? 'none' : '';
    document.getElementById('readyNotice').style.display = allReady ? '' : 'none';

    // Swap action button
    document.getElementById('pullSetupBtn').style.display = allReady ? 'none' : '';
    document.getElementById('skipPullBtn').style.display = allReady ? 'inline-flex' : 'none';
}


async function startWizardPull() {
    try {
        if (!distributionStatus || distributionStatus.needsInstall) {
            addLog('launcher', 'Installing Ligand-X runtime files...');
            distributionStatus = await window.go.main.App.InstallRuntimeBundle();
            await updateProjectPath();
            renderWizardLicenseSummary();
        }
        await ensureWizardAccount();
    } catch (err) {
        setWizardSetupError(err.message || err);
        return;
    }

    // Hide actions, show progress
    document.getElementById('pullSetupBtn').style.display = 'none';
    document.getElementById('skipPullBtn').style.display = 'none';
    document.getElementById('pullProgressContainer').classList.remove('hidden');
    document.getElementById('pullErrorBanner').classList.add('hidden');

    failedPullGroups = [];

    // Clear terminal logs
    const logsContainer = document.getElementById('wizardLogsContainer');
    while (logsContainer.firstChild) {
        logsContainer.removeChild(logsContainer.firstChild);
    }

    // Start pull
    window.go.main.App.PullServiceGroups(wizardSelectedGroups);
}

async function skipWizardPull() {
    // User already has images downloaded — skip straight to saving config
    await saveWizardConfig();
}

function handlePullComplete(data) {

    // Clear pulling state
    window.isPulling = false;

    if (data.success) {
        // Check if this was a wizard pull or a service tab pull
        if (wizardSelectedGroups && wizardSelectedGroups.length > 0 && document.getElementById('firstRunWizard').classList.contains('hidden') === false) {
            // This is a wizard pull
            saveWizardConfig();
        } else {
            // This is a service tab pull - refresh the tab
            if (window.currentPullingGroup) {
                const button = document.querySelector(`[data-group="${window.currentPullingGroup}"] button`);
                if (button) {
                    button.disabled = false;
                    button.textContent = 'Re-pull';
                }

                window.currentPullingGroup = null;
            }

            // Refresh services tab to show updated status
            renderServicesTab();
        }
    } else {
        // Pull failed
        failedPullGroups = data.failedGroups || wizardSelectedGroups;

        // Check if this was a wizard pull or service tab pull
        if (wizardSelectedGroups && wizardSelectedGroups.length > 0 && document.getElementById('firstRunWizard').classList.contains('hidden') === false) {
            // Wizard pull failed
            const errorBanner = document.getElementById('pullErrorBanner');
            const errorMsg = document.getElementById('errorMessage');

            if (data.reason === 'gpu_not_found') {
                errorMsg.textContent = 'NVIDIA GPU not detected. Deselect GPU services and retry.';
            } else {
                errorMsg.textContent = `Failed to pull: ${failedPullGroups.join(', ')}. Check your connection and retry.`;
            }

            errorBanner.classList.remove('hidden');

            // Reset buttons
            document.getElementById('pullSetupBtn').style.display = 'block';
            document.getElementById('skipPullBtn').style.display = 'none';
            document.getElementById('pullProgressContainer').classList.add('hidden');
        } else {
            // Service tab pull failed - re-enable button with error state
            if (window.currentPullingGroup) {
                const button = document.querySelector(`[data-group="${window.currentPullingGroup}"] button`);
                if (button) {
                    button.disabled = false;
                    button.textContent = 'Pull Failed - Retry';
                }

                window.currentPullingGroup = null;
            }

            // Re-enable logs from all services
            window.isPulling = false;
        }
    }
}

function retryPullFailed() {
    startWizardPull();
}

async function saveWizardConfig() {
    try {
        const config = await ensureWizardAccount();
        config.firstRunDone = true;
        config.selectedGroups = wizardSelectedGroups;
        config.license = wizardLicenseStatus || config.license;
        renderLicenseBadge(wizardLicenseStatus || { edition: 'free' });
        config.configVersion = 2;

        await window.go.main.App.SaveLauncherConfig(config);

        // Close wizard
        const wizard = document.getElementById('firstRunWizard');
        wizard.classList.add('hidden');

        // Clear wizard selections so future pulls are not confused as wizard pulls
        wizardSelectedGroups = [];

        // Refresh status panel and services tab
        await updateStatus();
        await renderServicesTab();
    } catch (err) {
        setWizardSetupError(err.message || String(err));
    }
}

// ============================================================
// Services Tab
// ============================================================

async function renderServicesTab() {
    try {
        const container = document.getElementById('servicesTabContent');

        // Fetch data with timeout (5 seconds)
        const fetchPromise = Promise.all([
            window.go.main.App.GetServiceGroups(),
            window.go.main.App.CheckImagePresence(),
            window.go.main.App.GetLauncherConfig(),
            window.go.main.App.CheckReinventModels(),
        ]);

        const timeoutPromise = new Promise((_, reject) =>
            setTimeout(() => reject(new Error('Backend request timeout')), 5000)
        );

        const [allGroups, imageStatus, config, reinventModelsPresent] = await Promise.race([fetchPromise, timeoutPromise]);
        reinventModelsReady = reinventModelsPresent;

        // Sync in-memory selection on first load:
        // prefer saved config (filtering out locked groups), otherwise default to all pulled groups
        if (serviceTabSelection.length === 0) {
            if (config.selectedGroups && config.selectedGroups.length > 0) {
                serviceTabSelection = config.selectedGroups.filter(id => {
                    const g = allGroups.find(grp => grp.id === id);
                    return g && !g.locked;
                });
            } else {
                serviceTabSelection = allGroups.filter(g => imageStatus[g.id] && !g.locked).map(g => g.id);
            }
        }

        // Clear container
        while (container.firstChild) {
            container.removeChild(container.firstChild);
        }

        const grid = document.createElement('div');
        grid.style.cssText = 'display: flex; flex-direction: column; gap: 12px;';

        const badgeBase = 'width: 24px; height: 24px; color: white; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-weight: bold; font-size: 14px;';

        allGroups.forEach(group => {
            const isPulled = imageStatus[group.id];
            const isSelected = serviceTabSelection.includes(group.id);
            const isPulling = window.currentPullingGroup === group.id;
            const isLocked = !!group.locked;

            const card = document.createElement('div');
            card.setAttribute('data-group', group.id);
            card.style.cssText = [
                'display: flex', 'align-items: center', 'gap: 12px', 'padding: 12px',
                'background: var(--bg-tertiary)',
                'border: 1px solid ' + (isLocked ? 'var(--accent-warning)' : (isSelected ? 'var(--accent-primary)' : 'var(--border-color)')),
                'border-radius: var(--radius-md)',
                'cursor: ' + (group.required || isLocked ? 'default' : 'pointer'),
                'transition: border-color 0.15s ease'
            ].join('; ') + ';';

            if (!group.required && !isLocked) {
                card.addEventListener('click', (e) => {
                    if (e.target.tagName === 'BUTTON' || e.target.closest('button')) return;
                    toggleServiceSelection(group.id);
                });
            }

            // Status badge (pulled / not pulled / pulling indicator)
            const badge = document.createElement('div');
            if (isPulling) {
                badge.textContent = '⟳';
                badge.style.cssText = 'width: 24px; height: 24px; background: var(--accent-warning); color: white; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-weight: bold; font-size: 14px; animation: spin 0.8s linear infinite;';
            } else if (isLocked) {
                badge.textContent = 'PRO';
                badge.style.cssText = 'min-width: 34px; height: 24px; padding: 0 6px; background: var(--accent-warning); color: white; border-radius: 12px; display: flex; align-items: center; justify-content: center; font-weight: bold; font-size: 10px;';
            } else if (isPulled) {
                badge.textContent = '✓';
                badge.style.cssText = badgeBase + ' background: var(--accent-success);';
            } else {
                badge.textContent = '✗';
                badge.style.cssText = badgeBase + ' background: var(--accent-danger);';
            }

            // Info
            const info = document.createElement('div');
            info.style.cssText = 'flex: 1; min-width: 0;';

            const name = document.createElement('p');
            name.style.cssText = 'margin: 0; font-size: 14px; font-weight: 500; color: var(--text-primary);';
            name.textContent = group.name;

            const desc = document.createElement('p');
            desc.style.cssText = 'margin: 4px 0 0; font-size: 12px; color: var(--text-muted);';
            desc.textContent = isLocked ? `${group.description} · License required` : group.description;

            info.appendChild(name);
            info.appendChild(desc);

            // Pull button
            const button = document.createElement('button');
            button.className = 'btn btn-sm btn-secondary';
            button.style.flexShrink = '0';

            if (isLocked) {
                button.textContent = 'Locked';
                button.disabled = true;
            } else if (isPulling) {
                button.textContent = 'Pulling...';
                button.disabled = true;
            } else {
                button.textContent = isPulled ? 'Re-pull' : 'Pull';
                button.disabled = false;
                button.onclick = (e) => { e.stopPropagation(); pullServiceGroup(group.id); };
            }

            card.appendChild(badge);
            card.appendChild(info);
            card.appendChild(button);

            if (isPulled && !isPulling) {
                const deleteBtn = document.createElement('button');
                deleteBtn.className = 'btn btn-sm btn-danger';
                deleteBtn.title = 'Delete image';
                deleteBtn.style.flexShrink = '0';
                const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
                svg.setAttribute('viewBox', '0 0 24 24');
                svg.setAttribute('fill', 'none');
                svg.setAttribute('stroke', 'currentColor');
                svg.setAttribute('stroke-width', '2');
                svg.setAttribute('width', '14');
                svg.setAttribute('height', '14');
                const pl = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
                pl.setAttribute('points', '3,6 5,6 21,6');
                const p1 = document.createElementNS('http://www.w3.org/2000/svg', 'path');
                p1.setAttribute('d', 'M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2');
                svg.appendChild(pl);
                svg.appendChild(p1);
                deleteBtn.appendChild(svg);
                deleteBtn.onclick = (e) => { e.stopPropagation(); deleteServiceGroupImages(group.id); };
                card.appendChild(deleteBtn);
            }

            // For the REINVENT group, add a model download row below the main card row
            if (group.id === 'reinvent') {
                const modelRow = document.createElement('div');
                modelRow.style.cssText = 'display:flex;align-items:center;gap:10px;margin-top:8px;padding-top:8px;border-top:1px solid var(--border-color);width:100%;';

                const modelLabel = document.createElement('span');
                modelLabel.style.cssText = 'font-size:12px;color:var(--text-muted);flex:1;';

                if (reinventModelDownloading) {
                    modelLabel.textContent = 'Downloading prior model...';
                    const progressWrap = document.createElement('div');
                    progressWrap.style.cssText = 'flex:1;';
                    const progressBg = document.createElement('div');
                    progressBg.style.cssText = 'height:4px;background:var(--border-color);border-radius:2px;overflow:hidden;margin-bottom:2px;';
                    const progressBar = document.createElement('div');
                    progressBar.id = 'reinventModelBar';
                    progressBar.style.cssText = 'height:100%;width:0%;background:var(--accent-primary);transition:width 0.2s;';
                    progressBg.appendChild(progressBar);
                    const progressLabel = document.createElement('span');
                    progressLabel.id = 'reinventModelLabel';
                    progressLabel.style.cssText = 'font-size:10px;color:var(--text-muted);';
                    progressLabel.textContent = '0 MB';
                    progressWrap.appendChild(progressBg);
                    progressWrap.appendChild(progressLabel);
                    modelRow.appendChild(modelLabel);
                    modelRow.appendChild(progressWrap);
                } else if (reinventModelsReady) {
                    modelLabel.textContent = 'Prior model: ready';
                    modelLabel.style.color = 'var(--accent-success)';
                    const redownloadBtn = document.createElement('button');
                    redownloadBtn.className = 'btn btn-sm btn-secondary';
                    redownloadBtn.textContent = 'Re-download';
                    redownloadBtn.onclick = (e) => { e.stopPropagation(); downloadReinventModels(); };
                    modelRow.appendChild(modelLabel);
                    modelRow.appendChild(redownloadBtn);
                } else if (reinventModelError) {
                    modelLabel.style.cssText = 'font-size:11px;color:var(--accent-danger);flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';
                    modelLabel.title = reinventModelError;
                    modelLabel.textContent = '✗ ' + reinventModelError;
                    const retryBtn = document.createElement('button');
                    retryBtn.className = 'btn btn-sm btn-primary';
                    retryBtn.textContent = 'Retry';
                    retryBtn.onclick = (e) => { e.stopPropagation(); downloadReinventModels(); };
                    modelRow.appendChild(modelLabel);
                    modelRow.appendChild(retryBtn);
                } else {
                    modelLabel.textContent = 'Prior model: not downloaded';
                    modelLabel.style.color = 'var(--accent-warning)';
                    const dlBtn = document.createElement('button');
                    dlBtn.className = 'btn btn-sm btn-primary';
                    dlBtn.textContent = 'Download Model';
                    dlBtn.onclick = (e) => { e.stopPropagation(); downloadReinventModels(); };
                    modelRow.appendChild(modelLabel);
                    modelRow.appendChild(dlBtn);
                }

                // modelRow spans the full card width below the flex row
                card.style.flexWrap = 'wrap';
                modelRow.style.flexBasis = '100%';
                card.appendChild(modelRow);
            }

            grid.appendChild(card);
        });

        container.appendChild(grid);
    } catch (err) {
        console.error('Failed to render Services tab:', err);
        const container = document.getElementById('servicesTabContent');
        container.textContent = 'Error loading services: ' + err.message || err;
    }
}

async function toggleServiceSelection(groupId) {
    const idx = serviceTabSelection.indexOf(groupId);
    if (idx > -1) {
        serviceTabSelection.splice(idx, 1);
    } else {
        serviceTabSelection.push(groupId);
    }

    // Persist to config
    try {
        const config = await window.go.main.App.GetLauncherConfig();
        config.selectedGroups = serviceTabSelection.slice();
        await window.go.main.App.SaveLauncherConfig(config);
    } catch (err) {
        console.error('Failed to save selection:', err);
    }

    renderServicesTab();
}

async function restartServiceGroup(groupId) {
    try {
        addLog('launcher', 'Restarting ' + groupId + '...');
        await window.go.main.App.RestartServiceGroups([groupId]);
        addLog('launcher', groupId + ' restarted');
    } catch (err) {
        addLog('launcher', 'Error restarting ' + groupId + ': ' + (err.message || err), 'error');
    }
}

async function deleteServiceGroupImages(groupId) {
    try {
        addLog('launcher', 'Deleting images for ' + groupId + '...');
        await window.go.main.App.DeleteServiceGroupImages(groupId);
        addLog('launcher', 'Images deleted for ' + groupId);
    } catch (err) {
        addLog('launcher', 'Error deleting images: ' + err.message || err, 'error');
    } finally {
        renderServicesTab();
    }
}

async function pullServiceGroup(groupId) {
    // Find and disable the button for this group
    const button = document.querySelector(`[data-group="${groupId}"] button`);
    if (button) {
        button.disabled = true;
        button.textContent = 'Pulling...';
    }

    // Store which group we're pulling for completion handling
    window.currentPullingGroup = groupId;
    window.isPulling = true;

    // Re-render so spinner and "Pulling..." button appear immediately
    await renderServicesTab();

    // Start pull
    window.go.main.App.PullServiceGroups([groupId]);
}

// ============================================================
// Config Tab
// ============================================================

function onEnvModeChange() {
    const configTab = document.querySelector('.tab-content[data-tab="config"]');
    if (configTab && configTab.classList.contains('active')) {
        loadEnvConfig();
    }
}

async function loadEnvConfig() {
    const mode = document.getElementById('envMode').value;
    const editor = document.getElementById('envEditor');
    try {
        const content = await window.go.main.App.GetEnvContent(mode);
        editor.value = content;
    } catch (err) {
        addLog('launcher', `Error loading .env: ${err.message || err}`, 'error');
    }
}

async function saveEnvConfig() {
    const mode = document.getElementById('envMode').value;
    const content = document.getElementById('envEditor').value;
    try {
        await window.go.main.App.SaveEnvContent(mode, content);
        addLog('launcher', `.env${mode === 'prod' ? '.production' : ''} saved successfully`);
    } catch (err) {
        addLog('launcher', `Error saving .env: ${err.message || err}`, 'error');
    }
}

// ============================================================
// REINVENT Model Download
// ============================================================

let reinventModelDownloading = false;
let reinventModelsReady = false;
let reinventModelError = null;

function handleReinventModelProgress(data) {
    reinventModelDownloading = true;
    const bar = document.getElementById('reinventModelBar');
    const label = document.getElementById('reinventModelLabel');
    if (bar) bar.style.width = data.percent.toFixed(1) + '%';
    if (label) {
        const mb = (data.bytesDone / 1024 / 1024).toFixed(1);
        const total = data.bytesTotal > 0 ? ' / ' + (data.bytesTotal / 1024 / 1024).toFixed(1) + ' MB' : '';
        label.textContent = mb + ' MB' + total + ' (' + data.percent.toFixed(0) + '%)';
    }
}

function handleReinventModelComplete(data) {
    reinventModelDownloading = false;
    reinventModelsReady = data.success;
    reinventModelError = data.success ? null : (data.error || 'Download failed');
    if (!data.success) {
        addLog('launcher', 'REINVENT model download failed: ' + reinventModelError, 'error');
    }
    renderServicesTab();
}

async function downloadReinventModels() {
    reinventModelError = null;
    reinventModelDownloading = true;
    reinventModelsReady = false;
    // Start download first, then re-render so the goroutine is already running
    // before we do the async render (avoids race where fast errors fire before render)
    window.go.main.App.DownloadReinventModels();
    renderServicesTab();
}

// Cleanup on unload
window.addEventListener('beforeunload', () => {
    if (statusInterval) {
        clearInterval(statusInterval);
    }
});


// ============================================================
// Workbench redesign glue
// ============================================================
const WORKBENCH_CONTAINERS = [
    { category: 'Core', service: 'Gateway', name: 'ligand-x-gateway', image: 'ligandx/gateway:local', port: '8000' },
    { category: 'Core', service: 'Frontend', name: 'ligand-x-frontend', image: 'ligandx/frontend:local', port: '3000' },
    { category: 'Core', service: 'PostgreSQL', name: 'ligand-x-postgres', image: 'postgres:16', port: '5432' },
    { category: 'Core', service: 'Redis', name: 'ligand-x-redis', image: 'redis:7', port: '6379' },
    { category: 'Compute', service: 'Worker CPU', name: 'ligand-x-worker-cpu', image: 'ligandx/worker:cpu', port: '-' },
    { category: 'Compute', service: 'Worker GPU', name: 'ligand-x-worker-gpu-short', image: 'ligandx/worker:gpu', port: '-' },
    { category: 'Prediction', service: 'Docking', name: 'ligand-x-docking', image: 'ligandx/docking:local', port: '8011' },
    { category: 'Prediction', service: 'MD', name: 'ligand-x-md', image: 'ligandx/md:local', port: '8012' },
    { category: 'Tools', service: 'Ketcher', name: 'ligand-x-ketcher', image: 'ligandx/ketcher:local', port: '8016' },
];
const WIZARD_STEPS = ['license', 'account', 'services', 'review'];
let currentWizardStep = 'license';
let lastServiceGroups = [];
let lastImageStatus = {};
let lastSystemStatus = null;

function setupTabSwitching() {
    document.querySelectorAll('.tab-button').forEach(button => {
        button.addEventListener('click', async () => {
            activateWorkbenchTab(button.getAttribute('data-tab'), button.textContent.trim());
            if (button.getAttribute('data-tab') === 'config') await loadEnvConfig();
            if (button.getAttribute('data-tab') === 'overview') await renderServicesTab();
        });
    });
    document.querySelectorAll('.wizard-step').forEach(button => {
        button.addEventListener('click', () => setWizardStep(button.dataset.step));
    });
    const paletteInput = document.getElementById('paletteInput');
    if (paletteInput) paletteInput.addEventListener('input', renderPaletteResults);
    document.addEventListener('keydown', event => {
        const isPaletteShortcut = (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k';
        if (isPaletteShortcut) {
            event.preventDefault();
            toggleCommandPalette();
        }
        if (event.key === 'Escape') {
            toggleCommandPalette(false);
            closeContainerDrawer();
            closeLicenseModal();
        }
    });
    renderPaletteResults();
    renderWorkbenchContainers();
}

function activateWorkbenchTab(tabName, label) {
    document.querySelectorAll('.tab-button').forEach(btn => btn.classList.toggle('active', btn.getAttribute('data-tab') === tabName));
    document.querySelectorAll('.tab-content').forEach(content => content.classList.toggle('active', content.getAttribute('data-tab') === tabName));
    const crumb = document.getElementById('breadcrumbCurrent');
    if (crumb) crumb.textContent = label || tabName;
}

async function updateProjectPath() {
    try {
        const path = await window.go.main.App.GetProjectPath();
        ['projectPath', 'projectPathStatus'].forEach(id => {
            const el = document.getElementById(id);
            if (el) el.textContent = path || 'Not set';
        });
    } catch (err) {
        ['projectPath', 'projectPathStatus'].forEach(id => {
            const el = document.getElementById(id);
            if (el) el.textContent = 'Error';
        });
    }
}

async function updateStatus() {
    try {
        const status = await window.go.main.App.GetSystemStatus();
        lastSystemStatus = status;
        updateDockerStatus(status.dockerRunning, '');
        const total = Number(status.totalRunning || 0);
        const running = document.getElementById('runningCount');
        if (running) running.textContent = total;
        const sub = document.getElementById('containerSubtitle');
        if (sub) sub.textContent = `${total} running services`;
        const statusCount = document.getElementById('statusContainerCount');
        if (statusCount) statusCount.textContent = `${total} containers`;
        renderWorkbenchContainers(status.services || []);
    } catch (err) {
        console.error('Failed to update status:', err);
    }
}

function updateDockerStatus(running, message) {
    const indicator = document.getElementById('dockerStatus');
    if (!indicator) return;
    const dot = indicator.querySelector('.status-dot');
    const text = indicator.querySelector('.status-text');
    if (dot) {
        dot.classList.remove('running', 'stopped');
        dot.classList.add(running ? 'running' : 'stopped');
    }
    if (text) text.textContent = running ? 'Docker Running' : 'Docker Not Running';
    ['startBtn', 'stopBtn', 'restartBtn'].forEach(id => {
        const btn = document.getElementById(id);
        if (btn) btn.disabled = !running;
    });
}

function updateTunnelUI(status) {
    const row = document.getElementById('tunnelRow');
    if (!row) return;
    if (!status.enabled) {
        row.style.display = 'none';
        return;
    }
    row.style.display = '';
    const dot = document.getElementById('tunnelDot');
    const text = document.getElementById('tunnelStatusText');
    const openBtn = document.getElementById('tunnelOpenBtn');
    const toggleBtn = document.getElementById('tunnelToggleBtn');
    _tunnelRunning = status.running;
    if (dot) {
        dot.classList.remove('running', 'external', 'stopped', 'error');
        dot.classList.add(status.running ? (status.managed ? 'running' : 'external') : 'stopped');
    }
    if (text) text.textContent = status.message || (status.running ? 'Tunnel running' : 'Tunnel stopped');
    if (openBtn) openBtn.disabled = !status.running;
    if (toggleBtn) {
        const label = toggleBtn.querySelector('span');
        if (label) label.textContent = status.running ? 'Tunnel on' : 'Tunnel';
        toggleBtn.disabled = false;
    }
}

async function renderServicesTab() {
    const container = document.getElementById('servicesTabContent');
    if (!container) return;
    try {
        const fetchPromise = Promise.all([
            window.go.main.App.GetServiceGroups(),
            window.go.main.App.CheckImagePresence(),
            window.go.main.App.GetLauncherConfig(),
            window.go.main.App.CheckReinventModels(),
        ]);
        const timeoutPromise = new Promise((_, reject) => setTimeout(() => reject(new Error('Backend request timeout')), 5000));
        const [allGroups, imageStatus, config, reinventModelsPresent] = await Promise.race([fetchPromise, timeoutPromise]);
        lastServiceGroups = allGroups || [];
        lastImageStatus = imageStatus || {};
        reinventModelsReady = reinventModelsPresent;
        if (serviceTabSelection.length === 0) {
            serviceTabSelection = (config.selectedGroups && config.selectedGroups.length > 0)
                ? config.selectedGroups.filter(id => (allGroups || []).some(g => g.id === id && !g.locked))
                : (allGroups || []).filter(g => imageStatus[g.id] && !g.locked).map(g => g.id);
        }
        renderServiceTree(allGroups || [], imageStatus || {});
        container.innerHTML = renderServiceTable(allGroups || [], imageStatus || {});
        container.querySelectorAll('[data-open-service]').forEach(row => {
            row.addEventListener('click', event => {
                if (event.target.closest('button')) return;
                openServiceDetail(row.dataset.openService);
            });
        });
        container.querySelectorAll('[data-toggle-service]').forEach(button => {
            button.addEventListener('click', event => {
                event.stopPropagation();
                toggleServiceSelection(button.dataset.toggleService);
            });
        });
        container.querySelectorAll('[data-pull-service]').forEach(button => {
            button.addEventListener('click', event => {
                event.stopPropagation();
                pullServiceGroup(button.dataset.pullService);
            });
        });
        container.querySelectorAll('[data-delete-service]').forEach(button => {
            button.addEventListener('click', event => {
                event.stopPropagation();
                deleteServiceGroupImages(button.dataset.deleteService);
            });
        });
        container.querySelectorAll('[data-restart-service]').forEach(button => {
            button.addEventListener('click', event => {
                event.stopPropagation();
                restartServiceGroup(button.dataset.restartService);
            });
        });
        renderContainerResourceTable();
        renderPaletteResults();
    } catch (err) {
        console.error('Failed to render Services tab:', err);
        container.textContent = 'Error loading services: ' + (err.message || err);
    }
}

function renderServiceTable(groups, imageStatus) {
    const rows = groups.map(group => {
        const pulled = !!imageStatus[group.id];
        const selected = serviceTabSelection.includes(group.id);
        const locked = !!group.locked;
        const tier = locked ? '<span class="tag pro">Pro</span>' : group.required ? '<span class="tag">Required</span>' : '<span class="tag">Optional</span>';
        const size = group.sizeMb ? `${(group.sizeMb / 1000).toFixed(1)} GB` : '-';
        return `<tr data-open-service="${escapeHtml(group.id)}">
            <td><input type="checkbox" ${selected ? 'checked' : ''} ${locked || group.required ? 'disabled' : ''} data-toggle-service="${escapeHtml(group.id)}"> ${escapeHtml(group.name)}</td>
            <td>${escapeHtml(group.category || 'Service')}</td>
            <td>${group.containerCount || inferContainerCount(group.id)}</td>
            <td>${size}</td>
            <td>${tier}</td>
            <td><span class="status-chip">${pulled ? 'on disk' : 'missing'}</span></td>
            <td><button class="btn btn-sm btn-secondary" ${locked ? 'disabled' : ''} data-restart-service="${escapeHtml(group.id)}">Restart</button> <button class="btn btn-sm btn-secondary" ${locked ? 'disabled' : ''} data-pull-service="${escapeHtml(group.id)}">${pulled ? 'Re-pull' : 'Pull'}</button> ${pulled ? `<button class="btn btn-sm btn-danger" data-delete-service="${escapeHtml(group.id)}">Uninstall</button>` : ''}</td>
        </tr>`;
    }).join('');
    const totalGb = groups.reduce((sum, group) => sum + (serviceTabSelection.includes(group.id) ? (group.sizeMb || 0) : 0), 0) / 1000;
    return `<table class="service-table"><thead><tr><th>Service</th><th>Category</th><th>Containers</th><th>Size</th><th>Tier</th><th>Status</th><th>Actions</th></tr></thead><tbody>${rows}</tbody></table><div class="summary-row">${serviceTabSelection.length} selected · ${totalGb.toFixed(1)} GB · ${groups.every(g => lastImageStatus[g.id] || g.locked) ? 'all on disk' : 'downloads available'}</div>`;
}

function inferContainerCount(id) {
    if (['base', 'core'].includes(id)) return 6;
    if (['md', 'docking'].includes(id)) return 2;
    return 1;
}

function renderServiceTree(groups, imageStatus) {
    const root = document.getElementById('serviceTreeRows');
    if (!root) return;
    root.innerHTML = groups.map(group => `<button class="tree-row" data-service-row="${escapeHtml(group.id)}">${escapeHtml(group.name)}${group.locked ? '<span class="badge-pro">Pro</span>' : ''}<span class="tree-meta">${imageStatus[group.id] ? 'disk' : 'off'}</span></button>`).join('');
    root.querySelectorAll('[data-service-row]').forEach(button => {
        button.addEventListener('click', () => openServiceDetail(button.dataset.serviceRow));
    });
}

function renderWorkbenchContainers(runningServices = []) {
    const root = document.getElementById('containerTreeRows');
    if (!root) return;
    const runningNames = new Set(runningServices.filter(s => s.running).map(s => String(s.name).toLowerCase()));
    const categories = [...new Set(WORKBENCH_CONTAINERS.map(c => c.category))];
    root.innerHTML = categories.map(category => {
        const rows = WORKBENCH_CONTAINERS.filter(c => c.category === category).map(container => {
            const isRunning = runningNames.size === 0 || runningNames.has(container.service.toLowerCase()) || runningNames.has(container.name.toLowerCase());
            return `<button class="tree-row" data-container-name="${escapeHtml(container.name)}"><span class="status-dot ${isRunning ? 'running' : 'stopped'}"></span> ${escapeHtml(container.name)} <span class="tree-meta">${escapeHtml(container.port)}</span></button>`;
        }).join('');
        return `<button class="tree-heading">${category}</button>${rows}`;
    }).join('');
    root.querySelectorAll('[data-container-name]').forEach(button => {
        button.addEventListener('click', () => openContainerDrawer(button.dataset.containerName));
    });
    renderContainerResourceTable();
}

function renderContainerResourceTable() {
    const root = document.getElementById('containerResourceTable');
    if (!root) return;
    root.innerHTML = `<table class="container-table"><thead><tr><th>Container name</th><th>Image</th><th>Port</th><th>CPU%</th><th>Memory</th><th>GPU</th><th>Uptime</th><th>Actions</th></tr></thead><tbody>${WORKBENCH_CONTAINERS.map((c, index) => `<tr onclick="openContainerDrawer('${escapeHtml(c.name)}')"><td>${escapeHtml(c.name)}</td><td>${escapeHtml(c.image)}</td><td>${escapeHtml(c.port)}</td><td>${(8 + index * 2) % 33}</td><td>${256 + index * 96} MB</td><td>${c.category === 'Compute' ? '12%' : '-'}</td><td>${index + 1}h ${index * 7}m</td><td><button class="btn btn-sm btn-secondary">Logs</button></td></tr>`).join('')}</tbody></table>`;
}

function openServiceDetail(groupId) {
    const group = lastServiceGroups.find(g => g.id === groupId) || { id: groupId, name: groupId, description: 'Ligand-X service group' };
    const container = document.getElementById('servicesTabContent');
    activateWorkbenchTab('overview', group.name);
    if (!container) return;
    const related = WORKBENCH_CONTAINERS.filter(c => c.service.toLowerCase().includes(group.id) || group.name.toLowerCase().includes(c.service.toLowerCase().split(' ')[0]));
    const rows = (related.length ? related : WORKBENCH_CONTAINERS.slice(0, 3)).map(c => `<tr onclick="openContainerDrawer('${escapeHtml(c.name)}')"><td>${escapeHtml(c.name)}</td><td>${escapeHtml(c.image)}</td><td>${escapeHtml(c.port)}</td><td>--</td><td>-</td><td>-</td><td>-</td><td><button class="btn btn-sm btn-secondary">Logs</button></td></tr>`).join('');
    container.innerHTML = `<div class="service-detail"><div class="service-detail-header"><div><h2>${escapeHtml(group.name)}</h2><p>${escapeHtml(group.description || '')}</p></div><div><button class="btn btn-sm btn-secondary" onclick="restartServices()">Restart</button> <button class="btn btn-sm btn-secondary" onclick="pullServiceGroup('${escapeHtml(group.id)}')">Re-pull</button> <button class="btn btn-sm btn-danger" onclick="deleteServiceGroupImages('${escapeHtml(group.id)}')">Uninstall</button></div></div><div class="metric-grid compact"><div class="metric-tile"><label>Containers</label><strong>${related.length || 3}</strong><small>declared</small></div><div class="metric-tile"><label>Size</label><strong>${((group.sizeMb || 1200) / 1000).toFixed(1)} GB</strong><small>image footprint</small></div><div class="metric-tile"><label>Tier</label><strong>${group.locked ? 'Pro' : 'Base'}</strong><small>${lastImageStatus[group.id] ? 'on disk' : 'not downloaded'}</small></div></div><table class="container-table"><thead><tr><th>Container name</th><th>Image</th><th>Port</th><th>CPU%</th><th>Memory</th><th>GPU</th><th>Uptime</th><th>Actions</th></tr></thead><tbody>${rows}</tbody></table></div>`;
}

function openContainerDrawer(name) {
    const container = WORKBENCH_CONTAINERS.find(c => c.name === name) || WORKBENCH_CONTAINERS[0];
    document.getElementById('drawerName').textContent = container.name;
    document.getElementById('drawerService').textContent = container.service;
    document.getElementById('drawerImage').textContent = container.image;
    document.getElementById('drawerPort').textContent = container.port;
    document.getElementById('containerDrawer').classList.remove('hidden');
}

function closeContainerDrawer() {
    const drawer = document.getElementById('containerDrawer');
    if (drawer) drawer.classList.add('hidden');
}

function toggleCommandPalette(force) {
    const overlay = document.getElementById('commandPalette');
    if (!overlay) return;
    const shouldShow = typeof force === 'boolean' ? force : overlay.classList.contains('hidden');
    overlay.classList.toggle('hidden', !shouldShow);
    if (shouldShow) {
        renderPaletteResults();
        const input = document.getElementById('paletteInput');
        if (input) {
            input.value = '';
            input.focus();
        }
    }
}

function renderPaletteResults() {
    const root = document.getElementById('paletteResults');
    if (!root) return;
    const query = (document.getElementById('paletteInput')?.value || '').toLowerCase();
    const actions = [
        { icon: '▶', label: 'Start stack', hint: 'Run selected services', kind: 'Action', run: 'startServices()' },
        { icon: '■', label: 'Stop stack', hint: 'Stop all services', kind: 'Action', run: 'stopServices()' },
        { icon: '↻', label: 'Restart stack', hint: 'Restart selected services', kind: 'Action', run: 'restartServices()' },
        { icon: '↓', label: 'Pull images', hint: 'Download selected service images', kind: 'Action', run: 'pullImages()' },
    ];
    const services = lastServiceGroups.map(g => ({ icon: '◇', label: g.name, hint: g.description || g.id, kind: 'Service', fn: () => openServiceDetail(g.id) }));
    const containers = WORKBENCH_CONTAINERS.map(c => ({ icon: '▣', label: c.name, hint: `${c.service} · ${c.port}`, kind: 'Container', fn: () => openContainerDrawer(c.name) }));
    const items = [...actions, ...services, ...containers].filter(item => !query || `${item.label} ${item.hint} ${item.kind}`.toLowerCase().includes(query));
    root.innerHTML = items.map((item, index) => `<div class="palette-row ${index === 0 ? 'active' : ''}" data-palette-index="${index}"><span>${item.icon}</span><span>${escapeHtml(item.label)} <small>${escapeHtml(item.hint || '')}</small></span><span class="palette-kind">${item.kind}</span></div>`).join('');
    root.querySelectorAll('[data-palette-index]').forEach(row => {
        row.addEventListener('click', () => {
            const item = items[Number(row.dataset.paletteIndex)];
            toggleCommandPalette(false);
            if (item.fn) item.fn();
            if (item.run) window[item.run.replace('()', '')]?.();
        });
    });
}

function toggleTweaks() {
    const panel = document.getElementById('tweaksPanel');
    if (panel) panel.classList.toggle('hidden');
}

function addLog(service, message, type = 'info') {
    const timestamp = new Date().toLocaleTimeString('en-US', { hour12: false });
    logs.push({ timestamp, service, message, type });
    if (logs.length > MAX_LOGS) logs = logs.slice(-MAX_LOGS);
    const container = document.getElementById('logsContainer');
    if (container) {
        const placeholder = container.querySelector('.log-placeholder');
        if (placeholder) placeholder.remove();
        const entry = document.createElement('div');
        entry.className = `log-entry ${type} fade-in`;
        entry.innerHTML = `<span class="log-time">${timestamp}</span><span>${type.toUpperCase()}</span><span class="log-service">${escapeHtml(service)}</span><span class="log-message">${escapeHtml(message)}</span>`;
        container.appendChild(entry);
        container.scrollTop = container.scrollHeight;
        while (container.children.length > MAX_LOGS) container.removeChild(container.firstChild);
    }
    const feed = document.getElementById('activityFeed');
    if (feed) {
        const item = document.createElement('div');
        item.className = `activity-item ${type}`;
        item.innerHTML = `<time>${timestamp}</time><span class="kind-dot"></span><span>${escapeHtml(service)}</span><span>${escapeHtml(message)}</span>`;
        feed.prepend(item);
        while (feed.children.length > 18) feed.removeChild(feed.lastChild);
    }
}

function clearLogs() {
    logs = [];
    const container = document.getElementById('logsContainer');
    if (container) container.innerHTML = '<div class="log-placeholder">Logs will appear here...</div>';
    const feed = document.getElementById('activityFeed');
    if (feed) feed.innerHTML = '';
}

function setWizardStep(step) {
    currentWizardStep = step;
    document.querySelectorAll('.wizard-step').forEach(btn => btn.classList.toggle('active', btn.dataset.step === step));
    document.querySelectorAll('.wizard-page').forEach(page => page.classList.toggle('active', page.dataset.step === step));
    const next = document.getElementById('pullSetupBtn');
    if (next) next.textContent = step === 'review' ? 'Launch Ligand-X' : 'Next';
}

function wizardBack() {
    const idx = WIZARD_STEPS.indexOf(currentWizardStep);
    setWizardStep(WIZARD_STEPS[Math.max(0, idx - 1)]);
}

function wizardNext() {
    const idx = WIZARD_STEPS.indexOf(currentWizardStep);
    if (idx < WIZARD_STEPS.length - 1) {
        setWizardStep(WIZARD_STEPS[idx + 1]);
        return;
    }
    startWizardPull();
}

function showWizard() {
    const wizard = document.getElementById('firstRunWizard');
    if (wizard) wizard.classList.remove('hidden');
    const progress = document.getElementById('pullProgressContainer');
    if (progress) progress.classList.add('hidden');
    const pull = document.getElementById('pullSetupBtn');
    if (pull) {
        pull.style.display = '';
        pull.textContent = 'Next';
    }
    const skip = document.getElementById('skipPullBtn');
    if (skip) skip.style.display = 'none';
    const banner = document.getElementById('pullErrorBanner');
    if (banner) banner.classList.add('hidden');
    setWizardStep('license');
    renderWizardServiceCards();
    updateEstimatedSize();
    renderWizardLicenseSummary();
}

async function cleanDocker() {
    try {
        await window.go.main.App.CleanDocker();
        addLog('launcher', 'Docker cleanup completed');
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    }
}


async function selectProjectFolder() {
    try {
        const path = await window.go.main.App.SelectProjectFolder();
        if (path) {
            ['projectPath', 'projectPathStatus'].forEach(id => {
                const el = document.getElementById(id);
                if (el) el.textContent = path;
            });
            addLog('launcher', `Runtime path set to: ${path}`);
            distributionStatus = await window.go.main.App.GetDistributionStatus();
        }
    } catch (err) {
        addLog('launcher', `Error: ${err.message || err}`, 'error');
    }
}

function onEnvModeChange() {
    const env = document.getElementById('envMode')?.selectedOptions?.[0]?.textContent || 'Production';
    const statusEnv = document.getElementById('statusEnv');
    if (statusEnv) statusEnv.textContent = env;
    const configTab = document.querySelector('.tab-content[data-tab="config"]');
    if (configTab && configTab.classList.contains('active')) loadEnvConfig();
}


// Wizard navigation policy: Launch is only available on the final Review step.
function wizardSelectedNeedsDownload() {
    return wizardSelectedGroups.filter(id => !wizardImageStatus[id]);
}

function syncWizardActionButtons() {
    const next = document.getElementById('pullSetupBtn');
    const legacyLaunch = document.getElementById('skipPullBtn');
    if (legacyLaunch) legacyLaunch.style.display = 'none';
    if (!next) return;
    next.style.display = '';
    next.textContent = currentWizardStep === 'review' ? 'Launch Ligand-X' : 'Next';
}

function setWizardStep(step) {
    currentWizardStep = step;
    document.querySelectorAll('.wizard-step').forEach(btn => btn.classList.toggle('active', btn.dataset.step === step));
    document.querySelectorAll('.wizard-page').forEach(page => page.classList.toggle('active', page.dataset.step === step));
    if (step === 'review') renderWizardReview();
    syncWizardActionButtons();
}

function makeReviewBlock(title, onEdit) {
    const block = document.createElement('div');
    block.className = 'review-block';
    const header = document.createElement('div');
    header.className = 'review-block-header';
    const titleEl = document.createElement('span');
    titleEl.className = 'review-block-title';
    titleEl.textContent = title;
    const editBtn = document.createElement('button');
    editBtn.className = 'btn btn-sm btn-secondary';
    editBtn.textContent = 'Edit';
    editBtn.addEventListener('click', onEdit);
    header.appendChild(titleEl);
    header.appendChild(editBtn);
    block.appendChild(header);
    return block;
}

function renderWizardReview() {
    const page = document.querySelector('.wizard-page[data-step="review"]');
    if (!page) return;
    while (page.firstChild) page.removeChild(page.firstChild);

    const license = wizardLicenseStatus;
    const edition = (license && license.edition) || 'free';

    const section = document.createElement('div');
    section.className = 'review-section';

    // top row: License + Account
    const row = document.createElement('div');
    row.className = 'review-row';

    const licBlock = makeReviewBlock('License', () => setWizardStep('license'));
    const licVal = document.createElement('p');
    licVal.className = 'review-block-value';
    licVal.textContent = edition === 'free' ? 'Free edition' : edition.toUpperCase();
    if (edition !== 'free' && license) {
        const sub = document.createElement('span');
        sub.style.cssText = 'display:block;color:var(--text-muted);font-size:11px;margin-top:2px;';
        sub.textContent = (license.customerName || '') + (license.expiresAt ? ' · expires ' + license.expiresAt.slice(0, 10) : '');
        licVal.appendChild(sub);
    }
    licBlock.appendChild(licVal);
    row.appendChild(licBlock);

    const accBlock = makeReviewBlock('Account', () => setWizardStep('account'));
    const accVal = document.createElement('p');
    accVal.className = 'review-block-value';
    accVal.textContent = document.getElementById('wizardUsername')?.value || '(not set)';
    const emailText = document.getElementById('wizardEmail')?.value || '';
    if (emailText) {
        const emailEl = document.createElement('span');
        emailEl.style.cssText = 'display:block;color:var(--text-muted);font-size:11px;margin-top:2px;';
        emailEl.textContent = emailText;
        accVal.appendChild(emailEl);
    }
    accBlock.appendChild(accVal);
    row.appendChild(accBlock);
    section.appendChild(row);

    // Services block
    const selectedGroups = wizardServiceGroups.filter(g => wizardSelectedGroups.includes(g.id));
    const needDownload   = selectedGroups.filter(g => !wizardImageStatus[g.id]);
    const totalGB = (needDownload.reduce((s, g) => s + (g.sizeMb || 0), 0) / 1000).toFixed(1);

    const svcBlock = makeReviewBlock('Services (' + selectedGroups.length + ' selected)', () => setWizardStep('services'));
    svcBlock.style.marginTop = '10px';

    const list = document.createElement('div');
    list.className = 'review-services-list';
    selectedGroups.forEach(g => {
        const item = document.createElement('div');
        item.className = 'review-service-row';
        const nameEl = document.createElement('span');
        nameEl.textContent = g.name;
        const statusEl = document.createElement('span');
        statusEl.className = 'review-service-status';
        if (wizardImageStatus[g.id]) {
            statusEl.textContent = 'on disk';
            statusEl.style.color = 'var(--accent-success)';
        } else {
            statusEl.textContent = '~' + (g.sizeMb / 1000).toFixed(1) + ' GB';
            statusEl.style.color = 'var(--text-muted)';
        }
        item.appendChild(nameEl);
        item.appendChild(statusEl);
        list.appendChild(item);
    });
    svcBlock.appendChild(list);

    const note = document.createElement('p');
    note.className = 'review-download-note';
    if (needDownload.length > 0) {
        note.textContent = 'Estimated download: ';
        const b = document.createElement('b');
        b.textContent = totalGB + ' GB';
        note.appendChild(b);
    } else {
        note.textContent = 'All selected services already on disk';
        note.style.color = 'var(--accent-success)';
    }
    svcBlock.appendChild(note);
    section.appendChild(svcBlock);

    page.appendChild(section);
}

function updateEstimatedSize() {
    const needDownload = wizardSelectedNeedsDownload();
    let total = 0;
    wizardServiceGroups.forEach(group => {
        if (needDownload.includes(group.id)) total += group.sizeMb;
    });
    const allReady = needDownload.length === 0 && wizardSelectedGroups.length > 0;
    const estimated = document.getElementById('estimatedSize');
    const downloadInfo = document.getElementById('downloadSizeInfo');
    const readyNotice = document.getElementById('readyNotice');
    if (estimated) estimated.textContent = (total / 1000).toFixed(1);
    if (downloadInfo) downloadInfo.style.display = allReady ? 'none' : '';
    if (readyNotice) readyNotice.style.display = allReady ? '' : 'none';
    syncWizardActionButtons();
}

function wizardNext() {
    const idx = WIZARD_STEPS.indexOf(currentWizardStep);
    if (idx < WIZARD_STEPS.length - 1) {
        setWizardStep(WIZARD_STEPS[idx + 1]);
        return;
    }
    if (wizardSelectedNeedsDownload().length === 0 && wizardSelectedGroups.length > 0) {
        skipWizardPull();
        return;
    }
    startWizardPull();
}

function showWizard() {
    const wizard = document.getElementById('firstRunWizard');
    if (wizard) wizard.classList.remove('hidden');
    const progress = document.getElementById('pullProgressContainer');
    if (progress) progress.classList.add('hidden');
    const banner = document.getElementById('pullErrorBanner');
    if (banner) banner.classList.add('hidden');
    setWizardStep('license');
    renderWizardServiceCards();
    updateEstimatedSize();
}


// ============================================================
// Live resource telemetry
// ============================================================
let resourceTelemetryInterval = null;
let latestResourceMetrics = null;
let previousNetSample = null;

const SPARKLINE_POINTS = 30;
const metricHistory = { cpu: [], ram: [], gpu: [], net: [] };
let currentDrawerContainerService = null;

function startResourceTelemetry() {
    if (resourceTelemetryInterval) return;
    updateResourceTelemetry();
    resourceTelemetryInterval = setInterval(updateResourceTelemetry, 3000);
}

function pushHistory(key, value) {
    metricHistory[key].push(Number(value) || 0);
    if (metricHistory[key].length > SPARKLINE_POINTS) metricHistory[key].shift();
}

function drawSparkline(el, data) {
    if (!el || data.length < 2) return;
    const w = el.clientWidth || 80;
    const h = el.clientHeight || 42;
    const max = Math.max(...data, 1);
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
    svg.setAttribute('preserveAspectRatio', 'none');
    svg.setAttribute('width', '100%');
    svg.setAttribute('height', '100%');
    const poly = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
    const pts = data.map((v, i) =>
        `${((i / (data.length - 1)) * w).toFixed(1)},${(h - (v / max) * (h - 2) - 1).toFixed(1)}`
    ).join(' ');
    poly.setAttribute('points', pts);
    poly.setAttribute('fill', 'none');
    poly.setAttribute('stroke', 'currentColor');
    poly.setAttribute('stroke-width', '1.5');
    poly.setAttribute('vector-effect', 'non-scaling-stroke');
    svg.appendChild(poly);
    el.replaceChildren(svg);
}

function updateAllSparklines() {
    const labelToKey = { 'CPU': 'cpu', 'RAM': 'ram', 'GPU': 'gpu', 'Net I/O': 'net', 'Net': 'net' };
    document.querySelectorAll('.metric-tile').forEach(tile => {
        const label = tile.querySelector('label')?.textContent;
        const key = labelToKey[label];
        const el = tile.querySelector('.sparkline');
        if (key && el) drawSparkline(el, metricHistory[key]);
    });
    const meterKeys = ['cpu', 'ram', 'gpu', 'net'];
    document.querySelectorAll('.inspector-meter').forEach((meter, i) => {
        const el = meter.querySelector('.mini-sparkline');
        if (el && meterKeys[i]) drawSparkline(el, metricHistory[meterKeys[i]]);
    });
    document.querySelectorAll('.resource-card').forEach((card, i) => {
        const el = card.querySelector('.wide-sparkline');
        if (el && meterKeys[i]) drawSparkline(el, metricHistory[meterKeys[i]]);
    });
}

async function updateResourceTelemetry() {
    try {
        if (!window.go?.main?.App?.GetResourceMetrics) return;
        const metrics = await window.go.main.App.GetResourceMetrics();
        latestResourceMetrics = metrics;
        renderResourceMetrics(metrics);
    } catch (err) {
        console.error('Failed to update resource metrics:', err);
    }
}

function renderResourceMetrics(metrics) {
    const prevNetSample = previousNetSample ? { ...previousNetSample } : null;
    const netRate = calculateNetRate(metrics);
    setMetricTile('cpuMetric', `${formatPercent(metrics.cpuPercent)}`, metrics.loadAverage ? `load ${metrics.loadAverage}` : 'host CPU');
    setMetricTile('ramMetric', formatBytesJS(metrics.memoryUsedBytes), `${formatPercent(metrics.memoryPercent)} of ${formatBytesJS(metrics.memoryTotalBytes)}`);
    setMetricTile('gpuMetric', metrics.gpuMemoryTotalMb ? `${formatPercent(metrics.gpuPercent)}` : 'n/a', metrics.gpuMemoryTotalMb ? `${metrics.gpuMemoryUsedMb} / ${metrics.gpuMemoryTotalMb} MB VRAM` : 'nvidia-smi unavailable');
    setMetricByLabel('Net I/O', netRate, `rx ${formatBytesJS(metrics.netRxBytes)} · tx ${formatBytesJS(metrics.netTxBytes)}`);
    setMetricByLabel('Disk', formatBytesJS(metrics.diskUsedBytes), `of ${formatBytesJS(metrics.diskTotalBytes)}`);
    setStatusCell('CPU', `CPU ${formatPercent(metrics.cpuPercent)}`);
    setStatusCell('RAM', `RAM ${formatPercent(metrics.memoryPercent)}`);
    setStatusCell('GPU', `GPU ${metrics.gpuMemoryTotalMb ? formatPercent(metrics.gpuPercent) : 'n/a'}`);
    renderInspectorMetrics(metrics, netRate);
    renderResourceCards(metrics, netRate);
    renderContainerResourceTable();

    pushHistory('cpu', metrics.cpuPercent);
    pushHistory('ram', metrics.memoryPercent);
    pushHistory('gpu', metrics.gpuPercent || 0);
    const netTotal = Number(metrics.netRxBytes || 0) + Number(metrics.netTxBytes || 0);
    if (prevNetSample) {
        const bytesPerSec = Math.max(0, netTotal - prevNetSample.total) / Math.max(1, (Date.now() - prevNetSample.at) / 1000);
        pushHistory('net', bytesPerSec);
    }
    updateAllSparklines();
}

function setMetricTile(valueId, value, subtitle) {
    const valueEl = document.getElementById(valueId);
    if (!valueEl) return;
    valueEl.textContent = value;
    const small = valueEl.parentElement?.querySelector('small');
    if (small) small.textContent = subtitle;
}

function setMetricByLabel(label, value, subtitle) {
    document.querySelectorAll('.metric-tile').forEach(tile => {
        if (tile.querySelector('label')?.textContent === label) {
            tile.querySelector('strong').textContent = value;
            tile.querySelector('small').textContent = subtitle;
        }
    });
}

function setStatusCell(prefix, text) {
    document.querySelectorAll('.status-cell').forEach(cell => {
        if (cell.textContent.trim().startsWith(prefix)) cell.textContent = text;
    });
}

function renderInspectorMetrics(metrics, netRate) {
    const meters = document.querySelectorAll('.inspector-meter');
    const values = [formatPercent(metrics.cpuPercent), formatPercent(metrics.memoryPercent), metrics.gpuMemoryTotalMb ? formatPercent(metrics.gpuPercent) : 'n/a', netRate.replace('/s', '')];
    meters.forEach((meter, index) => {
        const value = meter.querySelector('b');
        if (value) value.textContent = values[index] || '-';
    });
}

function renderResourceCards(metrics, netRate) {
    const cards = document.querySelectorAll('.resource-card');
    const data = [
        ['CPU', formatPercent(metrics.cpuPercent), metrics.loadAverage ? `Load average ${metrics.loadAverage}` : 'Host CPU'],
        ['RAM', formatBytesJS(metrics.memoryUsedBytes), `${formatPercent(metrics.memoryPercent)} of ${formatBytesJS(metrics.memoryTotalBytes)}`],
        ['GPU', metrics.gpuMemoryTotalMb ? formatPercent(metrics.gpuPercent) : 'n/a', metrics.gpuMemoryTotalMb ? `${metrics.gpuMemoryUsedMb} / ${metrics.gpuMemoryTotalMb} MB VRAM` : 'nvidia-smi unavailable'],
        ['Net', netRate, `rx ${formatBytesJS(metrics.netRxBytes)} · tx ${formatBytesJS(metrics.netTxBytes)}`],
    ];
    cards.forEach((card, index) => {
        const item = data[index];
        if (!item) return;
        card.querySelector('label').textContent = item[0];
        card.querySelector('strong').textContent = item[1];
        card.querySelector('small').textContent = item[2];
    });
}

function calculateNetRate(metrics) {
    const now = Date.now();
    const total = Number(metrics.netRxBytes || 0) + Number(metrics.netTxBytes || 0);
    if (!previousNetSample) {
        previousNetSample = { total, at: now };
        return '0 B/s';
    }
    const deltaBytes = Math.max(0, total - previousNetSample.total);
    const deltaSeconds = Math.max(1, (now - previousNetSample.at) / 1000);
    previousNetSample = { total, at: now };
    return `${formatBytesJS(deltaBytes / deltaSeconds)}/s`;
}

function formatPercent(value) {
    const n = Number(value || 0);
    return `${Math.max(0, n).toFixed(n >= 10 ? 0 : 1)}%`;
}

function formatBytesJS(value) {
    let n = Number(value || 0);
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) {
        n /= 1024;
        i++;
    }
    if (i === 0) return `${Math.round(n)} ${units[i]}`;
    return `${n.toFixed(n >= 10 ? 1 : 2)} ${units[i]}`;
}

function renderContainerResourceTable() {
    const root = document.getElementById('containerResourceTable');
    if (!root) return;
    const containers = latestResourceMetrics?.containers?.length ? latestResourceMetrics.containers : WORKBENCH_CONTAINERS.map(c => ({
        name: c.name,
        service: c.service,
        image: c.image,
        port: c.port,
        status: 'not detected',
        running: false,
        cpuPercent: 0,
        memoryText: '-',
        gpuPercent: 0,
        uptime: '-',
    }));
    root.innerHTML = `<table class="container-table"><thead><tr><th>Container name</th><th>Image</th><th>Port</th><th>CPU%</th><th>Memory</th><th>GPU</th><th>Uptime</th><th>Actions</th></tr></thead><tbody>${containers.map(c => `<tr onclick="openContainerDrawer('${escapeHtml(c.name)}')"><td><span class="status-dot ${c.running ? 'running' : 'stopped'}"></span> ${escapeHtml(c.name)}</td><td>${escapeHtml(c.image || '-')}</td><td>${escapeHtml(c.port || '-')}</td><td>${formatPercent(c.cpuPercent)}</td><td>${escapeHtml(c.memoryText || '-')}</td><td>${c.gpuPercent ? formatPercent(c.gpuPercent) : '-'}</td><td>${escapeHtml(c.uptime || '-')}</td><td><button class="btn btn-sm btn-secondary">Logs</button></td></tr>`).join('')}</tbody></table>`;
}

function openContainerDrawer(name) {
    const live = latestResourceMetrics?.containers?.find(c => c.name === name);
    const fallback = WORKBENCH_CONTAINERS.find(c => c.name === name) || WORKBENCH_CONTAINERS[0];
    const container = live || fallback;
    document.getElementById('drawerName').textContent = container.name;
    document.getElementById('drawerService').textContent = container.service || fallback.service || '-';
    document.getElementById('drawerImage').textContent = container.image || fallback.image || '-';
    document.getElementById('drawerPort').textContent = container.port || fallback.port || '-';
    const drawer = document.getElementById('containerDrawer');
    if (drawer) {
        const kv = drawer.querySelector('.kv-grid');
        if (kv && live) {
            kv.innerHTML = `<div><label>Status</label><strong>${escapeHtml(live.status)}</strong></div><div><label>Image</label><strong>${escapeHtml(live.image)}</strong></div><div><label>Port</label><strong>${escapeHtml(live.port || '-')}</strong></div><div><label>Uptime</label><strong>${escapeHtml(live.uptime || '-')}</strong></div>`;
        }
        const meters = drawer.querySelectorAll('.meter span');
        if (meters[0]) meters[0].style.width = `${Math.min(100, Number(live?.cpuPercent || 0))}%`;
        const memPct = live?.memoryLimit ? (Number(live.memoryBytes || 0) / Number(live.memoryLimit || 1)) * 100 : 0;
        if (meters[1]) meters[1].style.width = `${Math.min(100, memPct)}%`;
        if (meters[2]) meters[2].style.width = `${Math.min(100, Number(live?.gpuPercent || 0))}%`;
        drawer.classList.remove('hidden');
    }
}

function setupTabSwitching() {
    document.querySelectorAll('.tab-button').forEach(button => {
        button.addEventListener('click', async () => {
            activateWorkbenchTab(button.getAttribute('data-tab'), button.textContent.trim());
            if (button.getAttribute('data-tab') === 'config') await loadEnvConfig();
            if (button.getAttribute('data-tab') === 'overview') await renderServicesTab();
        });
    });
    document.querySelectorAll('.wizard-step').forEach(button => {
        button.addEventListener('click', () => setWizardStep(button.dataset.step));
    });
    const paletteInput = document.getElementById('paletteInput');
    if (paletteInput) paletteInput.addEventListener('input', renderPaletteResults);
    document.addEventListener('keydown', event => {
        const isPaletteShortcut = (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k';
        if (isPaletteShortcut) {
            event.preventDefault();
            toggleCommandPalette();
        }
        if (event.key === 'Escape') {
            toggleCommandPalette(false);
            closeContainerDrawer();
            closeLicenseModal();
        }
    });
    renderPaletteResults();
    renderWorkbenchContainers();
    startResourceTelemetry();
}

window.addEventListener('beforeunload', () => {
    if (resourceTelemetryInterval) clearInterval(resourceTelemetryInterval);
});


function openServiceDetail(groupId) {
    const group = lastServiceGroups.find(g => g.id === groupId) || { id: groupId, name: groupId, description: 'Ligand-X service group' };
    const container = document.getElementById('servicesTabContent');
    activateWorkbenchTab('overview', group.name);
    if (!container) return;
    const liveContainers = latestResourceMetrics?.containers || [];
    let related = liveContainers.filter(c => c.service === group.id || c.service?.includes(group.id) || group.name.toLowerCase().includes(String(c.service || '').toLowerCase()));
    if (related.length === 0) {
        related = WORKBENCH_CONTAINERS
            .filter(c => c.service.toLowerCase().includes(group.id) || group.name.toLowerCase().includes(c.service.toLowerCase().split(' ')[0]))
            .map(c => ({ name: c.name, image: c.image, port: c.port, cpuPercent: 0, memoryText: '-', gpuPercent: 0, uptime: '-', running: false }));
    }
    const rows = (related.length ? related : []).map(c => `<tr onclick="openContainerDrawer('${escapeHtml(c.name)}')"><td><span class="status-dot ${c.running ? 'running' : 'stopped'}"></span> ${escapeHtml(c.name)}</td><td>${escapeHtml(c.image || '-')}</td><td>${escapeHtml(c.port || '-')}</td><td>${formatPercent(c.cpuPercent)}</td><td>${escapeHtml(c.memoryText || '-')}</td><td>${c.gpuPercent ? formatPercent(c.gpuPercent) : '-'}</td><td>${escapeHtml(c.uptime || '-')}</td><td><button class="btn btn-sm btn-secondary">Logs</button></td></tr>`).join('');
    const memoryBytes = related.reduce((sum, c) => sum + Number(c.memoryBytes || 0), 0);
    const maxCPU = related.reduce((max, c) => Math.max(max, Number(c.cpuPercent || 0)), 0);
    container.innerHTML = `<div class="service-detail"><div class="service-detail-header"><div><h2>${escapeHtml(group.name)}</h2><p>${escapeHtml(group.description || '')}</p></div><div><button class="btn btn-sm btn-secondary" onclick="restartServices()">Restart</button> <button class="btn btn-sm btn-secondary" onclick="pullServiceGroup('${escapeHtml(group.id)}')">Re-pull</button> <button class="btn btn-sm btn-danger" onclick="deleteServiceGroupImages('${escapeHtml(group.id)}')">Uninstall</button></div></div><div class="metric-grid compact"><div class="metric-tile"><label>Containers</label><strong>${related.length}</strong><small>${related.filter(c => c.running).length} running</small></div><div class="metric-tile"><label>Memory</label><strong>${formatBytesJS(memoryBytes)}</strong><small>current usage</small></div><div class="metric-tile"><label>Peak CPU</label><strong>${formatPercent(maxCPU)}</strong><small>${lastImageStatus[group.id] ? 'image on disk' : 'image missing'}</small></div></div><table class="container-table"><thead><tr><th>Container name</th><th>Image</th><th>Port</th><th>CPU%</th><th>Memory</th><th>GPU</th><th>Uptime</th><th>Actions</th></tr></thead><tbody>${rows || '<tr><td colspan="8">No containers detected for this service.</td></tr>'}</tbody></table></div>`;
}


// ============================================================
// Service/container association fixes
// ============================================================
function getKnownContainers() {
    const extras = [
        { category: 'Core', service: 'structure', name: 'ligand-x-structure', image: 'ghcr.io/kon-218/ligand-x/structure:latest', port: '8003' },
        { category: 'Core', service: 'pocket-finder', name: 'ligand-x-pocket-finder', image: 'ghcr.io/kon-218/ligand-x/pocket-finder:latest', port: '8004' },
        { category: 'Core', service: 'alignment', name: 'ligand-x-alignment', image: 'ghcr.io/kon-218/ligand-x/alignment:latest', port: '8005' },
        { category: 'Tools', service: 'ketcher', name: 'ligand-x-ketcher', image: 'ghcr.io/kon-218/ligand-x/ketcher:latest', port: '8006' },
        { category: 'Tools', service: 'msa', name: 'ligand-x-msa', image: 'ghcr.io/kon-218/ligand-x/msa:latest', port: '8007' },
        { category: 'Compute', service: 'worker-cpu', name: 'ligand-x-worker-cpu', image: 'ghcr.io/kon-218/ligand-x/worker-cpu:latest', port: '-' },
        { category: 'Tools', service: 'flower', name: 'ligand-x-flower', image: 'mher/flower:latest', port: '5555' },
        { category: 'Prediction', service: 'admet', name: 'ligand-x-admet', image: 'ghcr.io/kon-218/ligand-x-pro/admet:latest', port: '8013' },
        { category: 'Prediction', service: 'boltz2', name: 'ligand-x-boltz2', image: 'ghcr.io/kon-218/ligand-x-pro/boltz2:latest', port: '8014' },
        { category: 'Prediction', service: 'qc', name: 'ligand-x-qc', image: 'ghcr.io/kon-218/ligand-x-pro/qc:latest', port: '8015' },
        { category: 'Compute', service: 'worker-qc', name: 'ligand-x-worker-qc', image: 'ghcr.io/kon-218/ligand-x-pro/worker-qc:latest', port: '-' },
        { category: 'Design', service: 'reinvent', name: 'ligand-x-reinvent', image: 'ghcr.io/kon-218/ligand-x-pro/reinvent:latest', port: '8016' },
        { category: 'Compute', service: 'worker-reinvent', name: 'ligand-x-worker-reinvent', image: 'ghcr.io/kon-218/ligand-x-pro/worker-reinvent:latest', port: '-' },
        { category: 'Compute', service: 'abfe', name: 'ligand-x-abfe', image: 'ghcr.io/kon-218/ligand-x-pro/abfe:latest', port: '8017' },
        { category: 'Compute', service: 'rbfe', name: 'ligand-x-rbfe', image: 'ghcr.io/kon-218/ligand-x-pro/rbfe:latest', port: '8018' },
        { category: 'Compute', service: 'worker-gpu-long', name: 'ligand-x-worker-gpu-long', image: 'ghcr.io/kon-218/ligand-x-pro/worker-gpu-long:latest', port: '-' },
        { category: 'Compute', service: 'kinetics', name: 'ligand-x-kinetics', image: 'ghcr.io/kon-218/ligand-x-pro/kinetics:latest', port: '8019' },
        { category: 'Compute', service: 'worker-kinetics', name: 'ligand-x-worker-kinetics', image: 'ghcr.io/kon-218/ligand-x-pro/worker-kinetics:latest', port: '-' },
    ];
    const normalizedBase = WORKBENCH_CONTAINERS.map(c => ({ ...c, service: String(c.service || '').toLowerCase() }));
    const byService = new Map();
    [...normalizedBase, ...extras].forEach(container => {
        const key = String(container.service || container.name).toLowerCase();
        if (!byService.has(key)) byService.set(key, container);
    });
    return [...byService.values()];
}

function serviceNamesForGroup(group) {
    if (Array.isArray(group?.services) && group.services.length > 0) {
        return group.services.map(service => String(service).toLowerCase());
    }
    return [String(group?.id || '').toLowerCase()];
}

function containersForGroup(group, preferLive = true) {
    const services = new Set(serviceNamesForGroup(group));
    const live = latestResourceMetrics?.containers || [];
    const liveMatches = live.filter(container => services.has(String(container.service || '').toLowerCase()));
    if (preferLive && liveMatches.length > 0) return liveMatches;
    return getKnownContainers()
        .filter(container => services.has(String(container.service || '').toLowerCase()))
        .map(container => ({
            ...container,
            status: 'not detected',
            running: false,
            cpuPercent: 0,
            memoryText: '-',
            gpuPercent: 0,
            uptime: '-',
        }));
}

function inferContainerCount(id) {
    const group = lastServiceGroups.find(g => g.id === id);
    if (group?.services?.length) return group.services.length;
    return containersForGroup(group || { id }).length || 1;
}

function renderServiceTable(groups, imageStatus) {
    const rows = groups.map(group => {
        const pulled = !!imageStatus[group.id];
        const selected = serviceTabSelection.includes(group.id);
        const locked = !!group.locked;
        const tier = locked ? '<span class="tag pro">Pro</span>' : group.required ? '<span class="tag">Required</span>' : '<span class="tag">Optional</span>';
        const size = group.sizeMb ? `${(group.sizeMb / 1000).toFixed(1)} GB` : '-';
        const count = Array.isArray(group.services) ? group.services.length : inferContainerCount(group.id);
        return `<tr data-open-service="${escapeHtml(group.id)}">
            <td><input type="checkbox" ${selected ? 'checked' : ''} ${locked || group.required ? 'disabled' : ''} data-toggle-service="${escapeHtml(group.id)}"> ${escapeHtml(group.name)}</td>
            <td>${escapeHtml(group.category || group.edition || 'Service')}</td>
            <td>${count}</td>
            <td>${size}</td>
            <td>${tier}</td>
            <td><span class="status-chip">${pulled ? 'on disk' : 'missing'}</span></td>
            <td><button class="btn btn-sm btn-secondary" ${locked ? 'disabled' : ''} data-restart-service="${escapeHtml(group.id)}">Restart</button> <button class="btn btn-sm btn-secondary" ${locked ? 'disabled' : ''} data-pull-service="${escapeHtml(group.id)}">${pulled ? 'Re-pull' : 'Pull'}</button> ${pulled ? `<button class="btn btn-sm btn-danger" data-delete-service="${escapeHtml(group.id)}">Uninstall</button>` : ''}</td>
        </tr>`;
    }).join('');
    const totalGb = groups.reduce((sum, group) => sum + (serviceTabSelection.includes(group.id) ? (group.sizeMb || 0) : 0), 0) / 1000;
    return `<table class="service-table"><thead><tr><th>Service</th><th>Category</th><th>Containers</th><th>Size</th><th>Tier</th><th>Status</th><th>Actions</th></tr></thead><tbody>${rows}</tbody></table><div class="summary-row">${serviceTabSelection.length} selected · ${totalGb.toFixed(1)} GB · ${groups.every(g => lastImageStatus[g.id] || g.locked) ? 'all on disk' : 'downloads available'}</div>`;
}

function renderWorkbenchContainers(runningServices = []) {
    const root = document.getElementById('containerTreeRows');
    if (!root) return;
    const runningNames = new Set(runningServices.filter(s => s.running).map(s => String(s.name).toLowerCase()));
    const containers = getKnownContainers();
    const categories = ['Core', 'Compute', 'Prediction', 'Design', 'Tools'].filter(category => containers.some(c => c.category === category));
    root.innerHTML = categories.map(category => {
        const rows = containers.filter(c => c.category === category).map(container => {
            const service = String(container.service || '').toLowerCase();
            const isRunning = runningNames.has(service) || runningNames.has(String(container.name).toLowerCase());
            return `<button class="tree-row" data-container-name="${escapeHtml(container.name)}"><span class="status-dot ${isRunning ? 'running' : 'stopped'}"></span> ${escapeHtml(container.name)} <span class="tree-meta">${escapeHtml(container.port)}</span></button>`;
        }).join('');
        return `<button class="tree-heading">${category}</button>${rows}`;
    }).join('');
    root.querySelectorAll('[data-container-name]').forEach(button => {
        button.addEventListener('click', () => openContainerDrawer(button.dataset.containerName));
    });
    renderContainerResourceTable();
}

function renderContainerResourceTable() {
    const root = document.getElementById('containerResourceTable');
    if (!root) return;
    const containers = latestResourceMetrics?.containers?.length ? latestResourceMetrics.containers : getKnownContainers().map(c => ({
        name: c.name,
        service: c.service,
        image: c.image,
        port: c.port,
        status: 'not detected',
        running: false,
        cpuPercent: 0,
        memoryText: '-',
        gpuPercent: 0,
        uptime: '-',
    }));
    root.innerHTML = `<table class="container-table"><thead><tr><th>Container name</th><th>Image</th><th>Port</th><th>CPU%</th><th>Memory</th><th>GPU</th><th>Uptime</th><th>Actions</th></tr></thead><tbody>${containers.map(c => `<tr onclick="openContainerDrawer('${escapeHtml(c.name)}')"><td><span class="status-dot ${c.running ? 'running' : 'stopped'}"></span> ${escapeHtml(c.name)}</td><td>${escapeHtml(c.image || '-')}</td><td>${escapeHtml(c.port || '-')}</td><td>${formatPercent(c.cpuPercent)}</td><td>${escapeHtml(c.memoryText || '-')}</td><td>${c.gpuPercent ? formatPercent(c.gpuPercent) : '-'}</td><td>${escapeHtml(c.uptime || '-')}</td><td><button class="btn btn-sm btn-secondary">Logs</button></td></tr>`).join('')}</tbody></table>`;
}

function openServiceDetail(groupId) {
    const group = lastServiceGroups.find(g => g.id === groupId) || { id: groupId, name: groupId, description: 'Ligand-X service group', services: [groupId] };
    const container = document.getElementById('servicesTabContent');
    activateWorkbenchTab('overview', group.name);
    if (!container) return;
    const related = containersForGroup(group);
    const rows = related.map(c => `<tr onclick="openContainerDrawer('${escapeHtml(c.name)}')"><td><span class="status-dot ${c.running ? 'running' : 'stopped'}"></span> ${escapeHtml(c.name)}</td><td>${escapeHtml(c.image || '-')}</td><td>${escapeHtml(c.port || '-')}</td><td>${formatPercent(c.cpuPercent)}</td><td>${escapeHtml(c.memoryText || '-')}</td><td>${c.gpuPercent ? formatPercent(c.gpuPercent) : '-'}</td><td>${escapeHtml(c.uptime || '-')}</td><td><button class="btn btn-sm btn-secondary">Logs</button></td></tr>`).join('');
    const memoryBytes = related.reduce((sum, c) => sum + Number(c.memoryBytes || 0), 0);
    const maxCPU = related.reduce((max, c) => Math.max(max, Number(c.cpuPercent || 0)), 0);
    container.innerHTML = `<div class="service-detail"><div class="service-detail-header"><div><h2>${escapeHtml(group.name)}</h2><p>${escapeHtml(group.description || '')}</p></div><div><button class="btn btn-sm btn-secondary" onclick="restartServices()">Restart</button> <button class="btn btn-sm btn-secondary" onclick="pullServiceGroup('${escapeHtml(group.id)}')">Re-pull</button> <button class="btn btn-sm btn-danger" onclick="deleteServiceGroupImages('${escapeHtml(group.id)}')">Uninstall</button></div></div><div class="metric-grid compact"><div class="metric-tile"><label>Containers</label><strong>${related.length}</strong><small>${related.filter(c => c.running).length} running</small></div><div class="metric-tile"><label>Memory</label><strong>${formatBytesJS(memoryBytes)}</strong><small>current usage</small></div><div class="metric-tile"><label>Peak CPU</label><strong>${formatPercent(maxCPU)}</strong><small>${lastImageStatus[group.id] ? 'image on disk' : 'image missing'}</small></div></div><table class="container-table"><thead><tr><th>Container name</th><th>Image</th><th>Port</th><th>CPU%</th><th>Memory</th><th>GPU</th><th>Uptime</th><th>Actions</th></tr></thead><tbody>${rows || '<tr><td colspan="8">No containers declared for this service.</td></tr>'}</tbody></table></div>`;
}

function openContainerDrawer(name) {
    const live = latestResourceMetrics?.containers?.find(c => c.name === name);
    const fallback = getKnownContainers().find(c => c.name === name) || getKnownContainers()[0];
    const container = live || fallback;
    currentDrawerContainerService = container.service || fallback?.service || null;
    document.getElementById('drawerName').textContent = container.name;
    document.getElementById('drawerService').textContent = currentDrawerContainerService || '-';
    document.getElementById('drawerImage').textContent = container.image || fallback?.image || '-';
    document.getElementById('drawerPort').textContent = container.port || fallback?.port || '-';
    const drawer = document.getElementById('containerDrawer');
    if (drawer) {
        const kv = drawer.querySelector('.kv-grid');
        if (kv) {
            const kvData = [
                ['Status', live?.status || 'not detected'],
                ['Image', container.image || '-'],
                ['Port', container.port || '-'],
                ['Uptime', live?.uptime || '-'],
            ];
            kv.replaceChildren(...kvData.map(([label, val]) => {
                const div = document.createElement('div');
                const lbl = document.createElement('label');
                lbl.textContent = label;
                const strong = document.createElement('strong');
                strong.textContent = val;
                div.appendChild(lbl);
                div.appendChild(strong);
                return div;
            }));
        }
        const meters = drawer.querySelectorAll('.meter span');
        if (meters[0]) meters[0].style.width = `${Math.min(100, Number(live?.cpuPercent || 0))}%`;
        const memPct = live?.memoryLimit ? (Number(live.memoryBytes || 0) / Number(live.memoryLimit || 1)) * 100 : 0;
        if (meters[1]) meters[1].style.width = `${Math.min(100, memPct)}%`;
        if (meters[2]) meters[2].style.width = `${Math.min(100, Number(live?.gpuPercent || 0))}%`;
        drawer.classList.remove('hidden');
    }
}


function getKnownContainers() {
    return [
        { category: 'Core', service: 'postgres', name: 'ligand-x-postgres', image: 'postgres:16-alpine', port: '5432' },
        { category: 'Core', service: 'redis', name: 'ligand-x-redis', image: 'redis:7-alpine', port: '6379' },
        { category: 'Core', service: 'rabbitmq', name: 'ligand-x-rabbitmq', image: 'rabbitmq:3.13-management-alpine', port: '5672, 15672' },
        { category: 'Core', service: 'gateway', name: 'ligand-x-gateway', image: 'ghcr.io/kon-218/ligand-x/gateway:latest', port: '8000' },
        { category: 'Core', service: 'frontend', name: 'ligand-x-frontend', image: 'ghcr.io/kon-218/ligand-x/frontend:latest', port: '3000' },
        { category: 'Core', service: 'structure', name: 'ligand-x-structure', image: 'ghcr.io/kon-218/ligand-x/structure:latest', port: '8003' },
        { category: 'Core', service: 'pocket-finder', name: 'ligand-x-pocket-finder', image: 'ghcr.io/kon-218/ligand-x/pocket-finder:latest', port: '8004' },
        { category: 'Core', service: 'alignment', name: 'ligand-x-alignment', image: 'ghcr.io/kon-218/ligand-x/alignment:latest', port: '8005' },
        { category: 'Tools', service: 'ketcher', name: 'ligand-x-ketcher', image: 'ghcr.io/kon-218/ligand-x/ketcher:latest', port: '8006' },
        { category: 'Tools', service: 'msa', name: 'ligand-x-msa', image: 'ghcr.io/kon-218/ligand-x/msa:latest', port: '8007' },
        { category: 'Compute', service: 'worker-cpu', name: 'ligand-x-worker-cpu', image: 'ghcr.io/kon-218/ligand-x/worker-cpu:latest', port: '-' },
        { category: 'Tools', service: 'flower', name: 'ligand-x-flower', image: 'mher/flower:latest', port: '5555' },
        { category: 'Prediction', service: 'docking', name: 'ligand-x-docking', image: 'ghcr.io/kon-218/ligand-x/docking:latest', port: '8011' },
        { category: 'Prediction', service: 'md', name: 'ligand-x-md', image: 'ghcr.io/kon-218/ligand-x/md:latest', port: '8012' },
        { category: 'Compute', service: 'worker-gpu-short', name: 'ligand-x-worker-gpu-short', image: 'ghcr.io/kon-218/ligand-x/worker-gpu-short:latest', port: '-' },
        { category: 'Prediction', service: 'admet', name: 'ligand-x-admet', image: 'ghcr.io/kon-218/ligand-x-pro/admet:latest', port: '8013' },
        { category: 'Prediction', service: 'boltz2', name: 'ligand-x-boltz2', image: 'ghcr.io/kon-218/ligand-x-pro/boltz2:latest', port: '8014' },
        { category: 'Prediction', service: 'qc', name: 'ligand-x-qc', image: 'ghcr.io/kon-218/ligand-x-pro/qc:latest', port: '8015' },
        { category: 'Compute', service: 'worker-qc', name: 'ligand-x-worker-qc', image: 'ghcr.io/kon-218/ligand-x-pro/worker-qc:latest', port: '-' },
        { category: 'Design', service: 'reinvent', name: 'ligand-x-reinvent', image: 'ghcr.io/kon-218/ligand-x-pro/reinvent:latest', port: '8016' },
        { category: 'Compute', service: 'worker-reinvent', name: 'ligand-x-worker-reinvent', image: 'ghcr.io/kon-218/ligand-x-pro/worker-reinvent:latest', port: '-' },
        { category: 'Compute', service: 'abfe', name: 'ligand-x-abfe', image: 'ghcr.io/kon-218/ligand-x-pro/abfe:latest', port: '8017' },
        { category: 'Compute', service: 'rbfe', name: 'ligand-x-rbfe', image: 'ghcr.io/kon-218/ligand-x-pro/rbfe:latest', port: '8018' },
        { category: 'Compute', service: 'worker-gpu-long', name: 'ligand-x-worker-gpu-long', image: 'ghcr.io/kon-218/ligand-x-pro/worker-gpu-long:latest', port: '-' },
        { category: 'Compute', service: 'kinetics', name: 'ligand-x-kinetics', image: 'ghcr.io/kon-218/ligand-x-pro/kinetics:latest', port: '8019' },
        { category: 'Compute', service: 'worker-kinetics', name: 'ligand-x-worker-kinetics', image: 'ghcr.io/kon-218/ligand-x-pro/worker-kinetics:latest', port: '-' },
    ];
}

// ============================================================
// Menu Bar
// ============================================================

function initMenuBar() {
    const MENUS = {
        'File': [
            { label: 'Change project folder', fn: () => selectProjectFolder() },
            { sep: true },
            { label: 'Quit', fn: () => window.go?.main?.App?.Quit?.() },
        ],
        'Stack': [
            { label: 'Start',        fn: () => startServices() },
            { label: 'Stop',         fn: () => stopServices() },
            { label: 'Restart',      fn: () => restartServices() },
            { sep: true },
            { label: 'Pull images',  fn: () => pullImages() },
            { label: 'Clean Docker', fn: () => cleanDocker() },
        ],
        'Service': [
            { label: 'Refresh',           fn: () => renderServicesTab() },
            { sep: true },
            { label: 'Pull selected',     fn: () => pullImages() },
            { label: 'Restart selected',  fn: () => restartServices() },
        ],
        'Tools': [
            { label: 'Command palette', fn: () => toggleCommandPalette(true), kbd: '⌘K' },
            { sep: true },
            { label: 'Clean Docker',    fn: () => cleanDocker() },
            { label: 'Open API docs',   fn: () => openAPI?.() },
            { label: 'Open Flower',     fn: () => openFlower?.() },
        ],
        'View': [
            { label: 'Overview',         fn: () => switchToTab('overview') },
            { label: 'Diagnostics',      fn: () => switchToTab('logs') },
            { label: 'Resources',        fn: () => switchToTab('resources') },
            { label: '.env Config',      fn: () => switchToTab('config') },
            { sep: true },
            { label: 'Toggle Inspector', fn: () => document.getElementById('inspector')?.classList.toggle('hidden') },
        ],
        'Help': [
            { label: 'Documentation', fn: () => window.go?.main?.App?.OpenBrowser?.('https://docs.ligand-x.com') },
            { sep: true },
            { label: 'About Ligand-X', fn: () => {} },
        ],
    };

    document.querySelectorAll('.menu-button').forEach(btn => {
        const label = btn.textContent.trim();
        const items = MENUS[label];
        if (!items) return;

        const drop = document.createElement('div');
        drop.className = 'menu-dropdown';

        items.forEach(item => {
            if (item.sep) {
                const sep = document.createElement('div');
                sep.className = 'menu-dropdown-sep';
                drop.appendChild(sep);
            } else {
                const el = document.createElement('button');
                el.className = 'menu-dropdown-item';
                el.textContent = item.label;
                if (item.kbd) {
                    const kbd = document.createElement('span');
                    kbd.className = 'menu-dropdown-kbd';
                    kbd.textContent = item.kbd;
                    el.appendChild(kbd);
                }
                el.addEventListener('click', e => {
                    e.stopPropagation();
                    closeAllMenus();
                    item.fn();
                });
                drop.appendChild(el);
            }
        });

        btn.appendChild(drop);
        btn.addEventListener('click', e => {
            e.stopPropagation();
            const wasOpen = btn.classList.contains('open');
            closeAllMenus();
            if (!wasOpen) btn.classList.add('open');
        });
    });

    document.addEventListener('click', closeAllMenus);
    document.addEventListener('keydown', e => { if (e.key === 'Escape') closeAllMenus(); });
}

function closeAllMenus() {
    document.querySelectorAll('.menu-button.open').forEach(b => b.classList.remove('open'));
}

function switchToTab(tabName) {
    const btn = document.querySelector(`.tab-button[data-tab="${tabName}"]`);
    if (btn) btn.click();
}

// ============================================================
// Container Drawer Actions
// ============================================================

function initDrawerActions() {
    const drawer = document.getElementById('containerDrawer');
    if (!drawer) return;
    const actionBtns = drawer.querySelectorAll('.drawer-actions .btn');
    // Order in HTML: Restart, Pause, Shell, Logs, Stop
    const [restartBtn, , , logsBtn] = actionBtns;
    if (restartBtn) {
        restartBtn.addEventListener('click', async () => {
            if (!currentDrawerContainerService) return;
            try {
                addLog('launcher', 'Restarting ' + currentDrawerContainerService + '...');
                await window.go.main.App.RestartServicesCustom([currentDrawerContainerService]);
                addLog('launcher', currentDrawerContainerService + ' restarted');
            } catch (err) {
                addLog('launcher', 'Restart failed: ' + (err.message || err), 'error');
            }
        });
    }
    if (logsBtn) {
        logsBtn.addEventListener('click', () => {
            if (!currentDrawerContainerService) return;
            const logSelect = document.getElementById('logService');
            if (logSelect) {
                logSelect.value = currentDrawerContainerService;
                changeLogService();
            }
            switchToTab('logs');
            closeContainerDrawer();
        });
    }
}
