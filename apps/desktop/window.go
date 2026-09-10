package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/gioutil"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"

	"github.com/zealish/zchat/apps/desktop/client"
	"github.com/zealish/zchat/apps/desktop/daemonctl"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

var (
	chatModelType    = gioutil.NewListModelType[*zchatv1.Chat]()
	messageModelType = gioutil.NewListModelType[*zchatv1.Message]()
)

type window struct {
	ctx  context.Context
	log  zerolog.Logger
	app  *adw.Application
	sock string

	win          *adw.ApplicationWindow
	toastOverlay *adw.ToastOverlay
	mainStack    *gtk.Stack
	qrImage      *gtk.Picture
	splitView    *adw.NavigationSplitView
	contentPage  *adw.NavigationPage
	chatList     *gtk.ListView
	chatFilter   *adw.ToggleGroup
	messageList  *gtk.ListView
	messageScrl  *gtk.ScrolledWindow
	messageEntry *gtk.Entry
	sendButton   *gtk.Button
	searchEntry  *gtk.SearchEntry

	chats             *gioutil.ListModel[*zchatv1.Chat]
	chatSel           *gtk.SingleSelection
	messages          *gioutil.ListModel[*zchatv1.Message]
	messageRows       map[string]*gtk.ListItem
	chatOrder         []*zchatv1.Chat
	chatIndex         map[string]*zchatv1.Chat
	chatRebuildQueued bool
	searchQuery       string
	searchResults     []*zchatv1.Chat
	mediaHandlers     map[*gtk.Button]coreglib.SignalHandle
	showGroups        bool
	stickToBottom     bool
	bottomQueued      bool
	scrollingToBottom bool

	client      *client.Client
	activeChat  string
	activeGroup bool
}

func newWindow(ctx context.Context, app *adw.Application, log zerolog.Logger, socketPath string) *window {
	builder := gtk.NewBuilderFromResource("/com/zealish/ZChat/window.ui")

	w := &window{
		ctx:           ctx,
		log:           log,
		app:           app,
		sock:          socketPath,
		win:           builder.GetObject("window").Cast().(*adw.ApplicationWindow),
		toastOverlay:  builder.GetObject("toast_overlay").Cast().(*adw.ToastOverlay),
		mainStack:     builder.GetObject("main_stack").Cast().(*gtk.Stack),
		qrImage:       builder.GetObject("qr_image").Cast().(*gtk.Picture),
		splitView:     builder.GetObject("split_view").Cast().(*adw.NavigationSplitView),
		contentPage:   builder.GetObject("content_page").Cast().(*adw.NavigationPage),
		chatList:      builder.GetObject("chat_list").Cast().(*gtk.ListView),
		chatFilter:    builder.GetObject("chat_filter").Cast().(*adw.ToggleGroup),
		messageList:   builder.GetObject("message_list").Cast().(*gtk.ListView),
		messageScrl:   builder.GetObject("message_scroll").Cast().(*gtk.ScrolledWindow),
		messageEntry:  builder.GetObject("message_entry").Cast().(*gtk.Entry),
		sendButton:    builder.GetObject("send_button").Cast().(*gtk.Button),
		searchEntry:   builder.GetObject("search_entry").Cast().(*gtk.SearchEntry),
		chats:         chatModelType.New(),
		messages:      messageModelType.New(),
		chatIndex:     make(map[string]*zchatv1.Chat),
		messageRows:   make(map[string]*gtk.ListItem),
		mediaHandlers: make(map[*gtk.Button]coreglib.SignalHandle),
	}

	w.win.SetApplication(&app.Application)
	w.setupChatList()
	w.setupMessageList()
	w.setupComposer()
	w.setupSearch()
	w.setupAutoScroll()
	w.mainStack.SetVisibleChildName("qr")

	go w.connect()
	return w
}

func (w *window) present() { w.win.Present() }

