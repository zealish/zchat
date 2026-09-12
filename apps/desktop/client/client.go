// Package client wraps the daemon's gRPC stubs and marshals every result back
// onto the GTK main loop.
//
// GTK is not thread safe: no callback in this package may touch a widget from a
// background goroutine, so every result is delivered through glib.IdleAdd.
package client

import (
	"context"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"google.golang.org/grpc"

	"github.com/zealish/zchat/packages/ipc"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

const callTimeout = 10 * time.Second

// Client is a daemon connection bound to the UI thread.
type Client struct {
	conn *grpc.ClientConn
	svc  zchatv1.ChatServiceClient
}

// Dial connects to the daemon socket.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	conn, err := ipc.Dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, svc: zchatv1.NewChatServiceClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// StreamEvents pumps daemon events onto the main loop until ctx is cancelled,
// reconnecting after transport errors.
func (c *Client) StreamEvents(ctx context.Context, onEvent func(*zchatv1.Event), onError func(error)) {
	go func() {
		delay := 2 * time.Second
		for ctx.Err() == nil {
			stream, err := c.svc.StreamEvents(ctx, &zchatv1.StreamEventsRequest{})
			if err == nil {
				err = receiveLoop(stream, onEvent)
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				idle(func() { onError(err) })
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < 60*time.Second {
				delay *= 2
				if delay > 60*time.Second {
					delay = 60 * time.Second
				}
			}
		}
	}()
}

func receiveLoop(stream zchatv1.ChatService_StreamEventsClient, onEvent func(*zchatv1.Event)) error {
	for {
		evt, err := stream.Recv()
		if err != nil {
			return err
		}
		e := evt
		idle(func() { onEvent(e) })
	}
}

// GetChats loads a chat list off the main loop. archived selects the archived
// list instead of the active one.
func (c *Client) GetChats(ctx context.Context, archived bool, onDone func(bool, []*zchatv1.Chat, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.GetChats(callCtx, &zchatv1.GetChatsRequest{Archived: archived})
		var chats []*zchatv1.Chat
		if resp != nil {
			chats = resp.GetChats()
		}
		idle(func() { onDone(archived, chats, err) })
	}()
}

// GetContacts loads synced contacts off the main loop.
func (c *Client) GetContacts(ctx context.Context, onDone func([]*zchatv1.Contact, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		resp, err := c.svc.GetContacts(callCtx, &zchatv1.GetContactsRequest{})
		var contacts []*zchatv1.Contact
		if resp != nil {
			contacts = resp.GetContacts()
		}
		idle(func() { onDone(contacts, err) })
	}()
}

// StartChat creates or opens a direct conversation without sending a message.
func (c *Client) StartChat(ctx context.Context, recipient string, onDone func(*zchatv1.Chat, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		resp, err := c.svc.StartChat(callCtx, &zchatv1.StartChatRequest{Recipient: recipient})
		var chat *zchatv1.Chat
		if resp != nil {
			chat = resp.GetChat()
		}
		idle(func() { onDone(chat, err) })
	}()
}

// GetMessages loads a page of chat messages off the main loop.
func (c *Client) GetMessages(ctx context.Context, chatJID string, before int64, onDone func(string, []*zchatv1.Message, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.GetMessages(callCtx, &zchatv1.GetMessagesRequest{ChatJid: chatJID, BeforeTimestamp: before})
		var messages []*zchatv1.Message
		if resp != nil {
			messages = resp.GetMessages()
		}
		idle(func() { onDone(chatJID, messages, err) })
	}()
}

// SendMessage sends text off the main loop. quotedID, when set, makes the
// message a reply.
func (c *Client) SendMessage(ctx context.Context, chatJID, body, quotedID string, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		_, err := c.svc.SendMessage(callCtx, &zchatv1.SendMessageRequest{
			ChatJid:         chatJID,
			Body:            body,
			QuotedMessageId: quotedID,
		})
		idle(func() { onDone(err) })
	}()
}

// SendMedia uploads a local file as an attachment off the main loop. Uploads
// can far outlast a regular call, so they use the caller's context directly.
func (c *Client) SendMedia(ctx context.Context, chatJID, filePath, caption, quotedID string, onDone func(error)) {
	go func() {
		_, err := c.svc.SendMedia(ctx, &zchatv1.SendMediaRequest{
			ChatJid:         chatJID,
			FilePath:        filePath,
			Caption:         caption,
			QuotedMessageId: quotedID,
		})
		idle(func() { onDone(err) })
	}()
}

// ForwardMessage re-sends a message to another chat off the main loop.
func (c *Client) ForwardMessage(ctx context.Context, messageID, toChatJID string, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		_, err := c.svc.ForwardMessage(callCtx, &zchatv1.ForwardMessageRequest{
			MessageId: messageID,
			ToChatJid: toChatJID,
		})
		idle(func() { onDone(err) })
	}()
}

