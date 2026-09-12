package main

import (
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// setupShortcuts registers the window actions from the PRD and binds their
// accelerators. Ctrl+V is not an action: it has to run before the composer
// entry consumes the key, so it lives on a key controller instead.
func (w *window) setupShortcuts() {
	actions := []struct {
		name   string
		accels []string
		run    func()
	}{
		{"search", []string{"<Control>k"}, w.focusSearch},
		{"find", []string{"<Control>f"}, w.toggleFind},
		{"new-chat", []string{"<Control>n"}, w.showNewChatDialog},
		{"focus-chats", []string{"<Control>l"}, w.focusChatList},
		{"mute-chat", []string{"<Control><Shift>m"}, w.toggleMuteActiveChat},
		{"shortcuts", []string{"<Control>question"}, w.showShortcutsHelp},
		{"close", []string{"<Control>w"}, func() { w.win.Close() }},
		{"quit", []string{"<Control>q"}, func() { w.app.Quit() }},
		{"cancel", []string{"Escape"}, w.escape},
		// Menu-only entries: no accelerator, but the primary menu needs the
		// actions to exist in the same "win" group.
		{"preferences", nil, w.showSettings},
		{"about", nil, w.showAbout},
	}

	group := gio.NewSimpleActionGroup()
	for _, entry := range actions {
		run := entry.run
		action := gio.NewSimpleAction(entry.name, nil)
		action.ConnectActivate(func(*glib.Variant) { run() })
		group.AddAction(action)
		if len(entry.accels) > 0 {
			w.app.SetAccelsForAction("win."+entry.name, entry.accels)
		}
	}
	w.win.InsertActionGroup("win", group)
}

func (w *window) focusSearch() {
	w.splitView.SetShowContent(false)
	w.searchEntry.GrabFocus()
}

func (w *window) focusChatList() {
	w.splitView.SetShowContent(false)
	w.chatList.GrabFocus()
}

// toggleMuteActiveChat mutes the open chat indefinitely, or unmutes it when it
// is already muted.
func (w *window) toggleMuteActiveChat() {
	chat, ok := w.chatIndex[w.activeChat]
	if !ok {
		return
	}

	until := int64(muteForever)
	if isMuted(chat) {
		until = 0
	}
	w.updateChat(chat.GetJid(), nil, nil, &until)
}

// escape backs out of whatever is currently in progress: the find bar, a
// pending reply, an active search, or the open conversation on narrow layouts.
func (w *window) escape() {
	switch {
	case w.findBar.SearchMode():
		w.findBar.SetSearchMode(false)
	case w.pending != nil:
		w.cancelAttachment()
	case w.replyTo != nil:
		w.cancelReply()
	case w.searchEntry.Text() != "":
		w.searchEntry.SetText("")
	default:
		w.splitView.SetShowContent(false)
	}
}
