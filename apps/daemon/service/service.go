// Package service exposes the daemon's gRPC surface over the Unix socket.
package service

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/zealish/zchat/apps/daemon/store"
	"github.com/zealish/zchat/apps/daemon/wa"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

const maxPageSize = 200

// Service implements zchatv1.ChatServiceServer.
type Service struct {
	zchatv1.UnimplementedChatServiceServer

	log     zerolog.Logger
	store   *store.Store
	session *wa.Session
	broker  *Broker
}

// New builds the gRPC service.
func New(log zerolog.Logger, st *store.Store, session *wa.Session, broker *Broker) *Service {
	return &Service{log: log, store: st, session: session, broker: broker}
}

// GetChats returns a page of conversations. Archived chats are a separate list
// and are only returned when explicitly requested.
func (s *Service) GetChats(ctx context.Context, req *zchatv1.GetChatsRequest) (*zchatv1.GetChatsResponse, error) {
	limit := int(req.GetLimit())
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}
	offset := int(req.GetOffset())
	if offset < 0 {
		offset = 0
	}

	chats, err := s.store.ListChats(ctx, limit, offset, req.GetArchived())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	resp := &zchatv1.GetChatsResponse{Chats: make([]*zchatv1.Chat, 0, len(chats))}
	for _, c := range chats {
		resp.Chats = append(resp.Chats, wa.ToProtoChat(c))
	}
	return resp, nil
}

// SearchChats returns chats matching a free-text query.
func (s *Service) SearchChats(ctx context.Context, req *zchatv1.SearchChatsRequest) (*zchatv1.SearchChatsResponse, error) {
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return &zchatv1.SearchChatsResponse{}, nil
	}

	limit := int(req.GetLimit())
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}

	chats, err := s.store.SearchChats(ctx, query, limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	resp := &zchatv1.SearchChatsResponse{Chats: make([]*zchatv1.Chat, 0, len(chats))}
	for _, c := range chats {
		resp.Chats = append(resp.Chats, wa.ToProtoChat(c))
	}
	return resp, nil
}

// DownloadMedia fetches a message's attachment to the local media directory.
func (s *Service) DownloadMedia(ctx context.Context, req *zchatv1.DownloadMediaRequest) (*zchatv1.DownloadMediaResponse, error) {
	id := req.GetMessageId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "message_id is required")
	}

	msg, err := s.session.DownloadMedia(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "message has no media")
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	// Other windows should see the now-local path too.
	s.broker.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: msg}})
	return &zchatv1.DownloadMediaResponse{Message: msg}, nil
}

// GetMessages returns a page of messages and marks the chat as read.
func (s *Service) GetMessages(ctx context.Context, req *zchatv1.GetMessagesRequest) (*zchatv1.GetMessagesResponse, error) {
	chatJID := req.GetChatJid()
	if chatJID == "" {
		return nil, status.Error(codes.InvalidArgument, "chat_jid is required")
	}

	limit := int(req.GetLimit())
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}

	msgs, err := s.store.ListMessages(ctx, chatJID, limit, req.GetBeforeTimestamp())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Opening a chat clears its unread badge everywhere, locally and on the
	// user's phone.
	if req.GetBeforeTimestamp() == 0 {
		unread := int32(0)
		if chat, err := s.store.GetChat(ctx, chatJID); err == nil {
			unread = chat.Unread
		}
		if err := s.store.ClearUnread(ctx, chatJID); err != nil {
			s.log.Warn().Err(err).Str("chat", chatJID).Msg("clear unread")
		} else if chat, err := s.store.GetChat(ctx, chatJID); err == nil {
			s.broker.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ChatUpdated{ChatUpdated: wa.ToProtoChat(chat)}})
		}
		if unread > 0 {
			if err := s.session.MarkRead(ctx, chatJID, int(unread)); err != nil {
				s.log.Warn().Err(err).Str("chat", chatJID).Msg("send read receipt")
			}
		}
	}

	resp := &zchatv1.GetMessagesResponse{Messages: make([]*zchatv1.Message, 0, len(msgs))}
	for _, m := range msgs {
		out := wa.ToProtoMessage(m)
		// Rows stored before the sender name was resolvable (history sync omits
		// PushName) are filled in on the way out.
		if out.GetSenderName() == "" && !out.GetOutgoing() {
			out.SenderName = s.session.DisplayName(ctx, out.GetSender())
		}
		resp.Messages = append(resp.Messages, out)
	}
	return resp, nil
}

