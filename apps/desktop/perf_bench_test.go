package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

func sampleMessage(i int) *zchatv1.Message {
	return &zchatv1.Message{
		Id:        fmt.Sprintf("msg-%d", i),
		ChatJid:   "1@s.whatsapp.net",
		Body:      "a reasonably sized message body",
		Timestamp: int64(1700000000 + i),
	}
}

func BenchmarkTimestampSpacer(b *testing.B) {
	if !gtk.InitCheck() {
		b.Skip("no display")
	}
	w := &window{spacerWidths: make(map[string]int)}
	body := gtk.NewLabel("hello there, this is a message body")
	meta := gtk.NewLabel("12:34 ✓✓")
	b.ResetTimer()
	for b.Loop() {
		_ = w.timestampSpacer(body, meta)
	}
}

func BenchmarkFindMessage(b *testing.B) {
	if !gtk.InitCheck() {
		b.Skip("no display")
	}
	w := &window{messages: messageModelType.New()}
	for i := range 500 {
		w.messages.Append(sampleMessage(i))
	}
	b.ResetTimer()
	for b.Loop() {
		// The common case: a status update for the newest message.
		w.findMessage("msg-499")
	}
}

func BenchmarkThumbnailDecode(b *testing.B) {
	if !gtk.InitCheck() {
		b.Skip("no display")
	}
	jpeg, err := os.ReadFile("testdata/still.webp")
	if err != nil {
		b.Skip("no fixture")
	}
	w := &window{ctx: context.Background(), thumbCache: make(map[[32]byte]*gdk.Texture)}
	preview := gtk.NewPicture()
	b.ResetTimer()
	for b.Loop() {
		w.showThumbnail(preview, jpeg)
	}
}

func BenchmarkPangoLayoutCreate(b *testing.B) {
	if !gtk.InitCheck() {
		b.Skip("no display")
	}
	body := gtk.NewLabel("x")
	sample := strings.Repeat("\u00a0", 20)
	b.ResetTimer()
	for b.Loop() {
		_, _ = body.CreatePangoLayout(sample).PixelSize()
	}
}

// BenchmarkBindMessageRow measures what the ListView actually pays per row
// while scrolling: the full bind, media included.
func BenchmarkBindMessageRow(b *testing.B) {
	if !gtk.InitCheck() {
		b.Skip("no display")
	}
	thumb, err := os.ReadFile("testdata/still.webp")
	if err != nil {
		b.Skip("no fixture")
	}
	w := &window{
		ctx:           context.Background(),
		messages:      messageModelType.New(),
		messageRows:   make(map[string]*gtk.ListItem),
		mediaCells:    make(map[uintptr]*mediaCell),
		mediaHandlers: make(map[uintptr]coreglib.SignalHandle),
		previewPaths:  make(map[uintptr]string),
		animTimers:    make(map[uintptr]glib.SourceHandle),
		thumbCache:    make(map[[32]byte]*gdk.Texture),
		spacerWidths:  make(map[string]int),
	}

	msg := sampleMessage(1)
	msg.Media = &zchatv1.MediaInfo{
		Mime:      "image/jpeg",
		Size:      4096,
		Width:     800,
		Height:    600,
		Thumbnail: thumb,
	}

	media := gtk.NewBox(gtk.OrientationVertical, 4)
	w.newMediaCell(media)
	body := gtk.NewLabel("")
	meta := gtk.NewLabel("12:34")

	b.ResetTimer()
	for b.Loop() {
		w.bindMedia(media, msg)
		meta.SetText("12:34")
		w.setBodyText(body, meta, msg.GetBody())
	}
}
