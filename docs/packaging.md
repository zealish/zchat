# Packaging

ZChat ships as an RPM and a Flatpak. Both are driven from the Makefile and both
build from the same vendored source tarball (PRD §16).

```bash
make rpm VERSION=0.7.0
make flatpak VERSION=0.7.0
```

`VERSION ?= 0.7.0` in the Makefile, so plain `make rpm` / `make flatpak` use
that default. `APP_ID := com.zealish.ZChat`, `DIST_DIR := dist`.

## `make dist-tarball`

Both packaging targets depend on it. The Makefile comment states the purpose:

> `dist-tarball` produces the vendored source archive both packaging paths build
> from, so neither needs network access at build time. The tree is listed via git
> so ignored build output stays out, but the files come from the working copy
> rather than HEAD.

Steps:

1. `rm -rf dist/zchat-$(VERSION)` and recreate it.
2. `git ls-files -z --cached --others --exclude-standard | tar --null -T - -c -f - | tar -x -C dist/zchat-$(VERSION)`
   — copies the tracked-and-not-ignored working tree, so `build/`, `dist/` and
   generated artifacts are excluded while uncommitted edits are included.
3. `cd dist/zchat-$(VERSION) && go mod vendor` — materialises every dependency
   into `vendor/`.
4. `tar -czf dist/zchat-$(VERSION).tar.gz -C dist zchat-$(VERSION)`.
5. Removes the staging directory.

### Why vendoring makes the build offline

RPM build roots and Flatpak sandboxes have no network access (and should not:
downloading modules at build time would make the result unreproducible and
unauditable). With `vendor/` in the tarball, both consumers set
`GOFLAGS=-mod=vendor` and the Go toolchain resolves every import from the
archive itself:

- The spec: `export GOFLAGS="-mod=vendor"`, with the comment "The tarball ships
  the module cache in `vendor/`, so the build never needs the network."
- The manifest: `build-options.env.GOFLAGS: -mod=vendor` and
  `GOTOOLCHAIN: local`, with the source comment "Built from the same vendored
  tarball as the RPM, so the sandbox never needs network access to fetch Go
  modules."

`GOTOOLCHAIN: local` matters for the same reason — it stops Go from trying to
download a newer toolchain to satisfy the `go` directive in `go.mod`.

## RPM

```bash
make rpm VERSION=0.7.0
```

After `dist-tarball`, the target:

1. Creates `dist/rpmbuild/SOURCES` and copies `dist/zchat-$(VERSION).tar.gz`
   into it.
2. Runs `rpmbuild --define "_topdir $(CURDIR)/dist/rpmbuild" --define
   "_zchat_version $(VERSION)" -bb packaging/rpm/zchat.spec` — `-bb` builds
   binary packages only.
3. Copies every `*.rpm` found under `dist/rpmbuild/RPMS` into `dist/`.

PRD §16 gives the expected output shape: `dist/zchat-x.y.z.fc42.x86_64.rpm`.

### Spec highlights (`packaging/rpm/zchat.spec`)

- `%global appid com.zealish.ZChat`
- `Version: %{?_zchat_version}%{!?_zchat_version:0.7.0}` — takes the version
  the Makefile passes in, falling back to `0.7.0`.
- `Release: 1%{?dist}`, `License: GPL-3.0-or-later`,
  `URL: https://github.com/zealish/zchat`, `Source0: %{name}-%{version}.tar.gz`.
- `BuildRequires: golang >= 1.24`, `gcc`, `pkgconfig(gtk4)`,
  `pkgconfig(libadwaita-1)`, `blueprint-compiler`, `glib2-devel`,
  `desktop-file-utils`. `Requires: gtk4`, `libadwaita`.

#### The cgo CFLAGS / LTO workaround

```spec
# Go links its own binaries, but RPM feeds these CFLAGS into every cgo
# compilation unit. gotk4 is ~380k lines of generated bindings, so -flto=auto
# -ffat-lto-objects makes cc1 grind for tens of minutes with no benefit to a
# statically linked Go binary.
%global _lto_cflags %{nil}
```

Fedora's default `%_lto_cflags` (`-flto=auto -ffat-lto-objects`) reaches every
cgo compilation unit. Since Go does its own linking, LTO buys nothing here and
costs tens of minutes across gotk4's generated bindings, so it is blanked out.

