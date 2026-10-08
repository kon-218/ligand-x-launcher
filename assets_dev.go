//go:build !public

package main

import "embed"

// assets is the embedded frontend for the default (developer/operator) launcher:
// the full dashboard UI under frontend/. Built with no special tags.
//
//go:embed all:frontend
var assets embed.FS
