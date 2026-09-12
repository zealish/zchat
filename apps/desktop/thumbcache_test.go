package main

import (
	"context"
	"os"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Thumbnails are cached by content, so distinct images must not collide and the
// same bytes must reuse one texture.
func TestThumbnailCacheKeyedByContent(t *testing.T) {
	if !gtk.InitCheck() {
		t.Skip("no display")
	}
	still, err := os.ReadFile("testdata/still.webp")
	if err != nil {
		t.Skip("missing fixture")
	}
	anim, err := os.ReadFile("testdata/anim.webp")
	if err != nil {
		t.Skip("missing fixture")
	}

	w := &window{ctx: context.Background(), thumbCache: make(map[[32]byte]*gdk.Texture)}
	preview := gtk.NewPicture()

	w.showThumbnail(preview, still)
	first := preview.Paintable()
	if first == nil {
		t.Fatal("no paintable set for the first thumbnail")
	}

	w.showThumbnail(preview, anim)
	second := preview.Paintable()
	if second == nil {
		t.Fatal("no paintable set for the second thumbnail")
	}
	if first.Eq(second) {
		t.Error("two different thumbnails resolved to the same texture")
	}

	// Rebinding the first image must reuse the cached texture rather than
	// decoding it again.
	w.showThumbnail(preview, still)
	if !preview.Paintable().Eq(first) {
		t.Error("re-showing a thumbnail did not reuse its cached texture")
	}
	if len(w.thumbCache) != 2 {
		t.Errorf("cache holds %d entries, want 2", len(w.thumbCache))
	}
}

// A thumbnail that cannot be decoded must fall back to the empty placeholder
// instead of painting a stale image from the recycled row.
func TestThumbnailDecodeFailureClearsPreview(t *testing.T) {
	if !gtk.InitCheck() {
		t.Skip("no display")
	}
	w := &window{ctx: context.Background(), thumbCache: make(map[[32]byte]*gdk.Texture)}
	preview := gtk.NewPicture()

	w.showThumbnail(preview, []byte("not an image"))
	if preview.Paintable() != nil {
		t.Error("a corrupt thumbnail left a paintable on the preview")
	}
	if !preview.HasCSSClass("zchat-media-empty") {
		t.Error("a corrupt thumbnail did not fall back to the empty placeholder")
	}
}
