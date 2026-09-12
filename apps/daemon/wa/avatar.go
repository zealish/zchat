package wa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

// maxAvatarSize bounds a downloaded profile picture. Previews are a few
// kilobytes, so anything larger is not one.
const maxAvatarSize = 2 << 20

// avatarTimeout bounds the whole fetch, which involves an IQ round trip plus an
// HTTP download.
const avatarTimeout = 20 * time.Second

// avatarCache remembers the resolved picture per JID so the sidebar, which asks
// for every visible row, only hits the network once. An empty path is a
// remembered "no picture", which is just as worth caching as a hit.
type avatarCache struct {
	mu      sync.Mutex
	entries map[string]string
}

func newAvatarCache() *avatarCache {
	return &avatarCache{entries: make(map[string]string)}
}

func (c *avatarCache) get(jid string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path, ok := c.entries[jid]
	return path, ok
}

func (c *avatarCache) put(jid, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[jid] = path
}

func (c *avatarCache) drop(jid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, jid)
}

// ProfilePicture returns the local path of a contact's or group's avatar,
// downloading it when it is not cached yet. An empty path means the target has
// no picture, which is not an error.
func (s *Session) ProfilePicture(ctx context.Context, rawJID string) (string, error) {
	jid, err := types.ParseJID(rawJID)
	if err != nil {
		return "", fmt.Errorf("parse jid %q: %w", rawJID, err)
	}
	jid = jid.ToNonAD()
	key := jid.String()

	if path, ok := s.avatars.get(key); ok {
		if path == "" {
			return "", nil
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		s.avatars.drop(key)
	}

	client := s.currentClient()
	if client == nil || !client.IsLoggedIn() {
		return "", errors.New("session not started")
	}

	ctx, cancel := context.WithTimeout(ctx, avatarTimeout)
	defer cancel()

	info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true})
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet),
		errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		s.avatars.put(key, "")
		return "", nil
	case err != nil:
		return "", fmt.Errorf("profile picture info %s: %w", key, err)
	case info == nil || info.URL == "":
		s.avatars.put(key, "")
		return "", nil
	}

	path, err := downloadAvatar(ctx, key, info.URL)
	if err != nil {
		return "", err
	}
	s.avatars.put(key, path)
	return path, nil
}

// onPictureChanged invalidates the cached avatar and re-fetches it so every
// connected window can swap the image without asking.
func (s *Session) onPictureChanged(ctx context.Context, e *events.Picture) {
	key := e.JID.ToNonAD().String()
	s.avatars.drop(key)

	if e.Remove {
		s.avatars.put(key, "")
		s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ProfilePictureUpdated{
			ProfilePictureUpdated: &zchatv1.ProfilePictureUpdate{Jid: key},
		}})
		return
	}

	path, err := s.ProfilePicture(ctx, key)
	if err != nil {
		s.log.Warn().Err(err).Str("jid", key).Msg("refresh profile picture")
		return
	}
	s.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_ProfilePictureUpdated{
		ProfilePictureUpdated: &zchatv1.ProfilePictureUpdate{Jid: key, Path: path},
	}})
}

// downloadAvatar stores the picture under the avatar cache directory. The JID
// is hashed so a hostile one can never escape the directory.
func downloadAvatar(ctx context.Context, jid, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("avatar request %s: %w", jid, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download avatar %s: %w", jid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download avatar %s: unexpected status %s", jid, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarSize))
	if err != nil {
		return "", fmt.Errorf("read avatar %s: %w", jid, err)
	}

	dir, err := xdgpaths.AvatarDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(jid))
	dest := filepath.Join(dir, hex.EncodeToString(sum[:16])+".jpg")
	if err := os.WriteFile(dest, data, fileMode); err != nil {
		return "", fmt.Errorf("write avatar %s: %w", dest, err)
	}
	return dest, nil
}
