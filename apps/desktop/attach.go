package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

// attachment is a file staged in the composer, waiting for the user to add a
// caption and press send.
type attachment struct {
	path string
	mime string
	size int64
}

// chooseAttachment opens a file picker and stages the chosen file on the
// composer.
func (w *window) chooseAttachment() {
	if w.activeChat == "" {
		return
	}

	dialog := gtk.NewFileDialog()
	dialog.SetTitle("Attach a file")
	dialog.SetModal(true)
	dialog.Open(w.ctx, &w.win.Window, func(result gio.AsyncResulter) {
		file, err := dialog.OpenFinish(result)
		if err != nil {
			// Dismissing the picker reports an error too; it is not worth a toast.
			return
		}
		if path := file.Path(); path != "" {
			w.stageAttachment(path)
		}
	})
}

// stageAttachment shows a file above the composer as the pending attachment.
// Only one file can be staged at a time, matching the one caption the composer
// holds.
func (w *window) stageAttachment(path string) {
	if w.activeChat == "" {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		w.log.Error().Err(err).Str("path", path).Msg("stat attachment")
		w.toast("Could not read that file")
		return
	}

	_, mime := gio.ContentTypeGuess(path, nil)
	w.pending = &attachment{path: path, mime: mime, size: info.Size()}
	w.showAttachmentBar()
	w.messageEntry.GrabFocus()
}

// showAttachmentBar renders the staged file: a thumbnail for images, the
// content type's icon otherwise.
func (w *window) showAttachmentBar() {
	att := w.pending
	w.attachmentName.SetText(filepath.Base(att.path))
	w.attachmentSize.SetText(humanSize(att.size))

	// The composer preview is a still, so only the first frame is used.
	var thumb *gdkpixbuf.Pixbuf
	if strings.HasPrefix(att.mime, "image/") {
		anim, err := w.loadAnimation(att.path)
		if err != nil {
			w.log.Warn().Err(err).Str("path", att.path).Msg("render attachment thumbnail")
		} else {
			thumb = anim.frames[0]
		}
	}
	if thumb != nil {
		w.attachmentThumb.SetPixbuf(thumb)
		w.attachmentThumb.SetVisible(true)
		w.attachmentIcon.SetVisible(false)
	} else {
		w.attachmentThumb.SetVisible(false)
		w.attachmentIcon.SetFromGIcon(gio.ContentTypeGetIcon(att.mime))
		w.attachmentIcon.SetVisible(true)
	}
	w.attachmentBar.SetVisible(true)
	w.messageEntry.SetPlaceholderText("Add a caption")
}

// cancelAttachment drops the staged file without sending it.
func (w *window) cancelAttachment() {
	w.pending = nil
	w.attachmentBar.SetVisible(false)
	w.attachmentThumb.SetVisible(false)
	w.attachmentIcon.SetVisible(false)
	w.messageEntry.SetPlaceholderText("Write a message")
}

// sendAttachment uploads the staged file with the composer text as its caption.
func (w *window) sendAttachment() {
	att := w.pending
	if att == nil || w.activeChat == "" || w.client == nil {
		return
	}

	caption := strings.TrimSpace(w.messageEntry.Text())
	w.messageEntry.SetText("")
	quotedID := w.replyTo.GetId()
	w.toast("Uploading " + filepath.Base(att.path) + "…")
	w.client.SendMedia(w.ctx, w.activeChat, att.path, caption, quotedID, func(err error) {
		if err != nil {
			w.log.Error().Err(err).Str("path", att.path).Msg("send media")
			w.toast("Attachment could not be sent")
			return
		}
		w.cancelAttachment()
		w.toast("Attachment sent")
	})
}

// setupDragAndDrop accepts files dropped anywhere on the window and stages them
// on the composer.
func (w *window) setupDragAndDrop() {
	target := gtk.NewDropTarget(gdk.GTypeFileList, gdk.ActionCopy)
	target.ConnectDrop(func(value *coreglib.Value, _, _ float64) bool {
		if w.activeChat == "" {
			w.toast("Open a chat before dropping files")
			return false
		}

		files, ok := value.GoValue().(*gdk.FileList)
		if !ok {
			return false
		}
		// Only one file can be staged, so the first usable drop wins.
		for _, file := range files.Files() {
			if path := file.Path(); path != "" {
				w.stageAttachment(path)
				return true
			}
		}
		return false
	})
	w.win.AddController(target)
}

// setupPasteShortcut intercepts Ctrl+V before the composer entry sees it, so an
// image on the clipboard is staged as an attachment while text pastes as usual.
func (w *window) setupPasteShortcut() {
	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, _ uint, state gdk.ModifierType) bool {
		if keyval != gdk.KEY_v || state&gdk.ControlMask == 0 {
			return false
		}
		if w.activeChat == "" || !w.win.Clipboard().Formats().ContainGType(gdk.GTypeTexture) {
			return false
		}
		w.pasteClipboardImage()
		return true
	})
	w.win.AddController(keys)
}

// pasteClipboardImage stages an image sitting on the clipboard. The clipboard
// holds pixels rather than a file, so it is written to the cache directory
// first.
func (w *window) pasteClipboardImage() {
	if w.activeChat == "" {
		return
	}

	clipboard := w.win.Clipboard()
	clipboard.ReadTextureAsync(w.ctx, func(result gio.AsyncResulter) {
		texture, err := clipboard.ReadTextureFinish(result)
		if err != nil || texture == nil {
			// No image on the clipboard: let the entry paste text as usual.
			return
		}

		path, err := writeClipboardImage(texture)
		if err != nil {
			w.log.Error().Err(err).Msg("save pasted image")
			w.toast("Could not read the pasted image")
			return
		}
		w.stageAttachment(path)
	})
}

// writeClipboardImage stores a clipboard texture as a PNG under the cache
// directory and returns its path.
func writeClipboardImage(texture gdk.Texturer) (string, error) {
	cache, err := xdgpaths.CacheDir()
	if err != nil {
		return "", err
	}

	path := filepath.Join(cache, fmt.Sprintf("pasted-%d.png", time.Now().UnixNano()))
	if !gdk.BaseTexture(texture).SaveToPNG(path) {
		return "", fmt.Errorf("encode pasted image")
	}
	return path, nil
}