The `%build` section makes the complementary point about *not* touching
`CGO_CFLAGS`:

```spec
# cgo does not read RPM's CFLAGS/LDFLAGS, only CGO_CFLAGS/CGO_LDFLAGS, which
# default to "-O2 -g". Leaving those defaults alone matters: the go build cache
# keys on them, and overriding them invalidates every one of the ~380k lines of
# generated gotk4 bindings on each build.
```

#### Other notable choices

- `%global debug_package %{nil}` — "find-debuginfo over ~110MB of Go binaries is
  slow and the DWARF it extracts is not usable by the usual RPM debuginfo
  tooling. Strip at link time instead." The stripping happens via
  `GO_LDFLAGS="-s -w"`.
- `%global _binary_payload w3.zstdio` — "The default w19.zstdio spends minutes
  squeezing a payload that level 3 packs nearly as small."
- The build line is a plain Makefile invocation:

  ```bash
  make build GOFLAGS="-mod=vendor -trimpath" GO_LDFLAGS="-s -w"
  ```

  `-s -w` strips the symbol table and DWARF, matching `%{debug_package} %{nil}`;
  `-trimpath` keeps the build reproducible. Note this runs `make build`, which
  depends on `ui`, so `blueprint-compiler` and `glib2-devel` are build
  requirements.
- `CGO_ENABLED=1` is exported for the desktop binary; the Makefile still builds
  the daemon with `CGO_ENABLED=0`.

#### Install and files

```
%{_bindir}/zchat
%{_bindir}/zchat-daemon
%{_datadir}/applications/com.zealish.ZChat.desktop
%{_datadir}/metainfo/com.zealish.ZChat.metainfo.xml
%{_datadir}/icons/hicolor/scalable/apps/com.zealish.ZChat.svg
```

plus `%license LICENSE` and `%doc README.md`. `%check` runs
`desktop-file-validate` on the installed desktop file.

