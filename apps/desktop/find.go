package main

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// findPageLimit bounds how many older pages are pulled in while walking back to
// a match, so a hit deep in a large history cannot spin forever.
const findPageLimit = 10

// setupFind wires the in-conversation find bar. Matches come from the daemon so
// the whole stored history is searched, not just the loaded page.
func (w *window) setupFind() {
	w.findBar.ConnectEntry(w.findEntry)
	w.findEntry.ConnectSearchChanged(func() { w.runFind(w.findEntry.Text()) })
	w.findEntry.ConnectActivate(func() { w.stepMatch(1) })
	w.findNext.ConnectClicked(func() { w.stepMatch(1) })
	w.findPrev.ConnectClicked(func() { w.stepMatch(-1) })
	w.findBar.NotifyProperty("search-mode-enabled", func() {
		if !w.findBar.SearchMode() {
			w.clearFind()
		}
	})
}

// toggleFind opens the find bar over the conversation, or closes it when it is
// already open.
func (w *window) toggleFind() {
	if w.activeChat == "" {
		return
	}
	if w.findBar.SearchMode() {
		w.findBar.SetSearchMode(false)
		return
	}
	w.findBar.SetSearchMode(true)
	w.findEntry.GrabFocus()
}

// clearFind drops the match list and the highlight.
func (w *window) clearFind() {
	w.findEntry.SetText("")
	w.findMatches = nil
	w.findIndex = 0
	w.setFindCurrent("")
	w.updateFindStatus()
}

func (w *window) runFind(query string) {
	if query == "" || w.client == nil || w.activeChat == "" {
		w.findMatches = nil
		w.findIndex = 0
		w.setFindCurrent("")
		w.updateFindStatus()
		return
	}

	chatJID := w.activeChat
	w.client.SearchMessages(w.ctx, query, chatJID, func(q string, msgs []*zchatv1.Message, err error) {
		if err != nil {
			w.log.Error().Err(err).Msg("search messages")
			w.toast("Could not search this conversation")
			return
		}
		// A slower reply for an older query, or another chat, must not win.
		if chatJID != w.activeChat || q != w.findEntry.Text() {
			return
		}

		// The daemon returns newest first; find walks oldest to newest so
		// "next" moves down the conversation.
		w.findMatches = make([]string, 0, len(msgs))
		for i := len(msgs) - 1; i >= 0; i-- {
			w.findMatches = append(w.findMatches, msgs[i].GetId())
		}
		w.findIndex = len(w.findMatches) - 1
		w.updateFindStatus()
		if len(w.findMatches) > 0 {
			w.revealMatch()
		} else {
			w.setFindCurrent("")
		}
	})
}

// stepMatch moves the selection by delta, wrapping at both ends.
func (w *window) stepMatch(delta int) {
	if len(w.findMatches) == 0 {
		return
	}
	w.findIndex = (w.findIndex + delta + len(w.findMatches)) % len(w.findMatches)
	w.updateFindStatus()
	w.revealMatch()
}

func (w *window) updateFindStatus() {
	total := len(w.findMatches)
	switch {
	case w.findEntry.Text() == "":
		w.findStatus.SetText("")
	case total == 0:
		w.findStatus.SetText("No matches")
	default:
		w.findStatus.SetText(fmt.Sprintf("%d of %d", w.findIndex+1, total))
	}
	w.findPrev.SetSensitive(total > 1)
	w.findNext.SetSensitive(total > 1)
}

// revealMatch scrolls the current match into view, paging in older messages
// when the hit predates what is loaded.
func (w *window) revealMatch() {
	id := w.findMatches[w.findIndex]
	w.setFindCurrent(id)
	w.scrollToMessage(id, findPageLimit)
}

// setFindCurrent marks which message the highlight belongs to and repaints the
// affected rows. Rows are recycled, so bindMessageRow applies the class too.
func (w *window) setFindCurrent(id string) {
	previous := w.findCurrent
	w.findCurrent = id
	for _, target := range []string{previous, id} {
		if target == "" {
			continue
		}
		if item, ok := w.messageRows[target]; ok {
			highlightRow(item, target == id)
		}
	}
}

func highlightRow(item *gtk.ListItem, on bool) {
	bubble := bubbleOf(item)
	if on {
		bubble.AddCSSClass("zchat-found")
		return
	}
	bubble.RemoveCSSClass("zchat-found")
}

// scrollToMessage brings a message into view, loading older pages while the
// message sits above what has been fetched. budget bounds that walk.
func (w *window) scrollToMessage(id string, budget int) {
	for i := range w.messages.Len() {
		if w.messages.At(i).GetId() != id {
			continue
		}
		// Scrolling must not re-arm the stick-to-bottom behaviour.
		w.stickToBottom = false
		w.messageList.ScrollTo(uint(i), gtk.ListScrollNone, nil)
		return
	}

	if budget <= 0 || w.client == nil || w.messages.Len() == 0 {
		w.toast("Match is further back in this conversation")
		return
	}

	chatJID := w.activeChat
	oldest := w.messages.At(0).GetTimestamp()
	w.client.GetMessages(w.ctx, chatJID, oldest, func(gotJID string, msgs []*zchatv1.Message, err error) {
		if err != nil || gotJID != w.activeChat || len(msgs) == 0 {
			if len(msgs) == 0 {
				w.hasOlder = false
			}
			return
		}
		w.messages.Splice(0, 0, msgs...)
		// The rows for the new page are measured on the next frame, so the
		// retry waits for the list to settle.
		glib.IdleAdd(func() bool {
			w.scrollToMessage(id, budget-1)
			return false
		})
	})
}
