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
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
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
	contactModelType = gioutil.NewListModelType[*zchatv1.Contact]()
	messageModelType = gioutil.NewListModelType[*zchatv1.Message]()
)

// bubbleMaxWidth caps a message bubble's width in pixels, mirroring the
// readable column WhatsApp Web keeps its bubbles within.
const bubbleMaxWidth = 560

type window struct {
	ctx  context.Context
	log  zerolog.Logger
	app  *adw.Application
	sock string

	win             *adw.ApplicationWindow
	toastOverlay    *adw.ToastOverlay
	mainStack       *gtk.Stack
	qrImage         *gtk.Picture
	syncPage        *adw.StatusPage
	syncProgress    *gtk.ProgressBar
	syncDetail      *gtk.Label
	syncCounts      *gtk.Label
	syncing         bool
	syncPulse       glib.SourceHandle
	splitView       *adw.NavigationSplitView
	contentPage     *adw.NavigationPage
	contentTitle    *adw.WindowTitle
	headerAvatar    *adw.Avatar
	chatList        *gtk.ListView
	chatFilter      *adw.ToggleGroup
	messageList     *gtk.ListView
	messageScrl     *gtk.ScrolledWindow
	messageEntry    *gtk.Entry
	searchEntry     *gtk.SearchEntry
	sendButton      *gtk.Button
	attachButton    *gtk.Button
	emojiButton     *gtk.MenuButton
	reactionChooser *gtk.EmojiChooser
	reactionTarget  string
	newChatButton   *gtk.Button
	chatInfoButton  *gtk.Button
	newChatDialog   *adw.Dialog
	connected       bool
	ownJID          string
	connStatus      string
	replyBar        *gtk.Box
	replySender     *gtk.Label
	replyBody       *gtk.Label
	replyCancel     *gtk.Button
	findBar         *gtk.SearchBar
	findEntry       *gtk.SearchEntry
	findStatus      *gtk.Label
	findPrev        *gtk.Button
	findNext        *gtk.Button
	findMatches     []string
	findIndex       int
	findCurrent     string

	attachmentBar    *gtk.Box
	attachmentThumb  *gtk.Picture
	attachmentIcon   *gtk.Image
	attachmentName   *gtk.Label
	attachmentSize   *gtk.Label
	attachmentCancel *gtk.Button

	chats             *gioutil.ListModel[*zchatv1.Chat]
	chatSel           *gtk.SingleSelection
	chatRows          map[string]*gtk.ListItem
	messages          *gioutil.ListModel[*zchatv1.Message]
	messageRows       map[string]*gtk.ListItem
	chatOrder         []*zchatv1.Chat
	chatIndex         map[string]*zchatv1.Chat
	chatRebuildQueued bool
	searchQuery       string
	searchResults     []*zchatv1.Chat
	mediaHandlers     map[*gtk.Button]coreglib.SignalHandle
	filter            chatFilter
	stickToBottom     bool
	bottomQueued      bool
	scrollingToBottom bool
	loadingOlder      bool
	hasOlder          bool
	presence          map[string]*chatPresence
	typingTimers      map[string]glib.SourceHandle
	idleTimer         glib.SourceHandle
	animCache         map[string]*animation
	animPending       map[string][]func(*animation)
	animTimers        map[*gtk.Picture]glib.SourceHandle
	previewPaths      map[*gtk.Picture]string
	avatars           map[string]*gdk.Texture
	avatarRows        map[*adw.Avatar]string

	client      *client.Client
	activeChat  string
	activeGroup bool
	replyTo     *zchatv1.Message
	pending     *attachment
	settings    settings
}

// chatFilter selects which slice of the chat list the sidebar shows.
type chatFilter int

const (
	filterDirect chatFilter = iota
	filterGroups
	filterArchived
)

