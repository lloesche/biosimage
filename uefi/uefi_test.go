package uefi

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

// --- synthetic firmware helpers ---

// buildVolume returns an FFS2 volume of the given length holding files.
func buildVolume(t *testing.T, length int, erase byte, files ...[]byte) []byte {
	t.Helper()
	const hdrLen = 0x48
	v := bytes.Repeat([]byte{erase}, length)
	h := v[:hdrLen]
	clear(h)
	copy(h[16:], FFS2GUID[:])
	binary.LittleEndian.PutUint64(h[32:], uint64(length))
	copy(h[40:], fvSignature)
	attrs := uint32(0x0004FEFF)
	if erase == 0 {
		attrs &^= fvb2ErasePolarity
	}
	binary.LittleEndian.PutUint32(h[44:], attrs)
	binary.LittleEndian.PutUint16(h[48:], hdrLen)
	h[55] = 2 // revision
	binary.LittleEndian.PutUint32(h[56:], uint32(length/0x1000))
	binary.LittleEndian.PutUint32(h[60:], 0x1000)
	var sum uint16
	for i := 0; i < hdrLen; i += 2 {
		sum += binary.LittleEndian.Uint16(h[i:])
	}
	binary.LittleEndian.PutUint16(h[50:], -sum)
	p := hdrLen
	for _, f := range files {
		p = alignUp(p, 8)
		copy(v[p:], f)
		p += len(f)
	}
	if p > length {
		t.Fatalf("files do not fit into volume")
	}
	return v
}

// buildImage wraps volumes in a fake 0x1000-byte capsule-like prefix.
func buildImage(vols ...[]byte) []byte {
	img := make([]byte, 0x1000)
	for _, v := range vols {
		img = append(img, v...)
	}
	return img
}

