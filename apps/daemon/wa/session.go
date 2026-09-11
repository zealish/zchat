// Package wa owns the WhatsMeow client and translates its events into store
// writes and IPC broadcasts.
package wa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	daemonstore "github.com/zealish/zchat/apps/daemon/store"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// Publisher receives events destined for connected desktop clients.
type Publisher interface {
	Publish(evt *zchatv1.Event)
}

// Session wraps a whatsmeow client bound to the ZChat store.
type Session struct {
	log       zerolog.Logger
	waLog     waLog.Logger
	store     *daemonstore.Store
	container *sqlstore.Container
	pub       Publisher

	client *whatsmeow.Client

	mu    sync.RWMutex
	state *zchatv1.ConnectionState
	qr    *zchatv1.QRUpdate

	historyCh chan *events.HistorySync
	startOnce sync.Once
}

// New builds a session sharing the daemon's SQLite handle with whatsmeow.
func New(ctx context.Context, db *sql.DB, st *daemonstore.Store, log zerolog.Logger, waLogger waLog.Logger, pub Publisher) (*Session, error) {
	container := sqlstore.NewWithDB(db, "sqlite", waLogger.Sub("sqlstore"))
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}

	s := &Session{
		log:       log,
		waLog:     waLogger,
		store:     st,
		container: container,
		pub:       pub,
		state:     &zchatv1.ConnectionState{Status: zchatv1.ConnectionStatus_CONNECTION_STATUS_DISCONNECTED},
		historyCh: make(chan *events.HistorySync, 8),
	}
	return s, nil
}

// Start connects the client, arming QR pairing when no device is stored.
func (s *Session) Start(ctx context.Context) error {
	device, err := s.container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}

	client := whatsmeow.NewClient(device, s.waLog.Sub("client"))
	client.AddEventHandler(s.handleEvent)

	s.mu.Lock()
	s.client = client
	s.mu.Unlock()

	s.startOnce.Do(func() { go s.historyWorker(context.WithoutCancel(ctx)) })

	if device.ID == nil {
		// GetQRChannel must be called before Connect.
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("open qr channel: %w", err)
		}
		s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_LOGGED_OUT, "")
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		go s.consumeQR(qrChan)
		return nil
	}

	s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_CONNECTING, "")
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

// Stop disconnects the client.
func (s *Session) Stop() {
	if c := s.currentClient(); c != nil {
		c.Disconnect()
	}
}

// State returns the last known connection state.
func (s *Session) State() *zchatv1.ConnectionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return proto.Clone(s.state).(*zchatv1.ConnectionState)
}

// PendingQR returns the current unexpired QR code, or nil.
func (s *Session) PendingQR() *zchatv1.QRUpdate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.qr == nil || s.qr.GetExpiresAt() <= time.Now().Unix() {
		return nil
	}
	return proto.Clone(s.qr).(*zchatv1.QRUpdate)
}

// Logout unlinks the device and re-arms QR pairing.
func (s *Session) Logout(ctx context.Context) error {
	client := s.currentClient()
	if client == nil {
		return errors.New("session not started")
	}
	if err := client.Logout(ctx); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	s.restart()
	return nil
}

func (s *Session) currentClient() *whatsmeow.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

func (s *Session) consumeQR(ch <-chan whatsmeow.QRChannelItem) {
	for item := range ch {
		switch item.Event {
		case "code":
			qr := &zchatv1.QRUpdate{Code: item.Code, ExpiresAt: time.Now().Add(item.Timeout).Unix()}
			s.mu.Lock()
			s.qr = qr
			s.mu.Unlock()
			s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_QrUpdated{QrUpdated: qr}})
		case "success":
			s.mu.Lock()
			s.qr = nil
			s.mu.Unlock()
			s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_CONNECTING, "")
		case "error":
			msg := "pairing failed"
			if item.Error != nil {
				msg = item.Error.Error()
			}
			s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_LOGGED_OUT, msg)
		default:
			s.log.Warn().Str("event", item.Event).Msg("qr channel terminated")
			s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_LOGGED_OUT, item.Event)
		}
	}
}