func newWindow(ctx context.Context, app *adw.Application, log zerolog.Logger, socketPath string) *window {
	builder := gtk.NewBuilderFromResource("/com/zealish/ZChat/window.ui")

	w := &window{
		ctx:            ctx,
		log:            log,
		app:            app,
		sock:           socketPath,
		win:            builder.GetObject("window").Cast().(*adw.ApplicationWindow),
		toastOverlay:   builder.GetObject("toast_overlay").Cast().(*adw.ToastOverlay),
		mainStack:      builder.GetObject("main_stack").Cast().(*gtk.Stack),
		qrImage:        builder.GetObject("qr_image").Cast().(*gtk.Picture),
		syncPage:       builder.GetObject("sync_page").Cast().(*adw.StatusPage),
		syncProgress:   builder.GetObject("sync_progress").Cast().(*gtk.ProgressBar),
		syncDetail:     builder.GetObject("sync_detail").Cast().(*gtk.Label),
		syncCounts:     builder.GetObject("sync_counts").Cast().(*gtk.Label),
		splitView:      builder.GetObject("split_view").Cast().(*adw.NavigationSplitView),
		headerAvatar:   builder.GetObject("header_avatar").Cast().(*adw.Avatar),
		contentPage:    builder.GetObject("content_page").Cast().(*adw.NavigationPage),
		contentTitle:   builder.GetObject("content_title").Cast().(*adw.WindowTitle),
		chatList:       builder.GetObject("chat_list").Cast().(*gtk.ListView),
		chatFilter:     builder.GetObject("chat_filter").Cast().(*adw.ToggleGroup),
		messageList:    builder.GetObject("message_list").Cast().(*gtk.ListView),
		messageScrl:    builder.GetObject("message_scroll").Cast().(*gtk.ScrolledWindow),
		messageEntry:   builder.GetObject("message_entry").Cast().(*gtk.Entry),
		searchEntry:    builder.GetObject("search_entry").Cast().(*gtk.SearchEntry),
		sendButton:     builder.GetObject("send_button").Cast().(*gtk.Button),
		attachButton:   builder.GetObject("attach_button").Cast().(*gtk.Button),
		emojiButton:    builder.GetObject("emoji_button").Cast().(*gtk.MenuButton),
		newChatButton:  builder.GetObject("new_chat_button").Cast().(*gtk.Button),
		chatInfoButton: builder.GetObject("chat_info_button").Cast().(*gtk.Button),

		replyBar:    builder.GetObject("reply_bar").Cast().(*gtk.Box),
		replySender: builder.GetObject("reply_sender").Cast().(*gtk.Label),
		replyBody:   builder.GetObject("reply_body").Cast().(*gtk.Label),
		replyCancel: builder.GetObject("reply_cancel").Cast().(*gtk.Button),

		findBar:    builder.GetObject("find_bar").Cast().(*gtk.SearchBar),
		findEntry:  builder.GetObject("find_entry").Cast().(*gtk.SearchEntry),
		findStatus: builder.GetObject("find_status").Cast().(*gtk.Label),
		findPrev:   builder.GetObject("find_prev").Cast().(*gtk.Button),
		findNext:   builder.GetObject("find_next").Cast().(*gtk.Button),

		attachmentBar:    builder.GetObject("attachment_bar").Cast().(*gtk.Box),
		attachmentThumb:  builder.GetObject("attachment_thumb").Cast().(*gtk.Picture),
		attachmentIcon:   builder.GetObject("attachment_icon").Cast().(*gtk.Image),
		attachmentName:   builder.GetObject("attachment_name").Cast().(*gtk.Label),
		attachmentSize:   builder.GetObject("attachment_size").Cast().(*gtk.Label),
		attachmentCancel: builder.GetObject("attachment_cancel").Cast().(*gtk.Button),

		chats:         chatModelType.New(),
		messages:      messageModelType.New(),
		chatIndex:     make(map[string]*zchatv1.Chat),
		chatRows:      make(map[string]*gtk.ListItem),
		messageRows:   make(map[string]*gtk.ListItem),
		mediaHandlers: make(map[*gtk.Button]coreglib.SignalHandle),
		presence:      make(map[string]*chatPresence),
		typingTimers:  make(map[string]glib.SourceHandle),
		animCache:     make(map[string]*animation),
		animPending:   make(map[string][]func(*animation)),
		animTimers:    make(map[*gtk.Picture]glib.SourceHandle),
		previewPaths:  make(map[*gtk.Picture]string),
		avatars:       make(map[string]*gdk.Texture),
		avatarRows:    make(map[*adw.Avatar]string),
		settings:      loadSettings(),
	}

	w.win.SetApplication(&app.Application)
	w.applyTheme()
	w.setupChatList()
	w.newChatButton.ConnectClicked(func() { w.showNewChatDialog() })
	w.chatInfoButton.ConnectClicked(func() { w.showChatInfo() })
	w.setupMessageList()
	w.setupComposer()
	w.setupSearch()
	w.setupFind()
	w.setupShortcuts()
	w.setupDragAndDrop()
	w.setupPasteShortcut()
	w.setupTypingNotifier()
	w.setupAutoScroll()
	w.mainStack.SetVisibleChildName("qr")

	go w.connect()
	return w
}

func (w *window) present() { w.win.Present() }

