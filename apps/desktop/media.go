package main

import (
	"crypto/sha256"
	"fmt"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// Attachment kinds, matching the type strings the daemon puts on a message.
const (
	mediaKindImage    = "image"
	mediaKindSticker  = "sticker"
	mediaKindVideo    = "video"
	mediaKindAudio    = "audio"
	mediaKindDocument = "document"
)

// Preview bounds. WhatsApp keeps an inline attachment inside a narrow column
// and lets tall images run further down than wide ones, rather than letterboxing
// everything into one box.
const (
	previewMaxWidth  = 320
	previewMaxHeight = 400
	previewMinWidth  = 140
	stickerMaxSize   = 180
)

// mediaCell is the attachment area of a recycled message row. The widgets are
// built once in the list factory's setup pass and refilled on every bind, so
// they are looked up through a map instead of being rediscovered by walking
// siblings.
type mediaCell struct {
	overlay *gtk.Overlay
	preview *gtk.Picture
	badge   *gtk.Label

	// The circular action sits on top of the thumbnail: download before the
	// file is here, play once a video is.
	action     *gtk.Button
	actionIcon *gtk.Image
	spinner    *gtk.Spinner

	// Documents and audio get a row instead of a thumbnail.
	fileRow    *gtk.Box
	fileIcon   *gtk.Image
	fileName   *gtk.Label
	fileSize   *gtk.Label
	fileAction *gtk.Button

	// activate runs when the thumbnail itself is clicked.
	activate func()
	// boundID is the message the cell currently shows. Download callbacks
	// check it because a row may have been recycled while one was in flight.
	boundID string
}

// widgetKey identifies a widget by its underlying GObject.
//
// gotk4 allocates a fresh Go wrapper every time a widget is reached through the
// tree — FirstChild, NextSibling and friends all return a new pointer — so a
// map keyed on the wrapper never finds an entry stored under a different one.
// The GObject address behind it is stable for the widget's lifetime.
func widgetKey(widget gtk.Widgetter) uintptr {
	return coreglib.BaseObject(widget).Native()
}

// newMediaCell builds a row's attachment widgets and registers them against
// their container.
func (w *window) newMediaCell(media *gtk.Box) {
	cell := &mediaCell{}

	cell.preview = gtk.NewPicture()
	cell.preview.SetContentFit(gtk.ContentFitCover)
	cell.preview.AddCSSClass("zchat-media")

	cell.overlay = gtk.NewOverlay()
	cell.overlay.SetHAlign(gtk.AlignStart)
	cell.overlay.SetChild(cell.preview)

	cell.badge = gtk.NewLabel("")
	cell.badge.SetHAlign(gtk.AlignEnd)
	cell.badge.SetVAlign(gtk.AlignEnd)
	cell.badge.SetCanTarget(false)
	cell.badge.SetVisible(false)
	cell.badge.AddCSSClass("zchat-media-badge")
	cell.overlay.AddOverlay(cell.badge)

	cell.actionIcon = gtk.NewImageFromIconName("folder-download-symbolic")
	cell.actionIcon.SetPixelSize(20)
	cell.spinner = gtk.NewSpinner()
	cell.spinner.SetVisible(false)

	actionContent := gtk.NewBox(gtk.OrientationHorizontal, 0)
	actionContent.SetHAlign(gtk.AlignCenter)
	actionContent.Append(cell.actionIcon)
	actionContent.Append(cell.spinner)

	cell.action = gtk.NewButton()
	cell.action.SetChild(actionContent)
	cell.action.SetHAlign(gtk.AlignCenter)
	cell.action.SetVAlign(gtk.AlignCenter)
	cell.action.AddCSSClass("zchat-media-action")
	cell.overlay.AddOverlay(cell.action)

	// Clicking the thumbnail opens the viewer. The overlaid button claims its
	// own presses, so the two never fire together.
	click := gtk.NewGestureClick()
	click.SetButton(gdk.BUTTON_PRIMARY)
	click.ConnectReleased(func(_ int, _, _ float64) {
		if cell.activate != nil {
			cell.activate()
		}
	})
	cell.overlay.AddController(click)
	media.Append(cell.overlay)

	cell.fileRow = gtk.NewBox(gtk.OrientationHorizontal, 8)
	cell.fileRow.SetVisible(false)
	cell.fileRow.AddCSSClass("zchat-media-file")

	cell.fileIcon = gtk.NewImageFromIconName("text-x-generic-symbolic")
	cell.fileIcon.SetPixelSize(32)
	cell.fileRow.Append(cell.fileIcon)

	fileText := gtk.NewBox(gtk.OrientationVertical, 0)
	fileText.SetHExpand(true)
	fileText.SetVAlign(gtk.AlignCenter)

	cell.fileName = gtk.NewLabel("")
	cell.fileName.SetXAlign(0)
	cell.fileName.SetEllipsize(pango.EllipsizeMiddle)
	cell.fileName.SetMaxWidthChars(28)
	fileText.Append(cell.fileName)

	cell.fileSize = gtk.NewLabel("")
	cell.fileSize.SetXAlign(0)
	cell.fileSize.SetEllipsize(pango.EllipsizeEnd)
	cell.fileSize.AddCSSClass("dim-label")
	cell.fileSize.AddCSSClass("caption")
	fileText.Append(cell.fileSize)
	cell.fileRow.Append(fileText)

	cell.fileAction = gtk.NewButtonFromIconName("folder-download-symbolic")
	cell.fileAction.SetVAlign(gtk.AlignCenter)
	cell.fileAction.AddCSSClass("flat")
	cell.fileAction.AddCSSClass("circular")
	cell.fileRow.Append(cell.fileAction)
	media.Append(cell.fileRow)

	w.mediaCells[widgetKey(media)] = cell
}

// bindMedia renders a message's attachment: the WhatsApp thumbnail, or the
// downloaded image itself, with the download or play action overlaid on it the
// way WhatsApp does. Photos and videos open in the in-app viewer.
func (w *window) bindMedia(media *gtk.Box, msg *zchatv1.Message) {
	cell, ok := w.mediaCells[widgetKey(media)]
	if !ok {
		return
	}

	// Rows are recycled, so the previous message's handlers and running
	// animation must go before anything else.
	w.disconnectMedia(cell.action)
	w.disconnectMedia(cell.fileAction)
	w.stopAnimation(cell.preview)
	cell.activate = nil
	cell.stopSpinner()

	info := msg.GetMedia()
	if info == nil {
		media.SetVisible(false)
		return
	}
	media.SetVisible(true)
	cell.boundID = msg.GetId()

	if isVisualMedia(msg.GetType()) {
		cell.overlay.SetVisible(true)
		cell.fileRow.SetVisible(false)
		w.bindVisualMedia(cell, msg, info)
		return
	}
	cell.overlay.SetVisible(false)
	cell.fileRow.SetVisible(true)
	w.bindFileMedia(cell, msg, info)
}

// bindVisualMedia fills the thumbnail half of a cell: photos, stickers and
// videos.
func (w *window) bindVisualMedia(cell *mediaCell, msg *zchatv1.Message, info *zchatv1.MediaInfo) {
	kind := msg.GetType()
	local := info.GetPath()

	width, height := previewSize(kind, info)
	cell.preview.SetSizeRequest(width, height)

	previewKey := widgetKey(cell.preview)
	switch {
	case local != "" && isDecodableImage(kind):
		// Decoding is asynchronous, so the row may already show another
		// message by the time it lands. The path the preview was last bound
		// to says whether the result is still wanted.
		w.previewPaths[previewKey] = local
		if thumb := info.GetThumbnail(); len(thumb) > 0 {
			w.showThumbnail(cell.preview, thumb)
		}
		w.loadAnimation(local, func(anim *animation) {
			if anim == nil || w.previewPaths[previewKey] != local {
				return
			}
			w.stopAnimation(cell.preview)
			w.showAnimation(cell.preview, anim)
			cell.preview.RemoveCSSClass("zchat-media-empty")
		})
	case len(info.GetThumbnail()) > 0:
		delete(w.previewPaths, previewKey)
		w.showThumbnail(cell.preview, info.GetThumbnail())
	default:
		// Nothing to paint yet: a flat placeholder keeps the download button
		// on a surface rather than floating in the bubble.
		delete(w.previewPaths, previewKey)
		cell.preview.SetPaintable(nil)
		cell.preview.AddCSSClass("zchat-media-empty")
	}

	switch {
	case kind == mediaKindVideo && info.GetDuration() > 0:
		cell.badge.SetText(formatDuration(int(info.GetDuration())))
		cell.badge.SetVisible(true)
	case local == "" && info.GetSize() > 0:
		cell.badge.SetText(humanSize(info.GetSize()))
		cell.badge.SetVisible(true)
	default:
		cell.badge.SetVisible(false)
	}

	if local == "" {
		cell.showAction("folder-download-symbolic", downloadLabel(kind, info.GetSize()))
		w.connectMedia(cell.action, func() { w.downloadInto(cell, msg.GetId()) })
		cell.overlay.SetCursorFromName("default")
		return
	}

	if !isViewableMedia(kind) {
		// A downloaded sticker is just shown inline; WhatsApp has no
		// fullscreen view for one.
		cell.action.SetVisible(false)
		cell.overlay.SetCursorFromName("default")
		return
	}

	cell.overlay.SetCursorFromName("pointer")
	cell.activate = func() { w.showMediaViewer(msg) }
	if kind == mediaKindVideo {
		cell.showAction("media-playback-start-symbolic", "Play video")
		w.connectMedia(cell.action, func() { w.showMediaViewer(msg) })
		return
	}
	cell.action.SetVisible(false)
}

// bindFileMedia fills the document/audio half of a cell, which WhatsApp draws
// as a named row rather than a thumbnail.
func (w *window) bindFileMedia(cell *mediaCell, msg *zchatv1.Message, info *zchatv1.MediaInfo) {
	kind := msg.GetType()
	local := info.GetPath()

	cell.fileIcon.SetFromGIcon(gio.ContentTypeGetIcon(info.GetMime()))

	name := info.GetFilename()
	if name == "" {
		name = mediaNoun(kind)
	}
	cell.fileName.SetText(name)
	cell.fileSize.SetText(fileSubtitle(kind, info))

	if local == "" {
		cell.fileAction.SetIconName("folder-download-symbolic")
		cell.fileAction.SetTooltipText(downloadLabel(kind, info.GetSize()))
		w.connectMedia(cell.fileAction, func() { w.downloadInto(cell, msg.GetId()) })
		return
	}
	cell.fileAction.SetIconName("document-open-symbolic")
	cell.fileAction.SetTooltipText("Open")
	w.connectMedia(cell.fileAction, func() { w.openFile(local) })
}

// downloadInto fetches an attachment and swaps the overlay button for a spinner
// while it runs.
func (w *window) downloadInto(cell *mediaCell, id string) {
	if w.client == nil {
		return
	}
	cell.startSpinner()
	w.client.DownloadMedia(w.ctx, id, func(updated *zchatv1.Message, err error) {
		// The row may have been recycled onto another message in the
		// meantime, in which case its widgets belong to that one now.
		if cell.boundID != id {
			return
		}
		cell.stopSpinner()
		if err != nil {
			w.log.Error().Err(err).Str("id", id).Msg("download media")
			w.toast("Download failed")
			cell.showAction("view-refresh-symbolic", "Retry download")
			cell.fileAction.SetIconName("view-refresh-symbolic")
			return
		}
		w.onMessage(updated, true)
	})
}

func (w *window) connectMedia(button *gtk.Button, run func()) {
	w.mediaHandlers[widgetKey(button)] = button.ConnectClicked(run)
}

func (w *window) disconnectMedia(button *gtk.Button) {
	key := widgetKey(button)
	if handle, ok := w.mediaHandlers[key]; ok {
		button.HandlerDisconnect(handle)
		delete(w.mediaHandlers, key)
	}
}

func (c *mediaCell) showAction(icon, tooltip string) {
	c.actionIcon.SetFromIconName(icon)
	c.actionIcon.SetVisible(true)
	c.action.SetTooltipText(tooltip)
	c.action.SetVisible(true)
	c.action.SetSensitive(true)
}

func (c *mediaCell) startSpinner() {
	c.action.SetSensitive(false)
	c.fileAction.SetSensitive(false)
	c.actionIcon.SetVisible(false)
	c.spinner.SetVisible(true)
	c.spinner.Start()
}

func (c *mediaCell) stopSpinner() {
	c.spinner.SetVisible(false)
	c.actionIcon.SetVisible(true)
	c.action.SetSensitive(true)
	c.fileAction.SetSensitive(true)
}

// thumbCacheLimit caps the decoded WhatsApp thumbnails kept around. They are
// small, so many more fit than full previews.
const thumbCacheLimit = 256

// showThumbnail paints the small inline preview WhatsApp ships with a message.
// It stands in until the full image finishes decoding, so an attachment never
// pops in from an empty box.
//
// Decoding costs several milliseconds, and the ListView rebinds a row every
// time it scrolls past, so the result is cached against the thumbnail bytes.
func (w *window) showThumbnail(preview *gtk.Picture, thumbnail []byte) {
	key := thumbKey(thumbnail)
	paintable, ok := w.thumbCache[key]
	if !ok {
		pixbuf, err := pixbufFromBytes(w.ctx, thumbnail)
		if err != nil {
			preview.SetPaintable(nil)
			preview.AddCSSClass("zchat-media-empty")
			return
		}
		paintable = gdk.NewTextureForPixbuf(pixbuf)
		if len(w.thumbCache) >= thumbCacheLimit {
			clear(w.thumbCache)
		}
		w.thumbCache[key] = paintable
	}
	preview.SetPaintable(paintable)
	preview.RemoveCSSClass("zchat-media-empty")
}

// thumbKey identifies thumbnail bytes without holding on to them.
func thumbKey(thumbnail []byte) [32]byte {
	return sha256.Sum256(thumbnail)
}

// isVisualMedia reports whether an attachment is drawn as a thumbnail rather
// than a named file row.
func isVisualMedia(kind string) bool {
	return kind == mediaKindImage || kind == mediaKindSticker || kind == mediaKindVideo
}

// isDecodableImage reports whether the downloaded file itself can replace the
// WhatsApp thumbnail.
func isDecodableImage(kind string) bool {
	return kind == mediaKindImage || kind == mediaKindSticker
}

// isViewableMedia reports whether an attachment opens in the in-app viewer.
func isViewableMedia(kind string) bool {
	return kind == mediaKindImage || kind == mediaKindVideo
}

// previewSize fits an attachment's real dimensions into the inline column,
// falling back to a 4:3 box when WhatsApp did not report any.
func previewSize(kind string, info *zchatv1.MediaInfo) (int, int) {
	maxWidth, maxHeight := previewMaxWidth, previewMaxHeight
	if kind == mediaKindSticker {
		maxWidth, maxHeight = stickerMaxSize, stickerMaxSize
	}

	width, height := int(info.GetWidth()), int(info.GetHeight())
	if width <= 0 || height <= 0 {
		return maxWidth, maxWidth * 3 / 4
	}

	if width > maxWidth {
		height = height * maxWidth / width
		width = maxWidth
	}
	if height > maxHeight {
		width = width * maxHeight / height
		height = maxHeight
	}
	if kind != mediaKindSticker && width < previewMinWidth {
		width = previewMinWidth
	}
	if height < 1 {
		height = 1
	}
	return width, height
}

// formatDuration renders a clip length the way WhatsApp labels one.
func formatDuration(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	if hours := seconds / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// fileSubtitle is the second line of a document or audio row: its length for
// audio, its size otherwise.
func fileSubtitle(kind string, info *zchatv1.MediaInfo) string {
	if kind == mediaKindAudio && info.GetDuration() > 0 {
		return formatDuration(int(info.GetDuration()))
	}
	if info.GetSize() <= 0 {
		return mediaKindNoun(kind)
	}
	return humanSize(info.GetSize())
}

// mediaKindNoun names an attachment kind in running text, where mediaNoun's
// capitalised form would read as a heading.
func mediaKindNoun(kind string) string {
	switch kind {
	case mediaKindImage:
		return "photo"
	case mediaKindVideo:
		return "video"
	case mediaKindAudio:
		return "audio"
	case mediaKindSticker:
		return "sticker"
	case mediaKindDocument:
		return "document"
	}
	return "file"
}

func downloadLabel(kind string, size int64) string {
	noun := mediaKindNoun(kind)
	if size <= 0 {
		return "Download " + noun
	}
	return fmt.Sprintf("Download %s (%s)", noun, humanSize(size))
}

func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGT"[exp])
}
