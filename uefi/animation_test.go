package uefi

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
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
	data, first, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, b, b}, Delays: []int{1, 25, 100}})
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "BIA1" || binary.LittleEndian.Uint32(data[8:]) != 3 {
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
	if _, _, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, b}, Delays: []int{1, 6001}}); err == nil {
		t.Fatal("unbounded delay accepted")
	}
	if _, _, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{nil, b}, Delays: []int{1, 10}}); err == nil {
		t.Fatal("nil frame accepted")
	}
}

func TestAnimationGateRejectsUnknownFirmwareWithoutMutation(t *testing.T) {
	fw := buildImage(buildVolume(t, 0x8000, 0xff, rawLogoFile(t, makeBMP(8, 8, false), 0xff)))
	before := bytes.Clone(fw)
	if err := Parse(fw).AnimationSupport(); err == nil {
		t.Fatal("unknown firmware accepted")
	}
	if _, err := Parse(fw).ReplaceAnimatedLogo([]byte("GIF89a"), LogoFileGUID); err == nil {
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
	if _, err := Parse(fw).ReplaceAnimatedLogo(nil, LogoFileGUID); err == nil || !strings.Contains(err.Error(), "Boot Guard") {
		t.Fatalf("protection bypassed: %v", err)
	}
	if !bytes.Equal(fw, before) {
		t.Fatal("protected input changed")
	}
}
