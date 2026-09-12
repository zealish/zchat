package main

import (
	"testing"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// gotk4 hands back a fresh Go wrapper every time a widget is reached through
// the tree, so a map keyed on the wrapper pointer misses entries stored under a
// different one. This is what made a sent image bind into nothing: bindMedia
// looked its cell up by the box it walked to and always came back empty.
func TestWidgetKeyIsStableAcrossTreeWalks(t *testing.T) {
	if !gtk.InitCheck() {
		t.Skip("no display")
	}

	parent := gtk.NewBox(gtk.OrientationVertical, 0)
	child := gtk.NewBox(gtk.OrientationVertical, 0)
	parent.Append(child)
	walked := parent.FirstChild().(*gtk.Box)

	if child == walked {
		t.Skip("gotk4 now returns identical wrappers; the keying bug cannot recur")
	}
	if widgetKey(child) != widgetKey(walked) {
		t.Fatalf("widgetKey differs across a tree walk: %#x vs %#x",
			widgetKey(child), widgetKey(walked))
	}

	cells := map[uintptr]string{widgetKey(child): "cell"}
	if _, ok := cells[widgetKey(walked)]; !ok {
		t.Fatal("a walked widget does not resolve to the cell registered for it")
	}
}
