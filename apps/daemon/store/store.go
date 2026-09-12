// Package store persists chats and messages in the shared SQLite database.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Message status values as persisted in the database.
const (
	StatusPending   = "pending"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// statusRank orders the delivery lifecycle so a late receipt cannot downgrade a
// message that already reached a later state. "failed" is unranked and always
// applies.
var statusRank = map[string]int{
	StatusPending:   1,
	StatusSent:      2,
	StatusDelivered: 3,
	StatusRead:      4,
}

// Chat is a conversation row.
type Chat struct {
	JID         string
	Name        string
	Unread      int32
	Archived    bool
	Pinned      bool
	LastMessage string
	UpdatedAt   int64
	IsGroup     bool
	// MutedUntil is a unix timestamp, -1 for muted indefinitely and 0 for not
	// muted.
	MutedUntil int64
}

// Message is a single message row. Media is nil for plain text, Quoted is nil
// unless the message is a reply.
type Message struct {
	ID         string
	ChatJID    string
	Sender     string
	SenderName string
	Body       string
	Type       string
	Timestamp  int64
	Outgoing   bool
	Status     string
	Forwarded  bool
	Reaction   string
	Media      *Media
	Quoted     *Quoted
}

// Quoted is the snapshot of the message a reply points at. The original is not
// necessarily stored locally, so its preview travels with the reply.
type Quoted struct {
	ID         string
	Sender     string
	SenderName string
	Body       string
	Type       string
}

// Media is an attachment belonging to a message. Payload holds the serialised
// waE2E.Message so the daemon can decrypt the file on demand long after the
// event was received.
type Media struct {
	MessageID string
	Path      string
	Mime      string
	Size      int64
	Filename  string
	Caption   string
	Thumbnail []byte
	Width     int32
	Height    int32
	Duration  int32
	Payload   []byte
}

// Store owns the database handle shared with whatsmeow's session store.
type Store struct {
	db *sql.DB
}

const schema = `
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
`

// Open opens (creating if needed) the database at path and applies the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection serialises writes between the whatsmeow event loop and the
	// gRPC handlers, avoiding SQLITE_BUSY.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	for table, cols := range addedColumns {
		if err := migrateColumns(ctx, db, table, cols); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// addedColumns lists columns added after a table's first release. Existing
// databases already have the table, so CREATE TABLE IF NOT EXISTS alone would
// leave them without these columns.
var addedColumns = map[string][]struct{ name, decl string }{
	"media": {
		{"filename", "TEXT"},
		{"caption", "TEXT"},
		{"thumbnail", "BLOB"},
		{"width", "INTEGER"},
		{"height", "INTEGER"},
		{"duration", "INTEGER"},
		{"payload", "BLOB"},
	},
	"chats": {
		{"muted_until", "INTEGER DEFAULT 0"},
	},
	"messages": {
		{"forwarded", "BOOLEAN DEFAULT FALSE"},
		{"quoted_id", "TEXT"},
		{"quoted_sender", "TEXT"},
		{"quoted_sender_name", "TEXT"},
		{"quoted_body", "TEXT"},
		{"quoted_type", "TEXT"},
		{"reaction", "TEXT DEFAULT ''"},
	},
}

func migrateColumns(ctx context.Context, db *sql.DB, table string, cols []struct{ name, decl string }) error {
	// The table name comes from the fixed map above, never from user input.
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s table: %w", table, err)
	}
	defer rows.Close()

	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan %s column: %w", table, err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, col := range cols {
		if existing[col.name] {
			continue
		}
		// Column names come from the fixed list above, never from user input.
		if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+col.name+` `+col.decl); err != nil {
			return fmt.Errorf("add %s column %s: %w", table, col.name, err)
		}
	}
	return nil
}

// DB exposes the handle so whatsmeow's sqlstore can share it.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// chatColumns selects every chat field in the order scanChat expects.
const chatColumns = `
SELECT jid, name, unread, archived, pinned, COALESCE(last_message, ''), COALESCE(updated_at, 0),
       is_group, COALESCE(muted_until, 0)
FROM chats`

// realChatFilter excludes newsletters, broadcast lists and status updates. Rows
// for them may exist from before they were filtered on ingest.
const realChatFilter = `(jid LIKE '%@s.whatsapp.net' OR jid LIKE '%@c.us' OR jid LIKE '%@lid' OR jid LIKE '%@g.us')`

func scanChat(sc interface{ Scan(...any) error }) (Chat, error) {
	var c Chat
	err := sc.Scan(&c.JID, &c.Name, &c.Unread, &c.Archived, &c.Pinned, &c.LastMessage,
		&c.UpdatedAt, &c.IsGroup, &c.MutedUntil)
	return c, err
}