func (s *Session) setState(status zchatv1.ConnectionStatus, errMsg string) {
	s.mu.Lock()
	state := &zchatv1.ConnectionState{Status: status, Error: errMsg}
	if s.client != nil && s.client.Store != nil {
		if id := s.client.Store.ID; id != nil {
			state.OwnJid = id.String()
		}
		state.PushName = s.client.Store.PushName
	}
	s.state = state
	s.mu.Unlock()

	s.pub.Publish(&zchatv1.Event{
		Payload: &zchatv1.Event_ConnectionState{ConnectionState: proto.Clone(state).(*zchatv1.ConnectionState)},
	})
}

func (s *Session) handleEvent(evt any) {
	ctx := context.Background()

	switch e := evt.(type) {
	case *events.Connected:
		s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_CONNECTED, "")
		go s.backfillChatNames(ctx)
	case *events.Disconnected:
		s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_DISCONNECTED, "")
	case *events.LoggedOut:
		s.log.Warn().Str("reason", e.Reason.String()).Msg("logged out by server")
		s.setState(zchatv1.ConnectionStatus_CONNECTION_STATUS_LOGGED_OUT, e.Reason.String())
		s.restart()
	case *events.Message:
		s.onMessage(ctx, e)
	case *events.Receipt:
		s.onReceipt(ctx, e)
	case *events.HistorySync:
		select {
		case s.historyCh <- e:
		default:
			s.log.Warn().Msg("history sync queue full, processing inline")
			s.processHistorySync(ctx, e)
		}
	case *events.Pin:
		pinned := e.Action.GetPinned()
		s.onAppStateChat(ctx, e.JID, &pinned, nil, nil)
	case *events.Archive:
		archived := e.Action.GetArchived()
		s.onAppStateChat(ctx, e.JID, nil, &archived, nil)
	case *events.Mute:
		muted := muteUntilFrom(e.Action.GetMuted(), e.Action.GetMuteEndTimestamp())
		s.onAppStateChat(ctx, e.JID, nil, nil, &muted)
	case *events.DeleteForMe:
		s.onDeleteForMe(ctx, e)
	case *events.OfflineSyncCompleted:
		s.log.Info().Int("count", e.Count).Msg("offline sync completed")
	}
}

func (s *Session) restart() {
	go func() {
		ctx := context.Background()
		if c := s.currentClient(); c != nil {
			c.Disconnect()
		}
		if err := s.Start(ctx); err != nil {
			s.log.Error().Err(err).Msg("restart session")
		}
	}()
}

// backfillChatNames repairs chats stored with a bare phone number as their
// name, which happens when history sync outruns the contact sync.
func (s *Session) backfillChatNames(ctx context.Context) {
	chats, err := s.store.ChatsNamedByNumber(ctx)
	if err != nil {
		s.log.Warn().Err(err).Msg("scan chats for name backfill")
		return
	}

	fixed := 0
	for _, chatJID := range chats {
		jid, err := types.ParseJID(chatJID)
		if err != nil {
			continue
		}
		name := s.contactName(ctx, jid)
		if name == "" {
			name = s.displayNumber(ctx, jid)
		}
		if name == "" || name == jid.User {
			continue
		}
		if err := s.store.SetChatName(ctx, chatJID, name); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("backfill chat name")
			continue
		}
		fixed++
		if chat, err := s.store.GetChat(ctx, chatJID); err == nil {
			s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ChatUpdated{ChatUpdated: ToProtoChat(chat)}})
		}
	}
	if fixed > 0 {
		s.log.Info().Int("fixed", fixed).Int("scanned", len(chats)).Msg("chat names backfilled")
	}
}

func (s *Session) onMessage(ctx context.Context, e *events.Message) {
	if !supportedChat(e.Info.Chat) {
		return
	}
	// A revoke arrives as a regular message carrying a protocol payload; it
	// removes the target message rather than adding one. REVOKE is the zero
	// value of the type enum, so the payload itself must be checked first.
	if protoMsg := e.Message.GetProtocolMessage(); protoMsg != nil && protoMsg.GetType() == waE2E.ProtocolMessage_REVOKE {
		s.onRevoke(ctx, e.Info.Chat, protoMsg.GetKey().GetID())
		return
	}
	msg, ok := s.persistMessage(ctx, e.Info, e.Message)
	if !ok {
		return
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageReceived{MessageReceived: msg}})
	s.publishChat(ctx, msg.GetChatJid())
}

