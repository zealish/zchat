package wa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	daemonstore "github.com/zealish/zchat/apps/daemon/store"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// MuteForever is the muted_until value for a chat muted with no end date.
const MuteForever = -1

// UpdateChat toggles a chat's pinned, archived and muted flags. Nil fields are
// left untouched. The local row is updated first so the UI reacts immediately;
// the app-state patch then propagates the change to the user's other devices.
func (s *Session) UpdateChat(ctx context.Context, chatJID string, pinned, archived *bool, mutedUntil *int64) (*zchatv1.Chat, error) {
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, fmt.Errorf("parse jid %q: %w", chatJID, err)
	}

	if err := s.store.SetChatFlags(ctx, chatJID, pinned, archived, mutedUntil); err != nil {
		return nil, err
	}
	chat, err := s.store.GetChat(ctx, chatJID)
	if err != nil {
		return nil, err
	}
	out := ToProtoChat(chat)
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ChatUpdated{ChatUpdated: out}})

	// A missing client only costs the other devices their update; the local
	// state is already correct.
	client := s.currentClient()
	if client == nil || !client.IsLoggedIn() {
		return out, nil
	}

	lastTS := time.Unix(chat.UpdatedAt, 0)
	patches := make([]appstate.PatchInfo, 0, 3)
	if pinned != nil {
		patches = append(patches, appstate.BuildPin(jid, *pinned))
	}
	if archived != nil {
		patches = append(patches, appstate.BuildArchive(jid, *archived, lastTS, nil))
	}
	if mutedUntil != nil {
		patches = append(patches, muteePatch(jid, *mutedUntil))
	}
	for _, patch := range patches {
		if err := client.SendAppState(ctx, patch); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("send app state patch")
		}
	}
	return out, nil
}

// maxReadReceipts caps how many messages one MarkRead call acknowledges. The
// unread counter can be huge after a long absence, and WhatsApp only needs the
// newest messages to consider the chat read.
const maxReadReceipts = 50

// MarkRead acknowledges the newest count incoming messages of a chat so the
// user's phone and the sender both see them as read.
func (s *Session) MarkRead(ctx context.Context, chatJID string, count int) error {
	client := s.currentClient()
	if client == nil || !client.IsLoggedIn() {
		return nil
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse jid %q: %w", chatJID, err)
	}
	if count <= 0 || count > maxReadReceipts {
		count = maxReadReceipts
	}
	refs, err := s.store.RecentIncoming(ctx, chatJID, count)
	if err != nil {
		return err
	}

	// Receipts are per sender, so group the batch by author before sending.
	bySender := make(map[types.JID][]types.MessageID)
	for _, ref := range refs {
		sender, err := types.ParseJID(ref.Sender)
		if err != nil {
			continue
		}
		bySender[sender.ToNonAD()] = append(bySender[sender.ToNonAD()], types.MessageID(ref.ID))
	}

	now := time.Now()
	for sender, ids := range bySender {
		if err := client.MarkRead(ctx, ids, now, jid, sender); err != nil {
			return fmt.Errorf("mark read %s: %w", chatJID, err)
		}
	}
	return nil
}

func muteePatch(jid types.JID, mutedUntil int64) appstate.PatchInfo {
	switch {
	case mutedUntil == 0:
		return appstate.BuildMuteAbs(jid, false, nil)
	case mutedUntil < 0:
		return appstate.BuildMuteAbs(jid, true, nil)
	default:
		endMS := mutedUntil * 1000
		return appstate.BuildMuteAbs(jid, true, &endMS)
	}
}

