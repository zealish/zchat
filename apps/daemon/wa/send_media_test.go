package wa

import "testing"

func TestMediaKindClassifiesUploads(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/png", TypeImage},
		{"image/jpeg", TypeImage},
		// WhatsApp renders WebP as a sticker, never as a photo.
		{"image/webp", TypeSticker},
		{"video/mp4", TypeVideo},
		{"audio/ogg", TypeAudio},
		{"application/pdf", TypeDocument},
		{"", TypeDocument},
	}

	for _, tc := range tests {
		if got := mediaKind(tc.mime); got != tc.want {
			t.Errorf("mediaKind(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestDetectMimeFallsBackToSniffing(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")

	if got := detectMime("photo.png", png); got != "image/png" {
		t.Errorf("extension lookup = %q, want image/png", got)
	}
	// Pasted clipboard images arrive without a usable extension.
	if got := detectMime("clipboard", png); got != "image/png" {
		t.Errorf("content sniffing = %q, want image/png", got)
	}
}