// connect starts the daemon if needed, then attaches the event stream.
func (w *window) connect() {
	if err := daemonctl.EnsureRunning(w.ctx, w.sock); err != nil {
		w.log.Error().Err(err).Msg("ensure daemon")
		glib.IdleAdd(func() { w.toast("Could not start the ZChat daemon — see logs") })
		return
	}

	c, err := client.Dial(w.ctx, w.sock)
	if err != nil {
		w.log.Error().Err(err).Msg("dial daemon")
		glib.IdleAdd(func() { w.toast("Could not connect to the ZChat daemon") })
		return
	}

	glib.IdleAdd(func() {
		w.client = c
		c.StreamEvents(w.ctx, w.onEvent, func(err error) {
			w.log.Warn().Err(err).Msg("event stream interrupted")
			w.toast("Disconnected from daemon")
		})
	})
}

func (w *window) toast(text string) {
	w.toastOverlay.AddToast(adw.NewToast(text))
}

func (w *window) onEvent(evt *zchatv1.Event) {
	switch payload := evt.GetPayload().(type) {
	case *zchatv1.Event_QrUpdated:
		w.showQR(payload.QrUpdated.GetCode())
	case *zchatv1.Event_ConnectionState:
		w.onConnectionState(payload.ConnectionState)
	case *zchatv1.Event_ChatUpdated:
		w.upsertChat(payload.ChatUpdated)
	case *zchatv1.Event_MessageReceived:
		w.notify(payload.MessageReceived)
		w.onMessage(payload.MessageReceived, false)
	case *zchatv1.Event_MessageUpdated:
		w.onMessage(payload.MessageUpdated, true)
	}
}

func (w *window) onConnectionState(state *zchatv1.ConnectionState) {
	switch state.GetStatus() {
	case zchatv1.ConnectionStatus_CONNECTION_STATUS_CONNECTED:
		w.mainStack.SetVisibleChildName("chat")
		w.loadChats()
	case zchatv1.ConnectionStatus_CONNECTION_STATUS_LOGGED_OUT:
		w.mainStack.SetVisibleChildName("qr")
		if msg := state.GetError(); msg != "" {
			w.toast("WhatsApp login required: " + msg)
		}
	}
}

func (w *window) showQR(code string) {
	png, err := qrcode.Encode(code, qrcode.Medium, 320)
	if err != nil {
		w.log.Error().Err(err).Msg("encode qr")
		return
	}
	stream := gio.NewMemoryInputStreamFromBytes(glib.NewBytes(png))
	pixbuf, err := gdkpixbuf.NewPixbufFromStream(w.ctx, stream)
	if err != nil {
		w.log.Error().Err(err).Msg("decode qr image")
		return
	}
	w.qrImage.SetPixbuf(pixbuf)
	w.mainStack.SetVisibleChildName("qr")
}

func (w *window) loadChats() {
	if w.client == nil {
		return
	}
	w.client.GetChats(w.ctx, func(chats []*zchatv1.Chat, err error) {
		if err != nil {
			w.log.Error().Err(err).Msg("load chats")
			w.toast("Could not load chats")
			return
		}
		w.chatOrder = chats
		w.chatIndex = make(map[string]*zchatv1.Chat, len(chats))
		for _, c := range chats {
			w.chatIndex[c.GetJid()] = c
		}
		w.rebuildChatModel()
	})
}

// upsertChat replaces a chat and schedules a sidebar refresh. History sync
// delivers hundreds of these in bursts, so rebuilds are coalesced rather than
// run per event.
func (w *window) upsertChat(chat *zchatv1.Chat) {
	if existing, ok := w.chatIndex[chat.GetJid()]; ok {
		for i, c := range w.chatOrder {
			if c == existing {
				w.chatOrder[i] = chat
				break
			}
		}
	} else {
		w.chatOrder = append(w.chatOrder, chat)
	}
	w.chatIndex[chat.GetJid()] = chat
	w.scheduleChatRebuild()
}

func (w *window) scheduleChatRebuild() {
	if w.chatRebuildQueued {
		return
	}
	w.chatRebuildQueued = true
	glib.TimeoutAdd(150, func() bool {
		w.chatRebuildQueued = false
		w.rebuildChatModel()
		return false
	})
}

