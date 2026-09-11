package main

import (
	"image"
	"io"
	"os"

	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	// WhatsApp stickers are WebP, which gdk-pixbuf only decodes when the
	// separate webp-pixbuf-loader package is installed, and which it never
	// animates. Decoding in Go keeps rendering independent of that package.
	"github.com/gen2brain/webp"
	xdraw "golang.org/x/image/draw"
)

// maxFrameSize bounds a decoded frame. Stickers arrive at 512px but render at
// roughly a third of that, and an animation holds every frame in memory at
// once, so they are scaled down before being handed to GTK.
const maxFrameSize = 192

// defaultFrameDelay is used for frames whose delay is missing or absurdly
// short, matching how browsers clamp animation timings.
const defaultFrameDelay = 100

// animation is a decoded image: a single frame for stills, several for
// animated stickers. delays is empty when there is nothing to animate.
type animation struct {
	frames []*gdkpixbuf.Pixbuf
	delays []int // milliseconds, one per frame
}

func (a *animation) animated() bool { return len(a.frames) > 1 }

// loadAnimation decodes an image file, going through the Go WebP decoder for
// WebP and leaving every other format to gdk-pixbuf. Results are cached because
// the ListView rebinds rows constantly while scrolling.
func (w *window) loadAnimation(path string) (*animation, error) {
	if cached, ok := w.animCache[path]; ok {
		return cached, nil
	}

	anim, err := decodeAnimation(path)
	if err != nil {
		return nil, err
	}
	w.animCache[path] = anim
	return anim, nil
}

// showAnimation renders a decoded image into a Picture, starting a frame timer
// when it has more than one frame.
func (w *window) showAnimation(preview *gtk.Picture, anim *animation) {
	preview.SetPixbuf(anim.frames[0])
	if !anim.animated() {
		return
	}

	frame := 0
	var tick func() bool
	tick = func() bool {
		// The row may have been recycled onto another message since the last
		// frame, which stopAnimation records by dropping the entry.
		if w.animTimers[preview] == 0 {
			return false
		}
		frame = (frame + 1) % len(anim.frames)
		preview.SetPixbuf(anim.frames[frame])
		w.animTimers[preview] = glib.TimeoutAdd(uint(anim.delays[frame]), tick)
		return false
	}
	w.animTimers[preview] = glib.TimeoutAdd(uint(anim.delays[0]), tick)
}

// stopAnimation halts the frame timer attached to a Picture, if any.
func (w *window) stopAnimation(preview *gtk.Picture) {
	if handle, ok := w.animTimers[preview]; ok {
		if handle != 0 {
			glib.SourceRemove(handle)
		}
		delete(w.animTimers, preview)
	}
}

func decodeAnimation(path string) (*animation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var header [12]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, err
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		pixbuf, err := gdkpixbuf.NewPixbufFromFile(path)
		if err != nil {
			return nil, err
		}
		return &animation{frames: []*gdkpixbuf.Pixbuf{pixbuf}}, nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	decoded, err := webp.DecodeAll(f)
	if err != nil {
		return nil, err
	}

	anim := &animation{
		frames: make([]*gdkpixbuf.Pixbuf, 0, len(decoded.Image)),
		delays: make([]int, 0, len(decoded.Image)),
	}
	for i, img := range decoded.Image {
		anim.frames = append(anim.frames, pixbufFromImage(scaleDown(img, maxFrameSize)))

		delay := defaultFrameDelay
		if i < len(decoded.Delay) && decoded.Delay[i] >= 10 {
			delay = decoded.Delay[i]
		}
		anim.delays = append(anim.delays, delay)
	}
	return anim, nil
}

// scaleDown shrinks an image so neither side exceeds max, and converts it to
// RGBA on the way. Images already within bounds are only converted.
func scaleDown(img image.Image, max int) *image.RGBA {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width > max || height > max {
		if width >= height {
			width, height = max, height*max/width
		} else {
			width, height = width*max/height, max
		}
	}

	out := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, bounds, xdraw.Src, nil)
	return out
}

// pixbufFromImage copies an RGBA image into a pixbuf, which is the only layout
// GTK accepts alongside plain RGB.
func pixbufFromImage(img *image.RGBA) *gdkpixbuf.Pixbuf {
	size := img.Bounds().Size()
	return gdkpixbuf.NewPixbufFromBytes(
		glib.NewBytes(img.Pix), gdkpixbuf.ColorspaceRGB, true, 8,
		size.X, size.Y, img.Stride,
	)
}
