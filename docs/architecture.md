# Architecture

ZChat is split into two processes: `zchat` (the GTK4/Libadwaita desktop client)
and `zchat-daemon` (the Go backend that owns the WhatsApp Multi-Device session).
They talk gRPC over a Unix socket; nothing else crosses the boundary.

## High level

Per PRD §5:

```
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
```

Concretely:

```
┌──────────────────────────── zchat (apps/desktop) ───────────────────────────┐
│ main.go          adw.Application "com.zealish.ZChat", loads ui/zchat.gresource │
│ window.go        GTK main loop: widgets, chat list, message list, composer  │
│ client/client.go gRPC stubs; every call on a goroutine, every result via    │
│                  glib.IdleAdd                                              │
│ daemonctl/       spawns zchat-daemon when the socket does not answer        │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ gRPC over AF_UNIX
                                │ /run/user/$UID/zchat.sock (mode 0600)
┌───────────────────────────────┴──── zchat-daemon (apps/daemon) ─────────────┐
│ main.go            ipc.Listen + grpc.NewServer + RegisterChatServiceServer  │
│ service/service.go ChatService implementation (20 unary RPCs + StreamEvents)│
│ service/broker.go  Broker: fan-out of *zchatv1.Event to every stream        │
│ wa/session.go      whatsmeow client, QR pairing, event handler              │
│ wa/media.go        media manager: extract, download, path hashing           │
│ wa/avatar.go       avatar cache under ~/.cache/zchat/avatars                │
│ wa/presence.go     typing / online presence tracker                         │
│ wa/history.go      serialised history-sync worker                           │
│ store/store.go     SQLite (WAL), shared with whatsmeow's sqlstore           │
└───────────────────────────────┬─────────────────────────────────────────────┘
                                │ whatsmeow
                                ▼
                     WhatsApp Multi-Device
```

## Process model

`apps/desktop/main.go` builds an `adw.Application` with app id
`com.zealish.ZChat`, registers the embedded `ui/zchat.gresource` blob, and on
activate constructs the window with `xdgpaths.SocketPath()`.

`newWindow` finishes by launching `go w.connect()` (`apps/desktop/window.go:314`):

1. `daemonctl.EnsureRunning(ctx, socketPath)` — if no daemon answers, start one.
2. `client.Dial(ctx, socketPath)` — open the gRPC connection.
3. Back on the main loop (`glib.IdleAdd`), store the client and start
   `StreamEvents`.

### How daemonctl spawns and manages the daemon

`apps/desktop/daemonctl/daemonctl.go`:

- `EnsureRunning` first probes the socket: `ipc.Dial` plus a
  `GetConnectionState` call with a 300 ms timeout. If it succeeds, nothing is
  spawned — an already-running daemon (started by another window, or by hand) is
  reused.
- Otherwise `resolveBinary()` picks the executable, in order:
  1. `$ZCHAT_DAEMON`, if set;
  2. `zchat-daemon` next to the running client executable
     (`filepath.Dir(os.Executable())`);
  3. `zchat-daemon` on `$PATH`.
- The daemon is started as `exec.Command(bin, "--socket", socketPath)` with
  stdout and stderr wired to the client's stderr, and
  `SysProcAttr{Setsid: true}` so it gets its own session and outlives the client
  process. `cmd.Process.Release()` detaches it.
- `EnsureRunning` then polls the socket every 100 ms for up to 10 seconds, and
  returns `daemon did not become ready within 10s` if the daemon never answers.

The daemon is not killed when the client exits; it keeps the WhatsApp session
connected. `make reset-session` stops it (via `pgrep -x zchat-daemon` and
`kill`) before deleting the session database.

### Daemon startup

`apps/daemon/main.go` takes two flags: `--debug` and `--socket` (default
`xdgpaths.SocketPath()`). It then:

1. `logging.Setup("daemon", *debug)`.
2. `ipc.Listen(*socket)`. If `ipc.ErrAlreadyRunning` comes back, it logs
   `daemon already running` and exits cleanly, so racing spawns are harmless.
3. `signal.NotifyContext` for `SIGINT`/`SIGTERM`.
4. `store.Open(ctx, dbPath)` where `dbPath` is `xdgpaths.DatabasePath()`.
5. `service.NewBroker(log)`, then `wa.New(ctx, st.DB(), st, log,
   logging.WhatsmeowAdapter(log), broker)` and `session.Start(ctx)`. A failing
   `Start` is logged, not fatal — the gRPC surface still comes up so the UI can
   show the error.
6. `grpc.NewServer()` + `zchatv1.RegisterChatServiceServer(server,
   service.New(log, st, session, broker))` and `server.Serve(lis)`.

On context cancellation it runs `server.GracefulStop()` then `session.Stop()`,
and the deferred `os.Remove(*socket)` clears the socket file.