func (w *window) rebuildChatModel() {
	// A search shows daemon-ranked results verbatim, ignoring the group filter
	// so a query never silently hides matches on the other tab.
	if w.searchQuery != "" {
		w.chats.Splice(0, w.chats.Len(), w.searchResults...)
		return
	}

	sort.SliceStable(w.chatOrder, func(i, j int) bool {
		a, b := w.chatOrder[i], w.chatOrder[j]
		if a.GetPinned() != b.GetPinned() {
			return a.GetPinned()
		}
		return a.GetUpdatedAt() > b.GetUpdatedAt()
	})

	visible := make([]*zchatv1.Chat, 0, len(w.chatOrder))
	for _, c := range w.chatOrder {
		if c.GetIsGroup() == w.showGroups {
			visible = append(visible, c)
		}
	}

	selected := w.activeChat
	w.chats.Splice(0, w.chats.Len(), visible...)

	if selected != "" {
		for i, c := range visible {
			if c.GetJid() == selected {
				w.chatSel.SetSelected(uint(i))
				break
			}
		}
	}
}

// setupSearch swaps the sidebar between the chat list and daemon-side search
// results as the query changes.
func (w *window) setupSearch() {
	w.searchEntry.ConnectSearchChanged(func() {
		query := strings.TrimSpace(w.searchEntry.Text())
		w.searchQuery = query
		if query == "" {
			w.searchResults = nil
			w.rebuildChatModel()
			return
		}
		if w.client == nil {
			return
		}
		w.client.SearchChats(w.ctx, query, func(q string, chats []*zchatv1.Chat, err error) {
			// Slower earlier queries must not overwrite the newest results.
			if q != w.searchQuery {
				return
			}
			if err != nil {
				w.log.Error().Err(err).Msg("search chats")
				return
			}
			w.searchResults = chats
			w.rebuildChatModel()
		})
	})
}

func (w *window) setupChatList() {
	w.chatSel = gtk.NewSingleSelection(w.chats)
	w.chatSel.SetAutoselect(false)
	w.chatList.SetModel(w.chatSel)

	w.chatFilter.NotifyProperty("active", func() {
		w.showGroups = w.chatFilter.ActiveName() == "groups"
		w.rebuildChatModel()
	})

	factory := gtk.NewSignalListItemFactory()
	factory.ConnectSetup(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)

		row := gtk.NewBox(gtk.OrientationHorizontal, 12)
		row.SetMarginTop(6)
		row.SetMarginBottom(6)
		row.SetMarginStart(6)
		row.SetMarginEnd(6)

		avatar := adw.NewAvatar(40, "", true)
		row.Append(avatar)

		text := gtk.NewBox(gtk.OrientationVertical, 2)
		text.SetHExpand(true)

		name := gtk.NewLabel("")
		name.SetXAlign(0)
		name.SetEllipsize(pango.EllipsizeEnd)
		name.AddCSSClass("heading")
		text.Append(name)

		preview := gtk.NewLabel("")
		preview.SetXAlign(0)
		preview.SetSingleLineMode(true)
		preview.SetEllipsize(pango.EllipsizeEnd)
		preview.AddCSSClass("dim-label")
		text.Append(preview)

		row.Append(text)

		unread := gtk.NewLabel("")
		unread.SetVAlign(gtk.AlignCenter)
		unread.AddCSSClass("zchat-unread")
		row.Append(unread)

		item.SetChild(row)
	})
	factory.ConnectBind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		chat := chatModelType.ObjectValue(item.Item())

		row := item.Child().(*gtk.Box)
		avatar := row.FirstChild().(*adw.Avatar)
		text := avatar.NextSibling().(*gtk.Box)
		unread := text.NextSibling().(*gtk.Label)

		name := text.FirstChild().(*gtk.Label)
		preview := name.NextSibling().(*gtk.Label)

		display := displayName(chat)
		avatar.SetText(display)
		name.SetText(display)
		preview.SetText(chat.GetLastMessage())

		if chat.GetUnread() > 0 {
			unread.SetText(fmt.Sprintf("%d", chat.GetUnread()))
			unread.SetVisible(true)
		} else {
			unread.SetVisible(false)
		}
	})
	w.chatList.SetFactory(&factory.ListItemFactory)

	w.chatList.ConnectActivate(func(position uint) {
		if int(position) >= w.chats.Len() {
			return
		}
		w.openChat(w.chats.At(int(position)))
	})
}

