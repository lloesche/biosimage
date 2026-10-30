package uefi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"testing"
)

// Uses the existing pinned download cache; no firmware is saved inside the repository.
func TestAnimatedASUSFirmware(t *testing.T) {
	cache := os.Getenv("BIOSIMAGE_FIRMWARE_CACHE")
	if cache == "" {
		t.Skip("set BIOSIMAGE_FIRMWARE_CACHE for the pinned ASUS injection test")
	}
	b, err := os.ReadFile("../testdata/firmware.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []vendorFirmware
	if err = json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	var entry *vendorFirmware
	for i := range entries {
		if entries[i].Board == "TUF GAMING B850I WIFI NEO" {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("ASUS fixture missing")
	}
	fw, err := fetchFirmware(cache, *entry)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(fw)
	a := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for i := 3; i < len(a.Pix); i += 4 {
		a.Pix[i] = 255
	}
	c := clone(a)
	c.SetRGBA(8, 4, color.RGBA{255, 32, 16, 255})
	gif, err := EncodeGIF([]image.Image{a, c}, []int{10, 20}, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(fw).ReplaceAnimatedLogo(gif, LogoFileGUID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fw, before) {
		t.Fatal("input firmware modified")
	}
	if len(p.Data) != len(fw) {
		t.Fatal("firmware size changed")
	}
	if p.AnimationSize <= 0 || p.AnimationSize > p.AnimationRoom {
		t.Fatalf("animation takes %d of %d bytes of free space", p.AnimationSize, p.AnimationRoom)
	}
	fs, err := Parse(before).FreeSpace()
	if err != nil {
		t.Fatal(err)
	}
	// Outside the destination main FV and original logo volumes, every byte must remain identical.
	originalLogos, err := Parse(before).FindLogos(LogoFileGUID)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []Range{{fs.Volume.Offset, fs.Volume.Size}}
	for _, l := range originalLogos {
		allowed = append(allowed, Range{l.Volume.Offset, l.Volume.Length})
	}
	for i := range before {
		if before[i] == p.Data[i] {
			continue
		}
		ok := false
		for _, r := range allowed {
			if i >= r.Offset && i < r.End() {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("unexpected modification at %#x", i)
		}
	}
	root := Parse(p.Data)
	root.expandAll()
	var drivers, dataFiles int
	var payload []byte
	root.Walk(func(n *Node) bool {
		if n.Kind == KindFile && (n.File.GUID == AnimationDriverGUID || n.File.GUID == AnimationDataGUID) {
			if n.Parent == nil || n.Parent.Kind != KindVolume || n.Parent.Offset != fs.Volume.Offset || n.Buf.Owner != nil {
				t.Error("animation file is not directly in the selected FV_MAIN")
			}
			if !headerChecksumOK(n.Buf.Data, n.File) || !dataChecksumOK(n.Buf.Data, n.File) {
				t.Error("inserted file checksum failed")
			}
			if n.File.GUID == AnimationDriverGUID {
				drivers++
				if n.File.Type != 0x07 {
					t.Error("animation module is not a DXE driver")
				}
				pe, depex := false, false
				gop := MustParseGUID("9042A9DE-23DC-4A38-96FB-7ADED080516A")
				wantDepex := append([]byte{2}, gop[:]...)
				wantDepex = append(wantDepex, 8)
				for _, s := range n.Children {
					if s.Kind != KindSection {
						continue
					}
					switch s.Section.Type {
					case SectionPE32:
						pe = bytes.Equal(s.Section.Body(), animationDriver)
					case 0x13:
						depex = bytes.Equal(s.Section.Body(), wantDepex)
					}
				}
				if !pe || !depex {
					t.Error("driver PE or GOP dependency expression changed during packaging")
				}
			} else {
				dataFiles++
				n.Walk(func(s *Node) bool {
					if s.Kind == KindSection && s.Section.Type == SectionRaw {
						payload = s.Section.Body()
					}
					return true
				})
			}
		}
		return true
	})
	expected, first, err := EncodeBootAnimation(&Animation{Frames: []*image.RGBA{a, c}, Delays: []int{10, 20}})
	if err != nil {
		t.Fatal(err)
	}
	if drivers != 1 || dataFiles != 1 || !bytes.Equal(payload, expected) {
		t.Fatal("driver/data round trip failed")
	}
	for _, l := range p.Logos {
		if !bytes.Equal(l.Image, first) {
			t.Fatal("fallback does not match first frame")
		}
	}
	if _, err := p.Root.ReplaceAnimatedLogo(gif, LogoFileGUID); err == nil {
		t.Fatal("duplicate injection accepted")
	}
	t.Logf("verified first-frame fallback, DXE and compressed data in ASUS FV_MAIN; no firmware written")
}