func (w *window) showNewChatDialog() {
	if w.client == nil || !w.connected || w.newChatDialog != nil {
		return
	}
	d := adw.NewDialog()
	w.newChatDialog = d
	d.SetTitle("New chat")
	d.SetContentWidth(380)
	d.SetContentHeight(500)
	view := adw.NewToolbarView()
	header := adw.NewHeaderBar()
	view.AddTopBar(header)
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(12)
	box.SetMarginBottom(12)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	entry := gtk.NewSearchEntry()
	entry.SetPlaceholderText("Search contacts or enter phone number")
	box.Append(entry)
	status := gtk.NewLabel("Loading contacts…")
	status.SetXAlign(0)
	box.Append(status)
	model := contactModelType.New()
	list := gtk.NewListView(gtk.NewNoSelection(model), nil)
	list.AddCSSClass("navigation-sidebar")
	list.SetSingleClickActivate(true)
	// Without a scrolling viewport the ListView is given unlimited height and
	// builds a row for every contact up front, which stalls the dialog on a
	// large address book.
	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetVExpand(true)
	scroll.SetChild(list)
	box.Append(scroll)
	factory := gtk.NewSignalListItemFactory()
	factory.ConnectSetup(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		l := gtk.NewLabel("")
		l.SetXAlign(0)
		l.SetMarginTop(10)
		l.SetMarginBottom(10)
		l.SetMarginStart(12)
		item.SetChild(l)
	})
	factory.ConnectBind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		c := contactModelType.ObjectValue(item.Item())
		text := c.GetName()
		if text == "" {
			text = c.GetPhoneNumber()
		}
		item.Child().(*gtk.Label).SetText(text + "\n" + c.GetPhoneNumber())
	})
	list.SetFactory(&factory.ListItemFactory)
	view.SetContent(box)
	d.SetChild(view)
	open := func(recipient string) {
		recipient = strings.TrimSpace(recipient)
		if recipient == "" {
			status.SetText("Enter a phone number or choose a contact")
			return
		}
		if w.client == nil {
			return
		}
		status.SetText("Opening chat…")
		w.client.StartChat(w.ctx, recipient, func(chat *zchatv1.Chat, err error) {
			if w.newChatDialog != d {
				return
			}
			if err != nil {
				status.SetText("Could not open chat: " + err.Error())
				return
			}
			w.upsertChat(chat)
			d.Close()
			w.newChatDialog = nil
			w.openChat(chat)
		})
	}
	list.ConnectActivate(func(pos uint) {
		if int(pos) < model.Len() {
			open(model.At(int(pos)).GetJid())
		}
	})
	entry.ConnectSearchChanged(func() {
		q := strings.ToLower(strings.TrimSpace(entry.Text()))
		filtered := []*zchatv1.Contact{}
		for i := 0; i < model.Len(); i++ {
			c := model.At(i)
			if q == "" || strings.Contains(strings.ToLower(c.GetName()), q) || strings.Contains(c.GetPhoneNumber(), q) {
				filtered = append(filtered, c)
			}
		}
		model.Splice(0, model.Len(), filtered...)
	})
	entry.ConnectActivate(func() { open(entry.Text()) })
	d.ConnectClosed(func() {
		if w.newChatDialog == d {
			w.newChatDialog = nil
		}
	})
	d.Present(w.win)
	w.client.GetContacts(w.ctx, func(cs []*zchatv1.Contact, err error) {
		if w.newChatDialog != d {
			return
		}
		if err != nil {
			status.SetText("Could not load contacts: " + err.Error())
			return
		}
		model.Splice(0, model.Len(), cs...)
		status.SetText("Select a contact or enter an international number")
	})
}

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
		w.connected = true
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
	case *zchatv1.Event_MessageReceived:
		w.notify(payload.MessageReceived)
		w.onMessage(payload.MessageReceived, false)
	case *zchatv1.Event_MessageUpdated:
		w.onMessage(payload.MessageUpdated, true)
	case *zchatv1.Event_MessageDeleted:
		w.onMessageDeleted(payload.MessageDeleted)
	case *zchatv1.Event_PresenceChanged:
		w.onPresence(payload.PresenceChanged)
	case *zchatv1.Event_ProfilePictureUpdated:
		w.onProfilePictureUpdated(payload.ProfilePictureUpdated)
	case *zchatv1.Event_ChatUpdated:
		w.upsertChat(payload.ChatUpdated)
	case *zchatv1.Event_ChatDeleted:
		w.removeChat(payload.ChatDeleted.GetJid())
	case *zchatv1.Event_SyncState:
		w.onSyncState(payload.SyncState)
	}
}

func (w *window) onConnectionState(state *zchatv1.ConnectionState) {
	w.connected = state.GetStatus() == zchatv1.ConnectionStatus_CONNECTION_STATUS_CONNECTED
	w.ownJID = state.GetOwnJid()
	w.connStatus = state.GetStatus().String()
	if state.GetStatus() == zchatv1.ConnectionStatus_CONNECTION_STATUS_LOGGED_OUT {
		w.mainStack.SetVisibleChildName("qr")
		return
	}
	if w.connected && !w.syncing {
		w.mainStack.SetVisibleChildName("chat")
		w.loadChats()
	}
}

// onSyncState renders full-sync progress and keeps the main view blocked until
// the daemon reports the history and contact sync as finished, so the chat list
// is never shown half-populated.
func (w *window) onSyncState(state *zchatv1.SyncState) {
	switch state.GetStage() {
	case zchatv1.SyncStage_SYNC_STAGE_CONNECTING,
		zchatv1.SyncStage_SYNC_STAGE_CONTACTS,
		zchatv1.SyncStage_SYNC_STAGE_HISTORY,
		zchatv1.SyncStage_SYNC_STAGE_FINALIZING:
		w.syncing = true
	default:
		if !w.syncing {
			return
		}
		w.syncing = false
		w.stopSyncPulse()
		if w.connected {
			w.mainStack.SetVisibleChildName("chat")
			w.loadChats()
		}
		return
	}

	w.mainStack.SetVisibleChildName("sync")
	w.syncDetail.SetText(state.GetDetail())
	w.syncCounts.SetText(syncCounts(state))
	if state.GetIndeterminate() {
		w.startSyncPulse()
	} else {
		w.stopSyncPulse()
		w.syncProgress.SetFraction(state.GetProgress())
	}
}