func (w *window) openChat(chat *zchatv1.Chat) {
	w.activeChat = chat.GetJid()
	w.setComposerEnabled(true)
	w.activeGroup = chat.GetIsGroup()
	w.contentPage.SetTitle(displayName(chat))
	clear(w.messageRows)
	w.messages.Splice(0, w.messages.Len())
	w.splitView.SetShowContent(true)

	if w.client == nil {
		return
	}
	w.client.GetMessages(w.ctx, w.activeChat, func(chatJID string, msgs []*zchatv1.Message, err error) {
		if err != nil {
			w.log.Error().Err(err).Msg("load messages")
			w.toast("Could not load messages")
			return
		}
		if chatJID != w.activeChat {
			return
		}
		w.messages.Splice(0, w.messages.Len(), msgs...)
		w.scrollToBottom()
	})
}

func (w *window) onMessage(msg *zchatv1.Message, isUpdate bool) {
	if msg.GetChatJid() != w.activeChat {
		return
	}

	for i := range w.messages.Len() {
		existing := w.messages.At(i)
		if existing.GetId() != msg.GetId() {
			continue
		}
		// Splicing the model would destroy and recreate the row widget, which
		// flickers and drops the ListView's scroll anchor. Sent-status updates
		// only change a few fields, so the bound row is refreshed directly.
		existing.Body = msg.GetBody()
		existing.SenderName = msg.GetSenderName()
		existing.Timestamp = msg.GetTimestamp()
		existing.Status = msg.GetStatus()
		existing.Media = msg.GetMedia()
		if item, ok := w.messageRows[existing.GetId()]; ok {
			w.bindMessageRow(item, existing)
		}
		return
	}
	if isUpdate {
		return
	}

	atBottom := w.nearBottom()
	w.messages.Append(msg)
	if atBottom {
		w.scrollToBottom()
	}
}

func (w *window) nearBottom() bool {
	adj := w.messageScrl.VAdjustment()
	if adj.Upper() <= adj.PageSize() {
		return true
	}
	return adj.Upper()-adj.PageSize()-adj.Value() < 50
}

// scrollToBottom pins the view to the newest message. The ListView measures
// newly appended rows asynchronously, so the pin is held until the
// adjustment's "changed" signal reports the settled layout.
func (w *window) scrollToBottom() {
	w.stickToBottom = true
	if w.bottomQueued {
		return
	}
	w.bottomQueued = true
	glib.IdleAdd(func() {
		w.bottomQueued = false
		w.applyScrollToBottom()
	})
}

func (w *window) applyScrollToBottom() {
	last := w.messages.Len() - 1
	if last < 0 {
		return
	}
	w.scrollingToBottom = true
	// ScrollTo anchors the ListView on the newest row; the adjustment nudge
	// covers the frame before that row has been measured.
	w.messageList.ScrollTo(uint(last), gtk.ListScrollNone, nil)
	adj := w.messageScrl.VAdjustment()
	adj.SetValue(adj.Upper() - adj.PageSize())
	w.scrollingToBottom = false
}

func (w *window) setupAutoScroll() {
	adj := w.messageScrl.VAdjustment()
	// "changed" fires once the ListView has measured newly appended rows.
	adj.ConnectChanged(func() {
		if w.stickToBottom {
			w.applyScrollToBottom()
		}
	})
	adj.ConnectValueChanged(func() {
		// Scrolling away from the bottom releases the pin, but neither our own
		// programmatic scroll nor the jump a pending relayout causes must.
		if w.scrollingToBottom || w.bottomQueued {
			return
		}
		w.stickToBottom = adj.Upper()-adj.PageSize()-adj.Value() < 50
	})
}