## The Unix-socket-only boundary

`packages/ipc/socket.go` is the only transport code, and its package comment
states the rule: "The PRD forbids TCP transport entirely" (PRD §5 and §17: *No
TCP ports are exposed*, *No localhost HTTP server*).

- `Listen(path)` creates the socket's parent directory with mode `0700`, and if
  a socket file already exists it dials it with a 500 ms timeout. A live peer
  means `ErrAlreadyRunning`; a dead one means the stale file is removed. After
  `net.Listen("unix", path)` the socket is `chmod`'d to `0600`.
- `Dial(ctx, path)` returns
  `grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))`.
  Insecure credentials are safe precisely because the transport is a
  filesystem object owned by the user with mode `0600` — the kernel, not TLS,
  is the authentication boundary.

Because there is no TCP listener, no other user or machine can reach the
daemon, and the socket lives in the per-user runtime directory, which the OS
tears down at logout.

A second consequence: `SendMediaRequest.file_path` names a path the *daemon*
opens itself. The proto comment spells this out — "client and daemon must share
a filesystem, which the Unix socket transport already implies".

## Daemon internals

### whatsmeow session (`apps/daemon/wa/session.go`)

`Session` wraps a `*whatsmeow.Client` plus the `sqlstore.Container`, guarded by
a `sync.RWMutex` that protects `client`, `state` and `qr`.

`New` builds the container with `sqlstore.NewWithDB(db, "sqlite", ...)` — the
same `*sql.DB` handle the ZChat store uses — and runs `container.Upgrade(ctx)`
to apply whatsmeow's own migrations.

`Start`:

- `container.GetFirstDevice(ctx)`.
- Sets `store.DeviceProps.Os = "Mac OS"` and
  `PlatformType = DeviceProps_SAFARI` before creating the client.
- `client.AddEventHandler(s.handleEvent)`.
- Starts the history worker once, via `startOnce`, on a
  `context.WithoutCancel(ctx)`.
- If `device.ID == nil` there is no stored pairing: `GetQRChannel` is called
  *before* `Connect`, state goes to `CONNECTION_STATUS_LOGGED_OUT`, and
  `consumeQR` publishes each `code` item as a `QRUpdate` with an
  `expires_at`. On `success` the pending QR is cleared and state moves to
  `CONNECTION_STATUS_CONNECTING`.
- Otherwise state goes straight to `CONNECTION_STATUS_CONNECTING` and
  `client.Connect()` runs.

`setState` rebuilds the `ConnectionState` (filling `own_jid` and `push_name`
from `client.Store`), stores it, and publishes it to the broker. `State()` and
`PendingQR()` return `proto.Clone`d copies so no handler can mutate shared
state; `PendingQR` returns nil once `expires_at` has passed.

`handleEvent` is the single whatsmeow callback and dispatches:

| whatsmeow event | effect |
|---|---|
| `events.Connected` | `CONNECTION_STATUS_CONNECTED`, then `backfillChatNames` |
| `events.Disconnected` | reset presence tracker, `CONNECTION_STATUS_DISCONNECTED` |
| `events.LoggedOut` | `CONNECTION_STATUS_LOGGED_OUT`, then `restart()` |
| `events.Message` | `onMessage` — persist and publish |
| `events.Receipt` | `onReceipt` — delivery/read status |
| `events.ChatPresence` | `onChatPresence` — typing/recording |
| `events.Presence` | `onPresence` — online/last seen |
| `events.HistorySync` | queued to `historyCh` (cap 8), processed inline when full |
| `events.Pin` / `events.Archive` / `events.Mute` | `onAppStateChat` |
| `events.DeleteForMe` | `onDeleteForMe` |
| `events.Picture` | `onPictureChanged` |
| `events.OfflineSyncCompleted` | logged |

`Logout` calls `client.Logout(ctx)` then `restart()`, which disconnects and
re-runs `Start` on a goroutine so QR pairing is re-armed.

History sync is deliberately off the event loop: `historyWorker`
(`apps/daemon/wa/history.go`) drains `historyCh` so "the whatsmeow event loop is
never blocked and writes stay ordered against the single SQLite handle". Each
conversation is upserted, its messages inserted with `InsertMessages` (one
transaction for the whole batch), the preview refreshed, the synced unread count
restored, and a `ChatUpdated` event published.

### SQLite store (`apps/daemon/store/store.go`)

`Open` builds the DSN

```
file:<path>?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)
```

(`modernc.org/sqlite`, so the daemon builds with `CGO_ENABLED=0`) and then calls
`db.SetMaxOpenConns(1)`: a single connection serialises writes between the
whatsmeow event loop and the gRPC handlers, avoiding `SQLITE_BUSY`.

