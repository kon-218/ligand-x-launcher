//go:build public

package main

import "embed"

// assets is the embedded frontend for the simplified public launcher:
// the lean linear-flow UI under frontend-public/. Built with `-tags public`.
// Shares the internal/launcher backend with the dev launcher; only the
// embedded frontend and window metadata differ.
//
//go:embed all:frontend-public
var assets embed.FS
