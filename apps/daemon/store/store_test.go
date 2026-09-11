package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, ctx
}

func TestUpdateMessageStatusNeverGoesBackwards(t *testing.T) {
	s, ctx := newTestStore(t)

	if err := s.InsertMessage(ctx, Message{ID: "m1", ChatJID: "a@s.whatsapp.net", Body: "hi", Type: "text", Timestamp: 10, Outgoing: true, Status: StatusSent}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := s.UpdateMessageStatus(ctx, "m1", StatusRead); err != nil {
		t.Fatalf("update read: %v", err)
	}
	if m, _ := s.GetMessage(ctx, "m1"); m.Status != StatusRead {
		t.Fatalf("want %s, got %s", StatusRead, m.Status)
	}

	if err := s.UpdateMessageStatus(ctx, "m1", StatusDelivered); err != nil {
		t.Fatalf("update delivered: %v", err)
	}
	if m, _ := s.GetMessage(ctx, "m1"); m.Status != StatusRead {
		t.Fatalf("delivered downgraded read: got %s", m.Status)
	}

	if err := s.UpdateMessageStatus(ctx, "m1", StatusFailed); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if m, _ := s.GetMessage(ctx, "m1"); m.Status != StatusFailed {
		t.Fatalf("want %s, got %s", StatusFailed, m.Status)
	}
}

func TestInsertMessageIsIdempotent(t *testing.T) {
	s, ctx := newTestStore(t)

	m := Message{ID: "m1", ChatJID: "a@s.whatsapp.net", Body: "original", Type: "text", Timestamp: 10, Status: StatusDelivered}
	if err := s.InsertMessage(ctx, m); err != nil {
		t.Fatalf("insert: %v", err)
	}
	m.Body = "replayed"
	if err := s.InsertMessage(ctx, m); err != nil {
		t.Fatalf("re-insert: %v", err)
	}

	got, err := s.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Body != "original" {
		t.Fatalf("replay overwrote body: %q", got.Body)
	}
}

func TestInsertMessagesBatchSkipsReplays(t *testing.T) {
	s, ctx := newTestStore(t)

	batch := []Message{
		{ID: "m1", ChatJID: "a@s.whatsapp.net", Body: "first", Timestamp: 10, Status: StatusDelivered},
		{ID: "m2", ChatJID: "a@s.whatsapp.net", Body: "second", Timestamp: 20, Status: StatusDelivered},
	}
	if err := s.InsertMessages(ctx, batch); err != nil {
		t.Fatalf("batch insert: %v", err)
	}

	batch[0].Body = "replayed"
	batch = append(batch, Message{ID: "m3", ChatJID: "a@s.whatsapp.net", Body: "third", Timestamp: 30, Status: StatusDelivered})
	if err := s.InsertMessages(ctx, batch); err != nil {
		t.Fatalf("batch replay: %v", err)
	}

	msgs, err := s.ListMessages(ctx, "a@s.whatsapp.net", 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if msgs[0].Body != "first" {
		t.Fatalf("replay overwrote body: %q", msgs[0].Body)
	}

	if err := s.InsertMessages(ctx, nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
}

func TestListMessagesIsChronological(t *testing.T) {
	s, ctx := newTestStore(t)

	for _, m := range []Message{
		{ID: "m3", ChatJID: "a@s.whatsapp.net", Body: "third", Timestamp: 30},
		{ID: "m1", ChatJID: "a@s.whatsapp.net", Body: "first", Timestamp: 10},
		{ID: "m2", ChatJID: "a@s.whatsapp.net", Body: "second", Timestamp: 20},
	} {
		if err := s.InsertMessage(ctx, m); err != nil {
			t.Fatalf("insert %s: %v", m.ID, err)
		}
	}

	msgs, err := s.ListMessages(ctx, "a@s.whatsapp.net", 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"m1", "m2", "m3"}
	if len(msgs) != len(want) {
		t.Fatalf("want %d messages, got %d", len(want), len(msgs))
	}
	for i, id := range want {
		if msgs[i].ID != id {
			t.Fatalf("position %d: want %s, got %s", i, id, msgs[i].ID)
		}
	}
}

func TestRecentIncomingSkipsOwnMessages(t *testing.T) {
	s, ctx := newTestStore(t)

	msgs := []Message{
		{ID: "in1", ChatJID: "g@g.us", Sender: "a@s.whatsapp.net", Type: "text", Timestamp: 10},
		{ID: "out", ChatJID: "g@g.us", Sender: "me@s.whatsapp.net", Type: "text", Timestamp: 20, Outgoing: true},
		{ID: "in2", ChatJID: "g@g.us", Sender: "b@s.whatsapp.net", Type: "text", Timestamp: 30},
	}
	for _, m := range msgs {
		if err := s.InsertMessage(ctx, m); err != nil {
			t.Fatalf("insert %s: %v", m.ID, err)
		}
	}

	refs, err := s.RecentIncoming(ctx, "g@g.us", 10)
	if err != nil {
		t.Fatalf("recent incoming: %v", err)
	}
	want := []MessageRef{
		{ID: "in2", Sender: "b@s.whatsapp.net"},
		{ID: "in1", Sender: "a@s.whatsapp.net"},
	}
	if len(refs) != len(want) {
		t.Fatalf("want %d refs, got %d: %+v", len(want), len(refs), refs)
	}
	for i, ref := range refs {
		if ref != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, ref, want[i])
		}
	}
}
