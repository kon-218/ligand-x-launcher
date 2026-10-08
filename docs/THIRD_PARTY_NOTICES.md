# Third-Party Notices — Ligand-X Launcher

The Ligand-X Launcher is © Ligand-X Inc. and licensed under PolyForm Noncommercial 1.0.0 (see
`LICENSE`). The distributed launcher binaries statically link the Go modules listed here, each under
its own licence. Those licences apply to the identified components only, not to the Launcher as a
whole.

Because Go links dependencies into the executable, every binary we publish for Windows, Linux, and
macOS is a redistribution of these components, and their attribution requirements travel with it.
That is what this file discharges.

**How this list was produced.** Licences were read from the `LICENSE` file inside each module in the
Go module cache — not from memory or from a licence database. Regenerate after any dependency change:

```bash
go-licenses report ./...            # if installed
go list -m all                      # authoritative module list, including transitive
```

The table below covers the modules declared in `go.mod` (50 of them: Apache-2.0 (20) · BSD-2-Clause (4) · BSD-3-Clause (5) · ISC (1) · MIT (20)).
`go list -m all` expands to the full transitive set; the declared set is what we track here.

**No copyleft.** Every component is permissive and attribution-only. There is no GPL or LGPL code in
the launcher, so — unlike `ligand-x/` and `ligand-x-pro/` — this file carries no written offer for
source. **None of the Apache-2.0 modules listed below ships an upstream `NOTICE` file**, checked
individually in the module cache, so there is no NOTICE content requiring propagation under
Apache-2.0 section 4(d); the obligation is the licence copy and attribution provided here.

The embedded UI (`frontend/`, `frontend-public/`) is hand-written HTML/CSS/JavaScript plus
Wails-generated bindings. It has no `package.json` and bundles no third-party JavaScript, so there is
nothing further to declare for it.

---

## Direct dependencies

| Component | Version | Licence |
|---|---|---|
| `github.com/moby/moby/api` | v1.54.2 | Apache-2.0 |
| `github.com/moby/moby/client` | v0.4.1 | Apache-2.0 |
| `github.com/wailsapp/wails/v2` | v2.11.0 | MIT |
| `golang.org/x/sys` | v0.42.0 | BSD-3-Clause |

## Transitive dependencies

| Component | Version | Licence |
|---|---|---|
| `github.com/Microsoft/go-winio` | v0.6.2 | MIT |
| `github.com/bep/debounce` | v1.2.1 | MIT |
| `github.com/cespare/xxhash/v2` | v2.3.0 | MIT |
| `github.com/containerd/errdefs` | v1.0.0 | Apache-2.0 |
| `github.com/containerd/errdefs/pkg` | v0.3.0 | Apache-2.0 |
| `github.com/distribution/reference` | v0.6.0 | Apache-2.0 |
| `github.com/docker/go-connections` | v0.7.0 | Apache-2.0 |
| `github.com/docker/go-units` | v0.5.0 | Apache-2.0 |
| `github.com/felixge/httpsnoop` | v1.0.4 | MIT |
| `github.com/go-logr/logr` | v1.4.3 | Apache-2.0 |
| `github.com/go-logr/stdr` | v1.2.2 | Apache-2.0 |
| `github.com/go-ole/go-ole` | v1.3.0 | MIT |
| `github.com/godbus/dbus/v5` | v5.1.0 | BSD-2-Clause |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/gorilla/websocket` | v1.5.3 | BSD-2-Clause |
| `github.com/jchv/go-winloader` | v0.0.0-20210711035445-715c2860da7e | ISC |
| `github.com/labstack/echo/v4` | v4.13.3 | MIT |
| `github.com/labstack/gommon` | v0.4.2 | MIT |
| `github.com/leaanthony/go-ansi-parser` | v1.6.1 | MIT |
| `github.com/leaanthony/gosod` | v1.0.4 | MIT |
| `github.com/leaanthony/slicer` | v1.6.0 | MIT |
| `github.com/leaanthony/u` | v1.1.1 | MIT |
| `github.com/mattn/go-colorable` | v0.1.13 | MIT |
| `github.com/mattn/go-isatty` | v0.0.20 | MIT |
| `github.com/moby/docker-image-spec` | v1.3.1 | Apache-2.0 |
| `github.com/opencontainers/go-digest` | v1.0.0 | Apache-2.0 |
| `github.com/opencontainers/image-spec` | v1.1.1 | Apache-2.0 |
| `github.com/pkg/browser` | v0.0.0-20240102092130-5ac0b6a4141c | BSD-2-Clause |
| `github.com/pkg/errors` | v0.9.1 | BSD-2-Clause |
| `github.com/rivo/uniseg` | v0.4.7 | MIT |
| `github.com/samber/lo` | v1.49.1 | MIT |
| `github.com/tkrajina/go-reflector` | v0.5.8 | Apache-2.0 |
| `github.com/valyala/bytebufferpool` | v1.0.0 | MIT |
| `github.com/valyala/fasttemplate` | v1.2.2 | MIT |
| `github.com/wailsapp/go-webview2` | v1.0.22 | MIT |
| `github.com/wailsapp/mimetype` | v1.4.1 | MIT |
| `go.opentelemetry.io/auto/sdk` | v1.2.1 | Apache-2.0 |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | v0.66.0 | Apache-2.0 |
| `go.opentelemetry.io/otel` | v1.43.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/metric` | v1.43.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/sdk` | v1.43.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/sdk/metric` | v1.43.0 | Apache-2.0 |
| `go.opentelemetry.io/otel/trace` | v1.43.0 | Apache-2.0 |
| `golang.org/x/crypto` | v0.49.0 | BSD-3-Clause |
| `golang.org/x/net` | v0.52.0 | BSD-3-Clause |
| `golang.org/x/text` | v0.35.0 | BSD-3-Clause |

---

## Licence texts

Full licence texts ship with each module and are reproduced in the Go module cache under
`$(go env GOMODCACHE)/<module>@<version>/LICENSE`. Ligand-X Inc. will provide a copy of any licence
text listed above on request: **support@ligand-x.com**.

## Software the Launcher does not redistribute

The Launcher *downloads* Ligand-X container images at install time; it does not embed them. The
third-party software inside those images is covered by `THIRD_PARTY_NOTICES.md` in the `ligand-x` and
`ligand-x-pro` repositories, which travel with the images. Docker/Docker Desktop and ORCA are
supplied and licensed by the operator, not by Ligand-X Inc.

## Attribution

Wails (`github.com/wailsapp/wails/v2`, MIT) provides the desktop application framework.
The Docker Engine API client (`github.com/moby/moby/client`, Apache-2.0) is used to manage the
Ligand-X containers.