// SendMessage sends a text message to a chat, optionally as a reply.
func (s *Service) SendMessage(ctx context.Context, req *zchatv1.SendMessageRequest) (*zchatv1.SendMessageResponse, error) {
	if req.GetChatJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "chat_jid is required")
	}
	if strings.TrimSpace(req.GetBody()) == "" {
		return nil, status.Error(codes.InvalidArgument, "body is required")
	}

	msg, err := s.session.SendText(ctx, req.GetChatJid(), req.GetBody(), req.GetQuotedMessageId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "quoted message not found")
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &zchatv1.SendMessageResponse{Message: msg}, nil
}

// SendMedia uploads a local file and sends it as an attachment.
func (s *Service) SendMedia(ctx context.Context, req *zchatv1.SendMediaRequest) (*zchatv1.SendMediaResponse, error) {
	if req.GetChatJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "chat_jid is required")
	}
	if req.GetFilePath() == "" {
		return nil, status.Error(codes.InvalidArgument, "file_path is required")
	}

	msg, err := s.session.SendMedia(ctx, req.GetChatJid(), req.GetFilePath(), req.GetCaption(), req.GetQuotedMessageId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "quoted message not found")
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &zchatv1.SendMediaResponse{Message: msg}, nil
}

// ForwardMessage re-sends an existing message to another chat.
func (s *Service) ForwardMessage(ctx context.Context, req *zchatv1.ForwardMessageRequest) (*zchatv1.ForwardMessageResponse, error) {
	if req.GetMessageId() == "" {
		return nil, status.Error(codes.InvalidArgument, "message_id is required")
	}
	if req.GetToChatJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "to_chat_jid is required")
	}

	msg, err := s.session.ForwardMessage(ctx, req.GetMessageId(), req.GetToChatJid())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "message not found")
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &zchatv1.ForwardMessageResponse{Message: msg}, nil
}

// DeleteMessage removes a message locally, and for everyone when requested.
func (s *Service) DeleteMessage(ctx context.Context, req *zchatv1.DeleteMessageRequest) (*zchatv1.DeleteMessageResponse, error) {
	if req.GetMessageId() == "" {
		return nil, status.Error(codes.InvalidArgument, "message_id is required")
	}

	if _, err := s.session.DeleteMessage(ctx, req.GetMessageId(), req.GetRevoke()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "message not found")
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &zchatv1.DeleteMessageResponse{}, nil
}

// UpdateChat toggles a chat's pinned, archived and muted flags.
func (s *Service) UpdateChat(ctx context.Context, req *zchatv1.UpdateChatRequest) (*zchatv1.UpdateChatResponse, error) {
	if req.GetChatJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "chat_jid is required")
	}
	if req.Pinned == nil && req.Archived == nil && req.MutedUntil == nil {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	chat, err := s.session.UpdateChat(ctx, req.GetChatJid(), req.Pinned, req.Archived, req.MutedUntil)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "chat not found")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &zchatv1.UpdateChatResponse{Chat: chat}, nil
}

// GetConnectionState reports the current WhatsApp connection state.
func (s *Service) GetConnectionState(_ context.Context, _ *zchatv1.GetConnectionStateRequest) (*zchatv1.ConnectionState, error) {
	return s.session.State(), nil
}

// Logout unlinks the device and re-arms QR pairing.
func (s *Service) Logout(ctx context.Context, _ *zchatv1.LogoutRequest) (*zchatv1.LogoutResponse, error) {
	if err := s.session.Logout(ctx); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &zchatv1.LogoutResponse{}, nil
}

// StreamEvents pushes live updates. The current connection state (and any
// pending QR code) is sent first so an attaching client never has to poll.
func (s *Service) StreamEvents(_ *zchatv1.StreamEventsRequest, stream zchatv1.ChatService_StreamEventsServer) error {
	events, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()

	if err := stream.Send(&zchatv1.Event{
		Payload: &zchatv1.Event_ConnectionState{ConnectionState: s.session.State()},
	}); err != nil {
		return err
	}
	if qr := s.session.PendingQR(); qr != nil {
		if err := stream.Send(&zchatv1.Event{Payload: &zchatv1.Event_QrUpdated{QrUpdated: qr}}); err != nil {
			return err
		}
	}

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		}
	}
}
