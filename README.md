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

Detailed documentation lives under [`docs/`](docs/): architecture, the gRPC
contract, build instructions, and packaging.

## Packaging

```bash
make rpm VERSION=0.7.0
make flatpak VERSION=0.7.0
```

Runtime data is stored under `~/.local/share/zchat/`, configuration under
`~/.config/zchat/`, and cache under `~/.cache/zchat/`.

## Pairing

Start ZChat, scan the displayed QR code from WhatsApp's Linked devices screen,
and keep the application running until synchronization completes. The session
is persisted locally and survives restart. Use Log out in the main menu to
unlink the device.

## Shortcuts

Press `Ctrl+?` for the full list. `Ctrl+K` searches chats, `Ctrl+F` finds text
inside the open conversation, and `Ctrl+N` starts a new chat.

## Limitations

Voice/video calls, status upload, channel management, and WhatsApp Business API
features are outside the current MVP scope.

ZChat is independent software and is not affiliated with, endorsed by, or
sponsored by WhatsApp LLC or Meta Platforms, Inc.
