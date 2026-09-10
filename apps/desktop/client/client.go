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
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
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

// GetChats loads the chat list off the main loop.
func (c *Client) GetChats(ctx context.Context, onDone func([]*zchatv1.Chat, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.GetChats(callCtx, &zchatv1.GetChatsRequest{})
		idle(func() { onDone(resp.GetChats(), err) })
	}()
}

// GetMessages loads a chat's messages off the main loop.
func (c *Client) GetMessages(ctx context.Context, chatJID string, onDone func(string, []*zchatv1.Message, error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		resp, err := c.svc.GetMessages(callCtx, &zchatv1.GetMessagesRequest{ChatJid: chatJID})
		idle(func() { onDone(chatJID, resp.GetMessages(), err) })
	}()
}

// SendMessage sends text off the main loop.
func (c *Client) SendMessage(ctx context.Context, chatJID, body string, onDone func(error)) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		_, err := c.svc.SendMessage(callCtx, &zchatv1.SendMessageRequest{ChatJid: chatJID, Body: body})
		idle(func() { onDone(err) })
	}()
}

func idle(f func()) {
	glib.IdleAdd(f)
}
