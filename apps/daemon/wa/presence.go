package wa

import (
	"context"
	"fmt"
	"sync"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// presenceTracker remembers what was last sent or subscribed so repeated
// keystrokes and chat switches do not spam the server with identical nodes.
type presenceTracker struct {
	mu         sync.Mutex
	typing     map[string]bool
	subscribed map[string]bool
	available  bool
}

func newPresenceTracker() *presenceTracker {
	return &presenceTracker{typing: make(map[string]bool), subscribed: make(map[string]bool)}
}

// reset forgets every cached state, which a reconnect invalidates.
func (p *presenceTracker) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.typing)
	clear(p.subscribed)
	p.available = false
}

// SetPresence reports the local user's activity. available marks the user
// online, which WhatsApp requires before it delivers any presence back, and
// also subscribes to the chat partner's updates. typing sends the composing or
// paused chat state for chatJID.
func (s *Session) SetPresence(ctx context.Context, chatJID string, typing, available bool) error {
	client := s.currentClient()
	if client == nil || !client.IsLoggedIn() {
		return nil
	}

	if available {
		s.presence.mu.Lock()
		alreadyAvailable := s.presence.available
		s.presence.available = true
		s.presence.mu.Unlock()
		if !alreadyAvailable {
			if err := client.SendPresence(ctx, types.PresenceAvailable); err != nil {
				return fmt.Errorf("send presence: %w", err)
			}
		}
	}

	if chatJID == "" {
		return nil
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse jid %q: %w", chatJID, err)
	}

	if available && jid.Server != types.GroupServer {
		s.subscribePresence(ctx, jid)
	}

	s.presence.mu.Lock()
	changed := s.presence.typing[chatJID] != typing
	s.presence.typing[chatJID] = typing
	s.presence.mu.Unlock()
	if !changed {
		return nil
	}

	state := types.ChatPresencePaused
	if typing {
		state = types.ChatPresenceComposing
	}
	if err := client.SendChatPresence(ctx, jid, state, types.ChatPresenceMediaText); err != nil {
		return fmt.Errorf("send chat presence %s: %w", chatJID, err)
	}
	return nil
}

// subscribePresence asks the server for a contact's online updates once per
// connection. A failure only costs the presence line in the header, so it is
// logged rather than surfaced.
func (s *Session) subscribePresence(ctx context.Context, jid types.JID) {
	key := jid.ToNonAD().String()
	s.presence.mu.Lock()
	already := s.presence.subscribed[key]
	s.presence.subscribed[key] = true
	s.presence.mu.Unlock()
	if already {
		return
	}

	if err := s.currentClient().SubscribePresence(ctx, jid.ToNonAD()); err != nil {
		s.presence.mu.Lock()
		delete(s.presence.subscribed, key)
		s.presence.mu.Unlock()
		s.log.Debug().Err(err).Str("jid", key).Msg("subscribe presence")
	}
}

// onChatPresence broadcasts a participant's typing state. The user's own state,
// echoed back from their other devices, is dropped.
func (s *Session) onChatPresence(ctx context.Context, e *events.ChatPresence) {
	if e.IsFromMe || !supportedChat(e.Chat) {
		return
	}

	sender := e.Sender.ToNonAD()
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_PresenceChanged{
		PresenceChanged: &zchatv1.PresenceUpdate{
			ChatJid:   e.Chat.ToNonAD().String(),
			UserJid:   sender.String(),
			UserName:  s.contactName(ctx, sender),
			Typing:    e.State == types.ChatPresenceComposing,
			Recording: e.Media == types.ChatPresenceMediaAudio,
		},
	}})
}

// onPresence broadcasts a contact's online/offline transition. Direct chats are
// keyed by the contact's JID, so it doubles as the chat id.
func (s *Session) onPresence(ctx context.Context, e *events.Presence) {
	from := e.From.ToNonAD()
	if !supportedChat(from) {
		return
	}

	update := &zchatv1.PresenceUpdate{
		ChatJid:  from.String(),
		UserJid:  from.String(),
		UserName: s.contactName(ctx, from),
		Online:   !e.Unavailable,
	}
	if !e.LastSeen.IsZero() {
		update.LastSeen = e.LastSeen.Unix()
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_PresenceChanged{PresenceChanged: update}})
}
