package uefi

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"slices"
)

// MinFrameDelay is the shortest frame delay AMI firmware keeps, in units of 10 ms: it plays GIF
// animations at no more than 10 frames per second.
const MinFrameDelay = 10

// maxAnimationPixels bounds the frames DecodeAnimation returns, at 4 bytes per pixel.
const maxAnimationPixels = 32 << 20

// Animation is a decoded GIF animation: complete frames, each shown for its delay in units of
// 10 ms, as browsers play it (delays of 0 and 10 ms count as 100 ms).
type Animation struct {
	Frames []*image.RGBA
	Delays []int
}

// ShownFrame is a frame of an Animation and how long AMI firmware is to show it.
type ShownFrame struct{ Index, Delay int }

// Timing returns the frames to store and their delays, at least MinFrameDelay each. With
// keepSpeed, the animation keeps its speed and a frame is dropped if it would start less than
// MinFrameDelay after the previous one; otherwise every frame is kept and an animation faster
// than 10 frames per second plays more slowly.
func (a *Animation) Timing(keepSpeed bool) []ShownFrame {
	var shown []ShownFrame
	start, last := 0, 0 // start of frame i, and of the last frame shown
	for i, d := range a.Delays {
		if !keepSpeed || len(shown) == 0 || start >= last+MinFrameDelay {
			shown = append(shown, ShownFrame{Index: i})
			last = start
		}
		start += d
		shown[len(shown)-1].Delay = max(MinFrameDelay, start-last)
	}
	return shown
}

// DecodeAnimation decodes a GIF into complete frames, applying its disposal methods (which AMI
// firmware ignores).
func DecodeAnimation(b []byte) (*Animation, error) {
	g, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		return nil, errors.New("GIF has no size")
	}
	if len(g.Image)*bounds.Dx()*bounds.Dy() > maxAnimationPixels {
		return nil, fmt.Errorf("GIF has too many frames (%d) for its size", len(g.Image))
	}
	a := &Animation{}
	canvas := image.NewRGBA(bounds)
	for i, frame := range g.Image {
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		var before *image.RGBA
		if disposal == gif.DisposalPrevious {
			before = clone(canvas)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		d := g.Delay[i]
		if d <= 1 {
			d = MinFrameDelay
		}
		a.Frames, a.Delays = append(a.Frames, clone(canvas)), append(a.Delays, d)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = before
		}
	}
	return a, nil
}

func clone(m *image.RGBA) *image.RGBA {
	return &image.RGBA{Pix: bytes.Clone(m.Pix), Stride: m.Stride, Rect: m.Rect}
}

