%global appid com.zealish.ZChat

Name:           zchat
Version:        %{?_zchat_version}%{!?_zchat_version:0.3.0}
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
make build

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
* Fri Sep 11 2026 Zealish <dev@zealish.com> - 0.3.0-1
- Media sending, read receipts, keyboard shortcuts and drag & drop
