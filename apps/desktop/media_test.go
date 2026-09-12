package main

import (
	"testing"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// previewSize decides the inline thumbnail geometry, so it has to keep an
// attachment's aspect ratio while staying inside the column WhatsApp uses.
func TestPreviewSize(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kind          string
		width, height int32
		wantW, wantH  int
	}{
		{"landscape is capped by width", mediaKindImage, 1600, 900, previewMaxWidth, 180},
		{"portrait is capped by height", mediaKindImage, 900, 1600, 225, previewMaxHeight},
		{"small image keeps its size", mediaKindImage, 200, 150, 200, 150},
		{"narrow image gets a floor", mediaKindImage, 40, 120, previewMinWidth, 120},
		{"sticker uses the sticker box", mediaKindSticker, 512, 512, stickerMaxSize, stickerMaxSize},
		{"sticker keeps its floor-free ratio", mediaKindSticker, 60, 120, 60, 120},
		{"missing dimensions fall back to 4:3", mediaKindVideo, 0, 0, previewMaxWidth, previewMaxWidth * 3 / 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &zchatv1.MediaInfo{Width: tc.width, Height: tc.height}
			gotW, gotH := previewSize(tc.kind, info)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Fatalf("got %dx%d, want %dx%d", gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}
}

// A preview must never exceed the column, whatever dimensions WhatsApp reports.
func TestPreviewSizeStaysInBounds(t *testing.T) {
	for _, dims := range [][2]int32{{4000, 10}, {10, 4000}, {3000, 3000}, {1, 1}} {
		info := &zchatv1.MediaInfo{Width: dims[0], Height: dims[1]}
		width, height := previewSize(mediaKindImage, info)
		if width > previewMaxWidth || height > previewMaxHeight {
			t.Errorf("%dx%d rendered as %dx%d, want within %dx%d",
				dims[0], dims[1], width, height, previewMaxWidth, previewMaxHeight)
		}
		if width < 1 || height < 1 {
			t.Errorf("%dx%d rendered as %dx%d, want a positive size", dims[0], dims[1], width, height)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    string
	}{
		{0, "0:00"},
		{7, "0:07"},
		{65, "1:05"},
		{599, "9:59"},
		{3600, "1:00:00"},
		{3725, "1:02:05"},
		{-5, "0:00"},
	} {
		if got := formatDuration(tc.seconds); got != tc.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// The subtitle under a file row is a length for audio and a size otherwise.
func TestFileSubtitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		info *zchatv1.MediaInfo
		want string
	}{
		{"audio prefers its length", mediaKindAudio, &zchatv1.MediaInfo{Duration: 90, Size: 2048}, "1:30"},
		{"audio without a length falls back", mediaKindAudio, &zchatv1.MediaInfo{Size: 2048}, "2.0 KB"},
		{"document shows its size", mediaKindDocument, &zchatv1.MediaInfo{Size: 1536}, "1.5 KB"},
		{"unknown size names the kind", mediaKindDocument, &zchatv1.MediaInfo{}, "document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileSubtitle(tc.kind, tc.info); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Video joins images and stickers as thumbnail media, but only images and
// videos open in the viewer.
func TestMediaKindPredicates(t *testing.T) {
	for _, tc := range []struct {
		kind                        string
		visual, decodable, viewable bool
	}{
		{mediaKindImage, true, true, true},
		{mediaKindSticker, true, true, false},
		{mediaKindVideo, true, false, true},
		{mediaKindAudio, false, false, false},
		{mediaKindDocument, false, false, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := isVisualMedia(tc.kind); got != tc.visual {
				t.Errorf("isVisualMedia = %v, want %v", got, tc.visual)
			}
			if got := isDecodableImage(tc.kind); got != tc.decodable {
				t.Errorf("isDecodableImage = %v, want %v", got, tc.decodable)
			}
			if got := isViewableMedia(tc.kind); got != tc.viewable {
				t.Errorf("isViewableMedia = %v, want %v", got, tc.viewable)
			}
		})
	}
}

func TestDownloadLabel(t *testing.T) {
	if got, want := downloadLabel(mediaKindVideo, 0), "Download video"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := downloadLabel(mediaKindImage, 1024), "Download photo (1.0 KB)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