func mustFile(t *testing.T, g GUID, typ byte, data []byte, erase byte) []byte {
	t.Helper()
	f, err := BuildFile(g, typ, 0, data, erase)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func otherFile(t *testing.T, n int, erase byte) []byte {
	data := NewSection(SectionRaw, bytes.Repeat([]byte{0x5A, 0xA5, 0x11}, n))
	return mustFile(t, MustParseGUID("E2377389-E8F0-4765-9851-2EA2557E83B2"), FileTypeFreeform, data, erase)
}

func lzmaLogoFile(t *testing.T, img []byte, erase byte) []byte {
	t.Helper()
	inner := JoinSections(NewSection(SectionRaw, img), NewUISection("Logo.bmp"))
	payload, err := lzmaCompress(inner, lzmaParams{lzmaDefaultProps, lzmaDefaultDict})
	if err != nil {
		t.Fatal(err)
	}
	return mustFile(t, LogoFileGUID, FileTypeFreeform, newGuidedSection(LZMASectionGUID, guidedProcessingRequired, payload), erase)
}

func rawLogoFile(t *testing.T, img []byte, erase byte) []byte {
	return mustFile(t, LogoFileGUID, FileTypeFreeform, JoinSections(NewSection(SectionRaw, img), NewUISection("Logo")), erase)
}

// makeBMP builds a 24-bit bottom-up BMP; noise makes it compress badly.
func makeBMP(w, h int, noise bool) []byte {
	stride := (w*3 + 3) &^ 3
	b := make([]byte, 54+stride*h)
	copy(b, "BM")
	binary.LittleEndian.PutUint32(b[2:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[10:], 54)
	binary.LittleEndian.PutUint32(b[14:], 40)
	binary.LittleEndian.PutUint32(b[18:], uint32(w))
	binary.LittleEndian.PutUint32(b[22:], uint32(h))
	binary.LittleEndian.PutUint16(b[26:], 1)
	binary.LittleEndian.PutUint16(b[28:], 24)
	x := uint32(12345)
	for i := 54; i < len(b); i++ {
		if noise {
			x = x*1103515245 + 12345
			b[i] = byte(x >> 16)
		} else if (i/3)%97 < 10 {
			b[i] = 0xFF
		}
	}
	return b
}

// checkVolume asserts the patched volume parses with valid checksums and holds want.
func checkVolume(t *testing.T, buf []byte, want []byte) *Logo {
	t.Helper()
	logos, err := FindLogos(buf, LogoFileGUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(logos) != 1 {
		t.Fatalf("found %d logos", len(logos))
	}
	l := logos[0]
	files, _, err := l.Volume.Files(buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !headerChecksumOK(buf, f) || !dataChecksumOK(buf, f) {
			t.Errorf("bad checksum in file at %#x", f.Offset)
		}
	}
	if !bytes.Equal(l.Image, want) {
		t.Fatalf("logo image mismatch")
	}
	return l
}

// --- tests ---

func TestGUIDRoundTrip(t *testing.T) {
	want := []byte{0x99, 0x8B, 0xB2, 0x7B, 0xBB, 0x61, 0xD5, 0x11, 0x9A, 0x5D, 0x00, 0x90, 0x27, 0x3F, 0xC1, 0x4D}
	if !bytes.Equal(LogoFileGUID[:], want) {
		t.Fatalf("bytes %x", LogoFileGUID[:])
	}
	if s := LogoFileGUID.String(); s != "7BB28B99-61BB-11D5-9A5D-0090273FC14D" {
		t.Fatalf("string %s", s)
	}
	if _, err := ParseGUID("not-a-guid"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFileHeaderMatchesStockASUS(t *testing.T) {
	// Header bytes of the stock logo file in ASUS TUF GAMING B850I WIFI NEO BIOS 1804.
	data := make([]byte, 0x1279-24)
	f, err := BuildFile(LogoFileGUID, FileTypeFreeform, 0, data, 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	// With all-zero data the header checksum is the same as for the real contents.
	if got := f[16:24]; !bytes.Equal(got, []byte{0x25, 0xAA, 0x02, 0x00, 0x79, 0x12, 0x00, 0xF8}) {
		t.Fatalf("header %x", got)
	}
}

func TestLZMAFormat(t *testing.T) {
	data := makeBMP(64, 32, false)
	out, err := lzmaCompress(data, lzmaParams{lzmaDefaultProps, lzmaDefaultDict})
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != 0x5D || binary.LittleEndian.Uint32(out[1:]) != 1<<24 || binary.LittleEndian.Uint64(out[5:]) != uint64(len(data)) {
		t.Fatalf("header %x", out[:13])
	}
	back, err := lzmaDecompress(out)
	if err != nil || !bytes.Equal(back, data) {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestFindVolumesSkipsBadChecksum(t *testing.T) {
	v := buildVolume(t, 0x4000, 0xFF, otherFile(t, 10, 0xFF))
	bad := bytes.Clone(v)
	bad[50]++
	if n := len(FindVolumes(buildImage(bad, v))); n != 1 {
		t.Fatalf("found %d volumes, want 1", n)
	}
}

func TestReplaceLZMALastFileGrows(t *testing.T) {
	old, img := makeBMP(32, 16, false), makeBMP(200, 100, true)
	fw := buildImage(buildVolume(t, 0x40000, 0xFF, otherFile(t, 100, 0xFF), lzmaLogoFile(t, old, 0xFF)))
	out, changes, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	l := checkVolume(t, out, img)
	if l.Encoding != EncodingLZMA || l.Name != "Logo.bmp" {
		t.Fatalf("encoding %v name %q", l.Encoding, l.Name)
	}
	if changes[0].NewOffset != changes[0].OldOffset || !strings.Contains(changes[0].Placement, "last file") {
		t.Fatalf("unexpected placement %+v", changes[0])
	}
}

func TestReplaceRelocatesWhenNotLast(t *testing.T) {
	old, img := makeBMP(16, 16, false), makeBMP(100, 60, true)
	other := otherFile(t, 50, 0xFF)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, rawLogoFile(t, old, 0xFF), other))
	out, changes, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkVolume(t, out, img)
	c := changes[0]
	if c.NewOffset <= c.OldOffset || !strings.Contains(c.Placement, "moved") {
		t.Fatalf("unexpected placement %+v", c)
	}
	if !bytes.Contains(out, other) {
		t.Fatal("other file was modified")
	}
	if out[c.OldOffset+18] != FileTypePad {
		t.Fatal("old slot is not a pad file")
	}
}

func TestReplaceShrinksInPlaceWithPad(t *testing.T) {
	old, img := makeBMP(64, 64, true), makeBMP(16, 16, true)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, rawLogoFile(t, old, 0xFF), otherFile(t, 50, 0xFF)))
	out, changes, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkVolume(t, out, img)
	if c := changes[0]; c.NewOffset != c.OldOffset || !strings.Contains(c.Placement, "padded") {
		t.Fatalf("unexpected placement %+v", c)
	}
}

func TestReplaceKeepsUncompressed(t *testing.T) {
	img := makeBMP(40, 20, false)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	out, _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if l := checkVolume(t, out, img); l.Encoding != EncodingRawSection || l.Name != "Logo" {
		t.Fatalf("encoding %v name %q", l.Encoding, l.Name)
	}
}

func TestReplaceRawFileType(t *testing.T) {
	img := makeBMP(10, 10, false)
	fw := buildImage(buildVolume(t, 0x10000, 0xFF, mustFile(t, LogoFileGUID, FileTypeRaw, makeBMP(4, 4, false), 0xFF)))
	out, _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if l := checkVolume(t, out, img); l.Encoding != EncodingRawFile {
		t.Fatalf("encoding %v", l.Encoding)
	}
}

func TestReplaceNoCompressUnwrapsLZMA(t *testing.T) {
	img := makeBMP(30, 30, false)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, lzmaLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	out, _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{NoCompress: true})
	if err != nil {
		t.Fatal(err)
	}
	if l := checkVolume(t, out, img); l.Encoding != EncodingRawSection || l.Name != "Logo.bmp" {
		t.Fatalf("encoding %v name %q", l.Encoding, l.Name)
	}
}

func TestReplaceErasePolarityZero(t *testing.T) {
	img := makeBMP(30, 30, true)
	fw := buildImage(buildVolume(t, 0x20000, 0x00, otherFile(t, 10, 0x00), rawLogoFile(t, makeBMP(8, 8, false), 0x00)))
	out, _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	l := checkVolume(t, out, img)
	if s := out[l.File.Offset+23]; s != fileStateValid {
		t.Fatalf("state %#x", s)
	}
}

func TestReplaceNestedVolumeUpdatesParentChecksum(t *testing.T) {
	// AMI layout: main volume -> RAW file (with data checksum) -> small volume -> logo file.
	img := makeBMP(60, 40, true)
	inner := buildVolume(t, 0x8000, 0xFF, lzmaLogoFile(t, makeBMP(8, 8, false), 0xFF))
	raw, err := BuildFile(MustParseGUID("05CA020B-0FC1-11DC-9011-00173153EBA8"), FileTypeRaw, ffsAttribChecksum, inner, 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, otherFile(t, 20, 0xFF), raw))
	out, _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	l := checkVolume(t, out, img)
	if len(l.Volume.Parents) != 1 {
		t.Fatalf("expected the logo in a nested volume, parents: %d", len(l.Volume.Parents))
	}
	p := l.Volume.Parents[0]
	if !headerChecksumOK(out, p) || !dataChecksumOK(out, p) {
		t.Fatal("enclosing RAW file checksum not updated")
	}
}

func TestReplaceInsideCompressedVolume(t *testing.T) {
	// MSI-on-Intel layout: main volume -> FV image file -> LZMA section -> volume -> logo file.
	img := makeBMP(120, 80, true) // noisy, so the recompressed volume has to grow
	inner := buildVolume(t, 0x10000, 0xFF, otherFile(t, 40, 0xFF), rawLogoFile(t, makeBMP(8, 8, false), 0xFF))
	payload, err := lzmaCompress(NewSection(SectionFVImage, inner), lzmaParams{lzmaDefaultProps, lzmaDefaultDict})
	if err != nil {
		t.Fatal(err)
	}
	fvFile, err := BuildFile(MustParseGUID("9E21FD93-9C72-4C15-8C4B-E77F1DB2D792"), FileTypeFVImage, ffsAttribChecksum,
		newGuidedSection(LZMASectionGUID, guidedProcessingRequired, payload), 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	other := otherFile(t, 30, 0xFF)
	fw := buildImage(buildVolume(t, 0x40000, 0xFF, other, fvFile))
	out, changes, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changes[0].Container, "recompressed") {
		t.Fatalf("expected a container change, got %+v", changes[0])
	}
	logos, err := FindLogos(out, LogoFileGUID)
	if err != nil || len(logos) != 1 || logos[0].Container == nil || !bytes.Equal(logos[0].Image, img) {
		t.Fatalf("re-read failed: %v", err)
	}
	if !bytes.Contains(out, other) {
		t.Fatal("other file in the main volume changed")
	}
	c := logos[0].Container
	if !headerChecksumOK(out, c.File) || !dataChecksumOK(out, c.File) {
		t.Fatal("checksum of the rebuilt FV image file is invalid")
	}
}

func TestDetectEmbeddedSignature(t *testing.T) {
	block := append(append(append([]byte{}, AMICapsuleGUID[:]...), make([]byte, 24)...), CertRSA2048GUID[:]...)
	fw := append(buildImage(buildVolume(t, 0x2000, 0xFF, otherFile(t, 10, 0xFF))), block...)
	c := DetectCapsule(fw)
	if c == nil || !c.Signed || c.HeaderSize != 0 {
		t.Fatalf("embedded signature not detected: %+v", c)
	}
}

func TestReplaceTooBig(t *testing.T) {
	fw := buildImage(buildVolume(t, 0x2000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	_, _, err := ReplaceLogo(fw, makeBMP(100, 100, true), LogoFileGUID, ReplaceOptions{})
	if err == nil || !strings.Contains(err.Error(), "only") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestLogoNotFound(t *testing.T) {
	fw := buildImage(buildVolume(t, 0x2000, 0xFF, otherFile(t, 10, 0xFF)))
	if _, err := FindLogos(fw, LogoFileGUID); err == nil {
		t.Fatal("expected error")
	}
}

func TestInspectImage(t *testing.T) {
	info, err := InspectImage(makeBMP(672, 378, false))
	if err != nil || info.Format != "BMP" || info.Width != 672 || info.Height != 378 || len(info.Warnings) != 0 {
		t.Fatalf("%+v %v", info, err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"), 0, 0, 2, 0x58, 0, 0, 2, 0x58, 8, 6, 0, 0, 0)
	info, err = InspectImage(png)
	if err != nil || info.Width != 600 || len(info.Warnings) != 1 {
		t.Fatalf("%+v %v", info, err)
	}
	progressive := []byte{0xFF, 0xD8, 0xFF, 0xC2, 0, 11, 8, 0, 16, 0, 32, 1, 1, 0x11, 0}
	info, err = InspectImage(progressive)
	if err != nil || info.Detail != "progressive" || info.Width != 32 || info.Height != 16 || len(info.Warnings) != 1 {
		t.Fatalf("%+v %v", info, err)
	}
	frame := []byte{0x21, 0xF9, 4, 0, 5, 0, 0, 0, 0x2C, 0, 0, 0, 0, 1, 0, 1, 0, 0, 2, 2, 0x44, 0x01, 0}
	gif := append([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff"), frame...)
	gif = append(append(gif, frame...), 0x3B)
	info, err = InspectImage(gif)
	if err != nil || info.Detail != "2 frame(s)" || len(info.Warnings) != 2 {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := InspectImage([]byte("hello")); err == nil {
		t.Fatal("expected error")
	}
}

// TestRealFirmware runs against a vendor image when BIOSIMAGE_FIRMWARE points to one.
func TestRealFirmware(t *testing.T) {
	path := os.Getenv("BIOSIMAGE_FIRMWARE")
	if path == "" {
		t.Skip("set BIOSIMAGE_FIRMWARE to a firmware file to run")
	}
	fw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	logos, err := FindLogos(fw, LogoFileGUID)
	if err != nil {
		t.Fatal(err)
	}
	img := makeBMP(320, 200, false)
	out, _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := FindLogos(out, LogoFileGUID)
	if err != nil || len(again) != len(logos) || !bytes.Equal(again[0].Image, img) {
		t.Fatalf("re-read failed: %v", err)
	}
}
