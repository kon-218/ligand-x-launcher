package main

// TunnelStatus is returned to the frontend on demand and via the
// "tunnel-status" event whenever state changes (devtunnel builds only emit
// events when the feature is active).
type TunnelStatus struct {
	Running    bool   `json:"running"`
	Managed    bool   `json:"managed"` // true if the launcher spawned the process
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	URL        string `json:"url"`
	Message    string `json:"message"`
	BinaryPath string `json:"binaryPath"`
	// Enabled is true only in builds compiled with -tags devtunnel.
	Enabled bool `json:"enabled"`
}
