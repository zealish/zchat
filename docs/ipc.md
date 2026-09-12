# IPC contract

The desktop client and the daemon speak gRPC, defined in
`proto/zchat/v1/zchat.proto`:

- proto package: `zchat.v1`
- Go package: `github.com/zealish/zchat/packages/ipc/zchatv1;zchatv1`
- service: `ChatService` — 20 unary RPCs and one server-streaming RPC

## Transport

The only transport is a Unix domain socket. PRD §5 ("No TCP ports are exposed.
IPC uses Unix Socket only.") and §17 ("Unix Socket IPC only", "No localhost HTTP
server") are enforced in `packages/ipc/socket.go`, whose package comment says
"The PRD forbids TCP transport entirely".

- Socket path: `$XDG_RUNTIME_DIR/zchat.sock`, falling back to
  `/run/user/$UID/zchat.sock` (`xdgpaths.SocketPath()`). The daemon's
  `--socket` flag overrides it.
- `ipc.Listen(path)` creates the parent directory with mode `0700`, removes a
  stale socket file (after failing to dial it within 500 ms), listens, and
  `chmod`s the socket to `0600`. If a live daemon answers, it returns
  `ErrAlreadyRunning` instead.
- `ipc.Dial(ctx, path)` returns
  `grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))`.
  Insecure credentials are fine because the socket's file permissions are the
  access control.
- There is no TCP listener anywhere in the tree.

Because both ends share a filesystem, some requests pass paths instead of
bytes. `SendMediaRequest.file_path` is read by the daemon itself — the proto
notes "client and daemon must share a filesystem, which the Unix socket
transport already implies" — and `GetProfilePictureResponse.path` /
`MediaInfo.path` are local paths the client opens directly.

## RPCs

| RPC | Request | Response | Purpose |
|---|---|---|---|
| `GetChats` | `GetChatsRequest` | `GetChatsResponse` | Page of conversations; `archived` selects the archived list instead of the active one. Limit clamped to 200. |
| `GetContacts` | `GetContactsRequest` | `GetContactsResponse` | Locally synced contacts, including contacts without chats. |
| `StartChat` | `StartChatRequest` | `StartChatResponse` | Create/open a direct chat row for `recipient` without sending a message. |
| `GetMessages` | `GetMessagesRequest` | `GetMessagesResponse` | Page of messages for `chat_jid`, and marks the chat read. `before_timestamp` pages backwards; limit clamped to 200. |
| `SendMessage` | `SendMessageRequest` | `SendMessageResponse` | Send text to a chat; `quoted_message_id` turns it into a reply. |
| `SendMedia` | `SendMediaRequest` | `SendMediaResponse` | Upload `file_path` as an attachment, with optional caption and reply target. |
| `ForwardMessage` | `ForwardMessageRequest` | `ForwardMessageResponse` | Re-send an existing message to `to_chat_jid`. |
| `DeleteMessage` | `DeleteMessageRequest` | `DeleteMessageResponse` | Remove a message locally; with `revoke`, also revoke it for everyone. |
| `UpdateChat` | `UpdateChatRequest` | `UpdateChatResponse` | Toggle `pinned`, `archived`, `muted_until`; unset fields are left untouched. |
| `GetConnectionState` | `GetConnectionStateRequest` | `ConnectionState` | Current WhatsApp connection state. Also used as the daemon liveness probe. |
| `Logout` | `LogoutRequest` | `LogoutResponse` | Unlink the device and re-arm QR pairing. |
| `DownloadMedia` | `DownloadMediaRequest` | `DownloadMediaResponse` | Materialise a message's attachment under the media directory; returns the refreshed message with `MediaInfo.path` filled. |
| `SearchChats` | `SearchChatsRequest` | `SearchChatsResponse` | Free-text search over chat names and last messages, active and archived. |
| `SearchMessages` | `SearchMessagesRequest` | `SearchMessagesResponse` | Free-text search over message bodies; `chat_jid` scopes it to one chat, empty searches every chat. |
| `SetPresence` | `SetPresenceRequest` | `SetPresenceResponse` | Report local typing state for `chat_jid` and toggle the global `available` marker. |
| `RetryMessage` | `RetryMessageRequest` | `RetryMessageResponse` | Resend a failed outgoing text using its original payload, keeping the same message id. |
| `RetryMedia` | `RetryMessageRequest` | `RetryMediaResponse` | Resend a failed outgoing attachment using its stored encrypted payload. |
| `ReactMessage` | `ReactMessageRequest` | `ReactMessageResponse` | Set (or clear) the current user's emoji reaction on a message. |
| `GetChatInfo` | `GetChatInfoRequest` | `GetChatInfoResponse` | Metadata for one chat; the response carries `Chat` plus a `repeated Contact members` field. |
| `GetProfilePicture` | `GetProfilePictureRequest` | `GetProfilePictureResponse` | Cached avatar path for a JID, downloading on first use; empty path means no picture, not an error. |
| `StreamEvents` | `StreamEventsRequest` | `stream Event` | Server-streaming live update feed (see below). |

PRD §11 lists `GetChats()`, `GetMessages(chat)`, `SendMessage()`,
`DownloadMedia()`, `SearchChats()` and `StreamEvents()`; the implemented
service is a superset.

### Shared messages

- `Chat` — `jid`, `name`, `unread`, `archived`, `pinned`, `last_message`,
  `updated_at` (unix seconds), `is_group`, `muted_until` (unix seconds;
  `-1` = muted forever, `0` = not muted).
- `Message` — `id`, `chat_jid`, `sender`, `sender_name`, `body`, `type`
  (`"text"`, `"image"`, `"video"`, `"audio"`, `"document"`, `"sticker"`),
  `timestamp`, `outgoing`, `status`, `media`, `quoted`, `forwarded`, `reaction`
  (legacy current-user reaction) and `reactions`.
- `MessageStatus` — `MESSAGE_STATUS_UNSPECIFIED`, `_PENDING`, `_SENT`,
  `_DELIVERED`, `_READ`, `_FAILED`.
- `MediaInfo` — `mime`, `size`, `filename`, `caption`, `path` (empty until
  downloaded), `thumbnail` (JPEG preview embedded by WhatsApp, may be empty),
  `width`, `height`, `duration` (seconds, audio and video only).
- `QuotedMessage` — `id`, `sender`, `sender_name`, `body`, `type`. A snapshot,
  because "the original may be missing locally".
- `MessageReaction` — `sender`, `sender_name`, `emoji`.
- `Contact` — `jid`, `name`, `phone_number`.
- `ConnectionState` — `status`, `own_jid`, `push_name`, `error`.
- `ConnectionStatus` — `CONNECTION_STATUS_UNSPECIFIED`, `_DISCONNECTED`,
  `_CONNECTING`, `_CONNECTED`, `_LOGGED_OUT` (needs QR pairing).

## StreamEvents

`rpc StreamEvents(StreamEventsRequest) returns (stream Event);`

`Event` is a single `oneof payload`:

| Field | Type | Meaning |
|---|---|---|
| `message_received` = 1 | `Message` | A new incoming (or locally sent) message was persisted. |
| `message_updated` = 2 | `Message` | An existing message changed: delivery status, timestamp, reaction, downloaded media. |
| `chat_updated` = 3 | `Chat` | Chat metadata changed: preview, unread count, pin/archive/mute. |
| `qr_updated` = 4 | `QRUpdate` | A new pairing code (`code`, `expires_at`). |
| `connection_state` = 5 | `ConnectionState` | Connection status transition. |
| `message_deleted` = 6 | `MessageDeleted` | A message was revoked or deleted for me (`id`, `chat_jid`). |
| `presence_changed` = 7 | `PresenceUpdate` | Typing/recording or online/offline transition. |
| `profile_picture_updated` = 8 | `ProfilePictureUpdate` | An avatar changed; `path` is empty when the picture was removed. |

`PresenceUpdate` carries `chat_jid`, `user_jid`, `user_name`, `typing`,
`recording` ("typing a voice message rather than text"), `online`, and
`last_seen` (unix seconds; `0` when unknown or hidden). For direct chats
`chat_jid` equals the user's JID.

This matches PRD §11's event list (`MessageReceived`, `MessageUpdated`,
`ChatUpdated`, `PresenceChanged`, `QRUpdated`, `ConnectionState`), plus
`MessageDeleted` and `ProfilePictureUpdate`.

