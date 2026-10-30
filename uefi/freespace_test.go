package uefi

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"slices"
	"strings"
	"testing"
)

// namedVolume returns an FFS2 volume with an extended header naming it.
func namedVolume(t *testing.T, length int, name GUID, files ...[]byte) []byte {
	t.Helper()
	v := buildVolume(t, length, 0xFF)
	const hdrLen, extSize = 0x48, 0x14
	binary.LittleEndian.PutUint16(v[50:], 0)
	binary.LittleEndian.PutUint16(v[52:], hdrLen)
	var sum uint16
	for i := 0; i < hdrLen; i += 2 {
		sum += binary.LittleEndian.Uint16(v[i:])
	}
	binary.LittleEndian.PutUint16(v[50:], -sum)
	copy(v[hdrLen:], name[:])
	binary.LittleEndian.PutUint32(v[hdrLen+16:], extSize)
	p := alignUp(hdrLen+extSize, 8)
	for _, f := range files {
		p = alignUp(p, 8)
		copy(v[p:], f)
		p += len(f)
	}
	if p > length {
		t.Fatal("files do not fit into volume")
	}
	return v
}

var logoVolumeGUID = MustParseGUID("818A40E1-D82E-497D-A058-D261636A4CB7")

// romLayoutFile returns a file with an AMI ROM layout table of areas.
func romLayoutFile(t *testing.T, areas ...romArea) []byte {
	t.Helper()
	b := append(romLayoutSectionGUID[:], "ROML"...)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 40)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(areas)))
	for _, a := range areas {
		b = append(b, a.Name[:]...)
		b = binary.LittleEndian.AppendUint64(b, uint64(a.Offset))
		for _, v := range []uint32{uint32(a.Offset), uint32(a.Size), a.Type, a.Attributes} {
			b = binary.LittleEndian.AppendUint32(b, v)
		}
	}
	return mustFile(t, MustParseGUID("C91C3C17-FC74-46E5-BDBE-6F486A5A9F3C"), FileTypeFreeform, NewSection(0x18, b), 0xFF)
}

// freeSpaceImage returns an image like ASUS's: the logo in a small volume of its own and a
// main volume with AMITSE (with tseCode) and free space, published for DXE with mainAttrs.
func freeSpaceImage(t *testing.T, mainAttrs uint32, tseCode []byte) []byte {
	t.Helper()
	const logoLen, mainLen = 0x4000, 0x40000
	tse := tseFile(t, tseCode)
	layout := romLayoutFile(t,
		romArea{Name: logoVolumeGUID, Offset: 0, Size: logoLen, Attributes: 0xa},
		romArea{Name: mainVolumeGUID, Offset: logoLen, Size: mainLen, Attributes: mainAttrs})
	return buildImage(
		namedVolume(t, logoLen, logoVolumeGUID, otherFile(t, 10, 0xFF), rawLogoFile(t, makeBMP(8, 8, false), 0xFF)),
		namedVolume(t, mainLen, mainVolumeGUID, layout, tse, otherFile(t, 100, 0xFF)))
}

func TestUseFreeSpace(t *testing.T) {
	fw := freeSpaceImage(t, 0x20a, anyVolumeCode)
	root := Parse(fw)
	fs, err := root.FreeSpace()
	if err != nil {
		t.Fatal(err)
	}
	logos, err := root.FindLogos(LogoFileGUID)
	if err != nil {
		t.Fatal(err)
	}
	img := makeBMP(150, 100, true) // too big for the logo volume
	if _, _, err := logos[0].StoredSize(img, ReplaceOptions{}); !errors.Is(err, errTooBig) {
		t.Fatalf("image fits the logo volume: %v", err)
	}
	for _, c := range []struct {
		compression Compression
		storage     Storage
		encoding    Encoding
	}{
		{CompressAsOriginal, StoredUncompressed, EncodingRawSection},
		{CompressLZMA, StoredLZMAWrapped, EncodingLZMA},
	} {
		opt := ReplaceOptions{Compression: c.compression, UseFreeSpace: true}
		size, storage, err := fs.StoredSize(logos[0], img, opt)
		if err != nil || size > fs.Capacity || storage != c.storage {
			t.Fatalf("%v: StoredSize %d of %d, %v: %v", c.storage, size, fs.Capacity, storage, err)
		}
		p := replace(t, fw, img, opt)
		ch := p.Changes[0]
		if len(p.Logos) != 1 || p.Logos[0].Volume.Offset != fs.Volume.Offset || ch.NewSize != size || ch.Storage != c.storage {
			t.Fatalf("%v: logo not moved to the main volume: %+v", c.storage, ch)
		}
		if l := p.Logos[0]; l.Encoding != c.encoding || l.Name != "Logo" {
			t.Fatalf("%v: read back as %v, name %q", c.storage, l.Encoding, l.Name)
		}
		// The old logo file is a pad file now; the other files are unchanged.
		if p.Data[logos[0].File.Offset+18] != FileTypePad || !bytes.Contains(p.Data, otherFile(t, 10, 0xFF)) || !bytes.Contains(p.Data, otherFile(t, 100, 0xFF)) {
			t.Fatalf("%v: old logo not removed, or another file changed", c.storage)
		}
	}
}