// muteUntilFrom converts WhatsApp's mute end timestamp (milliseconds, 0 for
// indefinite) into the store's representation.
func muteUntilFrom(muted bool, endTimestampMS int64) int64 {
	if !muted {
		return 0
	}
	if endTimestampMS <= 0 {
		return MuteForever
	}
	return endTimestampMS / 1000
}

// supportedChat reports whether a chat is a regular direct or group
// conversation. Status updates, broadcast lists and newsletters are ignored.
func supportedChat(jid types.JID) bool {
	switch jid.Server {
	case types.GroupServer, types.DefaultUserServer, types.LegacyUserServer, types.HiddenUserServer:
		return jid.User != "status"
	default:
		return false
	}
}

func (s *Session) onReceipt(ctx context.Context, e *events.Receipt) {
	var status string
	switch e.Type {
	case types.ReceiptTypeDelivered:
		status = daemonstore.StatusDelivered
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		status = daemonstore.StatusRead
	default:
		return
	}

	for _, id := range e.MessageIDs {
		if err := s.store.UpdateMessageStatus(ctx, string(id), status); err != nil {
			if !errors.Is(err, daemonstore.ErrNotFound) {
				s.log.Warn().Err(err).Str("id", string(id)).Msg("update receipt status")
			}
			continue
		}
		row, err := s.store.GetMessage(ctx, string(id))
		if err != nil {
			continue
		}
		s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: toProtoMessage(row)}})
	}
}

// messageRow builds a message row from a parsed message. Messages carrying
// neither text nor a supported attachment are skipped.
func (s *Session) messageRow(ctx context.Context, info types.MessageInfo, msg *waE2E.Message) (daemonstore.Message, bool) {
	body := extractText(msg)
	kind, media := extractMedia(msg)
	if body == "" && media == nil {
		s.log.Debug().Str("type", info.Type).Str("id", info.ID).Msg("skipping unsupported message")
		return daemonstore.Message{}, false
	}
	if media == nil {
		kind = TypeText
	} else if body == "" {
		body = mediaPreview(kind, media)
	}

	status := daemonstore.StatusDelivered
	if info.IsFromMe {
		status = daemonstore.StatusSent
	}

	// Only group messages show a sender label. History sync omits PushName, so
	// fall back to the contact store.
	senderName := info.PushName
	if senderName == "" && !info.IsFromMe && info.Chat.Server == types.GroupServer {
		senderName = s.contactName(ctx, info.Sender)
	}

	ctxInfo := messageContext(msg)
	return daemonstore.Message{
		ID:         info.ID,
		ChatJID:    info.Chat.ToNonAD().String(),
		Sender:     info.Sender.ToNonAD().String(),
		SenderName: senderName,
		Body:       body,
		Type:       kind,
		Timestamp:  info.Timestamp.Unix(),
		Outgoing:   info.IsFromMe,
		Status:     status,
		Forwarded:  ctxInfo.GetIsForwarded(),
		Media:      media,
		Quoted:     s.quotedFrom(ctx, ctxInfo),
	}, true
}

// messageContext returns whichever ContextInfo the message carries, or nil.
func messageContext(msg *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetContextInfo()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetContextInfo()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetContextInfo()
	case msg.GetAudioMessage() != nil:
		return msg.GetAudioMessage().GetContextInfo()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetContextInfo()
	case msg.GetStickerMessage() != nil:
		return msg.GetStickerMessage().GetContextInfo()
	default:
		return nil
	}
}

// quotedFrom builds the reply snapshot from a message's context, or nil when
// the message is not a reply.
func (s *Session) quotedFrom(ctx context.Context, info *waE2E.ContextInfo) *daemonstore.Quoted {
	if info.GetStanzaID() == "" {
		return nil
	}

	quotedMsg := info.GetQuotedMessage()
	body := extractText(quotedMsg)
	kind, media := extractMedia(quotedMsg)
	if media == nil {
		kind = TypeText
	} else if body == "" {
		body = mediaPreview(kind, media)
	}

	sender := info.GetParticipant()
	senderName := ""
	if jid, err := types.ParseJID(sender); err == nil {
		sender = jid.ToNonAD().String()
		senderName = s.contactName(ctx, jid)
	}

	return &daemonstore.Quoted{
		ID:         info.GetStanzaID(),
		Sender:     sender,
		SenderName: senderName,
		Body:       body,
		Type:       kind,
	}
}

