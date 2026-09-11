package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

const (
	// typingExpiry drops a typing indicator that was never followed by a
	// "paused" state, which happens when the peer simply goes offline.
	typingExpiry = 12 * time.Second
	// typingIdle is how long the composer may sit untouched before the peer is
	// told the user stopped typing.
	typingIdle = 4 * time.Second
)

// chatPresence is the latest presence known for one conversation.
type chatPresence struct {
	typists  map[string]string // user jid -> display name
	online   bool
	lastSeen int64
}

// onPresence folds a daemon presence update into the per-chat state and
// refreshes the header when it concerns the open chat.
func (w *window) onPresence(update *zchatv1.PresenceUpdate) {
	chatJID := update.GetChatJid()
	state, ok := w.presence[chatJID]
	if !ok {
		state = &chatPresence{typists: make(map[string]string)}
		w.presence[chatJID] = state
	}

	if update.GetTyping() || update.GetRecording() {
		state.typists[update.GetUserJid()] = presenceName(update)
		w.scheduleTypingExpiry(chatJID, update.GetUserJid())
	} else {
		delete(state.typists, update.GetUserJid())
	}

	// Online transitions only ever come from direct chats, where the chat and
	// the contact are the same JID.
	if update.GetUserJid() == chatJID {
		state.online = update.GetOnline()
		if seen := update.GetLastSeen(); seen > 0 {
			state.lastSeen = seen
		}
		if !state.online {
			clear(state.typists)
		}
	}

	if chatJID == w.activeChat {
		w.refreshSubtitle()
	}
}

// scheduleTypingExpiry clears a typing flag that is never withdrawn.
func (w *window) scheduleTypingExpiry(chatJID, userJID string) {
	key := chatJID + "\x00" + userJID
	if handle, ok := w.typingTimers[key]; ok {
		glib.SourceRemove(handle)
	}
	w.typingTimers[key] = glib.TimeoutAdd(uint(typingExpiry.Milliseconds()), func() bool {
		delete(w.typingTimers, key)
		if state, ok := w.presence[chatJID]; ok {
			delete(state.typists, userJID)
		}
		if chatJID == w.activeChat {
			w.refreshSubtitle()
		}
		return false
	})
}

// refreshSubtitle renders the open chat's presence line under the header title.
func (w *window) refreshSubtitle() {
	w.contentTitle.SetSubtitle(w.subtitleFor(w.activeChat))
}

func (w *window) subtitleFor(chatJID string) string {
	state, ok := w.presence[chatJID]
	if !ok {
		return ""
	}

	if len(state.typists) > 0 {
		if !w.activeGroup {
			return "typing…"
		}
		names := make([]string, 0, len(state.typists))
		for _, name := range state.typists {
			names = append(names, name)
		}
		if len(names) == 1 {
			return names[0] + " is typing…"
		}
		return fmt.Sprintf("%d people are typing…", len(names))
	}

	switch {
	case w.activeGroup:
		return ""
	case state.online:
		return "online"
	case state.lastSeen > 0:
		return "last seen " + lastSeenText(time.Unix(state.lastSeen, 0))
	default:
		return ""
	}
}

func lastSeenText(at time.Time) string {
	now := time.Now()
	switch {
	case now.Sub(at) < time.Minute:
		return "just now"
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return "today at " + at.Format("15:04")
	case now.Sub(at) < 48*time.Hour:
		return "yesterday at " + at.Format("15:04")
	default:
		return at.Format("2 Jan 15:04")
	}
}

func presenceName(update *zchatv1.PresenceUpdate) string {
	if name := update.GetUserName(); name != "" {
		return name
	}
	return jidUser(update.GetUserJid())
}

// setupTypingNotifier reports the user's own typing state as the composer
// changes, and stops it once the composer has been idle for a moment.
func (w *window) setupTypingNotifier() {
	w.messageEntry.ConnectChanged(func() {
		if w.activeChat == "" || w.client == nil {
			return
		}
		if strings.TrimSpace(w.messageEntry.Text()) == "" {
			w.stopTyping()
			return
		}

		w.client.SetPresence(w.ctx, w.activeChat, true, true)
		if w.idleTimer != 0 {
			glib.SourceRemove(w.idleTimer)
		}
		w.idleTimer = glib.TimeoutAdd(uint(typingIdle.Milliseconds()), func() bool {
			w.idleTimer = 0
			w.stopTyping()
			return false
		})
	})
}

// stopTyping withdraws the typing state for the open chat.
func (w *window) stopTyping() {
	if w.idleTimer != 0 {
		glib.SourceRemove(w.idleTimer)
		w.idleTimer = 0
	}
	if w.activeChat == "" || w.client == nil {
		return
	}
	w.client.SetPresence(w.ctx, w.activeChat, false, true)
}
