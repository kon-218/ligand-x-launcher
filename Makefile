# ============================================================
# Ligand-X Launcher Makefile
# ============================================================
# Cross-platform desktop installer/launcher built with Wails
# (Go + web frontend). These targets wrap the scripts/ helpers
# and the standard Go toolchain.
#
#   make setup           - Install Wails build prerequisites
#   make dev             - Run in dev mode with hot reload
#   make build           - Build for the current platform
#   make build-all       - Build native binaries for all platforms
#   make test            - Run Go tests
#   make runtime-bundle  - Build the first-run runtime bundle zip
#
# Full installers (AppImage / DMG / NSIS) are produced by CI, not
# by this Makefile - see .github/workflows/.
# ============================================================

.PHONY: help setup dev dev-public build build-public build-all runtime-bundle sync-runtime-topology check-runtime-topology pin-release validate-staging test verify-docs fmt vet deps clean install-wails

# ============================================================
# Configuration
# ============================================================

VERSION ?= 1.0.0
PUBLIC_REPO ?= ../ligand-x
WAILS   := $(shell go env GOPATH)/bin/wails

# ============================================================
# Help
# ============================================================

help:
	@echo "Ligand-X Launcher Commands"
	@echo ""
	@echo "Setup:"
	@echo "  make setup            - Configure Wails build environment (Ubuntu 24.04)"
	@echo "  make install-wails    - Install the Wails CLI via go install"
	@echo "  make deps             - Download and tidy Go modules"
	@echo ""
	@echo "Develop:"
	@echo "  make dev              - Run dev launcher with hot reload (wails dev)"
	@echo "  make dev-public       - Run the simplified public launcher (frontend-public/)"
	@echo "  make fmt              - Format Go sources (go fmt)"
	@echo "  make vet              - Static checks (go vet)"
	@echo "  make test             - Run Go tests (go test ./...)"
	@echo "  make verify-docs      - Check links and reject agent-session artifacts"
	@echo ""
	@echo "Build:"
	@echo "  make build            - Build dev launcher for current platform -> build/bin/ligandx-launcher"
	@echo "  make build-public     - Build public launcher for current platform -> build/bin/ligandx"
	@echo "  make build-all        - Native binaries for all platforms -> dist/"
	@echo "  make runtime-bundle   - Build and validate canonical ligand-x-runtime.zip -> dist/"
	@echo "  make sync-runtime-topology - Regenerate launcher Compose snapshot + env template from public canonical"
	@echo "  make check-runtime-topology - Fail if the Compose snapshot or env template has drifted"
	@echo "  make pin-release RELEASE=vX.Y.Z - Pin .env.production VERSION to a release"
	@echo "  make validate-staging - Start pinned prod images and verify all 25 services are healthy"
	@echo "  make clean            - Remove build/bin, dist, and packaged artifacts"
	@echo ""
	@echo "Note: AppImage/DMG/NSIS installers are built in CI, not here."

# ============================================================
# Setup
# ============================================================

setup:
	@bash scripts/setup-build-env.sh

install-wails:
	@go install github.com/wailsapp/wails/v2/cmd/wails@latest

deps:
	@go mod download
	@go mod tidy

# ============================================================
# Develop
# ============================================================

dev:
	@bash scripts/build-dev.sh

dev-public:
	@bash scripts/build-dev.sh public

fmt:
	@go fmt ./...

vet:
	@go vet ./...

test: verify-docs
	@go test ./...

verify-docs:
	@python3 scripts/check_documentation.py

# ============================================================
# Build
# ============================================================

build:
	@bash scripts/build-current.sh

build-public:
	@bash scripts/build-current.sh public

build-all:
	@bash scripts/build-all.sh $(VERSION)

runtime-bundle:
	@VERSION="$(VERSION)" LIGANDX_PUBLIC_REPO="$(PUBLIC_REPO)" bash scripts/build-runtime-bundle.sh

sync-runtime-topology:
	@LIGANDX_PUBLIC_REPO="$(PUBLIC_REPO)" bash scripts/sync-runtime-topology.sh

check-runtime-topology:
	@LIGANDX_PUBLIC_REPO="$(PUBLIC_REPO)" bash scripts/check-runtime-topology.sh

pin-release:
	@if [ -z "$(RELEASE)" ]; then echo "Usage: make pin-release RELEASE=vX.Y.Z"; exit 1; fi
	@bash scripts/set-release-version.sh "$(RELEASE)"

validate-staging:
	@bash scripts/validate-staging-startup.sh

# ============================================================
# Clean
# ============================================================

clean:
	@rm -rf build/bin dist
	@rm -f *.AppImage *.dmg *.exe
	@echo "Cleaned build/bin, dist, and packaged artifacts."