// persistMessage stores a live message and updates its chat row.
func (s *Session) persistMessage(ctx context.Context, info types.MessageInfo, msg *waE2E.Message) (*zchatv1.Message, bool) {
	row, ok := s.messageRow(ctx, info, msg)
	if !ok {
		return nil, false
	}

	if err := s.store.InsertMessage(ctx, row); err != nil {
		s.log.Error().Err(err).Str("id", row.ID).Msg("persist message")
		return nil, false
	}

	isGroup := info.Chat.Server == types.GroupServer
	name := s.resolveChatName(ctx, info.Chat, isGroup)
	if err := s.store.TouchChat(ctx, row.ChatJID, name, row.Body, row.Timestamp, isGroup, !info.IsFromMe); err != nil {
		s.log.Error().Err(err).Str("chat", row.ChatJID).Msg("touch chat")
	}

	return toProtoMessage(row), true
}

// resolveChatName picks the best available display name for a chat, preferring
// the stored one so history-sync names are not overwritten.
func (s *Session) resolveChatName(ctx context.Context, jid types.JID, isGroup bool) string {
	chatJID := jid.ToNonAD().String()
	if name, err := s.store.ChatName(ctx, chatJID); err == nil && name != "" && name != jid.User {
		return name
	}

	client := s.currentClient()
	if isGroup {
		if client != nil {
			if info, err := client.GetGroupInfo(ctx, jid.ToNonAD()); err == nil && info.Name != "" {
				return info.Name
			}
		}
		return jid.User
	}

	if name := s.contactName(ctx, jid); name != "" {
		return name
	}
	// A bare LID is meaningless to the user; show the phone number when the
	// mapping is known.
	return s.displayNumber(ctx, jid)
}

// displayNumber returns the phone number for a JID, resolving LIDs when
// possible and otherwise falling back to the JID user part.
func (s *Session) displayNumber(ctx context.Context, jid types.JID) string {
	if jid.Server != types.HiddenUserServer {
		return jid.User
	}
	client := s.currentClient()
	if client == nil || client.Store == nil {
		return jid.User
	}
	if pn, err := client.Store.GetAltJID(ctx, jid.ToNonAD()); err == nil && !pn.IsEmpty() && pn.User != "" {
		return pn.User
	}
	return jid.User
}

// DisplayName resolves a sender JID to a contact name, falling back to the
// phone number. Used for rows persisted before the name was resolvable.
func (s *Session) DisplayName(ctx context.Context, rawJID string) string {
	jid, err := types.ParseJID(rawJID)
	if err != nil {
		return ""
	}
	if name := s.contactName(ctx, jid); name != "" {
		return name
	}
	return s.displayNumber(ctx, jid)
}

// contactName looks up a display name for a user, following the LID/phone
// mapping in both directions since history sync addresses many chats by LID.
func (s *Session) contactName(ctx context.Context, jid types.JID) string {
	client := s.currentClient()
	if client == nil || client.Store == nil || client.Store.Contacts == nil {
		return ""
	}

	candidates := []types.JID{jid.ToNonAD()}
	if alt, err := client.Store.GetAltJID(ctx, jid.ToNonAD()); err == nil && !alt.IsEmpty() {
		candidates = append(candidates, alt.ToNonAD())
	}

	for _, candidate := range candidates {
		contact, err := client.Store.Contacts.GetContact(ctx, candidate)
		if err != nil || !contact.Found {
			continue
		}
		for _, name := range []string{contact.FullName, contact.BusinessName, contact.PushName} {
			if name != "" {
				return name
			}
		}
	}
	return ""
}

func (s *Session) publishChat(ctx context.Context, chatJID string) {
	chat, err := s.store.GetChat(ctx, chatJID)
	if err != nil {
		return
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ChatUpdated{ChatUpdated: ToProtoChat(chat)}})
}

