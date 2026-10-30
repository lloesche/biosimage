package uefi

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
)

func TestBootAnimationRectanglesAndFallback(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			a.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	b := clone(a)
	b.SetRGBA(5, 3, color.RGBA{255, 0, 0, 255})
	data, first, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, b, b}, Delays: []int{1, 25, 100}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "BIA2" || binary.LittleEndian.Uint16(data[8:]) != 3 || binary.LittleEndian.Uint16(data[10:]) != 0 {
		t.Fatal("bad header")
	}
	info, err := InspectImage(first)
	if err != nil || info.Format != "BMP" || info.Width != 16 || info.Height != 8 {
		t.Fatalf("fallback: %+v %v", info, err)
	}
	off := animationHeader
	want := [][]int{{10, 0, 0, 16, 8, 0}, {25, 5, 3, 1, 1, 0}, {100, 0, 0, 1, 1, 0}}
	for _, record := range want {
		for j, v := range record {
			if got := int(binary.LittleEndian.Uint16(data[off+2*j:])); got != v {
				t.Fatalf("record %+v field%d=%d", record, j, got)
			}
		}
		off += 12 + record[3]*record[4]
	}
	if off != len(data) {
		t.Fatal("trailing data")
	}
	if _, _, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, b}, Delays: []int{1, 6001}}, 0); err == nil {
		t.Fatal("unbounded delay accepted")
	}
	if _, _, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{nil, b}, Delays: []int{1, 10}}, 0); err == nil {
		t.Fatal("nil frame accepted")
	}
}

// TestDecodeBootAnimation checks that the frame data decodes to the frames it was made from,
// and that damaged data is rejected.
func TestBootAnimationLogoFrame(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range a.Pix {
		a.Pix[i] = 255 // white
	}
	b := clone(a)
	b.SetRGBA(5, 3, color.RGBA{255, 0, 0, 255})
	anim := &Animation{Frames: []*image.RGBA{a, b}, Delays: []int{10, 10}}
	for frame, want := range []*image.RGBA{a, b} {
		data, logo, err := EncodeBootAnimation(anim, frame)
		if err != nil {
			t.Fatal(err)
		}
		bmp, err := EncodeImage(want, "BMP", 100)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(logo, bmp) {
			t.Errorf("logo is not frame %d", frame)
		}
		if first, _, err := EncodeBootAnimation(anim, 0); err != nil || !bytes.Equal(first, data) {
			t.Errorf("the logo frame changed the frame data: %v", err)
		}
	}
	for _, frame := range []int{-1, 2} {
		if _, _, err := EncodeBootAnimation(anim, frame); err == nil {
			t.Errorf("logo frame %d accepted", frame)
		}
	}
}

func TestDecodeBootAnimation(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for i := range a.Pix {
		a.Pix[i] = 255 // white
	}
	b, c := clone(a), clone(a)
	b.SetRGBA(5, 3, color.RGBA{255, 0, 0, 255})
	c.SetRGBA(15, 7, color.RGBA{0, 0, 255, 255})
	data, _, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, b, c}, Delays: []int{1, 25, 100}, Plays: 7}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBootAnimation(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames) != 3 || !slices.Equal(got.Delays, []int{10, 25, 100}) || got.Plays != 7 {
		t.Fatalf("%d frames, delays %v, %d plays", len(got.Frames), got.Delays, got.Plays)
	}
	if _, _, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, b}, Delays: []int{1, 10}, Plays: 1 << 16}, 0); err == nil {
		t.Error("play count out of range accepted")
	}
	for i, want := range []*image.RGBA{a, b, c} {
		if !bytes.Equal(got.Frames[i].Pix, want.Pix) {
			t.Errorf("frame %d differs", i)
		}
	}
	for name, bad := range map[string][]byte{
		"truncated": data[:len(data)-1],
		"no magic":  append([]byte("BIA1"), data[4:]...),
		"outside frame": func() []byte {
			d := bytes.Clone(data)
			binary.LittleEndian.PutUint16(d[animationHeader+12+16*8+2:], 16)
			return d
		}(),
	} {
		if _, err := DecodeBootAnimation(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAnimationGateRejectsUnknownFirmwareWithoutMutation(t *testing.T) {
	fw := buildImage(buildVolume(t, 0x8000, 0xff, rawLogoFile(t, makeBMP(8, 8, false), 0xff)))
	before := bytes.Clone(fw)
	if err := Parse(fw).AnimationSupport(); err == nil {
		t.Fatal("unknown firmware accepted")
	}
	if _, err := Parse(fw).ReplaceAnimatedLogo([]byte("GIF89a"), LogoFileGUID, AnimationOptions{}); err == nil {
		t.Fatal("unknown image patched")
	}
	if !bytes.Equal(before, fw) {
		t.Fatal("input modified on failure")
	}
}

func TestEmbeddedAnimationDriverIsProductionUEFI(t *testing.T) {
	if err := validateAnimationDriver(animationDriver); err != nil {
		t.Fatal(err)
	}
}

func TestAnimationRejectsBootGuardEvidence(t *testing.T) {
	fw := buildImage(buildVolume(t, 0x8000, 0xff, rawLogoFile(t, makeBMP(8, 8, false), 0xff)))
	fw = append(fw, []byte("__KEYM____ACBP__")...)
	before := bytes.Clone(fw)
	if err := Parse(fw).AnimationSupport(); err == nil || !strings.Contains(err.Error(), "Boot Guard") {
		t.Fatalf("protection not reported: %v", err)
	}
	if _, err := Parse(fw).ReplaceAnimatedLogo(nil, LogoFileGUID, AnimationOptions{}); err == nil || !strings.Contains(err.Error(), "Boot Guard") {
		t.Fatalf("protection bypassed: %v", err)
	}
	if !bytes.Equal(fw, before) {
		t.Fatal("protected input changed")
	}
}
