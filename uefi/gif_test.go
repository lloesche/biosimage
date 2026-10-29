package uefi

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"slices"
	"testing"
)

func filled(w, h int, c color.RGBA) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Rect, &image.Uniform{c}, image.Point{}, draw.Src)
	return m
}

var (
	red   = color.RGBA{0xFF, 0, 0, 0xFF}
	green = color.RGBA{0, 0xFF, 0, 0xFF}
	blue  = color.RGBA{0, 0, 0xFF, 0xFF}
)

func TestEncodeGIFStoresChangesOnly(t *testing.T) {
	a := filled(40, 30, red)
	b := clone(a)
	draw.Draw(b, image.Rect(5, 6, 9, 8), &image.Uniform{blue}, image.Point{}, draw.Src)
	frames := []image.Image{a, clone(a), b}
	for _, full := range []bool{false, true} {
		data, err := EncodeGIF(frames, []int{10, 15, 30}, full)
		if err != nil {
			t.Fatal(err)
		}
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Image) != 2 || !slices.Equal(g.Delay, []int{25, 30}) || g.LoopCount != 0 {
			t.Fatalf("%d frames, delays %v, loop %d", len(g.Image), g.Delay, g.LoopCount)
		}
		want := image.Rect(5, 6, 9, 8)
		if full {
			want = a.Rect
		}
		if r := g.Image[1].Bounds(); r != want {
			t.Fatalf("full=%v: second frame covers %v", full, r)
		}
		anim, err := DecodeAnimation(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(anim.Frames) != 2 || !bytes.Equal(anim.Frames[0].Pix, a.Pix) || !bytes.Equal(anim.Frames[1].Pix, b.Pix) {
			t.Fatal("frames do not decode to the input")
		}
	}
}

func TestEncodeGIFManyColours(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 64))
	for y := range 64 {
		for x := range 256 {
			img.Set(x, y, color.RGBA{byte(x), byte(y * 4), byte(255 - x), 0xFF})
		}
	}
	data, err := EncodeImage(img, "GIF", 0)
	if err != nil {
		t.Fatal(err)
	}
	anim, err := DecodeAnimation(data)
	if err != nil {
		t.Fatal(err)
	}
	worst := 0
	for i, v := range anim.Frames[0].Pix {
		d := int(v) - int(img.Pix[i])
		worst = max(worst, d, -d)
	}
	if worst > 24 {
		t.Fatalf("a channel is off by %d", worst)
	}
}

// encodeTestGIF encodes paletted frames as they are, with the given delays and disposals.
func encodeTestGIF(t *testing.T, frames []*image.Paletted, delays []int, disposal []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	g := &gif.GIF{Image: frames, Delay: delays, Disposal: disposal, Config: image.Config{Width: 20, Height: 20}}
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAnimationTiming(t *testing.T) {
	pal := color.Palette{red, green}
	var frames []*image.Paletted
	for i := range 6 {
		f := image.NewPaletted(image.Rect(0, 0, 20, 20), pal)
		f.Pix[i] = 1 // a different frame each time
		frames = append(frames, f)
	}
	// 40 ms per frame, 240 ms in all.
	anim, err := DecodeAnimation(encodeTestGIF(t, frames, []int{4, 4, 4, 4, 4, 4}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(anim.Frames) != 6 || anim.Frames[3].RGBAAt(3, 0) != green || anim.Frames[3].RGBAAt(2, 0) != red {
		t.Fatal("frames not decoded in order")
	}
	// Keeping the speed shows the frames at 0 and 120 ms; keeping all frames slows down.
	if got := anim.Timing(true); !slices.Equal(got, []ShownFrame{{0, 12}, {3, 12}}) {
		t.Fatalf("keeping speed: %v", got)
	}
	if got := anim.Timing(false); len(got) != 6 || got[5] != (ShownFrame{5, 10}) {
		t.Fatalf("keeping all frames: %v", got)
	}
	// A delay of 0 counts as 100 ms, like in browsers.
	anim, err = DecodeAnimation(encodeTestGIF(t, frames[:2], []int{0, 0}, nil))
	if err != nil || !slices.Equal(anim.Delays, []int{10, 10}) {
		t.Fatalf("delays %v, %v", anim.Delays, err)
	}
}

func TestDecodeAnimationDisposal(t *testing.T) {
	pal := color.Palette{red, blue, green, color.RGBA{}}
	full := image.NewPaletted(image.Rect(0, 0, 20, 20), pal) // red
	patch := image.NewPaletted(image.Rect(2, 2, 6, 6), pal)
	for i := range patch.Pix {
		patch.Pix[i] = 1 // blue
	}
	dot := image.NewPaletted(image.Rect(10, 10, 11, 11), pal)
	dot.Pix[0] = 2 // green
	for _, c := range []struct {
		disposal byte
		at3x3    color.RGBA
	}{
		{gif.DisposalNone, blue},
		{gif.DisposalBackground, color.RGBA{}},
		{gif.DisposalPrevious, red},
	} {
		data := encodeTestGIF(t, []*image.Paletted{full, patch, dot}, []int{10, 10, 10}, []byte{gif.DisposalNone, c.disposal, gif.DisposalNone})
		anim, err := DecodeAnimation(data)
		if err != nil {
			t.Fatal(err)
		}
		last := anim.Frames[2]
		if got := last.RGBAAt(3, 3); got != c.at3x3 || last.RGBAAt(10, 10) != green || last.RGBAAt(0, 0) != red {
			t.Fatalf("disposal %d: pixel at 3,3 is %v, want %v", c.disposal, got, c.at3x3)
		}
	}
}
