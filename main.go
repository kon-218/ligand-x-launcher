package main

import "ligandx-launcher/internal/launcher"

// Keep these symbols in main: local and CI release builds inject them with -X.
var runtimeBundlePublicKeyB64 string
var launcherVersion string
var enabledPreviewBundles string

func main() {
	launcher.SetPreviewBundles(enabledPreviewBundles)
	launcher.Run(assets, runtimeBundlePublicKeyB64, launcherVersion)
}