// UpsertChat inserts or updates a chat, preserving an existing name when the
// incoming one is empty.
func (s *Store) UpsertChat(ctx context.Context, c Chat) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO chats (jid, name, unread, archived, pinned, last_message, updated_at, is_group, muted_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
    unread = excluded.unread,
    archived = excluded.archived,
    pinned = excluded.pinned,
    last_message = CASE WHEN excluded.last_message != '' THEN excluded.last_message ELSE chats.last_message END,
    updated_at = MAX(excluded.updated_at, chats.updated_at),
    is_group = excluded.is_group,
    muted_until = excluded.muted_until`,
		c.JID, c.Name, c.Unread, c.Archived, c.Pinned, c.LastMessage, c.UpdatedAt, c.IsGroup, c.MutedUntil)
	if err != nil {
		return fmt.Errorf("upsert chat %s: %w", c.JID, err)
	}
	return nil
}

// ListChats returns regular direct and group chats, ordered pinned first, then
// most recently updated. Archived chats live on their own list, so archived
// selects which of the two is returned.
func (s *Store) ListChats(ctx context.Context, limit, offset int, archived bool) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, chatColumns+`
WHERE `+realChatFilter+` AND archived = ?
ORDER BY pinned DESC, updated_at DESC
LIMIT ? OFFSET ?`, archived, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// SetChatFlags updates a chat's pinned, archived and muted state. Nil fields
// are left untouched.
func (s *Store) SetChatFlags(ctx context.Context, jid string, pinned, archived *bool, mutedUntil *int64) error {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if pinned != nil {
		sets = append(sets, "pinned = ?")
		args = append(args, *pinned)
	}
	if archived != nil {
		sets = append(sets, "archived = ?")
		args = append(args, *archived)
		// WhatsApp unpins a chat when it is archived.
		if *archived {
			sets = append(sets, "pinned = FALSE")
		}
	}
	if mutedUntil != nil {
		sets = append(sets, "muted_until = ?")
		args = append(args, *mutedUntil)
	}
	if len(sets) == 0 {
		return nil
	}

	args = append(args, jid)
	// Every fragment above is a fixed literal; only values are parameterised.
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET `+strings.Join(sets, ", ")+` WHERE jid = ?`, args...)
	if err != nil {
		return fmt.Errorf("set chat flags %s: %w", jid, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetChat loads a single chat.
func (s *Store) GetChat(ctx context.Context, jid string) (Chat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx, chatColumns+` WHERE jid = ?`, jid))
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, fmt.Errorf("get chat %s: %w", jid, err)
	}
	return c, nil
}

// ChatName returns the stored display name, or "" when the chat is unknown.
func (s *Store) ChatName(ctx context.Context, jid string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM chats WHERE jid = ?`, jid).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get chat name %s: %w", jid, err)
	}
	return name, nil
}

// ChatsNamedByNumber returns direct chats whose stored name is still just the
// JID user part, meaning no contact name was available when they were created.
func (s *Store) ChatsNamedByNumber(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT jid FROM chats
WHERE is_group = FALSE AND (name = '' OR name = SUBSTR(jid, 1, INSTR(jid, '@') - 1))`)
	if err != nil {
		return nil, fmt.Errorf("scan chats named by number: %w", err)
	}
	defer rows.Close()

	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("scan chat jid: %w", err)
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}

// SetChatName overwrites a chat's display name.
func (s *Store) SetChatName(ctx context.Context, jid, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET name = ? WHERE jid = ?`, name, jid)
	if err != nil {
		return fmt.Errorf("set chat name %s: %w", jid, err)
	}
	return nil
}

// TouchChat records the newest message preview and optionally bumps the unread
// counter. It creates the chat row when it does not exist yet.
func (s *Store) TouchChat(ctx context.Context, jid, name, lastMessage string, ts int64, isGroup, incrementUnread bool) error {
	unread := 0
	if incrementUnread {
		unread = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO chats (jid, name, unread, last_message, updated_at, is_group)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    name = CASE WHEN chats.name != '' THEN chats.name ELSE excluded.name END,
    unread = chats.unread + ?,
    last_message = excluded.last_message,
    updated_at = MAX(excluded.updated_at, chats.updated_at),
    is_group = excluded.is_group`,
		jid, name, unread, lastMessage, ts, isGroup, unread)
	if err != nil {
		return fmt.Errorf("touch chat %s: %w", jid, err)
	}
	return nil
}

// RefreshLastMessage resets the preview from the newest stored message.
func (s *Store) RefreshLastMessage(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE chats
SET last_message = COALESCE((SELECT body FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 1), last_message)
WHERE jid = ?`, jid, jid)
	if err != nil {
		return fmt.Errorf("refresh last message %s: %w", jid, err)
	}
	return nil
}

// ClearUnread zeroes the unread counter for a chat.
func (s *Store) ClearUnread(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET unread = 0 WHERE jid = ?`, jid)
	if err != nil {
		return fmt.Errorf("clear unread %s: %w", jid, err)
	}
	return nil
}

