package main

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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

// maxStillSize bounds a still photo. Previews render at 240x180, so anything
// larger is only paid for in decode time and memory.
const maxStillSize = 512

// defaultFrameDelay is used for frames whose delay is missing or absurdly
// short, matching how browsers clamp animation timings.
const defaultFrameDelay = 100

// rawAnimation is a decoded image in plain Go form. Decoding happens on a
// worker goroutine, which may not touch GTK, so pixbufs are built from this on
// the main loop afterwards.
type rawAnimation struct {
	frames []*image.RGBA
	delays []int // milliseconds, one per frame
}

// animation is a decoded image ready for GTK: a single frame for stills,
// several for animated stickers. delays is empty when there is nothing to
// animate.
type animation struct {
	frames []*gdkpixbuf.Pixbuf
	delays []int // milliseconds, one per frame
}

func (a *animation) animated() bool { return len(a.frames) > 1 }

// loadAnimation resolves a decoded image and hands it to done, with nil when
// the file could not be read.
//
// Decoding a sticker takes tens of milliseconds per frame, which freezes the
// window when it runs during a list bind, so it is pushed onto a worker
// goroutine. done is always invoked on the main loop, immediately for a cache
// hit and later otherwise, so callers must re-check that the row they are
// filling still wants this image.
//
// Results are cached because the ListView rebinds rows constantly while
// scrolling, and concurrent requests for the same path share one decode.
func (w *window) loadAnimation(path string, done func(*animation)) {
	if cached, ok := w.animCache[path]; ok {
		done(cached)
		return
	}
	if waiters, inFlight := w.animPending[path]; inFlight {
		w.animPending[path] = append(waiters, done)
		return
	}
	w.animPending[path] = []func(*animation){done}

	go func() {
		raw, err := decodeAnimation(path)
		glib.IdleAdd(func() {
			waiters := w.animPending[path]
			delete(w.animPending, path)

			var anim *animation
			if err != nil {
				w.log.Warn().Err(err).Str("path", path).Msg("decode media preview")
			} else {
				anim = raw.toAnimation()
				w.animCache[path] = anim
			}
			for _, waiter := range waiters {
				waiter(anim)
			}
		})
	}()
}

// toAnimation uploads decoded frames into pixbufs. It must run on the main
// loop.
func (r *rawAnimation) toAnimation() *animation {
	anim := &animation{
		frames: make([]*gdkpixbuf.Pixbuf, 0, len(r.frames)),
		delays: r.delays,
	}
	for _, frame := range r.frames {
		anim.frames = append(anim.frames, pixbufFromImage(frame))
	}
	return anim
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

// decodeAnimation reads an image file into plain Go frames. It touches no GTK
// state, so it is safe to call from a worker goroutine.
func decodeAnimation(path string) (*rawAnimation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var header [12]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		img, _, err := image.Decode(f)
		if err != nil {
			return nil, err
		}
		return &rawAnimation{frames: []*image.RGBA{scaleDown(img, maxStillSize)}}, nil
	}

	decoded, err := webp.DecodeAll(f)
	if err != nil {
		return nil, err
	}

	anim := &rawAnimation{
		frames: make([]*image.RGBA, 0, len(decoded.Image)),
		delays: make([]int, 0, len(decoded.Image)),
	}
	for i, img := range decoded.Image {
		anim.frames = append(anim.frames, scaleDown(img, maxFrameSize))

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