func syncCounts(state *zchatv1.SyncState) string {
	if state.GetContactsSynced() == 0 {
		return ""
	}
	return fmt.Sprintf("%d contacts", state.GetContactsSynced())
}

// startSyncPulse animates the bar while the daemon cannot estimate progress.
func (w *window) startSyncPulse() {
	if w.syncPulse != 0 {
		return
	}
	w.syncProgress.Pulse()
	w.syncPulse = glib.TimeoutAdd(120, func() bool {
		w.syncProgress.Pulse()
		return true
	})
}

func (w *window) stopSyncPulse() {
	if w.syncPulse == 0 {
		return
	}
	glib.SourceRemove(w.syncPulse)
	w.syncPulse = 0
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

// loadChats fetches both the active and archived lists. They are kept in one
// slice and split by the sidebar filter, so a chat that is archived from
// another device simply moves between tabs without a refetch.
//
// Chats already on screen are merged rather than replaced, so a refetch after
// a reconnect leaves the sidebar's rows and scroll position alone.
func (w *window) loadChats() {
	if w.client == nil {
		return
	}
	for _, archived := range []bool{false, true} {
		w.client.GetChats(w.ctx, archived, func(_ bool, chats []*zchatv1.Chat, err error) {
			if err != nil {
				w.log.Error().Err(err).Msg("load chats")
				w.toast("Could not load chats")
				return
			}
			for _, c := range chats {
				w.upsertChat(c)
			}
		})
	}
}

// upsertChat merges a chat into the sidebar and schedules a refresh. History
// sync delivers hundreds of these in bursts, so rebuilds are coalesced rather
// than run per event.
//
// An existing chat is updated in place rather than swapped for the incoming
// pointer. The list model holds these pointers, so replacing one makes the row
// look new and forces a splice that destroys its widget — which drops the
// sidebar's scroll position and the focused row. Opening a chat clears its
// unread badge, so this path runs on every single click.
func (w *window) upsertChat(chat *zchatv1.Chat) {
	if existing, ok := w.chatIndex[chat.GetJid()]; ok {
		copyChatInto(existing, chat)
		w.refreshChatRow(existing)
		w.scheduleChatRebuild()
		return
	}
	w.chatOrder = append(w.chatOrder, chat)
	w.chatIndex[chat.GetJid()] = chat
	w.scheduleChatRebuild()
}

// copyChatInto overwrites dst's displayed and ordering fields with src's,
// keeping the pointer the list model already holds.
func copyChatInto(dst, src *zchatv1.Chat) {
	dst.Name = src.GetName()
	dst.Unread = src.GetUnread()
	dst.Archived = src.GetArchived()
	dst.Pinned = src.GetPinned()
	dst.LastMessage = src.GetLastMessage()
	dst.UpdatedAt = src.GetUpdatedAt()
	dst.IsGroup = src.GetIsGroup()
	dst.MutedUntil = src.GetMutedUntil()
}

// removeChat drops a chat the daemon no longer has, such as one merged into
// another row.
func (w *window) removeChat(jid string) {
	existing, ok := w.chatIndex[jid]
	if !ok {
		return
	}
	delete(w.chatIndex, jid)
	for i, c := range w.chatOrder {
		if c == existing {
			w.chatOrder = append(w.chatOrder[:i], w.chatOrder[i+1:]...)
			break
		}
	}
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
	// A search shows daemon-ranked results verbatim, ignoring the sidebar
	// filter so a query never silently hides matches on another tab. The
	// results are fresh pointers, so known chats are swapped back to the ones
	// the model already holds to keep their rows.
	if w.searchQuery != "" {
		results := make([]*zchatv1.Chat, 0, len(w.searchResults))
		for _, c := range w.searchResults {
			if existing, ok := w.chatIndex[c.GetJid()]; ok {
				copyChatInto(existing, c)
				w.refreshChatRow(existing)
				c = existing
			}
			results = append(results, c)
		}
		w.applyChatModel(results)
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
		if w.filterMatches(c) {
			visible = append(visible, c)
		}
	}

	w.applyChatModel(visible)

	if selected := w.activeChat; selected != "" {
		for i, c := range visible {
			if c.GetJid() == selected {
				w.chatSel.SetSelected(uint(i))
				break
			}
		}
	}
}

// applyChatModel moves the list model onto visible with the smallest splice
// that covers the difference.
//
// Replacing the whole model would destroy and rebuild every row, which resets
// the sidebar's scroll position to the top. Most updates — an unread badge
// being cleared, a new last message — only touch one row, so the untouched
// head and tail are left in place.
func (w *window) applyChatModel(visible []*zchatv1.Chat) {
	current := make([]*zchatv1.Chat, w.chats.Len())
	for i := range current {
		current[i] = w.chats.At(i)
	}

	head, removals, changed := spliceRange(current, visible)
	if !changed {
		return
	}
	w.chats.Splice(head, removals, visible[head:len(visible)-(len(current)-head-removals)]...)
}

// spliceRange finds the shortest run that turns current into want: the index to
// splice at and how many entries to drop there. changed is false when the two
// already match.
func spliceRange[T comparable](current, want []T) (position, removals int, changed bool) {
	head := 0
	for head < len(current) && head < len(want) && current[head] == want[head] {
		head++
	}
	if head == len(current) && head == len(want) {
		return 0, 0, false
	}

	tail := 0
	for tail < len(current)-head && tail < len(want)-head &&
		current[len(current)-1-tail] == want[len(want)-1-tail] {
		tail++
	}
	return head, len(current) - head - tail, true
}

// filterMatches reports whether a chat belongs on the currently selected tab.
// Archived chats are only ever shown on the archived tab.
func (w *window) filterMatches(chat *zchatv1.Chat) bool {
	if w.filter == filterArchived {
		return chat.GetArchived()
	}
	if chat.GetArchived() {
		return false
	}
	return chat.GetIsGroup() == (w.filter == filterGroups)
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
	// Without this a row only activates on the second click: the first one
	// just moves the selection.
	w.chatList.SetSingleClickActivate(true)

	w.chatFilter.NotifyProperty("active", func() {
		switch w.chatFilter.ActiveName() {
		case "groups":
			w.filter = filterGroups
		case "archived":
			w.filter = filterArchived
		default:
			w.filter = filterDirect
		}
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

		// Pin and mute markers sit between the preview and the unread badge.
		markers := gtk.NewLabel("")
		markers.SetVAlign(gtk.AlignCenter)
		markers.AddCSSClass("dim-label")
		row.Append(markers)

		unread := gtk.NewLabel("")
		unread.SetVAlign(gtk.AlignCenter)
		unread.AddCSSClass("zchat-unread")
		row.Append(unread)

		onRightClick(row, func(x, y float64) {
			chat := chatModelType.ObjectValue(item.Item())
			if chat == nil {
				return
			}
			showMenu(row, x, y, w.chatMenuEntries(chat))
		})

		item.SetChild(row)
	})
	factory.ConnectBind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		chat := chatModelType.ObjectValue(item.Item())
		w.chatRows[chat.GetJid()] = item
		w.bindChatRow(item, chat)
	})
	factory.ConnectUnbind(func(obj *coreglib.Object) {
		item := obj.Cast().(*gtk.ListItem)
		chat := chatModelType.ObjectValue(item.Item())
		if w.chatRows[chat.GetJid()] == item {
			delete(w.chatRows, chat.GetJid())
		}
		row := item.Child().(*gtk.Box)
		w.unbindAvatar(row.FirstChild().(*adw.Avatar))
	})
	w.chatList.SetFactory(&factory.ListItemFactory)

	w.chatList.ConnectActivate(func(position uint) {
		if int(position) >= w.chats.Len() {
			return
		}
		w.openChat(w.chats.At(int(position)))
	})
}

