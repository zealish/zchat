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

	got, err := s.ListChats(ctx, 10, 0, false)
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

func TestSetChatFlagsMovesChatToArchive(t *testing.T) {
	s, ctx := newTestStore(t)

	if err := s.UpsertChat(ctx, Chat{JID: "a@s.whatsapp.net", Name: "Alice", UpdatedAt: 100, Pinned: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	archived := true
	if err := s.SetChatFlags(ctx, "a@s.whatsapp.net", nil, &archived, nil); err != nil {
		t.Fatalf("archive: %v", err)
	}

	active, err := s.ListChats(ctx, 10, 0, false)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("want no active chats, got %d", len(active))
	}

	stored, err := s.GetChat(ctx, "a@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get chat: %v", err)
	}
	// Archiving unpins, matching WhatsApp's own behaviour.
	if !stored.Archived || stored.Pinned {
		t.Fatalf("want archived and unpinned, got %+v", stored)
	}

	muted := int64(-1)
	if err := s.SetChatFlags(ctx, "a@s.whatsapp.net", nil, nil, &muted); err != nil {
		t.Fatalf("mute: %v", err)
	}
	stored, _ = s.GetChat(ctx, "a@s.whatsapp.net")
	if stored.MutedUntil != -1 {
		t.Fatalf("want muted_until -1, got %d", stored.MutedUntil)
	}
}

func TestSetChatFlagsUnknownChat(t *testing.T) {
	s, ctx := newTestStore(t)

	pinned := true
	if err := s.SetChatFlags(ctx, "ghost@s.whatsapp.net", &pinned, nil, nil); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
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
