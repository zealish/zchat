package main

import (
	"testing"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// bindMedia has to find the cell that newMediaCell registered even when the box
// it is handed was reached by walking the row's widget tree, which is how
// bindMessageRow gets it. Missing the lookup silently skipped every attachment,
// so a sent image rendered as nothing at all.
func TestBindMediaFindsCellAfterTreeWalk(t *testing.T) {
	if !gtk.InitCheck() {
		t.Skip("no display")
	}

	w := &window{
		mediaCells:    make(map[uintptr]*mediaCell),
		mediaHandlers: make(map[uintptr]coreglib.SignalHandle),
		previewPaths:  make(map[uintptr]string),
		animTimers:    make(map[uintptr]glib.SourceHandle),
	}

	bubble := gtk.NewBox(gtk.OrientationVertical, 0)
	media := gtk.NewBox(gtk.OrientationVertical, 4)
	media.SetVisible(false)
	w.newMediaCell(media)
	bubble.Append(media)

	// Reach the box the way bindMessageRow does, through the parent.
	walked := bubble.FirstChild().(*gtk.Box)

	msg := &zchatv1.Message{
		Id:   "msg-1",
		Type: mediaKindImage,
		Media: &zchatv1.MediaInfo{
			Mime:   "image/jpeg",
			Size:   2048,
			Width:  800,
			Height: 600,
		},
	}
	w.bindMedia(walked, msg)

	if !media.Visible() {
		t.Fatal("attachment box stayed hidden: bindMedia did not resolve its cell")
	}
	cell := w.mediaCells[widgetKey(media)]
	if !cell.overlay.Visible() {
		t.Error("thumbnail overlay is hidden for an image")
	}
	if !cell.action.Visible() {
		t.Error("download action is hidden for an undownloaded image")
	}
	if cell.boundID != "msg-1" {
		t.Errorf("boundID = %q, want msg-1", cell.boundID)
	}
}
