package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDecodeAnimationWebP covers the sticker path: WhatsApp sends WebP, which
// gdk-pixbuf cannot decode without an optional system package.
func TestDecodeAnimationWebP(t *testing.T) {
	path := filepath.Join("testdata", "anim.webp")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("missing fixture: %v", err)
	}

	anim, err := decodeAnimation(path)
	if err != nil {
		t.Fatalf("decodeAnimation: %v", err)
	}
	if !anim.animated() {
		t.Fatalf("got %d frames, want more than one", len(anim.frames))
	}
	if len(anim.delays) != len(anim.frames) {
		t.Fatalf("got %d delays for %d frames", len(anim.delays), len(anim.frames))
	}
	for i, frame := range anim.frames {
		if w, h := frame.Width(), frame.Height(); w > maxFrameSize || h > maxFrameSize {
			t.Errorf("frame %d is %dx%d, want both sides <= %d", i, w, h, maxFrameSize)
		}
		if anim.delays[i] < 10 {
			t.Errorf("frame %d delay is %dms, want the clamped minimum", i, anim.delays[i])
		}
	}
}

// TestDecodeAnimationStill covers a non-animated WebP sticker, which still has
// to come back as a single usable frame.
func TestDecodeAnimationStill(t *testing.T) {
	path := filepath.Join("testdata", "still.webp")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("missing fixture: %v", err)
	}

	anim, err := decodeAnimation(path)
	if err != nil {
		t.Fatalf("decodeAnimation: %v", err)
	}
	if anim.animated() {
		t.Fatalf("got %d frames, want one", len(anim.frames))
	}
	if anim.frames[0].Width() == 0 || anim.frames[0].Height() == 0 {
		t.Fatal("decoded frame is empty")
	}
}