func (w *window) setupMessageList() {
	selection := gtk.NewNoSelection(w.messages)
	w.messageList.SetModel(selection)

	factory := gtk.NewSignalListItemFactory()
	factory.ConnectSetup(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)

		outer := gtk.NewBox(gtk.OrientationHorizontal, 0)
		outer.SetMarginTop(2)
		outer.SetMarginBottom(2)
		outer.SetMarginStart(12)
		outer.SetMarginEnd(12)
		outer.SetHExpand(true)

		bubble := gtk.NewBox(gtk.OrientationVertical, 2)
		bubble.AddCSSClass("zchat-bubble")
		// hexpand gives the bubble the full row allocation so halign can pin
		// it to the left (incoming) or right (outgoing) edge.
		bubble.SetHExpand(true)

		sender := gtk.NewLabel("")
		sender.SetXAlign(0)
		sender.AddCSSClass("zchat-sender")
		bubble.Append(sender)

		// Attachment area: an optional preview above an open/download button.
		media := gtk.NewBox(gtk.OrientationVertical, 4)
		media.SetVisible(false)

		preview := gtk.NewPicture()
		preview.SetSizeRequest(240, 180)
		preview.SetContentFit(gtk.ContentFitCover)
		preview.AddCSSClass("zchat-media")
		media.Append(preview)

		action := gtk.NewButtonWithLabel("")
		action.SetHAlign(gtk.AlignStart)
		media.Append(action)

		bubble.Append(media)

		body := gtk.NewLabel("")
		body.SetXAlign(0)
		body.SetWrap(true)
		// URLs have no break points, so word-only wrapping would make the
		// label's minimum width as wide as the whole link and stretch the
		// content pane past the window.
		body.SetWrapMode(pango.WrapWordChar)
		body.SetNaturalWrapMode(gtk.NaturalWrapWord)
		body.SetMaxWidthChars(48)
		body.SetSelectable(true)
		body.AddCSSClass("zchat-message")
		bubble.Append(body)

		meta := gtk.NewLabel("")
		meta.SetXAlign(1)
		meta.AddCSSClass("zchat-timestamp")
		bubble.Append(meta)

		outer.Append(bubble)
		item.SetChild(outer)
	})
	factory.ConnectBind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		msg := messageModelType.ObjectValue(item.Item())
		w.messageRows[msg.GetId()] = item
		w.bindMessageRow(item, msg)
	})
	factory.ConnectUnbind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		msg := messageModelType.ObjectValue(item.Item())
		if w.messageRows[msg.GetId()] == item {
			delete(w.messageRows, msg.GetId())
		}
	})
	w.messageList.SetFactory(&factory.ListItemFactory)
}

func (w *window) bindMessageRow(item *gtk.ListItem, msg *zchatv1.Message) {
	outer := item.Child().(*gtk.Box)
	bubble := outer.FirstChild().(*gtk.Box)
	sender := bubble.FirstChild().(*gtk.Label)
	media := sender.NextSibling().(*gtk.Box)
	body := media.NextSibling().(*gtk.Label)
	meta := body.NextSibling().(*gtk.Label)

	w.bindMedia(media, msg)

	body.SetText(msg.GetBody())

	if msg.GetOutgoing() {
		bubble.SetHAlign(gtk.AlignEnd)
		bubble.RemoveCSSClass("zchat-bubble-in")
		bubble.AddCSSClass("zchat-bubble-out")
		sender.SetVisible(false)
	} else {
		bubble.SetHAlign(gtk.AlignStart)
		bubble.RemoveCSSClass("zchat-bubble-out")
		bubble.AddCSSClass("zchat-bubble-in")
		if w.activeGroup {
			sender.SetText(senderName(msg))
			sender.SetVisible(true)
		} else {
			sender.SetVisible(false)
		}
	}

	stamp := time.Unix(msg.GetTimestamp(), 0).Format("15:04")
	meta.RemoveCSSClass("accent")
	meta.RemoveCSSClass("error")
	if msg.GetOutgoing() {
		glyph, css := statusGlyph(msg.GetStatus())
		meta.SetText(stamp + " " + glyph)
		if css != "" {
			meta.AddCSSClass(css)
		}
	} else {
		meta.SetText(stamp)
	}
}