// SendText sends a text message, recording an optimistic pending row first.
// quotedID, when set, turns the message into a reply to that message.
func (s *Session) SendText(ctx context.Context, chatJID, body, quotedID string) (*zchatv1.Message, error) {
	client := s.currentClient()
	if client == nil {
		return nil, errors.New("session not started")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, fmt.Errorf("parse jid %q: %w", chatJID, err)
	}

	payload := &waE2E.Message{Conversation: proto.String(body)}
	var quoted *daemonstore.Quoted
	if quotedID != "" {
		quoted, payload, err = s.buildReply(ctx, jid, body, quotedID)
		if err != nil {
			return nil, err
		}
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
		Body:       body,
		Type:       TypeText,
		Timestamp:  time.Now().Unix(),
		Outgoing:   true,
		Status:     daemonstore.StatusPending,
		Quoted:     quoted,
	}
	if err := s.store.InsertMessage(ctx, row); err != nil {
		return nil, err
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageReceived{MessageReceived: toProtoMessage(row)}})

	resp, sendErr := client.SendMessage(ctx, jid, payload, whatsmeow.SendRequestExtra{ID: id})
	if sendErr != nil {
		if err := s.store.UpdateMessageStatus(ctx, id, daemonstore.StatusFailed); err != nil {
			s.log.Warn().Err(err).Msg("mark message failed")
		}
		row.Status = daemonstore.StatusFailed
		s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: toProtoMessage(row)}})
		return nil, fmt.Errorf("send message: %w", sendErr)
	}

	s.finishSend(ctx, &row, jid, resp)
	out := toProtoMessage(row)
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: out}})
	s.publishChat(ctx, row.ChatJID)
	return out, nil
}

// buildReply resolves the quoted message and wraps the reply body in the
// extended text message WhatsApp expects for replies.
func (s *Session) buildReply(ctx context.Context, chat types.JID, body, quotedID string) (*daemonstore.Quoted, *waE2E.Message, error) {
	quoted, ctxInfo, err := s.replyContext(ctx, chat, quotedID)
	if err != nil {
		return nil, nil, err
	}

	payload := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(body),
			ContextInfo: ctxInfo,
		},
	}
	return quoted, payload, nil
}

// replyContext resolves the quoted message into the snapshot stored locally and
// the ContextInfo the recipient needs to render the quote.
func (s *Session) replyContext(ctx context.Context, chat types.JID, quotedID string) (*daemonstore.Quoted, *waE2E.ContextInfo, error) {
	original, err := s.store.GetMessage(ctx, quotedID)
	if err != nil {
		return nil, nil, err
	}
	if original.ChatJID != chat.ToNonAD().String() {
		return nil, nil, errors.New("quoted message belongs to another chat")
	}

	// The recipient resolves a quote by its (stanza id, participant) pair, so
	// the participant must name the original author. It is required in
	// one-on-one chats too, which is why BuildMessageKey, which deliberately
	// omits it there, is not used.
	sender, err := types.ParseJID(original.Sender)
	if err != nil {
		return nil, nil, fmt.Errorf("parse quoted sender %q: %w", original.Sender, err)
	}

	ctxInfo := &waE2E.ContextInfo{
		StanzaID:      proto.String(original.ID),
		Participant:   proto.String(sender.ToNonAD().String()),
		QuotedMessage: quotedPayload(ctx, s.store, original),
	}
	quoted := &daemonstore.Quoted{
		ID:         original.ID,
		Sender:     original.Sender,
		SenderName: original.SenderName,
		Body:       original.Body,
		Type:       original.Type,
	}
	return quoted, ctxInfo, nil
}

// quotedPayload rebuilds the message being replied to. WhatsApp renders the
// quote from this embedded copy, so replying to media needs the original
// attachment descriptor rather than its text preview.
func quotedPayload(ctx context.Context, st *daemonstore.Store, original daemonstore.Message) *waE2E.Message {
	if original.Media != nil {
		if raw, _, err := st.MediaPayload(ctx, original.ID); err == nil && len(raw) > 0 {
			var msg waE2E.Message
			if err := proto.Unmarshal(raw, &msg); err == nil {
				return &msg
			}
		}
	}
	return &waE2E.Message{Conversation: proto.String(original.Body)}
}

