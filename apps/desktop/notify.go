package main

import (
	"github.com/diamondburned/gotk4/pkg/gio/v2"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// notify raises a desktop notification for an incoming message. Own messages,
// and messages in the chat the user is already looking at with the window
// focused, stay silent.
func (w *window) notify(msg *zchatv1.Message) {
	if msg.GetOutgoing() {
		return
	}
	if msg.GetChatJid() == w.activeChat && w.win.IsActive() {
		return
	}

	title := msg.GetChatJid()
	if chat, ok := w.chatIndex[msg.GetChatJid()]; ok {
		title = displayName(chat)
	} else {
		title = jidUser(title)
	}
	if chat, ok := w.chatIndex[msg.GetChatJid()]; ok && chat.GetIsGroup() {
		title = senderName(msg) + " • " + title
	}

	n := gio.NewNotification(title)
	n.SetBody(msg.GetBody())
	n.SetPriority(gio.NotificationPriorityNormal)
	// One notification per chat: a new message replaces the previous banner
	// instead of stacking one per message.
	w.app.SendNotification("chat:"+msg.GetChatJid(), n)
}