func TestNoFreeSpace(t *testing.T) {
	// Not published for DXE.
	if _, err := Parse(freeSpaceImage(t, 0x200, anyVolumeCode)).FreeSpace(); !errors.Is(err, ErrNoFreeSpace) {
		t.Fatalf("got %v", err)
	}
	// AMITSE does not read the logo from every volume.
	if _, err := Parse(freeSpaceImage(t, 0x20a, []byte{0xc3})).FreeSpace(); !errors.Is(err, ErrNoFreeSpace) {
		t.Fatalf("got %v", err)
	}
	// No ROM layout.
	fw := buildImage(buildVolume(t, 0x10000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	if _, err := ReplaceLogo(fw, makeBMP(8, 8, true), LogoFileGUID, ReplaceOptions{UseFreeSpace: true}); !errors.Is(err, ErrNoFreeSpace) {
		t.Fatalf("got %v", err)
	}
}

func TestBootGuard(t *testing.T) {
	logoVol := buildVolume(t, 0x10000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF))
	hashFile := func(sum []byte, addr, size int) []byte {
		body := append(bytes.Clone(sum), make([]byte, 24)...)
		binary.LittleEndian.PutUint32(body[32:], uint32(addr))
		binary.LittleEndian.PutUint32(body[44:], uint32(size))
		return mustFile(t, bootGuardHashGUID, FileTypeFreeform, NewSection(SectionRaw, body), 0xFF)
	}
	manifests := mustFile(t, MustParseGUID("7ABBE3D6-0E64-4F1B-9B25-4A0D3E9F9E51"), FileTypeFreeform,
		NewSection(SectionRaw, []byte("__KEYM__....__ACBP__....")), 0xFF)
	// The image is laid out as [0x1000 prefix][logo volume][boot block]; the hash covers the
	// logo volume.
	const bbLen = 0x1000
	total := 0x1000 + len(logoVol) + bbLen
	base := 1<<32 - total
	sum := sha256.Sum256(logoVol)
	for _, c := range []struct {
		name              string
		hashed, manifests bool
		warn              string // in the warning, or "" for none
	}{
		{"hash", true, false, "which is verified against an Intel Boot Guard hash"},
		{"manifests and hash", true, true, "very likely not start"},
		{"manifests", false, true, "may not start"},
		{"nothing", false, false, ""},
	} {
		hash := make([]byte, 32) // an erased or stale hash matches nothing
		if c.hashed {
			hash = sum[:]
		}
		files := [][]byte{hashFile(hash, base+0x1000, len(logoVol))}
		if c.manifests {
			files = append(files, manifests)
		}
		fw := buildImage(logoVol, buildVolume(t, bbLen, 0xFF, files...))
		b := DetectBootGuard(fw)
		if b.Manifests != c.manifests || (b.Hashed != nil) != c.hashed {
			t.Fatalf("%s: detected %+v", c.name, b)
		}
		root := Parse(fw)
		logos, err := root.FindLogos(LogoFileGUID)
		if err != nil {
			t.Fatal(err)
		}
		predicted := b.Warning(logos, nil)
		p := replace(t, fw, makeBMP(8, 8, true), ReplaceOptions{})
		if p.BootGuardWarning != predicted || !strings.Contains(p.BootGuardWarning, c.warn) || (c.warn == "") != (p.BootGuardWarning == "") {
			t.Fatalf("%s: warning %q, predicted %q", c.name, p.BootGuardWarning, predicted)
		}
	}
}

func TestFirmwareTiming(t *testing.T) {
	var frames []image.Image
	for _, c := range []*image.RGBA{filled(8, 8, red), filled(8, 8, green), filled(8, 8, blue)} {
		frames = append(frames, c)
	}
	b, err := EncodeGIF(frames, []int{10, 20, 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	fw, err := FirmwareTiming(b)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(fw))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Delay, []int{30, 10, 20}) {
		t.Fatalf("delays %v", g.Delay)
	}
}