// bindChatRow fills a sidebar row's widgets from a chat.
func (w *window) bindChatRow(item *gtk.ListItem, chat *zchatv1.Chat) {
	row := item.Child().(*gtk.Box)
	avatar := row.FirstChild().(*adw.Avatar)
	text := avatar.NextSibling().(*gtk.Box)
	markers := text.NextSibling().(*gtk.Label)
	unread := markers.NextSibling().(*gtk.Label)

	name := text.FirstChild().(*gtk.Label)
	preview := name.NextSibling().(*gtk.Label)

	display := displayName(chat)
	avatar.SetText(display)
	w.bindAvatar(avatar, chat.GetJid())
	name.SetText(display)
	preview.SetText(chat.GetLastMessage())

	markers.SetText(chatMarkers(chat))
	markers.SetVisible(markers.Text() != "")

	if chat.GetUnread() > 0 {
		unread.SetText(fmt.Sprintf("%d", chat.GetUnread()))
		unread.SetVisible(true)
	} else {
		unread.SetVisible(false)
	}
}

// refreshChatRow repaints a row whose chat was updated in place. The model
// emits no change for that, since the pointer it holds is unchanged.
func (w *window) refreshChatRow(chat *zchatv1.Chat) {
	if item, ok := w.chatRows[chat.GetJid()]; ok {
		w.bindChatRow(item, chat)
	}
}

// chatMarkers renders the pinned and muted state as a compact glyph pair.
func chatMarkers(chat *zchatv1.Chat) string {
	markers := ""
	if chat.GetPinned() {
		markers += "📌"
	}
	if isMuted(chat) {
		markers += "🔕"
	}
	return markers
}