// MessageRef identifies a message and its author, which is all a read receipt
// needs.
type MessageRef struct {
	ID     string
	Sender string
}

// RecentIncoming returns the newest incoming messages of a chat, newest first.
func (s *Store) RecentIncoming(ctx context.Context, chatJID string, limit int) ([]MessageRef, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(sender, '') FROM messages
WHERE chat_jid = ? AND outgoing = FALSE
ORDER BY timestamp DESC
LIMIT ?`, chatJID, limit)
	if err != nil {
		return nil, fmt.Errorf("list incoming %s: %w", chatJID, err)
	}
	defer rows.Close()

	var refs []MessageRef
	for rows.Next() {
		var ref MessageRef
		if err := rows.Scan(&ref.ID, &ref.Sender); err != nil {
			return nil, fmt.Errorf("scan incoming message: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// InsertMessage stores a message and its attachment, ignoring replays from
// history sync.
func (s *Store) InsertMessage(ctx context.Context, m Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert %s: %w", m.ID, err)
	}
	defer tx.Rollback()

	if err := insertMessageTx(ctx, tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const insertMessageSQL = `
INSERT INTO messages (id, chat_jid, sender, sender_name, body, type, timestamp, outgoing, status,
                      forwarded, reaction, quoted_id, quoted_sender, quoted_sender_name, quoted_body, quoted_type)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`

const insertMediaSQL = `
INSERT INTO media (id, message_id, path, mime, size, filename, caption, thumbnail, width, height, duration, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`

func insertMessageTx(ctx context.Context, ex execer, m Message) error {
	q := m.Quoted
	if q == nil {
		q = &Quoted{}
	}
	if _, err := ex.ExecContext(ctx, insertMessageSQL,
		m.ID, m.ChatJID, m.Sender, m.SenderName, m.Body, m.Type, m.Timestamp, m.Outgoing, m.Status,
		m.Forwarded, m.Reaction, q.ID, q.Sender, q.SenderName, q.Body, q.Type); err != nil {
		return fmt.Errorf("insert message %s: %w", m.ID, err)
	}
	if m.Media == nil {
		return nil
	}
	if _, err := ex.ExecContext(ctx, insertMediaSQL,
		m.ID, m.ID, m.Media.Path, m.Media.Mime, m.Media.Size, m.Media.Filename, m.Media.Caption,
		m.Media.Thumbnail, m.Media.Width, m.Media.Height, m.Media.Duration, m.Media.Payload); err != nil {
		return fmt.Errorf("insert media %s: %w", m.ID, err)
	}
	return nil
}

// InsertMessages stores a batch of messages in one transaction. History sync
// delivers thousands of rows at a time, where per-row transactions dominate.
func (s *Store) InsertMessages(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch insert: %w", err)
	}
	defer tx.Rollback()

	for _, m := range msgs {
		if err := insertMessageTx(ctx, tx, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// messageColumns selects a message with its optional attachment and reply
// context.
const messageColumns = `
SELECT m.id, m.chat_jid, COALESCE(m.sender, ''), COALESCE(m.sender_name, ''), COALESCE(m.body, ''),
       COALESCE(m.type, ''), COALESCE(m.timestamp, 0), m.outgoing, COALESCE(m.status, ''),
       COALESCE(m.forwarded, FALSE), COALESCE(m.reaction, ''), COALESCE(m.quoted_id, ''), COALESCE(m.quoted_sender, ''),
       COALESCE(m.quoted_sender_name, ''), COALESCE(m.quoted_body, ''), COALESCE(m.quoted_type, ''),
       md.id IS NOT NULL, COALESCE(md.path, ''), COALESCE(md.mime, ''), COALESCE(md.size, 0),
       COALESCE(md.filename, ''), COALESCE(md.caption, ''), md.thumbnail,
       COALESCE(md.width, 0), COALESCE(md.height, 0), COALESCE(md.duration, 0)
FROM messages m LEFT JOIN media md ON md.id = m.id`

// scanMessage reads one row shaped by messageColumns.
func scanMessage(sc interface{ Scan(...any) error }) (Message, error) {
	var (
		m        Message
		md       Media
		q        Quoted
		hasMedia bool
	)
	err := sc.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.SenderName, &m.Body, &m.Type, &m.Timestamp,
		&m.Outgoing, &m.Status, &m.Forwarded, &m.Reaction, &q.ID, &q.Sender, &q.SenderName, &q.Body, &q.Type,
		&hasMedia, &md.Path, &md.Mime, &md.Size, &md.Filename,
		&md.Caption, &md.Thumbnail, &md.Width, &md.Height, &md.Duration)
	if err != nil {
		return Message{}, err
	}
	if hasMedia {
		md.MessageID = m.ID
		m.Media = &md
	}
	if q.ID != "" {
		m.Quoted = &q
	}
	return m, nil
}

// ListMessages returns messages in chronological order, newest page first when
// before is non-zero.
func (s *Store) ListMessages(ctx context.Context, chatJID string, limit int, before int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, messageColumns+`
WHERE m.chat_jid = ? AND (? = 0 OR m.timestamp < ?)
ORDER BY m.timestamp DESC
LIMIT ?`, chatJID, before, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages %s: %w", chatJID, err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// GetMessage loads a single message.
func (s *Store) GetMessage(ctx context.Context, id string) (Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, messageColumns+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("get message %s: %w", id, err)
	}
	return m, nil
}

// DeleteMessage removes a message and its attachment row. The downloaded file
// itself is left on disk; the media directory is cleaned up separately.
func (s *Store) DeleteMessage(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete %s: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete media %s: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete message %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// MediaPayload returns the stored waE2E payload needed to decrypt a message's
// attachment, plus the local path when it was already downloaded.
func (s *Store) MediaPayload(ctx context.Context, messageID string) (payload []byte, path string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(payload, X''), COALESCE(path, '') FROM media WHERE id = ?`, messageID).
		Scan(&payload, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("read media payload %s: %w", messageID, err)
	}
	return payload, path, nil
}