// EncodeGIF encodes opaque frames of the same size as a GIF that loops forever, frame i shown
// for delays[i] (in units of 10 ms). The frames share one palette: their exact colours if there
// are at most 256, else a median cut. Unless full is set, each frame after the first stores only
// the area that changed, as AMI firmware draws a frame over the last one. Unchanged frames are
// merged.
func EncodeGIF(frames []image.Image, delays []int, full bool) ([]byte, error) {
	if len(frames) == 0 {
		return nil, errors.New("no frames")
	}
	r := frames[0].Bounds()
	rgba := make([]*image.RGBA, len(frames))
	for i, f := range frames {
		if f.Bounds().Size() != r.Size() {
			return nil, errors.New("frames differ in size")
		}
		rgba[i] = toRGBA(f)
	}
	pal := gifPalette(rgba)
	index := paletteIndex(pal)
	g := &gif.GIF{Config: image.Config{ColorModel: pal, Width: r.Dx(), Height: r.Dy()}}
	var last *image.Paletted
	for i, f := range rgba {
		p := image.NewPaletted(image.Rect(0, 0, r.Dx(), r.Dy()), pal)
		for j := range p.Pix {
			p.Pix[j] = index(rgbKey(f.Pix[4*j:]))
		}
		delay := 0
		if i < len(delays) {
			delay = delays[i]
		}
		frame := p
		if last != nil {
			ch := changedRect(last, p)
			if ch.Empty() {
				g.Delay[len(g.Delay)-1] += delay
				continue
			}
			if !full {
				frame = p.SubImage(ch).(*image.Paletted)
			}
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, delay)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
		last = p
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// toRGBA returns img as an RGBA image whose bounds start at 0,0.
func toRGBA(img image.Image) *image.RGBA {
	if m, ok := img.(*image.RGBA); ok && m.Rect.Min == (image.Point{}) && m.Stride == 4*m.Rect.Dx() {
		return m
	}
	b := img.Bounds()
	m := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(m, m.Rect, img, b.Min, draw.Src)
	return m
}

func rgbKey(p []byte) uint32 { return uint32(p[0])<<16 | uint32(p[1])<<8 | uint32(p[2]) }

func keyColor(k uint32) color.RGBA { return color.RGBA{byte(k >> 16), byte(k >> 8), byte(k), 0xFF} }

// changedRect returns the smallest rectangle holding all pixels that differ between a and b.
func changedRect(a, b *image.Paletted) image.Rectangle {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	r := image.Rectangle{Min: image.Point{w, h}}
	for y := range h {
		for x := range w {
			if a.Pix[y*a.Stride+x] != b.Pix[y*b.Stride+x] {
				r.Min.X, r.Min.Y = min(r.Min.X, x), min(r.Min.Y, y)
				r.Max.X, r.Max.Y = max(r.Max.X, x+1), max(r.Max.Y, y+1)
			}
		}
	}
	if r.Max.X == 0 {
		return image.Rectangle{}
	}
	return r
}

// gifPalette returns the colours of frames if there are at most 256, else 256 colours chosen by
// median cut: the colour space is split, one box at a time, at the median of the widest channel
// of the box with the widest channel, and each box becomes the average of its colours.
func gifPalette(frames []*image.RGBA) color.Palette {
	hist := map[uint32]int{}
	for _, f := range frames {
		for i := 0; i < len(f.Pix); i += 4 {
			hist[rgbKey(f.Pix[i:])]++
		}
	}
	type entry struct {
		key uint32
		n   int
	}
	all := make([]entry, 0, len(hist))
	for k, n := range hist {
		all = append(all, entry{k, n})
	}
	slices.SortFunc(all, func(a, b entry) int { return int(a.key) - int(b.key) })
	if len(all) <= 256 {
		pal := make(color.Palette, len(all))
		for i, e := range all {
			pal[i] = keyColor(e.key)
		}
		return pal
	}
	channel := func(k uint32, c int) int { return int(k>>(16-8*c)) & 0xFF }
	// widest returns the channel with the largest range in box, and the range.
	widest := func(box []entry) (int, int) {
		best, width := 0, -1
		for c := range 3 {
			lo, hi := 255, 0
			for _, e := range box {
				v := channel(e.key, c)
				lo, hi = min(lo, v), max(hi, v)
			}
			if hi-lo > width {
				best, width = c, hi-lo
			}
		}
		return best, width
	}
	boxes := [][]entry{all}
	for len(boxes) < 256 {
		pick, c, width := -1, 0, 0
		for i, box := range boxes {
			if bc, bw := widest(box); bw > width {
				pick, c, width = i, bc, bw
			}
		}
		if pick < 0 {
			break
		}
		box := boxes[pick]
		slices.SortFunc(box, func(a, b entry) int { return channel(a.key, c) - channel(b.key, c) })
		total, half := 0, 0
		for _, e := range box {
			total += e.n
		}
		cut := 1
		for i, e := range box[:len(box)-1] {
			half += e.n
			if 2*half >= total {
				cut = i + 1
				break
			}
		}
		boxes[pick] = box[:cut]
		boxes = append(boxes, box[cut:])
	}
	pal := make(color.Palette, len(boxes))
	for i, box := range boxes {
		var r, g, b, n int
		for _, e := range box {
			r += channel(e.key, 0) * e.n
			g += channel(e.key, 1) * e.n
			b += channel(e.key, 2) * e.n
			n += e.n
		}
		pal[i] = color.RGBA{byte((r + n/2) / n), byte((g + n/2) / n), byte((b + n/2) / n), 0xFF}
	}
	return pal
}

// paletteIndex returns a function mapping a colour to its nearest palette entry.
func paletteIndex(pal color.Palette) func(uint32) uint8 {
	cache := map[uint32]uint8{}
	return func(k uint32) uint8 {
		if i, ok := cache[k]; ok {
			return i
		}
		i := uint8(pal.Index(keyColor(k)))
		cache[k] = i
		return i
	}
}
