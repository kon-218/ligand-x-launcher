//go:build devtunnel

package main

// Cloudflare Tunnel integration for the Ligand-X launcher.
//
// Spawns `cloudflared tunnel run <name>` as a child process, streams its
// stdout/stderr into the launcher log panel, and surfaces start/stop/status
// to the Wails frontend. Config lives in ~/.cloudflared/config.yml which
// already maps the tunnel name below to the configured hostname.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Hardcoded per user request — single tunnel for this project.
// The tunnel is served either by name (requires a working
// ~/.cloudflared/config.yml + credentials-file) or by passing --token to
// cloudflared. Token can be supplied via the CLOUDFLARED_TUNNEL_TOKEN env
// var or a file at ~/.cloudflared/<tunnelName>.token (see tokenForTunnel).
const (
	tunnelName     = "ligand-x"
	tunnelHostname = "lab.k-nom.com"
	tunnelLogTag   = "tunnel"
)

// GetTunnelStatus reports whether any cloudflared tunnel is currently active.
// It reports processes spawned by the launcher (Managed=true) as well as any
// cloudflared tunnel already running on the host (Managed=false).
func (a *App) GetTunnelStatus() TunnelStatus {
	status := TunnelStatus{
		Name:     tunnelName,
		Hostname: tunnelHostname,
		URL:      "https://" + tunnelHostname,
		Enabled:  true,
	}

	if bin, err := exec.LookPath("cloudflared"); err == nil {
		status.BinaryPath = bin
	}

	a.tunnelMux.Lock()
	managedAlive := a.tunnelCmd != nil && a.tunnelCmd.Process != nil && a.tunnelCmd.ProcessState == nil
	var managedPID int
	if managedAlive {
		managedPID = a.tunnelCmd.Process.Pid
	}
	a.tunnelMux.Unlock()

	if managedAlive {
		status.Running = true
		status.Managed = true
		status.PID = managedPID
		status.Message = "Tunnel managed by launcher"
		return status
	}

	if pid := detectExternalCloudflared(); pid > 0 {
		status.Running = true
		status.Managed = false
		status.PID = pid
		status.Message = "External cloudflared process detected (not started by launcher)"
		return status
	}

	if status.BinaryPath == "" {
		status.Message = "cloudflared not found in PATH"
	} else if a.tokenForTunnel() != "" {
		status.Message = "Tunnel stopped (token configured)"
	} else {
		status.Message = "Tunnel stopped"
	}
	return status
}

// StartTunnel spawns `cloudflared tunnel run <tunnelName>`. If a tunnel is
// already running (managed or external) this is a no-op and returns a
// descriptive error the frontend can display.
func (a *App) StartTunnel() error {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		a.logTunnel("cloudflared binary not found in PATH. Install it from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")
		a.emitTunnelStatus()
		return fmt.Errorf("cloudflared not found in PATH")
	}

	a.tunnelMux.Lock()
	if a.tunnelCmd != nil && a.tunnelCmd.Process != nil && a.tunnelCmd.ProcessState == nil {
		a.tunnelMux.Unlock()
		return fmt.Errorf("tunnel already running (PID %d)", a.tunnelCmd.Process.Pid)
	}

	if pid := detectExternalCloudflared(); pid > 0 {
		a.tunnelMux.Unlock()
		a.logTunnel(fmt.Sprintf("External cloudflared tunnel already running (PID %d). Stop it first or use the existing tunnel.", pid))
		a.emitTunnelStatus()
		return fmt.Errorf("external cloudflared process already running (PID %d)", pid)
	}

	// Prefer token-based launch when a token is available: it doesn't depend
	// on the possibly-stale ~/.cloudflared/config.yml and credentials file.
	// Fall back to `tunnel run <name>` (requires working config.yml).
	args := []string{"tunnel", "run"}
	authMode := "name"
	if tok := a.tokenForTunnel(); tok != "" {
		args = append(args, "--token", tok)
		authMode = "token"
	} else {
		args = append(args, tunnelName)
	}
	cmd := exec.Command(bin, args...)
	// Put the child in its own process group so Stop can signal the whole
	// group (cloudflared sometimes forks helpers).
	setSysProcAttr(cmd)
	_ = authMode // captured below for the user-facing log line

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.tunnelMux.Unlock()
		return fmt.Errorf("failed to pipe stdout: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		a.tunnelMux.Unlock()
		return fmt.Errorf("failed to pipe stderr: %v", err)
	}

	if err := cmd.Start(); err != nil {
		a.tunnelMux.Unlock()
		a.logTunnel(fmt.Sprintf("Failed to start cloudflared: %v", err))
		a.emitTunnelStatus()
		return fmt.Errorf("failed to start cloudflared: %v", err)
	}

	a.tunnelCmd = cmd
	a.tunnelMux.Unlock()

	a.logTunnel(fmt.Sprintf("Starting tunnel %q via %s → https://%s (PID %d)", tunnelName, authMode, tunnelHostname, cmd.Process.Pid))
	a.emitTunnelStatus()

	go a.streamTunnelOutput(stdout)
	go a.streamTunnelOutput(stderr)

	go func() {
		waitErr := cmd.Wait()

		a.tunnelMux.Lock()
		// Only clear if this is still the current cmd (StopTunnel may have
		// replaced/cleared it already).
		if a.tunnelCmd == cmd {
			a.tunnelCmd = nil
		}
		a.tunnelMux.Unlock()

		if waitErr != nil {
			// Don't scream on clean shutdown via signal.
			if ee, ok := waitErr.(*exec.ExitError); ok && ee.ExitCode() == -1 {
				a.logTunnel("Tunnel stopped")
			} else {
				a.logTunnel(fmt.Sprintf("Tunnel exited: %v", waitErr))
			}
		} else {
			a.logTunnel("Tunnel stopped")
		}
		a.emitTunnelStatus()
	}()

	return nil
}

