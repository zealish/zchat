package main

import (
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// muteForever is the muted_until value the daemon uses for an indefinite mute.
const muteForever = -1

// menuEntry is one row of a context menu.
type menuEntry struct {
	label   string
	destroy bool // renders with the destructive-action style
	run     func()
}

// showMenu pops a context menu up at (x, y) relative to anchor. The popover
// unparents itself when closed so recycled list rows never accumulate popovers.
func showMenu(anchor gtk.Widgetter, x, y float64, entries []menuEntry) {
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	popover := gtk.NewPopover()
	popover.SetHasArrow(false)
	popover.SetChild(box)
	popover.SetParent(anchor)
	popover.SetPosition(gtk.PosBottom)

	for _, entry := range entries {
		run := entry.run
		button := gtk.NewButtonWithLabel(entry.label)
		button.SetHAlign(gtk.AlignFill)
		child := button.Child().(*gtk.Label)
		child.SetXAlign(0)
		button.AddCSSClass("flat")
		if entry.destroy {
			button.AddCSSClass("destructive-action")
		}
		button.ConnectClicked(func() {
			popover.Popdown()
			run()
		})
		box.Append(button)
	}

	popover.ConnectClosed(func() { popover.Unparent() })
	rect := gdk.NewRectangle(int(x), int(y), 1, 1)
	popover.SetPointingTo(&rect)
	popover.Popup()
}

// onRightClick wires a secondary-button gesture that reports the click position
// in widget coordinates.
func onRightClick(widget gtk.Widgetter, handler func(x, y float64)) {
	gesture := gtk.NewGestureClick()
	gesture.SetButton(gdk.BUTTON_SECONDARY)
	gesture.ConnectPressed(func(_ int, x, y float64) { handler(x, y) })
	gtk.BaseWidget(widget).AddController(gesture)
}

// chatMenuEntries builds the pin/archive/mute actions for a sidebar row.
func (w *window) chatMenuEntries(chat *zchatv1.Chat) []menuEntry {
	jid := chat.GetJid()

	pinned := !chat.GetPinned()
	pinLabel := "Pin chat"
	if chat.GetPinned() {
		pinLabel = "Unpin chat"
	}

	archived := !chat.GetArchived()
	archiveLabel := "Archive chat"
	if chat.GetArchived() {
		archiveLabel = "Unarchive chat"
	}

	entries := []menuEntry{
		{label: pinLabel, run: func() { w.updateChat(jid, &pinned, nil, nil) }},
		{label: archiveLabel, run: func() { w.updateChat(jid, nil, &archived, nil) }},
	}

	if isMuted(chat) {
		unmute := int64(0)
		entries = append(entries, menuEntry{
			label: "Unmute chat",
			run:   func() { w.updateChat(jid, nil, nil, &unmute) },
		})
		return entries
	}
	for _, option := range []struct {
		label string
		until int64
	}{
		{"Mute for 8 hours", time.Now().Add(8 * time.Hour).Unix()},
		{"Mute for 1 week", time.Now().Add(7 * 24 * time.Hour).Unix()},
		{"Mute forever", muteForever},
	} {
		until := option.until
		entries = append(entries, menuEntry{
			label: option.label,
			run:   func() { w.updateChat(jid, nil, nil, &until) },
		})
	}
	return entries
}

func (w *window) updateChat(jid string, pinned, archived *bool, mutedUntil *int64) {
	if w.client == nil {
		return
	}
	w.client.UpdateChat(w.ctx, jid, pinned, archived, mutedUntil, func(err error) {
		if err != nil {
			w.log.Error().Err(err).Str("chat", jid).Msg("update chat")
			w.toast("Could not update the chat")
		}
	})
}

// isMuted reports whether a chat's mute is still in effect.
func isMuted(chat *zchatv1.Chat) bool {
	until := chat.GetMutedUntil()
	return until == muteForever || (until > 0 && until > time.Now().Unix())
}

// messageMenuEntries builds the reply/forward/delete actions for a bubble.
func (w *window) messageMenuEntries(msg *zchatv1.Message) []menuEntry {
	id := msg.GetId()
	entries := []menuEntry{
		{label: "Reply", run: func() { w.startReply(msg) }},
		{label: "Copy text", run: func() { w.copyText(msg.GetBody()) }},
		{label: "Forward", run: func() { w.showForwardDialog(msg) }},
		{label: "Delete for me", destroy: true, run: func() { w.deleteMessage(id, false) }},
	}
	if msg.GetOutgoing() {
		entries = append(entries, menuEntry{
			label:   "Delete for everyone",
			destroy: true,
			run:     func() { w.confirmRevoke(id) },
		})
	}
	return entries
}

func (w *window) copyText(text string) {
	if text == "" {
		return
	}
	w.win.Clipboard().SetText(text)
	w.toast("Copied to clipboard")
}

func (w *window) deleteMessage(id string, revoke bool) {
	if w.client == nil {
		return
	}
	w.client.DeleteMessage(w.ctx, id, revoke, func(err error) {
		if err != nil {
			w.log.Error().Err(err).Str("id", id).Msg("delete message")
			w.toast("Could not delete the message")
		}
	})
}

// confirmRevoke asks before deleting for everyone, which cannot be undone.
func (w *window) confirmRevoke(id string) {
	dialog := adw.NewAlertDialog("Delete for everyone?",
		"This message will be removed for everyone in the chat.")
	dialog.AddResponse("cancel", "Cancel")
	dialog.AddResponse("delete", "Delete")
	dialog.SetResponseAppearance("delete", adw.ResponseDestructive)
	dialog.SetDefaultResponse("cancel")
	dialog.SetCloseResponse("cancel")
	dialog.ConnectResponse(func(response string) {
		if response == "delete" {
			w.deleteMessage(id, true)
		}
	})
	dialog.Present(w.win)
}

// startReply pins a message above the composer as the reply target.
func (w *window) startReply(msg *zchatv1.Message) {
	w.replyTo = msg
	w.replySender.SetText(replyTitle(msg))
	w.replyBody.SetText(previewText(msg))
	w.replyBar.SetVisible(true)
	w.messageEntry.GrabFocus()
}

func (w *window) cancelReply() {
	w.replyTo = nil
	w.replyBar.SetVisible(false)
}

func replyTitle(msg *zchatv1.Message) string {
	if msg.GetOutgoing() {
		return "You"
	}
	return senderName(msg)
}

// previewText is the one-line summary of a message used in reply bars and
// quoted blocks.
func previewText(msg *zchatv1.Message) string {
	if body := msg.GetBody(); body != "" {
		return body
	}
	return mediaNoun(msg.GetType())
}

// quotedSenderName labels the quoted block; the daemon leaves sender_name empty
// for unknown contacts, so the bare JID user part is the fallback.
func quotedSenderName(q *zchatv1.QuotedMessage) string {
	if name := q.GetSenderName(); name != "" {
		return name
	}
	if sender := q.GetSender(); sender != "" {
		return strings.SplitN(sender, "@", 2)[0]
	}
	return "Unknown"
}

func quotedPreview(q *zchatv1.QuotedMessage) string {
	if body := q.GetBody(); body != "" {
		return body
	}
	return mediaNoun(q.GetType())
}

func mediaNoun(kind string) string {
	switch kind {
	case "image":
		return "Photo"
	case "video":
		return "Video"
	case "audio":
		return "Audio"
	case "sticker":
		return "Sticker"
	case "document":
		return "Document"
	default:
		return ""
	}
}

// showForwardDialog lets the user pick a destination chat for a message.
func (w *window) showForwardDialog(msg *zchatv1.Message) {
	dialog := adw.NewDialog()
	dialog.SetTitle("Forward to")
	dialog.SetContentWidth(360)
	dialog.SetContentHeight(480)

	// The dialog picks from the loaded sidebar chats, ordered as the sidebar
	// orders them, so the most likely destinations come first.
	model := chatModelType.New()
	targets := make([]*zchatv1.Chat, 0, len(w.chatOrder))
	for _, c := range w.chatOrder {
		if c.GetJid() != msg.GetChatJid() {
			targets = append(targets, c)
		}
	}
	model.Splice(0, model.Len(), targets...)

	factory := gtk.NewSignalListItemFactory()
	factory.ConnectSetup(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		label := gtk.NewLabel("")
		label.SetXAlign(0)
		label.SetEllipsize(pango.EllipsizeEnd)
		label.SetMarginTop(10)
		label.SetMarginBottom(10)
		label.SetMarginStart(12)
		label.SetMarginEnd(12)
		item.SetChild(label)
	})
	factory.ConnectBind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		item.Child().(*gtk.Label).SetText(displayName(chatModelType.ObjectValue(item.Item())))
	})

	list := gtk.NewListView(gtk.NewNoSelection(model), &factory.ListItemFactory)
	list.AddCSSClass("navigation-sidebar")
	list.ConnectActivate(func(position uint) {
		if int(position) >= model.Len() {
			return
		}
		w.forwardMessage(msg.GetId(), model.At(int(position)))
		dialog.Close()
	})

	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetVExpand(true)
	scroll.SetChild(list)

	view := adw.NewToolbarView()
	view.AddTopBar(adw.NewHeaderBar())
	view.SetContent(scroll)
	dialog.SetChild(view)
	dialog.Present(w.win)
}

func (w *window) forwardMessage(id string, target *zchatv1.Chat) {
	if w.client == nil {
		return
	}
	name := displayName(target)
	w.client.ForwardMessage(w.ctx, id, target.GetJid(), func(err error) {
		if err != nil {
			w.log.Error().Err(err).Str("id", id).Msg("forward message")
			w.toast("Could not forward the message")
			return
		}
		w.toast("Forwarded to " + name)
	})
}
