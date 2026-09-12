package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// showMediaViewer opens a downloaded photo or video in a window of its own,
// laid out the way WhatsApp's viewer is: sender and timestamp on the left of
// the header, save and close on the right, the caption along the bottom, and
// the media filling everything between them on a dark backdrop.
//
// Nothing is handed to an external application. Escape and Ctrl+W close it,
// matching the rest of the window's shortcuts.
func (w *window) showMediaViewer(msg *zchatv1.Message) {
	info := msg.GetMedia()
	path := info.GetPath()
	if path == "" || !isViewableMedia(msg.GetType()) {
		return
	}

	viewer := adw.NewWindow()
	viewer.SetTransientFor(&w.win.Window)
	viewer.SetModal(true)
	viewer.SetDefaultSize(900, 680)
	viewer.SetTitle(viewerTitle(msg))

	header := adw.NewHeaderBar()
	header.SetTitleWidget(adw.NewWindowTitle(viewerTitle(msg), viewerSubtitle(msg)))

	save := gtk.NewButtonFromIconName("document-save-symbolic")
	save.SetTooltipText("Save as…")
	save.AddCSSClass("flat")
	save.ConnectClicked(func() { w.saveMediaAs(viewer, info, msg.GetType()) })
	header.PackEnd(save)

	content := gtk.NewBox(gtk.OrientationVertical, 0)
	content.SetHExpand(true)
	content.SetVExpand(true)
	content.AddCSSClass("zchat-viewer")

	// A video needs its stream paused when the window goes away, otherwise
	// audio keeps playing after the viewer is gone. The stream is built here
	// rather than read back off the Video, whose getter returns whichever
	// concrete stream GTK picked.
	var stream *gtk.MediaFile
	if msg.GetType() == mediaKindVideo {
		stream = gtk.NewMediaFileForFilename(path)
		stream.SetLoop(false)
		video := gtk.NewVideoForMediaStream(stream)
		video.SetAutoplay(true)
		video.SetHExpand(true)
		video.SetVExpand(true)
		content.Append(video)
	} else {
		picture := gtk.NewPictureForFilename(path)
		// Contain, not cover: the viewer shows the whole photo, and
		// can-shrink lets an oversized one fit the window.
		picture.SetContentFit(gtk.ContentFitContain)
		picture.SetCanShrink(true)
		picture.SetHExpand(true)
		picture.SetVExpand(true)
		content.Append(picture)
	}

	if caption := viewerCaption(msg); caption != "" {
		label := gtk.NewLabel(caption)
		label.SetWrap(true)
		label.SetWrapMode(pango.WrapWordChar)
		label.SetSelectable(true)
		label.SetXAlign(0)
		label.AddCSSClass("zchat-viewer-caption")

		scroll := gtk.NewScrolledWindow()
		scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
		scroll.SetMaxContentHeight(120)
		scroll.SetPropagateNaturalHeight(true)
		scroll.SetChild(label)
		content.Append(scroll)
	}

	view := adw.NewToolbarView()
	view.AddTopBar(header)
	view.SetContent(content)
	viewer.SetContent(view)

	keys := gtk.NewEventControllerKey()
	keys.ConnectKeyPressed(func(keyval, _ uint, state gdk.ModifierType) bool {
		ctrlW := keyval == gdk.KEY_w && state&gdk.ControlMask != 0
		if keyval == gdk.KEY_Escape || ctrlW {
			viewer.Close()
			return true
		}
		return false
	})
	viewer.AddController(keys)

	viewer.ConnectCloseRequest(func() bool {
		if stream != nil {
			stream.Pause()
		}
		return false
	})
	viewer.Present()
}

// saveMediaAs copies a downloaded attachment out of the media cache to wherever
// the user picks. The file is already local, so this is a plain copy rather
// than another download.
func (w *window) saveMediaAs(parent *adw.Window, info *zchatv1.MediaInfo, kind string) {
	name := info.GetFilename()
	if name == "" {
		name = filepath.Base(info.GetPath())
	}

	dialog := gtk.NewFileDialog()
	dialog.SetTitle("Save " + mediaKindNoun(kind))
	dialog.SetModal(true)
	dialog.SetInitialName(name)
	dialog.Save(w.ctx, &parent.Window, func(result gio.AsyncResulter) {
		file, err := dialog.SaveFinish(result)
		if err != nil {
			// Dismissing the picker reports an error too; it is not worth a toast.
			return
		}
		dest := file.Path()
		if dest == "" {
			return
		}
		if err := copyFile(info.GetPath(), dest); err != nil {
			w.log.Error().Err(err).Str("dest", dest).Msg("save media")
			w.toast("Could not save the file")
			return
		}
		w.toast("Saved to " + dest)
	})
}

func copyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

// viewerTitle names the sender, the way WhatsApp heads its viewer.
func viewerTitle(msg *zchatv1.Message) string {
	if msg.GetOutgoing() {
		return "You"
	}
	return senderName(msg)
}

func viewerSubtitle(msg *zchatv1.Message) string {
	return time.Unix(msg.GetTimestamp(), 0).Format("2 Jan 2006, 15:04")
}

// viewerCaption is the text WhatsApp prints under the media: the caption the
// attachment was sent with, which the daemon also mirrors onto the body.
func viewerCaption(msg *zchatv1.Message) string {
	if caption := msg.GetMedia().GetCaption(); caption != "" {
		return caption
	}
	return msg.GetBody()
}
