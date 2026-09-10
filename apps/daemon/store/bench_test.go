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