func (w *window) openChat(chat *zchatv1.Chat) {
	// The previous chat must not be left with a dangling typing indicator.
	w.stopTyping()
	w.activeChat = chat.GetJid()
	w.setComposerEnabled(true)
	w.activeGroup = chat.GetIsGroup()
	w.findBar.SetSearchMode(false)
	w.cancelAttachment()
	w.contentPage.SetTitle(displayName(chat))
	w.contentTitle.SetTitle(displayName(chat))
	w.headerAvatar.SetText(displayName(chat))
	w.bindAvatar(w.headerAvatar, chat.GetJid())
	w.refreshSubtitle()
	clear(w.messageRows)
	w.messages.Splice(0, w.messages.Len())
	w.splitView.SetShowContent(true)
	if w.client == nil {
		return
	}
	w.hasOlder = true
	w.loadingOlder = false
	w.client.GetMessages(w.ctx, w.activeChat, 0, func(chatJID string, msgs []*zchatv1.Message, err error) {
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

// onMessageDeleted drops a revoked or locally deleted message from the open chat.
func (w *window) onMessageDeleted(evt *zchatv1.MessageDeleted) {
	if evt.GetChatJid() != w.activeChat {
		return
	}
	for i := range w.messages.Len() {
		if w.messages.At(i).GetId() != evt.GetId() {
			continue
		}
		w.messages.Splice(i, 1)
		delete(w.messageRows, evt.GetId())
		return
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
	// applyScrollToBottom moves the adjustment itself, which re-emits
	// "changed", so the guard keeps that from recursing per frame.
	adj.ConnectChanged(func() {
		if w.stickToBottom && !w.scrollingToBottom {
			w.applyScrollToBottom()
		}
	})
	adj.ConnectValueChanged(func() {
		if w.scrollingToBottom || w.bottomQueued {
			return
		}
		if adj.Value() < 80 {
			w.loadOlderMessages()
		}
		w.stickToBottom = adj.Upper()-adj.PageSize()-adj.Value() < 50
	})
}

func (w *window) loadOlderMessages() {
	if w.client == nil || w.activeChat == "" || w.messages.Len() == 0 || w.loadingOlder || !w.hasOlder {
		return
	}
	oldest := w.messages.At(0).GetTimestamp()
	if oldest == 0 {
		return
	}
	w.loadingOlder = true
	adj := w.messageScrl.VAdjustment()
	oldUpper := adj.Upper()
	w.client.GetMessages(w.ctx, w.activeChat, oldest, func(chatJID string, msgs []*zchatv1.Message, err error) {
		defer func() { w.loadingOlder = false }()
		if err != nil || chatJID != w.activeChat || len(msgs) == 0 {
			if len(msgs) == 0 {
				w.hasOlder = false
			}
			return
		}
		w.messages.Splice(0, 0, msgs...)
		glib.IdleAdd(func() bool { adj.SetValue(adj.Value() + adj.Upper() - oldUpper); return false })
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

		// The clamp caps how wide a bubble may grow, the way WhatsApp Web
		// does. Without it any child with a large natural width — an
		// ellipsized quote preview is the usual culprit — stretches the
		// bubble across the entire row.
		clamp := adw.NewClamp()
		clamp.SetMaximumSize(bubbleMaxWidth)
		clamp.SetTighteningThreshold(bubbleMaxWidth)
		// hexpand gives the clamp the full row allocation so halign can pin
		// the bubble to the left (incoming) or right (outgoing) edge.
		clamp.SetHExpand(true)

		bubble := gtk.NewBox(gtk.OrientationVertical, 2)
		bubble.AddCSSClass("zchat-bubble")

		sender := gtk.NewLabel("")
		sender.SetXAlign(0)
		sender.AddCSSClass("zchat-sender")
		bubble.Append(sender)

		forwarded := gtk.NewLabel("Forwarded")
		forwarded.SetXAlign(0)
		forwarded.SetVisible(false)
		forwarded.AddCSSClass("zchat-forwarded")
		bubble.Append(forwarded)

		// Quoted block: the replied-to sender above a one-line preview.
		quoted := gtk.NewBox(gtk.OrientationVertical, 0)
		quoted.SetVisible(false)
		quoted.AddCSSClass("zchat-quoted")

		quotedSender := gtk.NewLabel("")
		quotedSender.SetXAlign(0)
		quotedSender.SetEllipsize(pango.EllipsizeEnd)
		quotedSender.AddCSSClass("caption-heading")
		quoted.Append(quotedSender)

		quotedBody := gtk.NewLabel("")
		quotedBody.SetXAlign(0)
		quotedBody.SetSingleLineMode(true)
		quotedBody.SetEllipsize(pango.EllipsizeEnd)
		// A single-line ellipsized label reports the untruncated string as its
		// natural width, so it is capped here rather than left to the clamp.
		quotedBody.SetMaxWidthChars(36)
		quotedBody.AddCSSClass("dim-label")
		quoted.Append(quotedBody)

		bubble.Append(quoted)

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

		// WhatsApp tucks the timestamp into the body's last line when it
		// fits. An overlay pins it to the bottom-right corner and
		// bindMessageRow pads the body text so the two never collide.
		bodyOverlay := gtk.NewOverlay()
		body := gtk.NewLabel("")
		body.SetXAlign(0)
		body.SetWrap(true)
		body.SetWrapMode(pango.WrapWordChar)
		body.SetNaturalWrapMode(gtk.NaturalWrapWord)
		body.SetSelectable(true)
		body.AddCSSClass("zchat-message")
		bodyOverlay.SetChild(body)

		meta := gtk.NewLabel("")
		meta.SetXAlign(1)
		meta.SetHAlign(gtk.AlignEnd)
		meta.SetVAlign(gtk.AlignEnd)
		// The overlay sits on top of selectable text; without this it would
		// swallow clicks meant for the body.
		meta.SetCanTarget(false)
		meta.AddCSSClass("zchat-timestamp")
		bodyOverlay.AddOverlay(meta)
		bubble.Append(bodyOverlay)

		reaction := gtk.NewLabel("")
		reaction.SetXAlign(1)
		reaction.AddCSSClass("zchat-reaction")
		bubble.Append(reaction)
		onRightClick(bubble, func(x, y float64) {
			msg := messageModelType.ObjectValue(item.Item())
			if msg == nil {
				return
			}
			showMenu(bubble, x, y, w.messageMenuEntries(msg, bubble, x, y))
		})
		clamp.SetChild(bubble)
		outer.Append(clamp)
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
		// A row scrolled out of view must not keep ticking its animation, and
		// an in-flight decode for it is no longer wanted.
		preview := mediaPreviewOf(item)
		w.stopAnimation(preview)
		delete(w.previewPaths, preview)
	})
	w.messageList.SetFactory(&factory.ListItemFactory)
}

// clampOf returns the width-limiting clamp of a message row: the row is an
// outer box wrapping a clamp, which in turn holds the bubble.
func clampOf(item *gtk.ListItem) *adw.Clamp {
	return item.Child().(*gtk.Box).FirstChild().(*adw.Clamp)
}

// bubbleOf returns a message row's bubble.
func bubbleOf(item *gtk.ListItem) *gtk.Box {
	return clampOf(item).FirstChild().(*gtk.Box)
}

// mediaBoxOf returns a message row's attachment container, the fourth child of
// its bubble.
func mediaBoxOf(item *gtk.ListItem) *gtk.Box {
	bubble := bubbleOf(item)
	sender := bubble.FirstChild().(*gtk.Label)
	forwarded := sender.NextSibling().(*gtk.Label)
	return forwarded.NextSibling().(*gtk.Box).NextSibling().(*gtk.Box)
}

func mediaPreviewOf(item *gtk.ListItem) *gtk.Picture {
	return mediaBoxOf(item).FirstChild().(*gtk.Picture)
}

func (w *window) bindMessageRow(item *gtk.ListItem, msg *zchatv1.Message) {
	clamp := clampOf(item)
	bubble := clamp.FirstChild().(*gtk.Box)
	sender := bubble.FirstChild().(*gtk.Label)
	forwarded := sender.NextSibling().(*gtk.Label)
	quoted := forwarded.NextSibling().(*gtk.Box)
	media := mediaBoxOf(item)
	bodyOverlay := media.NextSibling().(*gtk.Overlay)
	body := bodyOverlay.Child().(*gtk.Label)
	meta := body.NextSibling().(*gtk.Label)
	reaction := bodyOverlay.NextSibling().(*gtk.Label)
	reaction.SetText(msg.GetReaction())
	reaction.SetVisible(msg.GetReaction() != "")
	// Rows are recycled, so the find highlight is reapplied on every bind.
	highlightRow(item, w.findCurrent != "" && msg.GetId() == w.findCurrent)

	w.bindMedia(media, msg)

	forwarded.SetVisible(msg.GetForwarded())

	if q := msg.GetQuoted(); q != nil {
		quotedSender := quoted.FirstChild().(*gtk.Label)
		quotedSender.SetText(quotedSenderName(q))
		quotedSender.NextSibling().(*gtk.Label).SetText(quotedPreview(q))
		quoted.SetVisible(true)
	} else {
		quoted.SetVisible(false)
	}

	// The clamp carries the alignment because it, not the bubble, is the
	// child that expands to fill the row.
	if msg.GetOutgoing() {
		clamp.SetHAlign(gtk.AlignEnd)
		bubble.SetHAlign(gtk.AlignEnd)
		bubble.RemoveCSSClass("zchat-bubble-in")
		bubble.AddCSSClass("zchat-bubble-out")
		sender.SetVisible(false)
	} else {
		clamp.SetHAlign(gtk.AlignStart)
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

	// The body is padded only once the timestamp's final width is known, so
	// this has to follow meta.SetText above.
	setBodyText(body, meta, msg.GetBody())
}

// setBodyText fills a bubble's body and reserves room on its last line for the
// timestamp overlaid in the bottom-right corner, the way WhatsApp Web does.
//
// A media-only message has no body to pad. Only the body label is hidden then,
// never the overlay, so the timestamp still renders under the attachment.
func setBodyText(body, meta *gtk.Label, text string) {
	body.SetVisible(text != "")
	if text == "" {
		return
	}
	body.SetText(text + timestampSpacer(body, meta))
}

// timestampSpacer returns non-breaking spaces wide enough to clear the
// timestamp. Pango measures both labels in their own font, so the reservation
// tracks the theme's font size rather than assuming a fixed width.
func timestampSpacer(body, meta *gtk.Label) string {
	// spacerSample is averaged over several spaces to smooth off the
	// sub-pixel advance width of a single one.
	const spacerSample = 20
	needed, _ := meta.CreatePangoLayout(meta.Text()).PixelSize()
	sampled, _ := body.CreatePangoLayout(strings.Repeat("\u00a0", spacerSample)).PixelSize()
	return spacerRun(needed, sampled, spacerSample)
}

// spacerRun sizes the run of non-breaking spaces that reserves needed pixels,
// given the measured width of sample spaces.
func spacerRun(needed, sampled, sample int) string {
	if sampled <= 0 || sample <= 0 || needed <= 0 {
		return ""
	}
	const gap = 8 // breathing room between the text and the timestamp
	count := (needed+gap)*sample/sampled + 1
	// A zero-width space gives Pango a break opportunity, so an overlong
	// spacer wraps on its own instead of dragging the last word down with it.
	return "\u200b" + strings.Repeat("\u00a0", count)
}

// bindMedia renders a message's attachment: the WhatsApp thumbnail (or the
// downloaded file itself) plus a button that downloads on demand and opens the
// result with the desktop's default handler.
func (w *window) bindMedia(media *gtk.Box, msg *zchatv1.Message) {
	info := msg.GetMedia()
	preview := media.FirstChild().(*gtk.Picture)
	action := preview.NextSibling().(*gtk.Button)

	// Rows are recycled, so the previous message's click handler and running
	// animation must go.
	if handle, ok := w.mediaHandlers[action]; ok {
		action.HandlerDisconnect(handle)
		delete(w.mediaHandlers, action)
	}
	w.stopAnimation(preview)

	if info == nil {
		media.SetVisible(false)
		return
	}
	media.SetVisible(true)

	local := info.GetPath()
	switch {
	case local != "" && isVisualMedia(msg.GetType()):
		// Decoding is asynchronous, so the row may already show another
		// message by the time it lands. The path the preview was last bound
		// to says whether the result is still wanted.
		w.previewPaths[preview] = local
		if thumb := info.GetThumbnail(); len(thumb) > 0 {
			w.showThumbnail(preview, thumb)
		}
		w.loadAnimation(local, func(anim *animation) {
			if anim == nil || w.previewPaths[preview] != local {
				return
			}
			w.stopAnimation(preview)
			w.showAnimation(preview, anim)
			preview.SetVisible(true)
		})
	case len(info.GetThumbnail()) > 0:
		delete(w.previewPaths, preview)
		w.showThumbnail(preview, info.GetThumbnail())
	default:
		delete(w.previewPaths, preview)
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

// showThumbnail paints the small inline preview WhatsApp ships with a message.
// It stands in until the full image finishes decoding, so an attachment never
// pops in from an empty box.
func (w *window) showThumbnail(preview *gtk.Picture, thumbnail []byte) {
	pixbuf, err := pixbufFromBytes(w.ctx, thumbnail)
	if err != nil {
		preview.SetVisible(false)
		return
	}
	preview.SetPixbuf(pixbuf)
	preview.SetVisible(true)
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
	w.attachButton.SetSensitive(enabled)
	w.emojiButton.SetSensitive(enabled)
}

func (w *window) setupComposer() {
	send := func() {
		if w.pending != nil {
			w.sendAttachment()
			return
		}

		body := strings.TrimSpace(w.messageEntry.Text())
		if body == "" || w.activeChat == "" || w.client == nil {
			return
		}
		w.messageEntry.SetText("")
		quotedID := w.replyTo.GetId()
		w.cancelReply()
		w.client.SendMessage(w.ctx, w.activeChat, body, quotedID, func(err error) {
			if err != nil {
				w.log.Error().Err(err).Msg("send message")
				w.toast("Message could not be sent")
			}
		})
	}

	w.attachButton.ConnectClicked(w.chooseAttachment)
	w.sendButton.ConnectClicked(send)
	w.messageEntry.ConnectActivate(send)
	w.replyCancel.ConnectClicked(w.cancelReply)
	w.attachmentCancel.ConnectClicked(w.cancelAttachment)
	w.setupEmojiChooser()
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