// DeleteMessage removes a message off the main loop, revoking it for everyone
// when revoke is set.
func (c *Client) DeleteMessage(ctx context.Context, messageID string, revoke bool, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, err := c.svc.DeleteMessage(callCtx, &zchatv1.DeleteMessageRequest{MessageId: messageID, Revoke: revoke})
		idle(func() { onDone(err) })
	}()
}

func (c *Client) ReactMessage(ctx context.Context, messageID, emoji string, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, err := c.svc.ReactMessage(callCtx, &zchatv1.ReactMessageRequest{MessageId: messageID, Emoji: emoji})
		idle(func() { onDone(err) })
	}()
}

// UpdateChat toggles chat flags off the main loop. Nil fields are left
// untouched by the daemon.
func (c *Client) UpdateChat(ctx context.Context, chatJID string, pinned, archived *bool, mutedUntil *int64, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		_, err := c.svc.UpdateChat(callCtx, &zchatv1.UpdateChatRequest{
			ChatJid:    chatJID,
			Pinned:     pinned,
			Archived:   archived,
			MutedUntil: mutedUntil,
		})
		idle(func() { onDone(err) })
	}()
}

// SearchChats queries the daemon off the main loop.
func (c *Client) SearchChats(ctx context.Context, query string, onDone func(string, []*zchatv1.Chat, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.SearchChats(callCtx, &zchatv1.SearchChatsRequest{Query: query})
		var chats []*zchatv1.Chat
		if resp != nil {
			chats = resp.GetChats()
		}
		idle(func() { onDone(query, chats, err) })
	}()
}

// SearchMessages queries message bodies off the main loop. chatJID scopes the
// search to one conversation; empty searches every chat.
func (c *Client) SearchMessages(ctx context.Context, query, chatJID string, onDone func(string, []*zchatv1.Message, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.SearchMessages(callCtx, &zchatv1.SearchMessagesRequest{Query: query, ChatJid: chatJID})
		var messages []*zchatv1.Message
		if resp != nil {
			messages = resp.GetMessages()
		}
		idle(func() { onDone(query, messages, err) })
	}()
}

// DownloadMedia fetches an attachment off the main loop. Media transfers can
// far outlast a regular call, so they use the caller's context directly.
func (c *Client) DownloadMedia(ctx context.Context, messageID string, onDone func(*zchatv1.Message, error)) {
	go func() {
		resp, err := c.svc.DownloadMedia(ctx, &zchatv1.DownloadMediaRequest{MessageId: messageID})
		var msg *zchatv1.Message
		if resp != nil {
			msg = resp.GetMessage()
		}
		idle(func() { onDone(msg, err) })
	}()
}

// RetryMessage resends a failed outgoing text message without blocking GTK.
func (c *Client) RetryMessage(ctx context.Context, messageID string, onDone func(*zchatv1.Message, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		resp, err := c.svc.RetryMessage(callCtx, &zchatv1.RetryMessageRequest{MessageId: messageID})
		var msg *zchatv1.Message
		if resp != nil {
			msg = resp.GetMessage()
		}
		idle(func() { onDone(msg, err) })
	}()
}

// RetryMedia resends a failed outgoing attachment without blocking GTK.
func (c *Client) RetryMedia(ctx context.Context, messageID string, onDone func(*zchatv1.Message, error)) {
	go func() {
		resp, err := c.svc.RetryMedia(ctx, &zchatv1.RetryMessageRequest{MessageId: messageID})
		var msg *zchatv1.Message
		if resp != nil {
			msg = resp.GetMessage()
		}
		idle(func() { onDone(msg, err) })
	}()
}

// SetPresence reports typing and online state off the main loop. It is fired
// on every keystroke burst, so failures are dropped rather than reported.
func (c *Client) SetPresence(ctx context.Context, chatJID string, typing, available bool) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		c.svc.SetPresence(callCtx, &zchatv1.SetPresenceRequest{
			ChatJid:   chatJID,
			Typing:    typing,
			Available: available,
		})
	}()
}

// Logout unlinks the device off the main loop.
func (c *Client) Logout(ctx context.Context, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, err := c.svc.Logout(callCtx, &zchatv1.LogoutRequest{})
		idle(func() { onDone(err) })
	}()
}

func (c *Client) GetChatInfo(ctx context.Context, chatJID string, onDone func(*zchatv1.GetChatInfoResponse, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		resp, err := c.svc.GetChatInfo(callCtx, &zchatv1.GetChatInfoRequest{ChatJid: chatJID})
		idle(func() { onDone(resp, err) })
	}()
}

// GetProfilePicture resolves an avatar path off the main loop. The daemon may
// have to download the image, so it gets the longer media timeout.
func (c *Client) GetProfilePicture(ctx context.Context, jid string, onDone func(string, string, error)) {
	go func() {
		resp, err := c.svc.GetProfilePicture(ctx, &zchatv1.GetProfilePictureRequest{Jid: jid})
		idle(func() { onDone(jid, resp.GetPath(), err) })
	}()
}

func idle(f func()) {
	glib.IdleAdd(f)
}