The `%changelog` records `0.7.0-1` ("Gate the UI on a progress screen until the
first full sync completes"), `0.6.0-1` ("Emoji picker, find in conversation and
a preferences dialog"), `0.5.0-1` ("Typing indicators, contact presence and
animated WebP stickers"), and `0.4.0-1` ("Media sending, read receipts,
keyboard shortcuts and drag & drop").

## Flatpak

```bash
make flatpak VERSION=0.7.0
```

After `dist-tarball`:

1. `cp dist/zchat-$(VERSION).tar.gz dist/zchat-src.tar.gz`. The Makefile
   comment explains: "The manifest cannot interpolate make variables, so the
   versioned tarball is copied to a stable name the flatpak source can point
   at." The manifest's source is `path: ../../dist/zchat-src.tar.gz`.
2. `$(FLATPAK_BUILDER) --force-clean --repo=dist/flatpak-repo
   dist/flatpak-build packaging/flatpak/com.zealish.ZChat.yaml`
   (`FLATPAK_BUILDER ?= flatpak-builder`).
3. `flatpak build-bundle dist/flatpak-repo dist/zchat-$(VERSION).flatpak
   com.zealish.ZChat`.

### Manifest (`packaging/flatpak/com.zealish.ZChat.yaml`)

- `app-id: com.zealish.ZChat`
- `runtime: org.gnome.Platform`, `runtime-version: '50'`
- `sdk: org.gnome.Sdk`
- `sdk-extensions: [org.freedesktop.Sdk.Extension.golang]` — the Go toolchain
  is not in the GNOME SDK, so it comes from the extension, whose bin directory
  is added via `build-options.append-path: /usr/lib/sdk/golang/bin`.
- `command: zchat`

`finish-args`:

| Permission | Reason |
|---|---|
| `--share=network` | WhatsApp connection |
| `--share=ipc` | X11 shared memory |
| `--socket=wayland` | native Wayland (PRD §9) |
| `--socket=fallback-x11` | X11 compatibility |
| `--device=dri` | GPU rendering |
| `--filesystem=xdg-download`, `--filesystem=xdg-pictures`, `--filesystem=xdg-documents` | "Sending attachments needs read access to the files the user picks" — the daemon opens `SendMediaRequest.file_path` itself |
| `--talk-name=org.freedesktop.Notifications` | native notifications |

No socket or port hole is punched for IPC: the daemon and client run inside the
same sandbox and share `$XDG_RUNTIME_DIR`, so the Unix socket needs no
permission.

`build-options.env`: `GOFLAGS: -mod=vendor`, `GOTOOLCHAIN: local`.

The single module `zchat` uses `buildsystem: simple` with:

```yaml
- make build
- install -Dpm 0755 build/zchat /app/bin/zchat
- install -Dpm 0755 build/zchat-daemon /app/bin/zchat-daemon
- install -Dpm 0644 packaging/com.zealish.ZChat.desktop /app/share/applications/...
- install -Dpm 0644 packaging/com.zealish.ZChat.metainfo.xml /app/share/metainfo/...
- install -Dpm 0644 packaging/com.zealish.ZChat.svg /app/share/icons/hicolor/scalable/apps/...
```

Installing `zchat-daemon` next to `zchat` in `/app/bin` is what lets
`daemonctl.resolveBinary()` find it as a sibling of the running executable,
with no `ZCHAT_DAEMON` override needed.

## AppStream metainfo

`packaging/com.zealish.ZChat.metainfo.xml` is installed to
`$datadir/metainfo/com.zealish.ZChat.metainfo.xml` by both packaging paths. It
is a `component type="desktop-application"` with:

- `<id>com.zealish.ZChat</id>`
- `<metadata_license>CC0-1.0</metadata_license>`,
  `<project_license>GPL-3.0-or-later</project_license>`
- `<name>ZChat</name>`,
  `<summary>Native Linux desktop client for WhatsApp Multi-Device</summary>`
- a description covering the GTK4/Libadwaita client, the local daemon over a
  Unix socket, XDG storage, and the independence disclaimer (PRD §22)
- `<launchable type="desktop-id">com.zealish.ZChat.desktop</launchable>`
- `<url type="homepage">https://github.com/zealish/zchat</url>`
- `<developer id="com.zealish"><name>Zealish</name></developer>`
- `<content_rating type="oars-1.1">` with
  `<content_attribute id="social-chat">intense</content_attribute>`

Release entries, newest first:

| Version | Date | Description |
|---|---|---|
| 0.7.0 | 2026-09-12 | Sync progress screen gating the UI until the first full history sync finishes, plus duplicate chat merging. |
| 0.6.0 | 2026-09-12 | Emoji picker, find in conversation, and a preferences dialog with theme selection. |
| 0.5.0 | 2026-09-11 | Typing indicators, contact presence, and animated WebP stickers. |
| 0.4.0 | 2026-09-11 | Sending attachments, read receipts, and RPM and Flatpak packaging. |
| 0.3.0 | 2026-09-11 | Replies, forwarding, message deletion, and pinned, archived and muted chats. |

A new release needs a matching `<release>` entry here and a `%changelog` entry
in the spec; software centres read the former for update notes.

The companion `packaging/com.zealish.ZChat.desktop` sets `Exec=zchat`,
`Icon=com.zealish.ZChat`,
`Categories=Network;InstantMessaging;Chat;` and
`StartupWMClass=com.zealish.ZChat` (matching the `appID` the client passes to
`adw.NewApplication`).

## Release workflow

`.github/workflows/release.yml`:

```yaml
name: Release
on:
  release:
    types: [published]
jobs:
  source:
    runs-on: ubuntu-latest
    container: fedora:44
```

The job runs on a published GitHub release and performs the same verification
steps as CI:

1. `dnf install -y gcc git make gtk4-devel libadwaita-devel blueprint-compiler`
   — the container is `fedora:44` because "Adw.ToggleGroup in window.blp needs
   libadwaita 1.7+, newer than the runner image ships. Fedora's GNOME 48
   packages are new enough."
2. `actions/checkout@v4`
3. `git config --global --add safe.directory "$GITHUB_WORKSPACE"` — the checkout
   is owned by a different uid than the container user, which otherwise breaks
   git status reporting and Go's VCS stamping.
4. `actions/setup-go@v5` with `go-version-file: go.mod`, because "Fedora's
   golang package trails the go directive in go.mod".
5. `make ui`
6. `go test ./...`
7. `go build ./apps/daemon`
8. `go build ./apps/desktop`

The workflow does not run `make rpm` or `make flatpak` and does not upload
artifacts; it validates that the published tag builds and tests clean.
Package artifacts are produced locally with `make rpm VERSION=...` /
`make flatpak VERSION=...` and land in `dist/`.