// DeleteMessage removes a message from the local store. When revoke is set and
// the message was sent by the user, it is also revoked for everyone.
func (s *Session) DeleteMessage(ctx context.Context, messageID string, revoke bool) (chatJID string, err error) {
	row, err := s.store.GetMessage(ctx, messageID)
	if err != nil {
		return "", err
	}

	if revoke {
		if !row.Outgoing {
			return "", errors.New("only your own messages can be revoked")
		}
		client := s.currentClient()
		if client == nil {
			return "", errors.New("session not started")
		}
		jid, err := types.ParseJID(row.ChatJID)
		if err != nil {
			return "", fmt.Errorf("parse jid %q: %w", row.ChatJID, err)
		}
		if _, err := client.SendMessage(ctx, jid, client.BuildRevoke(jid, types.EmptyJID, messageID)); err != nil {
			return "", fmt.Errorf("revoke message: %w", err)
		}
	}

	if err := s.store.DeleteMessage(ctx, messageID); err != nil {
		return "", err
	}
	if err := s.store.RefreshLastMessage(ctx, row.ChatJID); err != nil {
		s.log.Warn().Err(err).Str("chat", row.ChatJID).Msg("refresh preview after delete")
	}

	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageDeleted{
		MessageDeleted: &zchatv1.MessageDeleted{Id: messageID, ChatJid: row.ChatJID},
	}})
	s.publishChat(ctx, row.ChatJID)
	return row.ChatJID, nil
}

// ForwardMessage re-sends an existing message to another chat. Media is
// forwarded by replaying the original encrypted payload, so nothing has to be
// downloaded and re-uploaded.
func (s *Session) ForwardMessage(ctx context.Context, messageID, toChatJID string) (*zchatv1.Message, error) {
	client := s.currentClient()
	if client == nil {
		return nil, errors.New("session not started")
	}

	source, err := s.store.GetMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	jid, err := types.ParseJID(toChatJID)
	if err != nil {
		return nil, fmt.Errorf("parse jid %q: %w", toChatJID, err)
	}

	payload, err := forwardPayload(ctx, s.store, source)
	if err != nil {
		return nil, err
	}

	id := client.GenerateMessageID()
	sender := ""
	if client.Store != nil && client.Store.ID != nil {
		sender = client.Store.ID.ToNonAD().String()
	}
	row := daemonstore.Message{
		ID:         id,
		ChatJID:    jid.ToNonAD().String(),
		Sender:     sender,
		SenderName: pushNameOf(client.Store),
		Body:       source.Body,
		Type:       source.Type,
		Timestamp:  time.Now().Unix(),
		Outgoing:   true,
		Status:     daemonstore.StatusPending,
		Forwarded:  true,
		Media:      source.Media,
	}
	if err := s.store.InsertMessage(ctx, row); err != nil {
		return nil, err
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageReceived{MessageReceived: toProtoMessage(row)}})

	resp, sendErr := client.SendMessage(ctx, jid, payload, whatsmeow.SendRequestExtra{ID: id})
	if sendErr != nil {
		if err := s.store.UpdateMessageStatus(ctx, id, daemonstore.StatusFailed); err != nil {
			s.log.Warn().Err(err).Msg("mark forward failed")
		}
		row.Status = daemonstore.StatusFailed
		s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: toProtoMessage(row)}})
		return nil, fmt.Errorf("forward message: %w", sendErr)
	}

	s.finishSend(ctx, &row, jid, resp)
	out := toProtoMessage(row)
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: out}})
	s.publishChat(ctx, row.ChatJID)
	return out, nil
}

// ReactMessage sends or removes a reaction on an existing message.
func (s *Session) ReactMessage(ctx context.Context, messageID, emoji string) (*zchatv1.Message, error) {
	row, err := s.store.GetMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	client := s.currentClient()
	if client == nil {
		return nil, errors.New("session not started")
	}
	chat, err := types.ParseJID(row.ChatJID)
	if err != nil {
		return nil, err
	}
	sender, err := types.ParseJID(row.Sender)
	if err != nil {
		return nil, fmt.Errorf("invalid original sender %q: %w", row.Sender, err)
	}
	if sender.IsEmpty() {
		return nil, fmt.Errorf("invalid original sender %q", row.Sender)
	}
	payload := client.BuildReaction(chat, sender, types.MessageID(row.ID), emoji)
	if _, err := client.SendMessage(ctx, chat, payload); err != nil {
		return nil, err
	}
	if err := s.store.SetMessageReaction(ctx, messageID, emoji); err != nil {
		return nil, err
	}
	row.Reaction = emoji
	out := toProtoMessage(row)
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: out}})
	return out, nil
}