// StopTunnel terminates the launcher-managed cloudflared child, if any.
// It does not touch externally-started cloudflared processes.
func (a *App) StopTunnel() error {
	a.tunnelMux.Lock()
	cmd := a.tunnelCmd
	a.tunnelMux.Unlock()

	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		if pid := detectExternalCloudflared(); pid > 0 {
			a.logTunnel(fmt.Sprintf("Tunnel is running externally (PID %d); launcher cannot stop it. Kill it from the terminal where it was started.", pid))
			return fmt.Errorf("tunnel is running externally (PID %d); launcher only controls tunnels it started", pid)
		}
		return fmt.Errorf("tunnel is not running")
	}

	a.logTunnel(fmt.Sprintf("Stopping tunnel (PID %d)...", cmd.Process.Pid))
	if err := signalTunnel(cmd, syscall.SIGINT); err != nil {
		// Fall back to Kill
		_ = cmd.Process.Kill()
	}

	// Give cloudflared a moment to exit cleanly; Wait goroutine will finalize.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.tunnelMux.Lock()
		still := a.tunnelCmd
		a.tunnelMux.Unlock()
		if still == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Hard kill if still around.
	a.tunnelMux.Lock()
	if a.tunnelCmd != nil && a.tunnelCmd.Process != nil {
		_ = a.tunnelCmd.Process.Kill()
	}
	a.tunnelMux.Unlock()
	return nil
}

// OpenTunnelURL opens the configured hostname in the user's default browser.
func (a *App) OpenTunnelURL() {
	a.OpenBrowser("https://" + tunnelHostname)
}

// shutdownTunnel is called from App.shutdown to clean up the child on exit.
func (a *App) shutdownTunnel() {
	a.tunnelMux.Lock()
	cmd := a.tunnelCmd
	a.tunnelMux.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = signalTunnel(cmd, syscall.SIGINT)
	// Give it a brief grace period, then hard-kill.
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// detectExternalCloudflared returns the PID of a cloudflared process serving
// *our* tunnel (tunnelName) that was started outside of the launcher, or 0 if
// none found. Best-effort; relies on `pgrep` on Linux/macOS and is a no-op on
// other platforms.
//
// Scoped to tunnelName in argv so that an unrelated cloudflared instance
// (e.g. a systemd unit serving a different tunnel via --token) doesn't
// falsely register as "our" tunnel running. The tradeoff: if the user
// launched our tunnel via `--token` instead of the name, we won't detect it
// — which is acceptable, since we can't distinguish tokens without
// decoding them and that ambiguity is exactly what caused the original bug.
func detectExternalCloudflared() int {
	if goruntime.GOOS == "windows" {
		return 0
	}
	out, err := exec.Command("pgrep", "-a", "-f", "cloudflared").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Must be a `tunnel run <ourName>` invocation.
		if !strings.Contains(line, "tunnel") ||
			!strings.Contains(line, "run") ||
			!strings.Contains(line, tunnelName) {
			continue
		}
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sp])
		if err != nil || pid <= 0 {
			continue
		}
		return pid
	}
	return 0
}

// tokenForTunnel returns a Cloudflare tunnel token if one is configured, or
// "" if none found. Lookup order:
//  1. CLOUDFLARED_TUNNEL_TOKEN env var (useful for one-shot dev sessions)
//  2. ~/.cloudflared/<tunnelName>.token (recommended: single-line file
//     containing the token; must be chmod 600 on Unix)
//
// Security posture:
//   - Token value is never logged anywhere by this function or its callers.
//     The only place it touches the log pipeline is via cloudflared's own
//     "token:*****" redacted INF line.
//   - Token file must NOT be group/world-readable on Unix. If it is, we
//     refuse to load it and emit a warning — a loose token file is likely a
//     user mistake (e.g. copied under the repo, chmod'd wide) and we fail
//     closed rather than silently expose a long-lived credential.
//   - Token is passed to cloudflared via argv. This is a cloudflared-imposed
//     interface; the token becomes visible in /proc/<pid>/cmdline to the
//     same user. That matches how the user's existing manual invocation
//     already works and is the ecosystem norm.
func (a *App) tokenForTunnel() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARED_TUNNEL_TOKEN")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".cloudflared", tunnelName+".token")
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	// Refuse loose permissions on Unix. Windows lacks the same mode bits,
	// so we skip this check there and rely on NTFS ACLs.
	if goruntime.GOOS != "windows" {
		mode := info.Mode().Perm()
		if mode&0o077 != 0 {
			a.logTunnel(fmt.Sprintf(
				"Refusing to read tunnel token at %s: permissions are %04o (group/world readable). "+
					"Run: chmod 600 %s",
				path, mode, path))
			return ""
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (a *App) streamTunnelOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	// cloudflared emits some long lines (URLs, config dumps); bump buffer.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		a.logTunnel(scanner.Text())
	}
}

func (a *App) logTunnel(msg string) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "log", LogEntry{
		Service:   tunnelLogTag,
		Message:   msg,
		Timestamp: time.Now().Format("15:04:05"),
	})
}

func (a *App) emitTunnelStatus() {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "tunnel-status", a.GetTunnelStatus())
}
