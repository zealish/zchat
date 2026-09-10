package store

import "testing"

func TestListChatsOrdersPinnedThenRecent(t *testing.T) {
	s, ctx := newTestStore(t)

	chats := []Chat{
		{JID: "old@s.whatsapp.net", Name: "old", UpdatedAt: 100, Pinned: true},
		{JID: "new@s.whatsapp.net", Name: "new", UpdatedAt: 300},
		{JID: "mid@s.whatsapp.net", Name: "mid", UpdatedAt: 200},
	}
	for _, c := range chats {
		if err := s.UpsertChat(ctx, c); err != nil {
			t.Fatalf("upsert %s: %v", c.JID, err)
		}
	}

	got, err := s.ListChats(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"old@s.whatsapp.net", "new@s.whatsapp.net", "mid@s.whatsapp.net"}
	if len(got) != len(want) {
		t.Fatalf("want %d chats, got %d", len(want), len(got))
	}
	for i, jid := range want {
		if got[i].JID != jid {
			t.Fatalf("position %d: want %s, got %s", i, jid, got[i].JID)
		}
	}
}

func TestTouchChatIncrementsUnreadAndClearWorks(t *testing.T) {
	s, ctx := newTestStore(t)

	for i := range 3 {
		if err := s.TouchChat(ctx, "a@s.whatsapp.net", "Alice", "hello", int64(10+i), false, true); err != nil {
			t.Fatalf("touch: %v", err)
		}
	}
	c, err := s.GetChat(ctx, "a@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get chat: %v", err)
	}
	if c.Unread != 3 {
		t.Fatalf("want unread 3, got %d", c.Unread)
	}
	if c.LastMessage != "hello" || c.UpdatedAt != 12 {
		t.Fatalf("unexpected preview state: %+v", c)
	}

	if err := s.ClearUnread(ctx, "a@s.whatsapp.net"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	c, _ = s.GetChat(ctx, "a@s.whatsapp.net")
	if c.Unread != 0 {
		t.Fatalf("want unread 0, got %d", c.Unread)
	}
}
