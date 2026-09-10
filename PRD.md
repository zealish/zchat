# PRD.md

# ZChat

Native Linux Desktop Client for WhatsApp Multi-Device

Version: 1.0 (MVP)
Status: Draft
License: GPL-3.0
Platform: Fedora Linux (GNOME / Wayland)

> ZChat is an independent open-source desktop client for WhatsApp Multi-Device powered by WhatsMeow. This project is not affiliated with, endorsed by, or sponsored by WhatsApp or Meta.

---

# 1. Product Vision

ZChat is a lightweight, native Linux desktop application that provides a modern WhatsApp experience without Electron.

The project focuses on:

- Native GTK4 interface
- Low memory usage
- Fast startup
- Full Multi-Device support
- Open source architecture
- Fedora-first development

---

# 2. Goals

## Primary Goals

- Native Wayland application
- RAM usage below 80 MB while idle
- Startup under 1 second
- Seamless WhatsApp synchronization
- RPM & Flatpak distribution

## Non Goals (MVP)

- Voice calls
- Video calls
- Status upload
- Channel management
- Business API features

---

# 3. Target Users

### Primary

- Fedora users
- GNOME desktop users
- Linux developers
- Open source enthusiasts

### Secondary

- Ubuntu users
- Arch Linux users
- KDE users (supported later)

---

# 4. Technology Stack

## Desktop

- GTK4
- Libadwaita
- Blueprint UI
- GObject Introspection

## Backend

- Go 1.24+
- WhatsMeow
- SQLite
- gRPC
- Zerolog

## Build

- Meson
- Ninja
- Go Modules
- Flatpak Builder
- RPM Spec

---

# 5. High Level Architecture

Client UI
↓
Unix Socket (gRPC)
↓
Go Daemon
├── WhatsMeow
├── SQLite
├── Media Manager
├── Notification Service
└── Session Manager
↓
WhatsApp Multi Device

No TCP ports are exposed.

IPC uses Unix Socket only.

Socket path:

/run/user/$UID/zchat.sock

---

# 6. Monorepo Structure

zchat/
├── apps/
│   ├── daemon/
│   └── desktop/
│
├── proto/
│
├── packages/
│   ├── ipc/
│   └── shared/
│
├── packaging/
│   ├── rpm/
│   └── flatpak/
│
├── docs/
│
├── scripts/
│
├── Makefile
├── LICENSE
└── README.md

---

# 7. Features

## Authentication

- QR pairing
- Session persistence
- Auto reconnect
- Logout
- Device information

## Chat

- Chat list
- Search
- Unread counter
- Pin chat (local)
- Archive chat
- Mute chat
- Typing indicator

## Messaging

- Send text
- Emoji
- Reply
- Forward
- Delete for me
- Read receipts
- Timestamp
- Message status

## Media

- Images
- Videos
- Audio
- PDF
- Documents
- Stickers

## Desktop Integration

- Native notifications
- Dark mode
- Light mode
- Keyboard shortcuts
- File drag & drop
- Clipboard image paste

---

# 8. Functional Requirements

## FR-001 QR Login

User opens ZChat.

System requests QR code from WhatsMeow.

User scans QR using WhatsApp.

Session is stored locally.

Acceptance Criteria

- QR refreshes automatically.
- Login completes within 30 seconds.
- Session survives reboot.

---

## FR-002 Chat Synchronization

System downloads chat metadata after login.

Acceptance

- Chat list appears automatically.
- Latest message is visible.
- Unread count matches phone.

---

## FR-003 Send Message

User types message.

System sends through WhatsMeow.

Acceptance

- Delivery status updates.
- Failed messages can retry.

---

## FR-004 Receive Message

Incoming events are streamed.

UI updates instantly.

Acceptance

- No polling.
- Notification displayed.
- Chat order updates.

---

## FR-005 Media Download

User clicks image.

System downloads media.

Media cached locally.

Acceptance

- Progressive loading.
- Cached reopening.
- Thumbnail generation.

---

# 9. Non Functional Requirements

