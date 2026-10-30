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

// maxAnimationPlays is the largest play count of an animation.
const maxAnimationPlays = 1<<16 - 1

// Animation is a decoded GIF animation: complete frames, each shown for its delay in units of
// 10 ms, as browsers play it (delays of 0 and 10 ms count as 100 ms). Plays is how often it
// plays: 0 loops it forever.
type Animation struct {
	Frames []*image.RGBA
	Delays []int
	Plays  int
}

// gifPlays returns the Plays of a GIF with loop count n (image/gif: 0 loops forever, -1 plays
// once, n restarts n times).
func gifPlays(n int) int {
	if n == 0 {
		return 0
	}
	return min(max(n, 0)+1, maxAnimationPlays)
}

// gifLoopCount is the inverse of gifPlays.
func gifLoopCount(plays int) int {
	if plays == 0 {
		return 0
	}
	if plays == 1 {
		return -1
	}
	return plays - 1
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
	a := &Animation{Plays: gifPlays(g.LoopCount)}
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

// EncodeGIF encodes opaque frames of the same size as a GIF that plays plays times (0 loops
// forever), frame i shown for delays[i] (in units of 10 ms). The frames share one palette: their
// exact colours if there are at most 256, else a median cut. Unless full is set, each frame
// after the first stores only the area that changed, as AMI firmware draws a frame over the
// last one. Unchanged frames are merged; stored[i] is the frame of the GIF that shows frames[i].
func EncodeGIF(frames []image.Image, delays []int, plays int, full bool) (b []byte, stored []int, err error) {
	if len(frames) == 0 {
		return nil, nil, errors.New("no frames")
	}
	r := frames[0].Bounds()
	rgba := make([]*image.RGBA, len(frames))
	for i, f := range frames {
		if f.Bounds().Size() != r.Size() {
			return nil, nil, errors.New("frames differ in size")
		}
		rgba[i] = toRGBA(f)
	}
	stored = make([]int, len(frames))
	pal := gifPalette(rgba)
	index := paletteIndex(pal)
	g := &gif.GIF{Config: image.Config{ColorModel: pal, Width: r.Dx(), Height: r.Dy()}, LoopCount: gifLoopCount(plays)}
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
				stored[i] = len(g.Image) - 1
				continue
			}
			if !full {
				frame = p.SubImage(ch).(*image.Paletted)
			}
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, delay)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
		stored[i] = len(g.Image) - 1
		last = p
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), stored, nil
}

// FirmwareTiming returns a copy of the GIF b with every frame's delay moved to the frame
// before it, and the last frame's to the first. AMITSE waits for a frame's delay before
// drawing it, so that a frame stays for the next frame's delay; browsers show a frame for its
// own. With the delays moved, the firmware shows each frame as long as a browser shows b.
func FirmwareTiming(b []byte) ([]byte, error) {
	b = bytes.Clone(b)
	var delays []int // offsets of the delays in the graphic control extensions
	if len(b) < 13 {
		return nil, errors.New("truncated GIF")
	}
	p := 13
	if b[10]&0x80 != 0 {
		p += 3 << (b[10]&7 + 1)
	}
	skipBlocks := func() error {
		for p < len(b) && b[p] != 0 {
			p += 1 + int(b[p])
		}
		if p >= len(b) {
			return errors.New("truncated GIF data")
		}
		p++
		return nil
	}
	for p < len(b) && b[p] != 0x3B {
		switch {
		case b[p] == 0x21 && p+8 <= len(b):
			if b[p+1] == 0xF9 {
				delays = append(delays, p+4)
			}
			p += 2
		case b[p] == 0x2C && p+10 <= len(b):
			if b[p+9]&0x80 != 0 {
				p += 3 << (b[p+9]&7 + 1)
			}
			p += 11 // descriptor and LZW minimum code size
		default:
			return nil, fmt.Errorf("unexpected GIF block at %d", p)
		}
		if err := skipBlocks(); err != nil {
			return nil, err
		}
	}
	if len(delays) < 2 {
		return b, nil
	}
	last := [2]byte{b[delays[len(delays)-1]], b[delays[len(delays)-1]+1]}
	for i := len(delays) - 1; i > 0; i-- {
		copy(b[delays[i]:delays[i]+2], b[delays[i-1]:delays[i-1]+2])
	}
	copy(b[delays[0]:], last[:])
	return b, nil
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
	// A box is a set of colours, with the squared error of showing them all in their average
	// colour, weighted by their pixels, and the channel that contributes most to it.
	type box struct {
		entries []entry
		err     float64
		channel int
	}
	measure := func(entries []entry) box {
		b := box{entries: entries}
		worst := -1.0
		for c := range 3 {
			var n, sum, squares float64
			for _, e := range entries {
				v, w := float64(channel(e.key, c)), float64(e.n)
				n += w
				sum += v * w
				squares += v * v * w
			}
			variance := squares - sum*sum/n
			b.err += variance
			if variance > worst {
				b.channel, worst = c, variance
			}
		}
		return b
	}
	// Split the box with the largest error until the palette is full. So the colours go where
	// the error is: a gradient gets many, the black around a logo none.
	boxes := []box{measure(all)}
	for len(boxes) < 256 {
		pick := -1
		for i, b := range boxes {
			if b.err > 0 && (pick < 0 || b.err > boxes[pick].err) {
				pick = i
			}
		}
		if pick < 0 {
			break
		}
		b := boxes[pick]
		c := b.channel
		slices.SortFunc(b.entries, func(a, b entry) int { return channel(a.key, c) - channel(b.key, c) })
		// Cut where the two parts are best described by their averages.
		var n, sum float64
		for _, e := range b.entries {
			n += float64(e.n)
			sum += float64(channel(e.key, c)) * float64(e.n)
		}
		cut, best := 1, -1.0
		var ln, lsum float64
		for i, e := range b.entries[:len(b.entries)-1] {
			ln += float64(e.n)
			lsum += float64(channel(e.key, c)) * float64(e.n)
			if channel(e.key, c) == channel(b.entries[i+1].key, c) {
				continue
			}
			if score := lsum*lsum/ln + (sum-lsum)*(sum-lsum)/(n-ln); score > best {
				cut, best = i+1, score
			}
		}
		boxes[pick] = measure(b.entries[:cut])
		boxes = append(boxes, measure(b.entries[cut:]))
	}
	pal := make(color.Palette, len(boxes))
	for i, box := range boxes {
		var r, g, b, n int
		for _, e := range box.entries {
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
