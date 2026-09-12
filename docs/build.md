# Building ZChat

## Prerequisites

`README.md` states the requirements as: **Go 1.24+, GTK4, Libadwaita, Blueprint
Compiler, and SQLite.**

More precisely:

| Tool | Version / note | Needed for |
|---|---|---|
| Go | `go.mod` declares `go 1.26.7`; the RPM spec requires `golang >= 1.24` | both binaries |
| GTK4 | `pkgconfig(gtk4)` in the RPM spec | `zchat` (cgo, gotk4) |
| Libadwaita | `pkgconfig(libadwaita-1)`; **1.7+** is required — CI notes that `Adw.ToggleGroup` in `window.blp` needs libadwaita 1.7+ | `zchat` |
| `blueprint-compiler` | — | compiling `ui/window.blp` |
| `glib-compile-resources` | ships with `glib2-devel` | building `ui/zchat.gresource` |
| SQLite | no dev package needed: the daemon uses `modernc.org/sqlite v1.58.0`, a pure-Go driver, and builds with `CGO_ENABLED=0` | daemon storage |
| `gcc` | — | cgo for the desktop binary |
| `protoc` | only when `proto/zchat/v1/zchat.proto` changes | `make proto` |

On Fedora, the dependency set CI installs is:

```bash
dnf install -y gcc git make gtk4-devel libadwaita-devel blueprint-compiler
```

Key Go dependencies from `go.mod`: `github.com/diamondburned/gotk4/pkg v0.4.1`,
`github.com/diamondburned/gotk4-adwaita/pkg`, `go.mau.fi/whatsmeow`,
`google.golang.org/grpc v1.83.2`, `google.golang.org/protobuf v1.36.12`,
`github.com/rs/zerolog v1.35.1`, `modernc.org/sqlite v1.58.0`.

## Quick start

```bash
make test
make build
```

Or, to build and immediately run the client against the freshly built daemon:

```bash
make run
```

## Makefile targets

Variables: `GO ?= go`, `UI_DIR := apps/desktop/ui`, `BUILD_DIR := build`,
`DIST_DIR := dist`, `APP_ID := com.zealish.ZChat`, `VERSION ?= 0.7.1`,
`GO_LDFLAGS ?=`, `FLATPAK_BUILDER ?= flatpak-builder`.

`all` is `build`.

### `make proto`

Installs the pinned protobuf plugins and regenerates
`packages/ipc/zchatv1/`:

- `protoc-gen-go` — `PROTOC_GEN_GO_VERSION := v1.36.12`
- `protoc-gen-go-grpc` — `PROTOC_GEN_GO_GRPC_VERSION := v1.6.2`

They are installed into `GOBIN := $(go env GOPATH)/bin`, which is prepended to
`PATH` for the `protoc` invocation. Only needed when the `.proto` changes; the
generated files are committed.

### `make ui`

```bash
blueprint-compiler compile apps/desktop/ui/window.blp --output apps/desktop/ui/window.ui
glib-compile-resources --sourcedir=apps/desktop/ui \
	--target=apps/desktop/ui/zchat.gresource apps/desktop/ui/zchat.gresource.xml
```

### `make daemon`

```bash
CGO_ENABLED=0 go build -ldflags '-X main.version=$(VERSION) $(GO_LDFLAGS)' \
	-o build/zchat-daemon ./apps/daemon
```

Pure Go — no cgo, because the SQLite driver is `modernc.org/sqlite`.

### `make desktop`

Depends on `ui`, then:

```bash
CGO_ENABLED=1 go build -ldflags '-X main.version=$(VERSION) $(GO_LDFLAGS)' \
	-o build/zchat ./apps/desktop
```

cgo is mandatory here: gotk4 binds GTK4 and Libadwaita.

### `make build`

`daemon` + `desktop`. Produces `build/zchat-daemon` and `build/zchat`.

### `make run`

Depends on `build`, then:

```bash
ZCHAT_DAEMON=./build/zchat-daemon ./build/zchat
```

### `make test`

`go test ./...`

### `make vet`

`go vet ./...`

### `make clean`

`rm -rf build apps/desktop/ui/window.ui apps/desktop/ui/zchat.gresource` — the
binaries plus both generated UI artifacts.

### `make reset-session`

Wipes the local WhatsApp session so the next start shows a fresh QR code. It:

1. Finds `zchat-daemon` with `pgrep -u "$(id -u)" -x zchat-daemon` and, if
   running, `kill`s it and waits up to 5 seconds; failure to stop cleanly is a
   hard error.
2. Refuses to continue if a `zchat` client is still running
   (`Close ZChat before resetting the session.`).
3. Removes `$XDG_DATA_HOME/zchat/zchat.db` plus its `-wal` and `-shm` files,
   defaulting `XDG_DATA_HOME` to `$HOME/.local/share`.

### `make dist-tarball`, `make rpm`, `make flatpak`

See [packaging.md](packaging.md).

## The blueprint → .ui → gresource pipeline

