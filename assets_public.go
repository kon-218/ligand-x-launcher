//go:build public

package main

import "embed"

// assets is the embedded frontend for the simplified public launcher:
// the lean linear-flow UI under frontend-public/. Built with `-tags public`.
// Shares the entire Go backend (app.go) with the dev launcher; only the
// embedded frontend and window metadata differ.
//
//go:embed all:frontend-public
var assets embed.FS

// Window metadata for the public launcher (narrower window for the guided flow).
const (
	isPublicBuild = true
	appTitle      = "Ligand-X"
	appWidth      = 480
	appHeight     = 640
	appMinWidth   = 420
	appMinHeight  = 560
)
