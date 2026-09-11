package wa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	daemonstore "github.com/zealish/zchat/apps/daemon/store"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// maxUploadSize caps outgoing attachments. The file is encrypted in memory
// before upload, so an unbounded read would be a trivial way to exhaust the
// daemon's memory.
const maxUploadSize = 100 << 20

// SendMedia uploads a local file and sends it as an attachment, recording an
// optimistic pending row first. quotedID, when set, makes it a reply.
func (s *Session) SendMedia(ctx context.Context, chatJID, filePath, caption, quotedID string) (*zchatv1.Message, error) {
	client := s.currentClient()
	if client == nil {
		return nil, errors.New("session not started")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, fmt.Errorf("parse jid %q: %w", chatJID, err)
	}

	data, err := readUpload(filePath)
	if err != nil {
		return nil, err
	}
	filename := filepath.Base(filePath)
	mimeType := detectMime(filename, data)
	kind := mediaKind(mimeType)

	var (
		quoted  *daemonstore.Quoted
		ctxInfo *waE2E.ContextInfo
	)
	if quotedID != "" {
		quoted, ctxInfo, err = s.replyContext(ctx, jid, quotedID)
		if err != nil {
			return nil, err
		}
	}

	upload, err := client.Upload(ctx, data, uploadType(kind))
	if err != nil {
		return nil, fmt.Errorf("upload %s: %w", filename, err)
	}

	md := &daemonstore.Media{
		Mime:     mimeType,
		Size:     int64(len(data)),
		Filename: filename,
		Caption:  caption,
	}
	if kind == TypeImage {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			md.Width, md.Height = int32(cfg.Width), int32(cfg.Height)
		}
	}

	payload := uploadPayload(kind, md, upload, ctxInfo)
	raw, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode media payload: %w", err)
	}
	md.Payload = raw

	id := client.GenerateMessageID()
	// Keeping our own copy means the message survives the user moving or
	// deleting the file they picked.
	if dest, err := mediaPath(id, md); err != nil {
		s.log.Warn().Err(err).Msg("resolve media destination")
	} else if err := os.WriteFile(dest, data, fileMode); err != nil {
		s.log.Warn().Err(err).Str("path", dest).Msg("cache sent media")
	} else {
		md.Path = dest
	}

	sender := ""
	if client.Store != nil && client.Store.ID != nil {
		sender = client.Store.ID.ToNonAD().String()
	}
	row := daemonstore.Message{
		ID:         id,
		ChatJID:    jid.ToNonAD().String(),
		Sender:     sender,
		SenderName: pushNameOf(client.Store),
		Body:       caption,
		Type:       kind,
		Timestamp:  time.Now().Unix(),
		Outgoing:   true,
		Status:     daemonstore.StatusPending,
		Media:      md,
		Quoted:     quoted,
	}
	if row.Body == "" {
		row.Body = mediaPreview(kind, md)
	}
	if err := s.store.InsertMessage(ctx, row); err != nil {
		return nil, err
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageReceived{MessageReceived: toProtoMessage(row)}})

	resp, sendErr := client.SendMessage(ctx, jid, payload, whatsmeow.SendRequestExtra{ID: id})
	if sendErr != nil {
		if err := s.store.UpdateMessageStatus(ctx, id, daemonstore.StatusFailed); err != nil {
			s.log.Warn().Err(err).Msg("mark media send failed")
		}
		row.Status = daemonstore.StatusFailed
		s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: toProtoMessage(row)}})
		return nil, fmt.Errorf("send media: %w", sendErr)
	}

	s.finishSend(ctx, &row, jid, resp)
	out := toProtoMessage(row)
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_MessageUpdated{MessageUpdated: out}})
	s.publishChat(ctx, row.ChatJID)
	return out, nil
}

// readUpload loads an attachment, refusing anything larger than the daemon is
// willing to hold in memory.
func readUpload(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxUploadSize {
		return nil, fmt.Errorf("%s is larger than %d MB", filepath.Base(path), maxUploadSize>>20)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s is empty", filepath.Base(path))
	}
	return data, nil
}

// detectMime prefers the filename's extension and falls back to sniffing the
// content, which is all the desktop gives us for pasted images.
func detectMime(filename string, data []byte) string {
	if t := mime.TypeByExtension(filepath.Ext(filename)); t != "" {
		return strings.TrimSpace(strings.SplitN(t, ";", 2)[0])
	}
	return strings.TrimSpace(strings.SplitN(http.DetectContentType(data), ";", 2)[0])
}

// mediaKind maps a MIME type onto the message type WhatsApp expects.
func mediaKind(mimeType string) string {
	switch {
	case mimeType == "image/webp":
		// WhatsApp renders WebP as a sticker, not a photo.
		return TypeSticker
	case strings.HasPrefix(mimeType, "image/"):
		return TypeImage
	case strings.HasPrefix(mimeType, "video/"):
		return TypeVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return TypeAudio
	default:
		return TypeDocument
	}
}

func uploadType(kind string) whatsmeow.MediaType {
	switch kind {
	case TypeImage, TypeSticker:
		return whatsmeow.MediaImage
	case TypeVideo:
		return whatsmeow.MediaVideo
	case TypeAudio:
		return whatsmeow.MediaAudio
	default:
		return whatsmeow.MediaDocument
	}
}

// uploadPayload builds the outgoing message for an uploaded attachment.
func uploadPayload(kind string, md *daemonstore.Media, up whatsmeow.UploadResponse, ctxInfo *waE2E.ContextInfo) *waE2E.Message {
	switch kind {
	case TypeImage:
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption:       proto.String(md.Caption),
			Mimetype:      proto.String(md.Mime),
			Width:         proto.Uint32(uint32(md.Width)),
			Height:        proto.Uint32(uint32(md.Height)),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			ContextInfo:   ctxInfo,
		}}
	case TypeSticker:
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			Mimetype:      proto.String(md.Mime),
			Width:         proto.Uint32(uint32(md.Width)),
			Height:        proto.Uint32(uint32(md.Height)),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			ContextInfo:   ctxInfo,
		}}
	case TypeVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption:       proto.String(md.Caption),
			Mimetype:      proto.String(md.Mime),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			ContextInfo:   ctxInfo,
		}}
	case TypeAudio:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			Mimetype:      proto.String(md.Mime),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			ContextInfo:   ctxInfo,
		}}
	default:
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			FileName:      proto.String(md.Filename),
			Caption:       proto.String(md.Caption),
			Mimetype:      proto.String(md.Mime),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			ContextInfo:   ctxInfo,
		}}
	}
}