// bindMedia renders a message's attachment: the WhatsApp thumbnail (or the
// downloaded file itself) plus a button that downloads on demand and opens the
// result with the desktop's default handler.
func (w *window) bindMedia(media *gtk.Box, msg *zchatv1.Message) {
	info := msg.GetMedia()
	preview := media.FirstChild().(*gtk.Picture)
	action := preview.NextSibling().(*gtk.Button)

	// Rows are recycled, so the previous message's click handler must go.
	if handle, ok := w.mediaHandlers[action]; ok {
		action.HandlerDisconnect(handle)
		delete(w.mediaHandlers, action)
	}

	if info == nil {
		media.SetVisible(false)
		return
	}
	media.SetVisible(true)

	local := info.GetPath()
	switch {
	case local != "" && isVisualMedia(msg.GetType()):
		preview.SetFilename(local)
		preview.SetVisible(true)
	case len(info.GetThumbnail()) > 0:
		if pixbuf, err := pixbufFromBytes(w.ctx, info.GetThumbnail()); err == nil {
			preview.SetPixbuf(pixbuf)
			preview.SetVisible(true)
		} else {
			preview.SetVisible(false)
		}
	default:
		preview.SetVisible(false)
	}

	id := msg.GetId()
	if local != "" {
		action.SetLabel("Open")
		w.mediaHandlers[action] = action.ConnectClicked(func() { w.openFile(local) })
		return
	}

	action.SetLabel(downloadLabel(msg.GetType(), info.GetSize()))
	w.mediaHandlers[action] = action.ConnectClicked(func() {
		action.SetSensitive(false)
		action.SetLabel("Downloading…")
		w.client.DownloadMedia(w.ctx, id, func(updated *zchatv1.Message, err error) {
			action.SetSensitive(true)
			if err != nil {
				w.log.Error().Err(err).Str("id", id).Msg("download media")
				w.toast("Download failed")
				action.SetLabel("Retry download")
				return
			}
			w.onMessage(updated, true)
		})
	})
}

func (w *window) openFile(path string) {
	launcher := gtk.NewFileLauncher(gio.NewFileForPath(path))
	launcher.Launch(w.ctx, &w.win.Window, nil)
}

func isVisualMedia(kind string) bool {
	return kind == "image" || kind == "sticker"
}

func pixbufFromBytes(ctx context.Context, data []byte) (*gdkpixbuf.Pixbuf, error) {
	stream := gio.NewMemoryInputStreamFromBytes(glib.NewBytes(data))
	return gdkpixbuf.NewPixbufFromStream(ctx, stream)
}

func downloadLabel(kind string, size int64) string {
	noun := "file"
	switch kind {
	case "image":
		noun = "photo"
	case "video":
		noun = "video"
	case "audio":
		noun = "audio"
	case "sticker":
		noun = "sticker"
	case "document":
		noun = "document"
	}
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

func (w *window) setComposerEnabled(enabled bool) {
	w.messageEntry.SetSensitive(enabled)
	w.sendButton.SetSensitive(enabled)
}

func (w *window) setupComposer() {
	send := func() {
		body := strings.TrimSpace(w.messageEntry.Text())
		if body == "" || w.activeChat == "" || w.client == nil {
			return
		}
		w.messageEntry.SetText("")
		w.client.SendMessage(w.ctx, w.activeChat, body, func(err error) {
			if err != nil {
				w.log.Error().Err(err).Msg("send message")
				w.toast("Message could not be sent")
			}
		})
	}

	w.sendButton.ConnectClicked(send)
	w.messageEntry.ConnectActivate(send)
	w.setComposerEnabled(false)
}

func statusGlyph(status zchatv1.MessageStatus) (glyph, cssClass string) {
	switch status {
	case zchatv1.MessageStatus_MESSAGE_STATUS_PENDING:
		return "○", ""
	case zchatv1.MessageStatus_MESSAGE_STATUS_SENT:
		return "✓", ""
	case zchatv1.MessageStatus_MESSAGE_STATUS_DELIVERED:
		return "✓✓", ""
	case zchatv1.MessageStatus_MESSAGE_STATUS_READ:
		return "✓✓", "accent"
	case zchatv1.MessageStatus_MESSAGE_STATUS_FAILED:
		return "!", "error"
	default:
		return "", ""
	}
}

func displayName(chat *zchatv1.Chat) string {
	if name := chat.GetName(); name != "" {
		return name
	}
	return jidUser(chat.GetJid())
}

func senderName(msg *zchatv1.Message) string {
	if name := msg.GetSenderName(); name != "" {
		return name
	}
	return jidUser(msg.GetSender())
}

func jidUser(jid string) string {
	if at := strings.IndexByte(jid, '@'); at > 0 {
		return jid[:at]
	}
	return jid
}
