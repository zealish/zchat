package main

import (
	"fmt"
	"testing"
)

// The decoded-image cache used to grow for the lifetime of the window, holding
// a full-size pixbuf per attachment ever scrolled past.
func TestAnimCacheEvictsLeastRecentlyUsed(t *testing.T) {
	w := &window{animCache: make(map[string]*animation)}

	for i := range animCacheLimit * 8 {
		path := fmt.Sprintf("/tmp/img-%d.jpg", i)
		w.animCache[path] = &animation{}
		w.touchAnimCache(path)
	}

	if len(w.animCache) > animCacheLimit {
		t.Errorf("cache holds %d entries, want at most %d", len(w.animCache), animCacheLimit)
	}
	if len(w.animOrder) > animCacheLimit*2 {
		t.Errorf("use list grew to %d, want it compacted", len(w.animOrder))
	}
	// The most recent entry must survive; scrolling would otherwise re-decode
	// the image still on screen.
	newest := fmt.Sprintf("/tmp/img-%d.jpg", animCacheLimit*8-1)
	if _, ok := w.animCache[newest]; !ok {
		t.Error("most recently used entry was evicted")
	}
}

// A path used repeatedly must outlive newer one-off entries.
func TestAnimCacheKeepsHotEntry(t *testing.T) {
	w := &window{animCache: make(map[string]*animation)}

	hot := "/tmp/hot.jpg"
	w.animCache[hot] = &animation{}

	for i := range animCacheLimit * 4 {
		w.touchAnimCache(hot)
		path := fmt.Sprintf("/tmp/cold-%d.jpg", i)
		w.animCache[path] = &animation{}
		w.touchAnimCache(path)
	}

	if _, ok := w.animCache[hot]; !ok {
		t.Error("a repeatedly used entry was evicted")
	}
}