The schema, applied on every open, is exactly:

```sql
CREATE TABLE IF NOT EXISTS chats (
    jid TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    unread INTEGER DEFAULT 0,
    archived BOOLEAN DEFAULT FALSE,
    pinned BOOLEAN DEFAULT FALSE,
    last_message TEXT,
    updated_at INTEGER,
    is_group BOOLEAN DEFAULT FALSE,
    muted_until INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    chat_jid TEXT NOT NULL,
    sender TEXT,
    sender_name TEXT,
    body TEXT,
    type TEXT,
    timestamp INTEGER,
    outgoing BOOLEAN,
    status TEXT,
    forwarded BOOLEAN DEFAULT FALSE,
    quoted_id TEXT,
    quoted_sender TEXT,
    quoted_sender_name TEXT,
    quoted_body TEXT,
    quoted_type TEXT
);
CREATE TABLE IF NOT EXISTS media (
    id TEXT PRIMARY KEY,
    message_id TEXT,
    path TEXT,
    mime TEXT,
    size INTEGER,
    filename TEXT,
    caption TEXT,
    thumbnail BLOB,
    width INTEGER,
    height INTEGER,
    duration INTEGER,
    payload BLOB
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_jid ON messages(chat_jid);
CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_chats_updated_at ON chats(updated_at);
```

This matches PRD §10's three tables and its three indexes (`chat_jid`,
`timestamp`, `updated_at`), extended with the columns later features needed.

`media.payload` holds the serialised `waE2E.Message`, "so the daemon can decrypt
the file on demand long after the event was received" — that is what makes
`DownloadMedia` and `RetryMedia` possible without re-receiving the message.

Because `CREATE TABLE IF NOT EXISTS` does nothing to an existing table,
`addedColumns` lists every column added after a table's first release and
`migrateColumns` adds the missing ones:

- `media`: `filename`, `caption`, `thumbnail`, `width`, `height`, `duration`,
  `payload`
- `chats`: `muted_until`
- `messages`: `forwarded`, `quoted_id`, `quoted_sender`, `quoted_sender_name`,
  `quoted_body`, `quoted_type`, `reaction`

`migrateColumns` reads `pragma_table_info(?)` and issues `ALTER TABLE ... ADD
COLUMN` only for absent columns. Both table and column names come from the fixed
map, never from user input.

Other store invariants worth knowing:

- `statusRank` orders `pending` → `sent` → `delivered` → `read` so a late
  receipt cannot downgrade a message; `failed` is unranked and always applies
  (`UpdateMessageStatus`).
- `realChatFilter` restricts chat listings to `@s.whatsapp.net`, `@c.us`,
  `@lid` and `@g.us`, excluding newsletters, broadcast lists and status updates
  that older databases may still contain.
- `InsertMessage`/`InsertMessages` use `ON CONFLICT(id) DO NOTHING`, so history
  sync replays are idempotent.
- `DeleteMessage` removes the rows but leaves the downloaded file on disk.

### Media manager (`apps/daemon/wa/media.go`)

`extractMedia` turns an incoming `waE2E.Message` into a `store.Media` row,
capturing mime, size, filename, caption, the embedded JPEG thumbnail,
dimensions, duration, and the serialised payload. `path` stays empty until the
file is materialised.

`DownloadMedia(ctx, messageID)` re-reads the payload, decrypts and writes the
file under `xdgpaths.MediaDir()` with mode `0600`, records the path via
`SetMediaPath`, and returns the refreshed message. Already-downloaded media is
returned as is. `mediaPath` hashes the message id (SHA-256) so a hostile id can
never escape the media directory.

`apps/daemon/wa/avatar.go` does the equivalent for profile pictures: a
`avatarCache` keyed by JID (an empty path is a cached "no picture"), downloads
bounded to 2 MiB and 20 s, written under `xdgpaths.AvatarDir()` with the JID
hashed into the filename. `events.Picture` invalidates the entry, re-fetches,
and publishes a `ProfilePictureUpdate`.

### Event broker (`apps/daemon/service/broker.go`)

`Broker` is a mutex-guarded `map[int]chan *zchatv1.Event`.

- `Subscribe()` allocates a buffered channel (`subscriberBuffer = 256`) and
  returns it with an idempotent unsubscribe closure that deletes and closes it.
- `Publish(evt)` does a non-blocking send to every subscriber; a full channel
  logs `event dropped, subscriber too slow` and moves on. The comment is the
  design rule: "A stalled UI must never block the whatsmeow loop."

`Service.StreamEvents` subscribes, immediately sends the current
`ConnectionState` and any `PendingQR()` — "so an attaching client never has to
poll" — and then forwards from the channel until the stream context is done or
the channel closes.

