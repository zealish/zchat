package main

import (
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// setupEmojiChooser hangs a GtkEmojiChooser off the composer's emoji button and
// inserts the picked character at the cursor.
func (w *window) setupEmojiChooser() {
	chooser := gtk.NewEmojiChooser()
	chooser.ConnectEmojiPicked(func(text string) {
		w.insertEmoji(text)
	})
	w.emojiButton.SetPopover(&chooser.Popover)
}

// showReactionChooser pops an emoji chooser over a message bubble so any emoji
// can be sent as a reaction, not just the quick ones in the context menu.
//
// A GtkEmojiChooser fills itself with several thousand widgets, which costs a
// visible stall the first time it is built. One instance is kept and reparented
// onto whichever bubble is being reacted to, so that cost is paid only once.
func (w *window) showReactionChooser(anchor gtk.Widgetter, x, y float64, messageID string) {
	if w.reactionChooser == nil {
		chooser := gtk.NewEmojiChooser()
		chooser.SetHasArrow(false)
		chooser.ConnectEmojiPicked(func(text string) {
			w.reactMessage(w.reactionTarget, text)
		})
		chooser.ConnectClosed(func() { chooser.Unparent() })
		w.reactionChooser = chooser
	}

	w.reactionTarget = messageID
	w.reactionChooser.Unparent()
	w.reactionChooser.SetParent(anchor)
	rect := gdk.NewRectangle(int(x), int(y), 1, 1)
	w.reactionChooser.SetPointingTo(&rect)
	w.reactionChooser.Popup()
}

// insertEmoji replaces the current selection, if any, with text and leaves the
// cursor after it so typing continues naturally.
func (w *window) insertEmoji(text string) {
	if text == "" {
		return
	}

	position := w.messageEntry.Position()
	if start, end, ok := w.messageEntry.SelectionBounds(); ok {
		w.messageEntry.DeleteText(start, end)
		position = start
	}

	body, caret := spliceRunes(w.messageEntry.Text(), text, position)
	w.messageEntry.SetText(body)
	// GrabFocus selects the entire entry, so the caret is placed afterwards.
	w.messageEntry.GrabFocus()
	w.messageEntry.SetPosition(caret)
}

// spliceRunes inserts text into body at a rune offset and reports where the
// caret lands. GTK entry positions count characters, not bytes, so the text is
// spliced as runes.
func spliceRunes(body, text string, position int) (string, int) {
	runes := []rune(body)
	if position < 0 || position > len(runes) {
		position = len(runes)
	}
	inserted := []rune(text)
	updated := make([]rune, 0, len(runes)+len(inserted))
	updated = append(updated, runes[:position]...)
	updated = append(updated, inserted...)
	updated = append(updated, runes[position:]...)
	return string(updated), position + len(inserted)
}
