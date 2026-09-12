# ZChat

Native Linux desktop client for WhatsApp Multi-Device, powered by WhatsMeow.

## Development

Requirements: Go 1.24+, GTK4, Libadwaita, Blueprint Compiler, and SQLite.

```bash
make test
make build
```

The desktop client communicates with the daemon over the Unix socket at
`/run/user/$UID/zchat.sock`; no TCP port is exposed.

## Packaging

```bash
make rpm VERSION=0.5.0
make flatpak VERSION=0.5.0
```

Runtime data is stored under `~/.local/share/zchat/`, configuration under
`~/.config/zchat/`, and cache under `~/.cache/zchat/`.

## Pairing

Start ZChat, scan the displayed QR code from WhatsApp's Linked devices screen,
and keep the application running until synchronization completes. The session
is persisted locally and survives restart. Use Logout to unlink the device.

## Limitations

Voice/video calls, status upload, channel management, and WhatsApp Business API
features are outside the current MVP scope.

ZChat is independent software and is not affiliated with, endorsed by, or
sponsored by WhatsApp LLC or Meta Platforms, Inc.