// finishSend records the server-assigned timestamp and sent status, and bumps
// the chat preview.
func (s *Session) finishSend(ctx context.Context, row *daemonstore.Message, jid types.JID, resp whatsmeow.SendResponse) {
	row.Timestamp = resp.Timestamp.Unix()
	row.Status = daemonstore.StatusSent
	if err := s.store.UpdateMessageTimestamp(ctx, row.ID, row.Timestamp); err != nil {
		s.log.Warn().Err(err).Msg("update sent timestamp")
	}
	if err := s.store.UpdateMessageStatus(ctx, row.ID, daemonstore.StatusSent); err != nil {
		s.log.Warn().Err(err).Msg("mark message sent")
	}
	// The row was written with our phone-number JID, but LID chats and groups
	// send under the LID. Correcting it keeps replies quoting this message
	// addressable by the recipient.
	if sender := resp.Sender.ToNonAD(); !sender.IsEmpty() && sender.String() != row.Sender {
		row.Sender = sender.String()
		if err := s.store.UpdateMessageSender(ctx, row.ID, row.Sender); err != nil {
			s.log.Warn().Err(err).Msg("record send identity")
		}
	}
	isGroup := jid.Server == types.GroupServer
	name := s.resolveChatName(ctx, jid, isGroup)
	preview := row.Body
	if row.Media != nil {
		preview = mediaPreview(row.Type, row.Media)
	}
	if err := s.store.TouchChat(ctx, row.ChatJID, name, preview, row.Timestamp, isGroup, false); err != nil {
		s.log.Warn().Err(err).Msg("touch chat after send")
	}
}

func pushNameOf(d *store.Device) string {
	if d == nil {
		return ""
	}
	return d.PushName
}

func extractText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	return strings.TrimSpace(msg.GetExtendedTextMessage().GetText())
}

func toProtoMessage(m daemonstore.Message) *zchatv1.Message {
	out := &zchatv1.Message{
		Id:         m.ID,
		ChatJid:    m.ChatJID,
		Sender:     m.Sender,
		SenderName: m.SenderName,
		Body:       m.Body,
		Type:       m.Type,
		Timestamp:  m.Timestamp,
		Outgoing:   m.Outgoing,
		Status:     statusToProto(m.Status),
		Forwarded:  m.Forwarded,
	}
	if m.Media != nil {
		out.Media = &zchatv1.MediaInfo{
			Mime:      m.Media.Mime,
			Size:      m.Media.Size,
			Filename:  m.Media.Filename,
			Caption:   m.Media.Caption,
			Path:      m.Media.Path,
			Thumbnail: m.Media.Thumbnail,
			Width:     m.Media.Width,
			Height:    m.Media.Height,
			Duration:  m.Media.Duration,
		}
	}
	if m.Quoted != nil {
		out.Quoted = &zchatv1.QuotedMessage{
			Id:         m.Quoted.ID,
			Sender:     m.Quoted.Sender,
			SenderName: m.Quoted.SenderName,
			Body:       m.Quoted.Body,
			Type:       m.Quoted.Type,
		}
	}
	return out
}

func statusToProto(status string) zchatv1.MessageStatus {
	switch status {
	case daemonstore.StatusPending:
		return zchatv1.MessageStatus_MESSAGE_STATUS_PENDING
	case daemonstore.StatusSent:
		return zchatv1.MessageStatus_MESSAGE_STATUS_SENT
	case daemonstore.StatusDelivered:
		return zchatv1.MessageStatus_MESSAGE_STATUS_DELIVERED
	case daemonstore.StatusRead:
		return zchatv1.MessageStatus_MESSAGE_STATUS_READ
	case daemonstore.StatusFailed:
		return zchatv1.MessageStatus_MESSAGE_STATUS_FAILED
	default:
		return zchatv1.MessageStatus_MESSAGE_STATUS_UNSPECIFIED
	}
}

// ToProtoChat converts a store chat row to its IPC representation.
func ToProtoChat(c daemonstore.Chat) *zchatv1.Chat {
	return &zchatv1.Chat{
		Jid:         c.JID,
		Name:        c.Name,
		Unread:      c.Unread,
		Archived:    c.Archived,
		Pinned:      c.Pinned,
		LastMessage: c.LastMessage,
		UpdatedAt:   c.UpdatedAt,
		IsGroup:     c.IsGroup,
		MutedUntil:  c.MutedUntil,
	}
}

// ToProtoMessage converts a store message row to its IPC representation.
func ToProtoMessage(m daemonstore.Message) *zchatv1.Message { return toProtoMessage(m) }