Every publisher is a `wa.Session` method (`setState`, `consumeQR`,
`persistMessage`, `onReceipt`, presence handlers, `onPictureChanged`, the
history worker), so fan-out is entirely one-directional: daemon → clients.

## Threading rules on the desktop side

GTK is not thread safe. `apps/desktop/client/client.go` states the contract in
its package comment: "no callback in this package may touch a widget from a
background goroutine, so every result is delivered through `glib.IdleAdd`".

The pattern every wrapper follows:

```go
func (c *Client) GetChats(ctx context.Context, archived bool, onDone func(bool, []*zchatv1.Chat, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.GetChats(callCtx, &zchatv1.GetChatsRequest{Archived: archived})
		var chats []*zchatv1.Chat
		if resp != nil {
			chats = resp.GetChats()
		}
		idle(func() { onDone(archived, chats, err) })
	}()
}
```

- The gRPC call runs on a fresh goroutine, never on the main loop, so the UI
  never blocks on IPC.
- `idle(f)` is `glib.IdleAdd(f)`, so the callback runs on the GTK main loop.
- Responses are nil-checked before use, so a failed call still reaches the
  callback with a usable zero value plus the error.

`StreamEvents` runs its own goroutine: it opens the stream, pumps `stream.Recv()`
through `receiveLoop`, delivers each event via `idle`, and on error reconnects
with exponential backoff starting at 2 s and capped at 60 s, until the context
is cancelled. Stream errors are reported through `onError`, also on the main
loop.

`window.connect` itself runs on a goroutine and hops back with `glib.IdleAdd`
before assigning `w.client`, so window fields are only ever touched from the
main loop.

The UI coalesces bursts rather than rebuilding per event — `upsertChat` notes
that "history sync delivers hundreds of these in bursts, so rebuilds are
coalesced" via `scheduleChatRebuild`.

Some calls deliberately skip `callTimeout` (`10 * time.Second`) and use the
caller's context directly, because transfers "can far outlast a regular call":
`SendMedia`, `DownloadMedia`, `RetryMedia` and `GetProfilePicture`.
`SetPresence` is fire-and-forget — it is "fired on every keystroke burst, so
failures are dropped rather than reported".

## XDG directory layout

From `packages/shared/xdgpaths/xdgpaths.go`. Every directory is created with
`dirMode = 0o700`, which the file notes is "mandated by the PRD: all ZChat
directories are user-private" (PRD §17).

| Function | Path | Contents |
|---|---|---|
| `ConfigDir()` | `~/.config/zchat` | `settings.json` (desktop-only preferences) |
| `DataDir()` | `~/.local/share/zchat` | database and media |
| `CacheDir()` | `~/.cache/zchat` | disposable data |
| `LogDir()` | `~/.local/share/zchat/logs` | `daemon.log`, `desktop.log` |
| `MediaDir()` | `~/.local/share/zchat/media` | downloaded attachments |
| `AvatarDir()` | `~/.cache/zchat/avatars` | profile pictures |
| `DatabasePath()` | `~/.local/share/zchat/zchat.db` | SQLite (WAL) |
| `SocketPath()` | `$XDG_RUNTIME_DIR/zchat.sock`, else `/run/user/$UID/zchat.sock` | gRPC socket |

`dataHome()` honours `$XDG_DATA_HOME` and falls back to `~/.local/share`;
`ConfigDir` and `CacheDir` use `os.UserConfigDir()` / `os.UserCacheDir()`, which
honour `$XDG_CONFIG_HOME` / `$XDG_CACHE_HOME`. `SocketPath` prefers
`$XDG_RUNTIME_DIR` and otherwise formats `/run/user/%d/zchat.sock` from
`os.Getuid()`; its comment repeats that "the PRD forbids TCP, so this always
lives under the per-user runtime directory".

Avatars live in the cache rather than the data directory because "profile
pictures are cheap to re-fetch".

`apps/desktop/settings.go` keeps `settings.json` under `ConfigDir()` and notes
that "the daemon has no say in these, so they never cross the socket" — theme
(`system`/`light`/`dark`) and the notification toggle are purely client-side.

## Logging

`packages/shared/logging/logging.go` gives both binaries the same zerolog
configuration (PRD §18): a `ConsoleWriter` on stderr — picked up by journald,
hence `journalctl` — plus `~/.local/share/zchat/logs/<component>.log` opened
`O_APPEND` with mode `0600`. Components are `daemon` and `desktop`. Level is
`InfoLevel`, or `DebugLevel` when debug is on. A log directory or file that
cannot be opened is reported on stderr and the file sink is dropped; logging
problems never abort startup.

`WhatsmeowAdapter` bridges zerolog to whatsmeow's `waLog.Logger`, with `Sub`
adding a `module` field, so whatsmeow's internal `sqlstore` and `client` logs
land in the same stream.