```
apps/desktop/ui/window.blp
   │ blueprint-compiler compile
   ▼
apps/desktop/ui/window.ui
   │ glib-compile-resources (with ui/zchat.gresource.xml, plus ui/style.css)
   ▼
apps/desktop/ui/zchat.gresource
   │ //go:embed ui/zchat.gresource   (apps/desktop/main.go)
   ▼
build/zchat
```

`apps/desktop/ui/zchat.gresource.xml` declares the prefix `/com/zealish/ZChat`
with two entries: `window.ui` (with `preprocess="xml-stripblanks"`) and
`style.css`.

At runtime `apps/desktop/main.go` turns the embedded blob into a `gio` resource
and registers it; `newWindow` then loads
`gtk.NewBuilderFromResource("/com/zealish/ZChat/window.ui")` and `loadCSS`
loads `/com/zealish/ZChat/style.css`.

**`make ui` must run before any Go build of the desktop binary.** `main.go`
embeds the compiled resource with:

```go
//go:embed ui/zchat.gresource
var gresourceData []byte
```

`go:embed` is resolved at compile time, so if `ui/zchat.gresource` does not
exist the desktop package fails to build. `make desktop` declares `ui` as a
prerequisite for exactly this reason, and both `make clean` and a fresh clone
leave the file absent — which is why CI runs `make ui` before `go build
./apps/desktop`.

## Version stamping

`VERSION ?= 0.7.1` feeds:

```make
GO_BUILD_FLAGS := -ldflags '-X main.version=$(VERSION) $(GO_LDFLAGS)'
```

The Makefile comment explains: "main.version is stamped into both binaries so
the About dialog and `--version` report the packaged release rather than a
hardcoded constant."

`apps/desktop/main.go` declares the target symbol:

```go
// version is stamped by the Makefile via -ldflags; "dev" marks an ad-hoc build.
var version = "dev"
```

`apps/desktop/about.go` passes it to `dialog.SetVersion(version)`, so an
un-stamped build reports `dev` in the About dialog.

`GO_LDFLAGS` is the extension point packaging uses: the RPM spec builds with
`GO_LDFLAGS="-s -w"` to strip the symbol table and DWARF.

Override the version per build:

```bash
make build VERSION=0.7.1
```

## Running the client against a locally built daemon

`apps/desktop/daemonctl/daemonctl.go` resolves the daemon binary in this order
(`resolveBinary`):

1. `$ZCHAT_DAEMON`, if non-empty — used verbatim as the executable path.
2. `zchat-daemon` sitting next to the running client executable.
3. `zchat-daemon` found on `$PATH`.

So after `make build` either of these works:

```bash
make run                                   # sets ZCHAT_DAEMON for you
ZCHAT_DAEMON=./build/zchat-daemon ./build/zchat
./build/zchat                              # finds ./build/zchat-daemon as a sibling
```

`EnsureRunning` first probes the socket with a `GetConnectionState` call
(300 ms timeout) and only spawns when nothing answers, so an already-running
daemon — including one you started by hand — is reused. Spawning uses
`zchat-daemon --socket <path>` with `Setsid: true`, and waits up to 10 seconds
for the socket to answer.

To run the daemon yourself:

```bash
./build/zchat-daemon --debug
./build/zchat-daemon --socket /tmp/zchat-test.sock
```

`--socket` defaults to `xdgpaths.SocketPath()`
(`$XDG_RUNTIME_DIR/zchat.sock`, else `/run/user/$UID/zchat.sock`). A second
daemon on an occupied socket logs `daemon already running` and exits.

## Debug logging

- Desktop: `ZCHAT_DEBUG` — `apps/desktop/main.go` calls
  `logging.Setup("desktop", os.Getenv("ZCHAT_DEBUG") != "")`, so any non-empty
  value switches zerolog from `InfoLevel` to `DebugLevel`.

  ```bash
  ZCHAT_DEBUG=1 ./build/zchat
  ```

- Daemon: the `--debug` flag does the same for
  `logging.Setup("daemon", *debug)`.

Both binaries log to stderr (so `journalctl` picks them up when launched from a
desktop session) and to `~/.local/share/zchat/logs/desktop.log` and
`~/.local/share/zchat/logs/daemon.log`.

## Continuous integration

`.github/workflows/ci.yml` — job `go`, triggered on `push` and `pull_request`:

- `runs-on: ubuntu-latest` inside `container: fedora:44`. The comment explains
  why: "Adw.ToggleGroup in window.blp needs libadwaita 1.7+, newer than the
  runner image ships. Fedora's GNOME 48 packages are new enough."
- `dnf install -y gcc git make gtk4-devel libadwaita-devel blueprint-compiler`
- `actions/checkout@v4`
- `git config --global --add safe.directory "$GITHUB_WORKSPACE"` — "The checkout
  is owned by a different uid than the container user, so git refuses to report
  status and go's VCS stamping fails."
- `actions/setup-go@v5` with `go-version-file: go.mod` — "Fedora's golang
  package trails the go directive in go.mod, so the toolchain comes from
  setup-go rather than the container."
- `make ui`
- `go test ./...`
- `go build ./apps/daemon`
- `go build ./apps/desktop`

CI does not run `make vet`, `make proto`, or any packaging target.
`.github/workflows/release.yml` runs the identical steps under the job name
`source`, triggered on published releases.
