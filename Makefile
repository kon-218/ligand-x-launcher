# ============================================================
# Ligand-X Launcher Makefile
# ============================================================
# Cross-platform desktop installer/launcher built with Wails
# (Go + web frontend). Run `make help` for the target list.
#
# Packaged AppImage/DMG artifacts and the portable Windows release executable
# are produced by the centrally-dispatched CI workflow, not this Makefile.
# ============================================================

.PHONY: help dev dev-public build build-public runtime-bundle sync-runtime-topology check-runtime-topology pin-release validate-staging test verify-docs fmt vet deps clean install-wails

# ============================================================
# Configuration
# ============================================================

PUBLIC_REPO ?= ../ligand-x
WAILS       := $(shell go env GOPATH)/bin/wails

# Ubuntu 24.04+ ships only webkit2gtk-4.1; Wails needs the webkit2_41 tag to
# link against it (the release workflow passes the same tag).
ifeq ($(shell uname -s),Linux)
  ifeq ($(shell pkg-config --exists webkit2gtk-4.1 && echo yes),yes)
    WEBKIT_TAG := webkit2_41
  endif
endif
empty :=
space := $(empty) $(empty)
comma := ,
dev_tags    = $(WEBKIT_TAG)
public_tags = $(subst $(space),$(comma),$(strip public $(WEBKIT_TAG)))

# The runtime-bundle trust root is compiled in, not shipped alongside. A build
# without it fails closed: no user can install a runtime bundle. This is the
# public half of the release signing key (the private half lives outside this
# repo); override it to test against a different signing key.
LIGANDX_RUNTIME_PUBKEY ?= F1fZr213yHMWwiTMsLn8hIhxaGyjjiIDZjjPfjebzfY=
LDFLAGS := -X main.runtimeBundlePublicKeyB64=$(LIGANDX_RUNTIME_PUBKEY)

# ============================================================
# Help
# ============================================================

help:
	@echo "Ligand-X Launcher Commands"
	@echo ""
	@echo "Setup:"
	@echo "  make install-wails    - Install the Wails CLI via go install"
	@echo "  make deps             - Download and tidy Go modules"
	@echo ""
	@echo "Develop:"
	@echo "  make dev              - Run dev launcher with hot reload (wails dev)"
	@echo "  make dev-public       - Run the public launcher (frontend-public/) with hot reload"
	@echo "  make fmt              - Format Go sources (go fmt)"
	@echo "  make vet              - Static checks for both build variants (go vet)"
	@echo "  make test             - Docs check + Go tests for both build variants"
	@echo "  make verify-docs      - Check links and reject agent-session artifacts"
	@echo ""
	@echo "Build:"
	@echo "  make build            - Build dev launcher for current platform -> build/bin/ligandx-launcher"
	@echo "  make build-public     - Build public launcher for current platform -> build/bin/ligandx"
	@echo "  make runtime-bundle VERSION=vX.Y.Z - Build and validate ligand-x-runtime.zip -> dist/"
	@echo ""
	@echo "Runtime topology (release tooling):"
	@echo "  make sync-runtime-topology  - Regenerate Compose snapshot + env template from ../ligand-x"
	@echo "  make check-runtime-topology - Fail if the Compose snapshot or env template has drifted"
	@echo "  make validate-staging       - Start pinned prod images and verify every service is healthy"
	@echo "  make pin-release RELEASE=vX.Y.Z - Pin .env.production VERSION to a release"
	@echo ""
	@echo "  make clean            - Remove build/bin, dist, and packaged artifacts"
	@echo ""
	@echo "Note: release packaging is dispatched only by the central Pro release workflow."

# ============================================================
# Setup
# ============================================================

install-wails:
	@go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0

deps:
	@go mod download
	@go mod tidy

$(WAILS):
	@echo "wails CLI not found at $(WAILS). Run: make install-wails" >&2
	@exit 1

# ============================================================
# Develop
# ============================================================

dev: | $(WAILS)
	@$(WAILS) dev $(if $(dev_tags),-tags $(dev_tags))

dev-public: | $(WAILS)
	@$(WAILS) dev -tags $(public_tags) -ldflags "$(LDFLAGS)"

fmt:
	@go fmt ./...

vet:
	@go vet ./...
	@go vet -tags public ./...

test: verify-docs
	@go test ./...
	@go test -tags public ./...

verify-docs:
	@python3 scripts/check_documentation.py

# ============================================================
# Build
# ============================================================

build: | $(WAILS)
	@$(WAILS) build $(if $(dev_tags),-tags $(dev_tags))

build-public: | $(WAILS)
	@$(WAILS) build -tags $(public_tags) -ldflags "$(LDFLAGS)" -o ligandx

runtime-bundle:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make runtime-bundle VERSION=vX.Y.Z"; exit 1; fi
	@VERSION="$(VERSION)" LIGANDX_PUBLIC_REPO="$(PUBLIC_REPO)" bash scripts/build-runtime-bundle.sh

# ============================================================
# Runtime topology
# ============================================================

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
	@rm -f *.AppImage *.dmg *.exe ligandx-launcher
	@echo "Cleaned build/bin, dist, and packaged artifacts."
