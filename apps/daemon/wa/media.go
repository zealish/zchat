package wa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	daemonstore "github.com/zealish/zchat/apps/daemon/store"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

// Message type identifiers shared with the IPC layer.
const (
	TypeText     = "text"
	TypeImage    = "image"
	TypeVideo    = "video"
	TypeAudio    = "audio"
	TypeDocument = "document"
	TypeSticker  = "sticker"
)

// fileMode keeps downloaded media user-private, matching the PRD's 700 rule for
// the surrounding directory.
const fileMode = 0o600

// extractMedia builds the media row for a message, returning ("", nil) when the
// message carries no supported attachment. Payload holds the serialised message
// so the file can be decrypted later, on demand.
func extractMedia(msg *waE2E.Message) (string, *daemonstore.Media) {
	if msg == nil {
		return "", nil
	}

	var (
		kind string
		md   daemonstore.Media
	)
	switch {
	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		kind = TypeImage
		md = daemonstore.Media{
			Mime:      m.GetMimetype(),
			Size:      int64(m.GetFileLength()),
			Caption:   m.GetCaption(),
			Thumbnail: m.GetJPEGThumbnail(),
			Width:     int32(m.GetWidth()),
			Height:    int32(m.GetHeight()),
		}
	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		kind = TypeVideo
		md = daemonstore.Media{
			Mime:      m.GetMimetype(),
			Size:      int64(m.GetFileLength()),
			Caption:   m.GetCaption(),
			Thumbnail: m.GetJPEGThumbnail(),
			Width:     int32(m.GetWidth()),
			Height:    int32(m.GetHeight()),
			Duration:  int32(m.GetSeconds()),
		}
	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		kind = TypeAudio
		md = daemonstore.Media{
			Mime:     m.GetMimetype(),
			Size:     int64(m.GetFileLength()),
			Duration: int32(m.GetSeconds()),
		}
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		kind = TypeDocument
		md = daemonstore.Media{
			Mime:      m.GetMimetype(),
			Size:      int64(m.GetFileLength()),
			Filename:  m.GetFileName(),
			Caption:   m.GetCaption(),
			Thumbnail: m.GetJPEGThumbnail(),
		}
	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		kind = TypeSticker
		md = daemonstore.Media{
			Mime:   m.GetMimetype(),
			Size:   int64(m.GetFileLength()),
			Width:  int32(m.GetWidth()),
			Height: int32(m.GetHeight()),
		}
	default:
		return "", nil
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		return "", nil
	}
	md.Payload = payload
	return kind, &md
}

// mediaPreview is the chat-list text shown for an attachment.
func mediaPreview(kind string, md *daemonstore.Media) string {
	if md != nil && md.Caption != "" {
		return md.Caption
	}
	switch kind {
	case TypeImage:
		return "Photo"
	case TypeVideo:
		return "Video"
	case TypeAudio:
		return "Audio"
	case TypeSticker:
		return "Sticker"
	case TypeDocument:
		if md != nil && md.Filename != "" {
			return md.Filename
		}
		return "Document"
	default:
		return ""
	}
}

// DownloadMedia materialises a message's attachment under the XDG media
// directory and returns the refreshed message row. Already-downloaded media is
// returned as is.
func (s *Session) DownloadMedia(ctx context.Context, messageID string) (*zchatv1.Message, error) {
	payload, path, err := s.store.MediaPayload(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			return s.protoMessage(ctx, messageID)
		}
	}

	client := s.currentClient()
	if client == nil {
		return nil, errors.New("session not started")
	}

	var msg waE2E.Message
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return nil, fmt.Errorf("decode media payload %s: %w", messageID, err)
	}

	data, err := client.DownloadAny(ctx, &msg)
	if err != nil {
		return nil, fmt.Errorf("download media %s: %w", messageID, err)
	}

	row, err := s.store.GetMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	dest, err := mediaPath(messageID, row.Media)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(dest, data, fileMode); err != nil {
		return nil, fmt.Errorf("write media %s: %w", dest, err)
	}
	if err := s.store.SetMediaPath(ctx, messageID, dest, int64(len(data))); err != nil {
		return nil, err
	}

	return s.protoMessage(ctx, messageID)
}

func (s *Session) protoMessage(ctx context.Context, messageID string) (*zchatv1.Message, error) {
	row, err := s.store.GetMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	return toProtoMessage(row), nil
}

// mediaPath builds a collision-free destination inside the media directory. The
// message ID is hashed so hostile IDs can never escape the directory.
func mediaPath(messageID string, md *daemonstore.Media) (string, error) {
	dir, err := xdgpaths.MediaDir()
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(messageID))
	name := hex.EncodeToString(sum[:16])

	ext := ""
	if md != nil {
		if md.Filename != "" {
			ext = filepath.Ext(md.Filename)
		}
		if ext == "" && md.Mime != "" {
			if exts, err := mime.ExtensionsByType(strings.SplitN(md.Mime, ";", 2)[0]); err == nil && len(exts) > 0 {
				ext = exts[0]
			}
		}
	}
	return filepath.Join(dir, name+ext), nil
}
