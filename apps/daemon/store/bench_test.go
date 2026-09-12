package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// benchMessages mirrors one history-sync conversation batch.
func benchMessages(chat string, n int) []Message {
	msgs := make([]Message, n)
	for i := range n {
		msgs[i] = Message{
			ID:        fmt.Sprintf("%s-%d", chat, i),
			ChatJID:   chat,
			Sender:    chat,
			Body:      "a reasonably typical chat message body",
			Type:      "text",
			Timestamp: int64(i),
			Status:    StatusDelivered,
		}
	}
	return msgs
}

func BenchmarkInsertMessageIndividually(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		s, ctx := newBenchStore(b)
		msgs := benchMessages("a@s.whatsapp.net", 500)
		b.StartTimer()

		for _, m := range msgs {
			if err := s.InsertMessage(ctx, m); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkInsertMessagesBatched(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		s, ctx := newBenchStore(b)
		msgs := benchMessages("a@s.whatsapp.net", 500)
		b.StartTimer()

		if err := s.InsertMessages(ctx, msgs); err != nil {
			b.Fatal(err)
		}
	}
}

// seedChats fills the store with a history-sized corpus: many chats, and one
// large conversation to page through.
func seedChats(b *testing.B, s *Store, ctx context.Context, chats, perChat int) string {
	b.Helper()
	var busiest string
	for i := range chats {
		jid := fmt.Sprintf("chat%d@s.whatsapp.net", i)
		if err := s.UpsertChat(ctx, Chat{JID: jid, Name: jid, UpdatedAt: int64(i)}); err != nil {
			b.Fatal(err)
		}
		n := perChat
		if i == 0 {
			// One outsized conversation: ORDER BY without a covering index
			// degrades with the rows scanned, not the rows returned.
			busiest = jid
			n = perChat * 20
		}
		if err := s.InsertMessages(ctx, benchMessages(jid, n)); err != nil {
			b.Fatal(err)
		}
	}
	return busiest
}

func BenchmarkListMessagesPage(b *testing.B) {
	s, ctx := newBenchStore(b)
	busiest := seedChats(b, s, ctx, 50, 50)

	b.ResetTimer()
	for b.Loop() {
		if _, err := s.ListMessages(ctx, busiest, 200, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListChatsPage(b *testing.B) {
	s, ctx := newBenchStore(b)
	seedChats(b, s, ctx, 50, 50)

	b.ResetTimer()
	for b.Loop() {
		if _, err := s.ListChats(ctx, 200, 0, false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRefreshLastMessage(b *testing.B) {
	s, ctx := newBenchStore(b)
	busiest := seedChats(b, s, ctx, 50, 50)

	b.ResetTimer()
	for b.Loop() {
		if err := s.RefreshLastMessage(ctx, busiest); err != nil {
			b.Fatal(err)
		}
	}
}
func newBenchStore(b *testing.B) (*Store, context.Context) {
	b.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { s.Close() })
	return s, ctx
}