// SetMediaPath records where a downloaded attachment landed on disk.
func (s *Store) SetMediaPath(ctx context.Context, messageID, path string, size int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE media SET path = ?, size = CASE WHEN ? > 0 THEN ? ELSE size END WHERE id = ?`,
		path, size, size, messageID)
	if err != nil {
		return fmt.Errorf("set media path %s: %w", messageID, err)
	}
	return nil
}

// SearchChats returns chats whose name or last message matches query. Both
// active and archived chats are searched.
func (s *Store) SearchChats(ctx context.Context, query string, limit int) ([]Chat, error) {
	// LIKE with an escaped pattern keeps the caller's wildcards literal.
	pattern := "%" + escapeLike(query) + "%"
	rows, err := s.db.QueryContext(ctx, chatColumns+`
WHERE `+realChatFilter+`
  AND (name LIKE ? ESCAPE '\' OR COALESCE(last_message, '') LIKE ? ESCAPE '\')
ORDER BY pinned DESC, updated_at DESC
LIMIT ?`, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search chats: %w", err)
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// SearchMessages returns messages whose body matches query, newest first.
// chatJID scopes the search to one conversation; empty searches every chat.
func (s *Store) SearchMessages(ctx context.Context, query, chatJID string, limit int) ([]Message, error) {
	pattern := "%" + escapeLike(query) + "%"
	rows, err := s.db.QueryContext(ctx, messageColumns+`
WHERE (? = '' OR m.chat_jid = ?)
  AND COALESCE(m.body, '') LIKE ? ESCAPE '\'
ORDER BY m.timestamp DESC
LIMIT ?`, chatJID, chatJID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// UpdateMessageStatus advances a message's delivery status. Statuses never move
// backwards; "failed" always applies.
func (s *Store) UpdateMessageStatus(ctx context.Context, id, status string) error {
	rank, ranked := statusRank[status]
	if !ranked {
		_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, status, id)
		if err != nil {
			return fmt.Errorf("update status %s: %w", id, err)
		}
		return nil
	}

	var current string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(status, '') FROM messages WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read status %s: %w", id, err)
	}
	if currentRank, ok := statusRank[current]; ok && currentRank >= rank {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, status, id); err != nil {
		return fmt.Errorf("update status %s: %w", id, err)
	}
	return nil
}

// UpdateMessageTimestamp sets the server-assigned timestamp after a send.
func (s *Store) UpdateMessageTimestamp(ctx context.Context, id string, ts int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET timestamp = ? WHERE id = ?`, ts, id)
	if err != nil {
		return fmt.Errorf("update timestamp %s: %w", id, err)
	}
	return nil
}

// UpdateMessageSender records the identity a message was actually sent with.
// Outgoing rows are written before the send completes, when the phone-number
// JID is only a guess: LID-addressed chats and groups send under the LID.
func (s *Store) UpdateMessageSender(ctx context.Context, id, sender string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET sender = ? WHERE id = ?`, sender, id)
	if err != nil {
		return fmt.Errorf("update sender %s: %w", id, err)
	}
	return nil
}

func (s *Store) SetMessageReaction(ctx context.Context, id, reaction string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET reaction = ? WHERE id = ?`, reaction, id)
	if err != nil {
		return fmt.Errorf("set reaction %s: %w", id, err)
	}
	return nil
}