| Requirement | Target |
|------------|--------|
| Startup | <1s |
| Idle RAM | 40–80MB |
| CPU Idle | <1% |
| Database | SQLite WAL |
| Offline Cache | Yes |
| Wayland | Native |
| X11 | Compatible |

---

# 10. Database

## chats

CREATE TABLE chats (
    jid TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    unread INTEGER DEFAULT 0,
    archived BOOLEAN DEFAULT FALSE,
    pinned BOOLEAN DEFAULT FALSE,
    last_message TEXT,
    updated_at INTEGER
);

## messages

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    chat_jid TEXT NOT NULL,
    sender TEXT,
    body TEXT,
    type TEXT,
    timestamp INTEGER,
    outgoing BOOLEAN,
    status TEXT
);

## media

CREATE TABLE media (
    id TEXT PRIMARY KEY,
    message_id TEXT,
    path TEXT,
    mime TEXT,
    size INTEGER
);

Indexes

- chat_jid
- timestamp
- updated_at

---

# 11. IPC (gRPC)

service ChatService {

GetChats()

GetMessages(chat)

SendMessage()

DownloadMedia()

SearchChats()

StreamEvents()

}

Streaming events include:

- MessageReceived
- MessageUpdated
- ChatUpdated
- PresenceChanged
- QRUpdated
- ConnectionState

---

# 12. Desktop UI

## Main Layout

┌────────────┬───────────────────────────┐
│ Sidebar    │ Header                    │
│            ├───────────────────────────┤
│ Chats      │                           │
│            │ Conversation              │
│            │                           │
│            │                           │
│            ├───────────────────────────┤
│            │ Message Input             │
└────────────┴───────────────────────────┘

Sidebar width: 280px

Adaptive layout for smaller screens.

---

# 13. Design System

Font

Inter

Radius

12px

Spacing

4px scale

Colors

Accent: #25D366

Success: #16A34A

Error: #DC2626

Warning: #F59E0B

Dark mode follows Libadwaita automatically.

---

# 14. Keyboard Shortcuts

Ctrl+K → Search

Ctrl+N → New chat

Ctrl+F → Find

Ctrl+W → Close window

Ctrl+Q → Quit

Ctrl+Shift+M → Mute chat

Ctrl+L → Focus chat list

Esc → Close dialog

---

# 15. XDG Directories

Config

~/.config/zchat/

Data

~/.local/share/zchat/

Cache

~/.cache/zchat/

Runtime

/run/user/$UID/zchat.sock

Media

~/.local/share/zchat/media/

---

# 16. Packaging

## RPM

make rpm

Output

dist/zchat-x.y.z.fc42.x86_64.rpm

## Flatpak

make flatpak

Application ID

com.zealish.ZChat

---

# 17. Security

- Unix Socket IPC only
- No localhost HTTP server
- SQLite encryption optional
- Session stored with filesystem permissions 700
- Media isolated inside XDG data directory

---

# 18. Logging

Levels

- Debug
- Info
- Warn
- Error

Log locations

journalctl

and

~/.local/share/zchat/logs/

---

# 19. Roadmap

## v0.1 Alpha

- QR Login
- Chat List
- Send Text
- Receive Text

## v0.2 Beta

- Images
- PDF
- Stickers
- Notifications
- Search

## v0.3 RC

- Settings
- Keyboard shortcuts
- RPM package
- Flatpak package

## v1.0 Stable

- Public release
- GitHub Actions CI
- Auto update metadata
- Documentation

---

# 20. Success Metrics

| Metric | Target |
|---------|--------|
| Startup Time | <1s |
| Idle Memory | <80MB |
| Crash Free Sessions | >99% |
| Login Success | >95% |
| Message Latency | <300ms |

---

# 21. License

GPL-3.0

---

# 22. Disclaimer

ZChat is an independent open-source project utilizing WhatsMeow for WhatsApp Multi-Device communication.

This project is not affiliated with, endorsed by, or sponsored by WhatsApp LLC or Meta Platforms, Inc.
