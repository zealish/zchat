package wa

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	daemonstore "github.com/zealish/zchat/apps/daemon/store"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// historyWorker serialises history-sync processing so the whatsmeow event loop
// is never blocked and writes stay ordered against the single SQLite handle.
func (s *Session) historyWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-s.historyCh:
			s.processHistorySync(ctx, evt)
		}
	}
}

func (s *Session) processHistorySync(ctx context.Context, evt *events.HistorySync) {
	client := s.currentClient()
	if client == nil {
		return
	}

	conversations := evt.Data.GetConversations()
	s.log.Info().
		Str("sync_type", evt.Data.GetSyncType().String()).
		Int("conversations", len(conversations)).
		Msg("processing history sync")

	for _, conv := range conversations {
		jid, err := types.ParseJID(conv.GetID())
		if err != nil {
			s.log.Debug().Str("id", conv.GetID()).Msg("skipping unparseable conversation")
			continue
		}
		if !supportedChat(jid) {
			continue
		}

		updatedAt := int64(conv.GetConversationTimestamp())
		if updatedAt == 0 {
			updatedAt = int64(conv.GetLastMsgTimestamp())
		}
		isGroup := jid.Server == types.GroupServer

		chatJID := jid.ToNonAD().String()
		chat := daemonstore.Chat{
			JID:        chatJID,
			Name:       conv.GetName(),
			Unread:     int32(conv.GetUnreadCount()),
			Archived:   conv.GetArchived(),
			Pinned:     conv.GetPinned() > 0,
			UpdatedAt:  updatedAt,
			IsGroup:    isGroup,
			MutedUntil: muteUntilFrom(conv.GetMuteEndTime() > 0, int64(conv.GetMuteEndTime())),
		}
		if chat.Name == "" {
			chat.Name = s.resolveChatName(ctx, jid, isGroup)
		}
		if err := s.store.UpsertChat(ctx, chat); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("upsert conversation")
			continue
		}

		batch := make([]daemonstore.Message, 0, len(conv.GetMessages()))
		for _, hsMsg := range conv.GetMessages() {
			parsed, err := client.ParseWebMessage(jid, hsMsg.GetMessage())
			if err != nil {
				s.log.Debug().Err(err).Msg("parse history message")
				continue
			}
			if row, ok := s.messageRow(ctx, parsed.Info, parsed.Message); ok {
				batch = append(batch, row)
			}
		}
		if err := s.store.InsertMessages(ctx, batch); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("insert history messages")
		}

		if err := s.store.RefreshLastMessage(ctx, chatJID); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("refresh preview")
		}
		// The unread counter must mirror the phone, so restore the synced value
		// after persistMessage's per-message bookkeeping.
		if err := s.store.UpsertChat(ctx, chat); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("restore unread count")
		}

		stored, err := s.store.GetChat(ctx, chatJID)
		if err != nil {
			continue
		}
		s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ChatUpdated{ChatUpdated: ToProtoChat(stored)}})
	}
}
