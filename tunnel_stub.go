//go:build !devtunnel

package main

import "fmt"

func (a *App) GetTunnelStatus() TunnelStatus {
	return TunnelStatus{
		Enabled: false,
		Message: "Remote access is not available in this build",
	}
}

func (a *App) StartTunnel() error {
	return fmt.Errorf("remote access is not available in this build")
}

func (a *App) StopTunnel() error {
	return fmt.Errorf("remote access is not available in this build")
}

func (a *App) OpenTunnelURL() {
	// no-op
}

func (a *App) shutdownTunnel() {
	// no-op
}