// forwardPayload rebuilds the outgoing message for a forward, marking it as
// forwarded so the recipient sees the usual label.
func forwardPayload(ctx context.Context, st *daemonstore.Store, source daemonstore.Message) (*waE2E.Message, error) {
	if source.Media == nil {
		return &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String(source.Body),
				ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(1)},
			},
		}, nil
	}

	// The stored payload is the original waE2E.Message, whose media pointers
	// (URL, keys, digests) stay valid for re-sending.
	raw, _, err := st.MediaPayload(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("original media is no longer available")
	}
	var msg waE2E.Message
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("decode stored media payload: %w", err)
	}
	markForwarded(&msg)
	return &msg, nil
}

// markForwarded sets the forwarded flag on whichever attachment the message
// carries.
func markForwarded(msg *waE2E.Message) {
	ctxInfo := &waE2E.ContextInfo{IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(1)}
	switch {
	case msg.GetImageMessage() != nil:
		msg.ImageMessage.ContextInfo = ctxInfo
	case msg.GetVideoMessage() != nil:
		msg.VideoMessage.ContextInfo = ctxInfo
	case msg.GetAudioMessage() != nil:
		msg.AudioMessage.ContextInfo = ctxInfo
	case msg.GetDocumentMessage() != nil:
		msg.DocumentMessage.ContextInfo = ctxInfo
	case msg.GetStickerMessage() != nil:
		msg.StickerMessage.ContextInfo = ctxInfo
	}
}

// onAppStateChat applies a pin/archive/mute change made on another device.
func (s *Session) onAppStateChat(ctx context.Context, jid types.JID, pinned, archived *bool, mutedUntil *int64) {
	chatJID := jid.ToNonAD().String()
	if err := s.store.SetChatFlags(ctx, chatJID, pinned, archived, mutedUntil); err != nil {
		if !errors.Is(err, daemonstore.ErrNotFound) {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("apply app state chat change")
		}
		return
	}
	s.publishChat(ctx, chatJID)
}

// onDeleteForMe applies a message deletion made on another device.
func (s *Session) onDeleteForMe(ctx context.Context, e *events.DeleteForMe) {
	chatJID := e.ChatJID.ToNonAD().String()
	if err := s.store.DeleteMessage(ctx, e.MessageID); err != nil {
		if !errors.Is(err, daemonstore.ErrNotFound) {
			s.log.Warn().Err(err).Str("id", e.MessageID).Msg("delete message for me")
		}
		return
	}
	if err := s.store.RefreshLastMessage(ctx, chatJID); err != nil {
		s.log.Warn().Err(err).Str("chat", chatJID).Msg("refresh preview after remote delete")
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageDeleted{
		MessageDeleted: &zchatv1.MessageDeleted{Id: e.MessageID, ChatJid: chatJID},
	}})
	s.publishChat(ctx, chatJID)
}

// onRevoke drops a message another participant revoked for everyone.
func (s *Session) onRevoke(ctx context.Context, chatJID types.JID, messageID string) {
	chat := chatJID.ToNonAD().String()
	if err := s.store.DeleteMessage(ctx, messageID); err != nil {
		if !errors.Is(err, daemonstore.ErrNotFound) {
			s.log.Warn().Err(err).Str("id", messageID).Msg("delete revoked message")
		}
		return
	}
	if err := s.store.RefreshLastMessage(ctx, chat); err != nil {
		s.log.Warn().Err(err).Str("chat", chat).Msg("refresh preview after revoke")
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageDeleted{
		MessageDeleted: &zchatv1.MessageDeleted{Id: messageID, ChatJid: chat},
	}})
	s.publishChat(ctx, chat)
}