### Server behaviour

`Service.StreamEvents` (`apps/daemon/service/service.go`) subscribes to the
broker, then sends, before anything else:

1. the current `ConnectionState` from `session.State()`;
2. `session.PendingQR()`, if a non-expired QR code exists.

The comment explains why: "so an attaching client never has to poll". It then
forwards broker events until the stream context is done or the subscription
channel closes.

The broker (`apps/daemon/service/broker.go`) gives every subscriber a channel of
capacity `subscriberBuffer = 256` and publishes with a non-blocking send. A
subscriber that falls behind has its event dropped and logged as `event dropped,
subscriber too slow`, because "a stalled UI must never block the whatsmeow
loop". Events are fanned out to every connected client.

### Client behaviour

`Client.StreamEvents` (`apps/desktop/client/client.go`) runs a goroutine that
opens the stream, pumps it through `receiveLoop`, and on any error reports it
via `onError` and retries. Backoff starts at `2 * time.Second`, doubles, and is
capped at `60 * time.Second`; the loop exits when the context is cancelled.

`window.onEvent` (`apps/desktop/window.go`) switches on the payload and handles
`Event_QrUpdated`, `Event_ConnectionState`, `Event_MessageReceived` (which also
raises the desktop notification), `Event_MessageUpdated`,
`Event_MessageDeleted`, `Event_PresenceChanged` and
`Event_ProfilePictureUpdated`.

