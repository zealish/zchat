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
//
// Reads go to a separate read-only pool so a query never queues behind the
// single writer, which history sync keeps busy for minutes at a time.
type Store struct {
	db *sql.DB
	ro *sql.DB
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
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_chats_order ON chats(archived, pinned DESC, updated_at DESC);

-- Superseded by the composite indexes above, which lead with the same column.
-- Dropping them removes write amplification on every message and chat insert.
DROP INDEX IF EXISTS idx_messages_chat_jid;
DROP INDEX IF EXISTS idx_messages_timestamp;
DROP INDEX IF EXISTS idx_chats_updated_at;
`

// pragmas tunes SQLite for a desktop workload: NORMAL sync is safe under WAL,
// and the default 2MB cache with no mmap makes every chat-list scan hit the
// filesystem.
const pragmas = `&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)` +
	`&_pragma=synchronous(NORMAL)&_pragma=cache_size(-32000)` +
	`&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)`

// readConns bounds the reader pool. WAL allows readers to run concurrently
// with the single writer, so queries never queue behind a history-sync write.
const readConns = 4

// Open opens (creating if needed) the database at path and applies the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)%s", path, pragmas)
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
	// Without statistics the planner falls back to guesses and picks a table
	// scan over the chat-ordering index. optimize only re-analyses what has
	// changed, so it is cheap on every start after the first.
	if _, err := db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		db.Close()
		return nil, fmt.Errorf("optimize database: %w", err)
	}

	// query_only makes the reader pool's read-only intent enforced rather than
	// conventional, so a stray write can never bypass the single writer.
	ro, err := sql.Open("sqlite", dsn+"&_pragma=query_only(true)")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open reader pool: %w", err)
	}
	ro.SetMaxOpenConns(readConns)

	return &Store{db: db, ro: ro}, nil
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
func (s *Store) Close() error {
	return errors.Join(s.ro.Close(), s.db.Close())
}

// Meta returns a persisted key's value, or "" when the key is unset.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.ro.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read meta %q: %w", key, err)
	}
	return value, nil
}

// SetMeta stores a persisted key/value pair.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("write meta %q: %w", key, err)
	}
	return nil
}

// CountChats returns how many regular chats are stored.
func (s *Store) CountChats(ctx context.Context) (int, error) {
	var n int
	if err := s.ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE `+realChatFilter).Scan(&n); err != nil {
		return 0, fmt.Errorf("count chats: %w", err)
	}
	return n, nil
}

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
	if err := upsertChatTx(ctx, s.db, c); err != nil {
		return err
	}
	return nil
}

func upsertChatTx(ctx context.Context, ex execer, c Chat) error {
	_, err := ex.ExecContext(ctx, `
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

// SyncConversation ingests one history-sync conversation: the chat row, its
// message batch and the refreshed preview, in a single transaction.
//
// Run as four separate statements this cost four commits per conversation, and
// a full sync carries hundreds of them. The chat row is written before the
// messages so the preview refresh sees the batch.
func (s *Store) SyncConversation(ctx context.Context, c Chat, msgs []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sync %s: %w", c.JID, err)
	}
	defer tx.Rollback()

	if err := upsertChatTx(ctx, tx, c); err != nil {
		return err
	}
	for _, m := range msgs {
		if err := insertMessageTx(ctx, tx, m); err != nil {
			return err
		}
	}
	if err := refreshLastMessageTx(ctx, tx, c.JID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListChats returns regular direct and group chats, ordered pinned first, then
// most recently updated. Archived chats live on their own list, so archived
// selects which of the two is returned.
func (s *Store) ListChats(ctx context.Context, limit, offset int, archived bool) ([]Chat, error) {
	rows, err := s.ro.QueryContext(ctx, chatColumns+`
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
	c, err := scanChat(s.ro.QueryRowContext(ctx, chatColumns+` WHERE jid = ?`, jid))
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
	err := s.ro.QueryRowContext(ctx, `SELECT name FROM chats WHERE jid = ?`, jid).Scan(&name)
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
	rows, err := s.ro.QueryContext(ctx, `
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

// LIDChats returns chats stored under a WhatsApp LID rather than a phone JID.
func (s *Store) LIDChats(ctx context.Context) ([]string, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT jid FROM chats WHERE jid LIKE '%@lid'`)
	if err != nil {
		return nil, fmt.Errorf("scan lid chats: %w", err)
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

// MergeChat folds a duplicate chat row onto the JID it should have been stored
// under, moving its messages across and combining the two rows. The target may
// not exist yet, in which case the source row is simply re-keyed.
func (s *Store) MergeChat(ctx context.Context, from, to string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("merge chat %s: %w", from, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET chat_jid = ? WHERE chat_jid = ?`, to, from); err != nil {
		return fmt.Errorf("move messages of %s: %w", from, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO chats (jid, name, unread, archived, pinned, last_message, updated_at, is_group, muted_until)
SELECT ?, name, unread, archived, pinned, last_message, updated_at, is_group, muted_until
FROM chats WHERE jid = ?
ON CONFLICT(jid) DO UPDATE SET
    name = CASE WHEN chats.name != '' THEN chats.name ELSE excluded.name END,
    unread = chats.unread + excluded.unread,
    archived = chats.archived OR excluded.archived,
    pinned = chats.pinned OR excluded.pinned,
    last_message = CASE WHEN chats.last_message != '' THEN chats.last_message ELSE excluded.last_message END,
    updated_at = MAX(chats.updated_at, excluded.updated_at)`, to, from); err != nil {
		return fmt.Errorf("fold chat %s into %s: %w", from, to, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE jid = ?`, from); err != nil {
		return fmt.Errorf("drop merged chat %s: %w", from, err)
	}
	return tx.Commit()
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
	return refreshLastMessageTx(ctx, s.db, jid)
}

func refreshLastMessageTx(ctx context.Context, ex execer, jid string) error {
	_, err := ex.ExecContext(ctx, `
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
	rows, err := s.ro.QueryContext(ctx, `
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
	rows, err := s.ro.QueryContext(ctx, messageColumns+`
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
	m, err := scanMessage(s.ro.QueryRowContext(ctx, messageColumns+` WHERE m.id = ?`, id))
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
	err = s.ro.QueryRowContext(ctx,
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
	rows, err := s.ro.QueryContext(ctx, chatColumns+`
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
	rows, err := s.ro.QueryContext(ctx, messageColumns+`
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
