package main

import (
	"fmt"
	"os"

	"ligandx-launcher/internal/agentsession"
	"ligandx-launcher/internal/launcher"
)

// Keep these symbols in main: local and CI release builds inject them with -X.
var runtimeBundlePublicKeyB64 string
var launcherVersion string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agent-mcp" {
		if err := agentsession.RunConnector(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "Ligand-X MCP connector:", err)
			os.Exit(1)
		}
		return
	}
	launcher.Run(assets, runtimeBundlePublicKeyB64, launcherVersion)
}