## How the desktop client wraps calls

`apps/desktop/client/client.go` is the only place the generated stubs are
touched. Its package comment states the rule: "GTK is not thread safe: no
callback in this package may touch a widget from a background goroutine, so
every result is delivered through `glib.IdleAdd`."

Every wrapper follows the same shape:

1. Start a goroutine, so the GTK main loop never blocks on IPC.
2. Derive `callCtx, cancel := context.WithTimeout(ctx, callTimeout)` where
   `callTimeout = 10 * time.Second`.
3. Call the stub; nil-check the response before reading it.
4. Deliver the result with `idle(func() { onDone(...) })`, where
   `func idle(f func()) { glib.IdleAdd(f) }`.

Exceptions that deliberately skip the 10 s timeout and pass the caller's context
straight through, because the transfer "can far outlast a regular call":
`SendMedia`, `DownloadMedia`, `RetryMedia`, and `GetProfilePicture` (which "may
have to download the image, so it gets the longer media timeout").

`SetPresence` is the one fire-and-forget wrapper: it has no `onDone`, because it
"is fired on every keystroke burst, so failures are dropped rather than
reported".

`daemonctl.probe` is the other direct stub user: it dials the socket and calls
`GetConnectionState` with a 300 ms timeout to decide whether a daemon is
already running.

## Code generation

Generated Go lives in `packages/ipc/zchatv1/` (`zchat.pb.go`,
`zchat_grpc.pb.go`) and is regenerated with:

```bash
make proto
```

The target (`Makefile`) installs pinned plugin versions into `$(go env
GOPATH)/bin` and then runs `protoc`:

```
PROTOC_GEN_GO_VERSION      := v1.36.12
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2
```

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
PATH="$(go env GOPATH)/bin:$PATH" protoc --proto_path=proto \
	--go_out=. --go_opt=module=github.com/zealish/zchat \
	--go-grpc_out=. --go-grpc_opt=module=github.com/zealish/zchat \
	proto/zchat/v1/zchat.proto
```

`--go_opt=module=github.com/zealish/zchat` strips the module prefix from the
`go_package` option, so output lands in `packages/ipc/zchatv1/` relative to the
repository root. `protoc` is only needed when the proto changes; regular builds
compile the checked-in generated files.
