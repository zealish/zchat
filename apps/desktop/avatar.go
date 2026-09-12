package main

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// avatarBinding is a widget currently showing a JID's picture, held by native
// pointer so a recycled row resolves to the same entry however it is reached.
type avatarBinding struct {
	avatar *adw.Avatar
	jid    string
}

// bindAvatar shows a contact's profile picture on an Avatar widget, asking the
// daemon for it the first time that JID is seen. The widget keeps its fallback
// initials until the image arrives.
//
// The widget is remembered so a late or changed picture can be applied without
// rebuilding the list: the ListView recycles rows, so a bound widget may end up
// showing a different chat before the answer comes back.
func (w *window) bindAvatar(avatar *adw.Avatar, jid string) {
	w.avatarRows[widgetKey(avatar)] = avatarBinding{avatar: avatar, jid: jid}

	texture, resolved := w.avatars[jid]
	setAvatarImage(avatar, texture)
	if resolved || w.client == nil || jid == "" {
		return
	}

	// A nil entry marks the request as in flight so scrolling past the same row
	// again does not queue a second lookup.
	w.avatars[jid] = nil
	w.client.GetProfilePicture(w.ctx, jid, func(jid, path string, err error) {
		if err != nil {
			w.log.Debug().Err(err).Str("jid", jid).Msg("profile picture")
		}
		w.setAvatar(jid, path)
	})
}

// unbindAvatar forgets a recycled row's widget.
func (w *window) unbindAvatar(avatar *adw.Avatar) {
	delete(w.avatarRows, widgetKey(avatar))
}

// setAvatar caches a resolved picture and applies it to every widget currently
// showing that JID.
func (w *window) setAvatar(jid, path string) {
	var texture *gdk.Texture
	if path != "" {
		loaded, err := gdk.NewTextureFromFilename(path)
		if err != nil {
			w.log.Warn().Err(err).Str("path", path).Msg("load avatar")
		} else {
			texture = loaded
		}
	}
	w.avatars[jid] = texture

	for _, bound := range w.avatarRows {
		if bound.jid == jid {
			setAvatarImage(bound.avatar, texture)
		}
	}
}

// setAvatarImage clears the custom image when there is no texture. A typed nil
// pointer would still satisfy the paintable interface, so it has to be dropped
// before the call rather than passed through.
func setAvatarImage(avatar *adw.Avatar, texture *gdk.Texture) {
	if texture == nil {
		avatar.SetCustomImage(nil)
		return
	}
	avatar.SetCustomImage(texture)
}

// onProfilePictureUpdated applies a picture the daemon reports as changed.
func (w *window) onProfilePictureUpdated(update *zchatv1.ProfilePictureUpdate) {
	w.setAvatar(update.GetJid(), update.GetPath())
}
