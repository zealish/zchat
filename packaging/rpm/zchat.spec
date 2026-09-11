%global appid com.zealish.ZChat

# Go links its own binaries, but RPM feeds these CFLAGS into every cgo
# compilation unit. gotk4 is ~380k lines of generated bindings, so -flto=auto
# -ffat-lto-objects makes cc1 grind for tens of minutes with no benefit to a
# statically linked Go binary.
%global _lto_cflags %{nil}

# find-debuginfo over ~110MB of Go binaries is slow and the DWARF it extracts is
# not usable by the usual RPM debuginfo tooling. Strip at link time instead.
%global debug_package %{nil}

# The default w19.zstdio spends minutes squeezing a payload that level 3 packs
# nearly as small.
%global _binary_payload w3.zstdio

Name:           zchat
Version:        %{?_zchat_version}%{!?_zchat_version:0.5.0}
Release:        1%{?dist}
Summary:        Native Linux desktop client for WhatsApp Multi-Device

License:        GPL-3.0-or-later
URL:            https://github.com/zealish/zchat
Source0:        %{name}-%{version}.tar.gz

BuildRequires:  golang >= 1.24
BuildRequires:  gcc
BuildRequires:  pkgconfig(gtk4)
BuildRequires:  pkgconfig(libadwaita-1)
BuildRequires:  blueprint-compiler
BuildRequires:  glib2-devel
BuildRequires:  desktop-file-utils

Requires:       gtk4
Requires:       libadwaita

%description
ZChat is a lightweight GTK4 and Libadwaita client for WhatsApp Multi-Device.
It talks to a local background daemon over a Unix socket and stores chats,
messages and media in the user's XDG directories.

ZChat is an independent open source project and is not affiliated with,
endorsed by, or sponsored by WhatsApp LLC or Meta Platforms, Inc.

%prep
%autosetup -n %{name}-%{version}

%build
# The tarball ships the module cache in vendor/, so the build never needs the
# network.
export GOFLAGS="-mod=vendor"
export CGO_ENABLED=1

# cgo does not read RPM's CFLAGS/LDFLAGS, only CGO_CFLAGS/CGO_LDFLAGS, which
# default to "-O2 -g". Leaving those defaults alone matters: the go build cache
# keys on them, and overriding them invalidates every one of the ~380k lines of
# generated gotk4 bindings on each build.
#
# -s -w strips the symbol table and DWARF, matching %%{debug_package} %%{nil}
# above. -trimpath keeps the build reproducible.
make build GOFLAGS="-mod=vendor -trimpath" GO_LDFLAGS="-s -w"

%install
install -Dpm 0755 build/%{name} %{buildroot}%{_bindir}/%{name}
install -Dpm 0755 build/%{name}-daemon %{buildroot}%{_bindir}/%{name}-daemon
install -Dpm 0644 packaging/%{appid}.desktop %{buildroot}%{_datadir}/applications/%{appid}.desktop
install -Dpm 0644 packaging/%{appid}.metainfo.xml %{buildroot}%{_datadir}/metainfo/%{appid}.metainfo.xml
install -Dpm 0644 packaging/%{appid}.svg %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/%{appid}.svg

%check
desktop-file-validate %{buildroot}%{_datadir}/applications/%{appid}.desktop

%files
%license LICENSE
%doc PRD.md
%{_bindir}/%{name}
%{_bindir}/%{name}-daemon
%{_datadir}/applications/%{appid}.desktop
%{_datadir}/metainfo/%{appid}.metainfo.xml
%{_datadir}/icons/hicolor/scalable/apps/%{appid}.svg

%changelog
* Fri Sep 11 2026 Zealish <dev@zealish.com> - 0.5.0-1
- Typing indicators, contact presence and animated WebP stickers

* Fri Sep 11 2026 Zealish <dev@zealish.com> - 0.4.0-1
- Media sending, read receipts, keyboard shortcuts and drag & drop
