---

# 17.1 WhatsApp Companion Fingerprint

ZChat uses a **stable Desktop Companion identity** to maintain consistent multi-device sessions.

This fingerprint is **not** intended to bypass WhatsApp security. Its purpose is to provide a persistent and standards-compliant companion device identity.

## Companion Identity

| Property | Value |
|----------|-------|
| Device Type | Desktop |
| Platform | Mac OS |
| Browser | Safari |
| Mobile | false |
| Release Channel | Stable |

## Baileys Equivalent

```ts
browser: Browsers.macOS("Safari")
```

## WhatsMeow Equivalent

```go
type Fingerprint struct {
    Platform string
    OS       string
    Browser  string
    Mobile   bool
}

var MacSafari = Fingerprint{
    Platform: "DESKTOP",
    OS:       "Mac OS",
    Browser:  "Safari",
    Mobile:   false,
}
```

Rules:

- Fingerprint is generated once during first pairing.
- Browser identity must remain constant.
- Platform changes require a new linked device.
- ZChat branding is never included inside the WhatsApp UserAgent.

Existing sessions paired before this configuration retain their original WhatsMeow device properties. They must be explicitly logged out and paired again to advertise the Mac OS/Safari fingerprint.

---

# 17.2 Session Persistence

Sessions are treated as permanent device identities.

## Directory

```text
~/.local/share/zchat/session/

creds.json
identity.key
pre-keys/
sessions/
sender-keys/
app-state-sync/
```

## Requirements

- `creds.json` must never be regenerated automatically.
- QR pairing is required only after explicit logout.
- Device identity survives reboot and application updates.
- All session files use permission `0700`.

---

# 17.3 Connection Policy

ZChat establishes exactly one WebSocket connection per account.

## Reconnect Strategy

| Attempt | Delay |
| ------- | ----: |
| 1       |    2s |
| 2       |    4s |
| 3       |    8s |
| 4       |   16s |
| Max     |   60s |

Implementation uses exponential backoff with jitter.

Rules:

- Never reconnect aggressively in a loop.
- Restore app-state before requesting history.
- Preserve pending messages during reconnect.

---

# 17.4 Presence Policy

Presence reflects real user activity.

## Default Behavior

- Do not send `available` immediately after connect.
- Send typing state only while the user is actively typing.
- Clear composing state after message send or timeout.
- Do not emit periodic fake presence events.

This minimizes unnecessary network activity and keeps behavior consistent with desktop clients.

---

# 17.5 Version Negotiation

ZChat always negotiates the latest supported WhatsApp Web protocol version.

```go
version := FetchLatestVersion()

client := whatsmeow.NewClient(
    store,
    MacSafari,
    version,
)
```

Requirements:

- No hardcoded protocol version.
- Version is refreshed during startup.
- Failed negotiation retries using the latest compatible release.

---

# 17.6 Local Security

## IPC

- Unix Domain Socket only
- No HTTP server
- No TCP listener
- Socket permission: `0600`

## Database

SQLite runs in WAL mode.

Sensitive data:

- Session keys
- Identity keys
- Sender keys

are stored outside the cache directory.

## File Permissions

| Path                   | Permission |
| ---------------------- | ---------- |
| `~/.config/zchat`      | 700        |
| `~/.local/share/zchat` | 700        |
| `creds.json`           | 600        |
| `identity.key`         | 600        |

---

# 17.7 Media Cache

Media is stored separately from chat metadata.

```text
~/.local/share/zchat/media/

images/
videos/
audio/
documents/
stickers/
thumbnails/
```

Rules:

- Progressive download
- Thumbnail generated locally
- LRU cache eviction
- Original file retained until user clears cache

---

# 11.1 Streaming Events (Patch)

Add the following events to `StreamEvents()`:

- QRUpdated
- ConnectionStateChanged
- HistorySyncProgress
- MessageAckUpdated
- MediaDownloadProgress
- PresenceChanged
- PushNameUpdated

---

# 10.1 Additional Tables (Patch)

## settings

```sql
CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

## session_meta

```sql
CREATE TABLE session_meta (
    jid TEXT PRIMARY KEY,
    platform TEXT,
    browser TEXT,
    paired_at INTEGER,
    last_connect INTEGER
);
```

Indexes:

- last_connect
- paired_at

---

# 20.1 Additional Success Metrics

| Metric            | Target |
| ----------------- | ------ |
| Reconnect Success | >98%   |
| Session Restore   | >99%   |
| QR Pairing Time   | <30s   |
| Media Cache Hit   | >80%   |
| Idle CPU          | <1%    |

---
