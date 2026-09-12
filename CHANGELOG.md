# Changelog

All notable changes to ZChat are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.8.0] - 2026-09-12

### Added

- Photos and videos open in an in-app viewer instead of being handed to an
  external application: a dark modal window with the sender and timestamp in
  its header, the caption along the bottom, a "Save as…" action, and Escape or
  Ctrl+W to close. Videos play inline through GTK's media stream and are paused
  when the viewer closes.

### Changed

- Attachments follow the WhatsApp layout. The download control is a circular
  overlay centred on the thumbnail rather than a labelled button underneath it,
  and turns into a play button once a video has been downloaded. Size and clip
  length render as a badge in the thumbnail's bottom-right corner.
- Thumbnails are sized from the attachment's real dimensions instead of a fixed
  240x180 box, so photos keep their aspect ratio within the inline column.
- Videos show a thumbnail like photos and stickers do, rather than falling back
  to a bare download button.
- Documents and audio render as a named row with an icon, filename and size,
  matching how WhatsApp lists a file.
- Chat-list and message queries run against a read-only connection pool, so
  they no longer queue behind the single writer that history sync keeps busy
  for minutes at a time. The message and chat indexes are composite ones
  matching how those queries order, replacing single-column indexes that only
  amplified writes.
- Contact names are cached rather than resolved from the database per message,
  and invalidated when WhatsApp reports a contact, push name or business name
  change.

### Fixed

- Attachments rendered as nothing at all. Widget maps were keyed on gotk4's Go
  wrapper pointers, which are reallocated every time a widget is reached
  through the tree, so the lookup in a bound row never matched what the list
  factory registered. They are keyed on the underlying GObject address now,
  which also fixes recycled chat and message rows never being released and
  avatar updates being applied to stale rows.
- Scrolling a chat with attachments no longer stutters. Every row bind decoded
  the message's thumbnail from scratch (~10ms on the main loop) and laid out
  two Pango layouts to size the timestamp spacer; both are cached now, taking a
  bind from ~10.2ms to ~62us.
- Status updates, reactions and deletes scanned the message model forwards,
  allocating a wrapper per element through cgo. They scan from the newest end
  instead, which is where the target nearly always is.
- The decoded-image cache grew for the lifetime of the window, holding a
  full-size pixbuf for every attachment scrolled past. It is bounded and evicts
  least-recently-used entries.

## [0.7.1] - 2026-09-12

### Changed

- Message bubbles follow the WhatsApp Web layout: capped bubble width, the
  timestamp overlaid on the bubble's bottom-right corner, and a palette mixed
  against libadwaita's named colours so it follows the light/dark scheme.

### Fixed

- Chats and new-chat contacts open on a single click instead of two.
- The chat list no longer jumps back to the top when a chat is opened; chat
  updates are merged into the existing row instead of replacing it.
- The daemon no longer publishes a chat update when there was no unread count
  to clear.
- Images and stickers decode on a worker goroutine, so opening a conversation
  with media no longer freezes the window; concurrent decodes of one path are
  shared and stills are capped rather than decoded at full resolution.
- The reaction picker reuses a single emoji chooser instead of rebuilding it on
  every right click.
- The new-chat contact list scrolls instead of growing unbounded.
- Pinning the view to the newest message no longer recurses.

## [0.7.0] - 2026-09-12

### Added

- Full-sync progress tracking: the daemon reports stage and progress over a new
  `SyncState` event and `GetSyncState` RPC, and the desktop blocks on a sync
  progress screen until the first history sync completes.
- Sync completion is persisted in a meta table, so restarts never gate again.

### Changed

- History chunks canonicalise their chat JID, and chat reconciliation merges LID
  rows into their phone-number twin, announced via `ChatDeleted`.
- The desktop window returns to the QR page on logout.

## [0.6.0] - 2026-09-12

### Added

- Emoji picker on the composer and an emoji chooser for reactions, replacing the
  three hardcoded reaction entries.
- Find in conversation (`Ctrl+F`), backed by a new `SearchMessages` RPC that
  searches the whole stored history and pages backwards to older hits.
- Preferences dialog with theme selection and a notification toggle, persisted
  to `~/.config/zchat/`.
- Primary menu with preferences, a shortcuts window, and an about dialog whose
  version is stamped at build time.
- Chat info view and profile pictures for chats and contacts.
- Retry support for failed messages.
- Documentation under `docs/`: architecture, IPC contract, build, and packaging.
- CI workflows and RPM build/upload in the release job.

### Fixed

- Desktop startup failure.
- Session reset now stops the daemon and cleans up the XDG databases correctly.
- Fingerprint connection and presence policies are applied.

## [0.5.0] - 2026-09-11

### Added

- Typing indicators and contact presence: the daemon forwards chat presence as
  `PresenceUpdate` and reports local activity through `SetPresence`; the desktop
  shows who is typing or the contact's online/last-seen line in the header.
- Animated WebP stickers decoded in Go, removing the `webp-pixbuf-loader`
  dependency and playing frames from the delays in the file.
- Sticker rows carry their PNG thumbnail, so a preview appears before the file
  is downloaded.

### Changed

- Typing indicators expire after twelve seconds; local typing is withdrawn after
  four idle seconds or when switching chats.
- Cached chat states and presence subscriptions are dropped on disconnect.
- Sticker frames are scaled to 192px before being held in memory.

## [0.4.0] - 2026-09-11

### Added

- Outgoing attachments: attach button, drag-and-drop onto the message list, and
  `Ctrl+V` for clipboard images. The daemon reads the file over the Unix socket,
  sniffs the MIME type and uploads it, so captions and replies work as for text.
- Read receipts sent when a chat is opened, batched over the newest 50 incoming
  messages and grouped by author.
- Packaging: desktop entry, AppStream metainfo, icon, and RPM and Flatpak
  manifests building from a vendored source tarball, so packaging needs no
  network access.

### Changed

- Reply context extraction is shared between text and media messages.

## [0.3.0] - 2026-09-11

### Added

- Replies and quoting, message forwarding, delete-for-me and
  revoke-for-everyone.
- Pinned, archived and muted chats, synced through app state.

### Fixed

- Guard the revoke branch with a nil check: `REVOKE` is the zero value of
  `ProtocolMessage_Type`, so every incoming message was treated as a revoke and
  dropped.

## [0.2.0] - 2026-09-11

### Added

- Media messages: image, video, audio, document and sticker payloads are
  persisted with their metadata and serialised `waE2E` message, so files are
  decrypted on demand instead of downloaded eagerly.
- Downloads land in the XDG media directory under a hash of the message ID; the
  desktop shows the WhatsApp thumbnail with a download/open button.
- Chat search in the daemon over name and last message, with wildcards escaped
  so queries are always literal.
- Native desktop notifications for incoming messages, suppressed when the chat
  is already open in a focused window.

### Fixed

- Explicit migration adds the new media columns to databases created by 0.1.0.

## [0.1.0] - 2026-09-11

### Added

- Initial release: the zchat daemon, the GTK4/Libadwaita desktop application,
  and the shared IPC packages.
