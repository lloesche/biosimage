package uefi

import (
	"bytes"
	"encoding/binary"
	"errors"
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

func TestLZMARejectsHugeDictionary(t *testing.T) {
	// A mangled header claiming a 2 GiB dictionary must be rejected before any allocation.
	stream, err := lzmaCompress(makeBMP(8, 8, false), lzmaParams{lzmaDefaultProps, lzmaDefaultDict})
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(stream[1:], 2<<30)
	if _, err := lzmaDecompress(stream); err == nil || !strings.Contains(err.Error(), "dictionary") {
		t.Fatalf("expected dictionary error, got %v", err)
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

// replace runs ReplaceLogo and fails the test on error.
func replace(t *testing.T, fw, img []byte, opt ReplaceOptions) *Patch {
	t.Helper()
	p, err := ReplaceLogo(fw, img, LogoFileGUID, opt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReplaceLZMALastFileGrows(t *testing.T) {
	old, img := makeBMP(32, 16, false), makeBMP(200, 100, true)
	fw := buildImage(buildVolume(t, 0x40000, 0xFF, otherFile(t, 100, 0xFF), lzmaLogoFile(t, old, 0xFF)))
	p := replace(t, fw, img, ReplaceOptions{})
	l := checkVolume(t, p.Data, img)
	if l.Encoding != EncodingLZMA || l.Name != "Logo.bmp" {
		t.Fatalf("encoding %v name %q", l.Encoding, l.Name)
	}
	if c := p.Changes[0]; c.NewOffset != c.OldOffset || c.Placement != PlacedLast || c.Storage != StoredLZMA {
		t.Fatalf("unexpected change %+v", c)
	}
}

func TestReplaceRelocatesWhenNotLast(t *testing.T) {
	old, img := makeBMP(16, 16, false), makeBMP(100, 60, true)
	other := otherFile(t, 50, 0xFF)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, rawLogoFile(t, old, 0xFF), other))
	p := replace(t, fw, img, ReplaceOptions{})
	checkVolume(t, p.Data, img)
	c := p.Changes[0]
	if c.NewOffset <= c.OldOffset || c.Placement != PlacedMoved {
		t.Fatalf("unexpected placement %+v", c)
	}
	if !bytes.Contains(p.Data, other) {
		t.Fatal("other file was modified")
	}
	if p.Data[c.OldOffset+18] != FileTypePad {
		t.Fatal("old slot is not a pad file")
	}
}

func TestReplaceShrinksInPlaceWithPad(t *testing.T) {
	old, img := makeBMP(64, 64, true), makeBMP(16, 16, true)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, rawLogoFile(t, old, 0xFF), otherFile(t, 50, 0xFF)))
	p := replace(t, fw, img, ReplaceOptions{})
	checkVolume(t, p.Data, img)
	if c := p.Changes[0]; c.NewOffset != c.OldOffset || c.Placement != PlacedPadded {
		t.Fatalf("unexpected placement %+v", c)
	}
}

func TestReplaceKeepsUncompressed(t *testing.T) {
	img := makeBMP(40, 20, false)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	p := replace(t, fw, img, ReplaceOptions{})
	if l := checkVolume(t, p.Data, img); l.Encoding != EncodingRawSection || l.Name != "Logo" {
		t.Fatalf("encoding %v name %q", l.Encoding, l.Name)
	}
	if p.Changes[0].Storage != StoredUncompressed {
		t.Fatalf("storage %v", p.Changes[0].Storage)
	}
}

func TestReplaceRawFileType(t *testing.T) {
	img := makeBMP(10, 10, false)
	fw := buildImage(buildVolume(t, 0x10000, 0xFF, mustFile(t, LogoFileGUID, FileTypeRaw, makeBMP(4, 4, false), 0xFF)))
	p := replace(t, fw, img, ReplaceOptions{})
	if l := checkVolume(t, p.Data, img); l.Encoding != EncodingRawFile {
		t.Fatalf("encoding %v", l.Encoding)
	}
}

func TestReplaceNoCompressUnwrapsLZMA(t *testing.T) {
	img := makeBMP(30, 30, false)
	fw := buildImage(buildVolume(t, 0x20000, 0xFF, lzmaLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	p := replace(t, fw, img, ReplaceOptions{NoCompress: true})
	if l := checkVolume(t, p.Data, img); l.Encoding != EncodingRawSection || l.Name != "Logo.bmp" {
		t.Fatalf("encoding %v name %q", l.Encoding, l.Name)
	}
	if p.Changes[0].Storage != StoredUnwrapped {
		t.Fatalf("storage %v", p.Changes[0].Storage)
	}
}

func TestReplaceErasePolarityZero(t *testing.T) {
	img := makeBMP(30, 30, true)
	fw := buildImage(buildVolume(t, 0x20000, 0x00, otherFile(t, 10, 0x00), rawLogoFile(t, makeBMP(8, 8, false), 0x00)))
	p := replace(t, fw, img, ReplaceOptions{})
	l := checkVolume(t, p.Data, img)
	if s := p.Data[l.File.Offset+23]; s != fileStateValid {
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
	p := replace(t, fw, img, ReplaceOptions{})
	l := checkVolume(t, p.Data, img)
	hops := l.Enclosing()
	if len(hops) != 1 || hops[0].Compressed || l.Compressed() {
		t.Fatalf("expected the logo in an uncompressed nested volume, got %+v", hops)
	}
	if f := hops[0].File; !headerChecksumOK(p.Data, f) || !dataChecksumOK(p.Data, f) {
		t.Fatal("enclosing RAW file checksum not updated")
	}
}

// compressedVolumeFile returns an FV image file whose LZMA section holds a volume, as MSI
// uses on Intel boards.
func compressedVolumeFile(t *testing.T, inner []byte, attrs byte) []byte {
	t.Helper()
	payload, err := lzmaCompress(NewSection(SectionFVImage, inner), lzmaParams{lzmaDefaultProps, lzmaDefaultDict})
	if err != nil {
		t.Fatal(err)
	}
	f, err := BuildFile(MustParseGUID("9E21FD93-9C72-4C15-8C4B-E77F1DB2D792"), FileTypeFVImage, attrs,
		newGuidedSection(LZMASectionGUID, guidedProcessingRequired, payload), 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReplaceInsideCompressedVolume(t *testing.T) {
	// MSI-on-Intel layout: main volume -> FV image file -> LZMA section -> volume -> logo file.
	img := makeBMP(120, 80, true) // noisy, so the recompressed volume has to grow
	inner := buildVolume(t, 0x10000, 0xFF, otherFile(t, 40, 0xFF), rawLogoFile(t, makeBMP(8, 8, false), 0xFF))
	other := otherFile(t, 30, 0xFF)
	fw := buildImage(buildVolume(t, 0x40000, 0xFF, other, compressedVolumeFile(t, inner, ffsAttribChecksum)))
	p := replace(t, fw, img, ReplaceOptions{})
	if r := p.Changes[0].Recompressed; len(r) != 1 || r[0].NewSize <= r[0].File.Size {
		t.Fatalf("expected one grown recompressed volume, got %+v", r)
	}
	logos, err := FindLogos(p.Data, LogoFileGUID)
	if err != nil || len(logos) != 1 || !logos[0].Compressed() || !bytes.Equal(logos[0].Image, img) {
		t.Fatalf("re-read failed: %v", err)
	}
	if !bytes.Contains(p.Data, other) {
		t.Fatal("other file in the main volume changed")
	}
	hops := logos[0].Enclosing()
	if len(hops) != 1 || !hops[0].Compressed {
		t.Fatalf("enclosing files %+v", hops)
	}
	if f := hops[0].File; !headerChecksumOK(p.Data, f) || !dataChecksumOK(p.Data, f) {
		t.Fatal("checksum of the rebuilt FV image file is invalid")
	}
}

func TestTreeFindsLogoInCompressedVolume(t *testing.T) {
	inner := buildVolume(t, 0x10000, 0xFF, otherFile(t, 40, 0xFF), lzmaLogoFile(t, makeBMP(16, 8, false), 0xFF))
	root := Parse(buildImage(buildVolume(t, 0x40000, 0xFF, compressedVolumeFile(t, inner, 0))))
	if len(root.Children) != 2 || root.Children[0].Kind != KindRegion || root.Children[1].Kind != KindVolume {
		t.Fatalf("unexpected top level: %+v", root.Children)
	}
	logos, err := root.FindLogos(LogoFileGUID)
	if err != nil || len(logos) != 1 {
		t.Fatalf("logos %d, %v", len(logos), err)
	}
	path := logos[0].Node.Path()
	if path[0] != root || !logos[0].Compressed() {
		t.Fatalf("path does not start at the root, or is not inside decompressed data")
	}
	var img *Node
	logos[0].Node.Walk(func(n *Node) bool {
		if _, _, ok := n.Image(); ok {
			img = n
		}
		return true
	})
	if img == nil {
		t.Fatal("image section not found")
	}
	if info, off, _ := img.Image(); info.Format != "BMP" || !bytes.Equal(img.Bytes()[off:off+2], []byte("BM")) {
		t.Fatalf("image section unreadable: %+v", info)
	}
}

func TestLargeCompressedSectionsAreLazy(t *testing.T) {
	big := otherFile(t, lazyThreshold/3+1000, 0xFF) // over 1 MiB once decompressed
	inner := buildVolume(t, alignUp(len(big)+0x1000, 0x1000), 0xFF, big)
	root := Parse(buildImage(buildVolume(t, 0x40000, 0xFF, compressedVolumeFile(t, inner, 0))))
	var lzma *Node
	root.Walk(func(n *Node) bool {
		if _, ok := n.LZMAHeader(); ok {
			lzma = n
		}
		return true
	})
	if lzma == nil || !lzma.Lazy() || len(lzma.Children) != 0 {
		t.Fatal("large LZMA section was decompressed eagerly")
	}
	if err := lzma.Expand(); err != nil || lzma.Lazy() || len(lzma.Children) != 1 || !lzma.Children[0].Decompressed() {
		t.Fatalf("expand: %v", err)
	}
}

func TestReplaceShiftsFollowingFiles(t *testing.T) {
	// Not enough free space to move the grown logo to the end, but enough to grow it in place
	// and shift the following file, which needs 16-byte data alignment (attribute 0x08).
	old, img := makeBMP(40, 30, true), makeBMP(50, 40, true)
	follower, err := BuildFile(MustParseGUID("FFCFC736-9DD8-4C63-ABDD-6DFB301799DF"), FileTypeFreeform, 0x08,
		NewSection(SectionRaw, bytes.Repeat([]byte{0x42}, 1000)), 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	fw := buildImage(buildVolume(t, 0x2000, 0xFF, rawLogoFile(t, old, 0xFF), follower))
	p := replace(t, fw, img, ReplaceOptions{})
	l := checkVolume(t, p.Data, img)
	if p.Changes[0].Placement != PlacedShifted {
		t.Fatalf("unexpected placement %+v", p.Changes[0])
	}
	files, _, _ := l.Volume.Files(p.Data)
	for _, f := range files {
		if f.GUID == MustParseGUID("FFCFC736-9DD8-4C63-ABDD-6DFB301799DF") {
			if (f.DataOffset()-l.Volume.Offset)%16 != 0 {
				t.Fatalf("moved file data at %#x is not 16-byte aligned", f.DataOffset())
			}
			if !bytes.Equal(p.Data[f.Offset:f.End()], follower) {
				t.Fatal("moved file changed")
			}
			return
		}
	}
	t.Fatal("following file lost")
}

func TestReplaceCompressesUncompressedLogoThatDoesNotFit(t *testing.T) {
	// EVGA stores the logo uncompressed with little room; a larger (compressible) image is
	// then wrapped in an LZMA section instead of failing.
	img := makeBMP(100, 100, false) // ~30 KB raw, compresses well
	fw := buildImage(buildVolume(t, 0x2000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	p := replace(t, fw, img, ReplaceOptions{})
	if l := checkVolume(t, p.Data, img); l.Encoding != EncodingLZMA {
		t.Fatalf("encoding %v, want LZMA", l.Encoding)
	}
	if p.Changes[0].Storage != StoredLZMAFallback {
		t.Fatalf("storage %v", p.Changes[0].Storage)
	}
	if _, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{NoCompress: true}); !errors.Is(err, errTooBig) {
		t.Fatalf("-no-compress must not fall back to compression, got %v", err)
	}
}

func TestReplaceTooBig(t *testing.T) {
	fw := buildImage(buildVolume(t, 0x2000, 0xFF, rawLogoFile(t, makeBMP(8, 8, false), 0xFF)))
	_, err := ReplaceLogo(fw, makeBMP(100, 100, true), LogoFileGUID, ReplaceOptions{})
	if !errors.Is(err, errTooBig) {
		t.Fatalf("expected size error, got %v", err)
	}
}

// TestCapacityIsExact checks that a file of exactly Capacity bytes can be placed and that
// nothing larger can, for each placement strategy.
func TestCapacityIsExact(t *testing.T) {
	follower, err := BuildFile(MustParseGUID("FFCFC736-9DD8-4C63-ABDD-6DFB301799DF"), FileTypeFreeform, 0x08,
		NewSection(SectionRaw, bytes.Repeat([]byte{0x42}, 1000)), 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	logo := rawLogoFile(t, makeBMP(20, 20, true), 0xFF)
	for name, vol := range map[string][]byte{
		"last":    buildVolume(t, 0x4000, 0xFF, otherFile(t, 30, 0xFF), logo),
		"move":    buildVolume(t, 0x8000, 0xFF, logo, otherFile(t, 30, 0xFF)),
		"shift":   buildVolume(t, 0x2000, 0xFF, logo, follower),
		"no room": buildVolume(t, alignUp(0x48+len(logo), 8)+len(follower)+8, 0xFF, logo, follower),
	} {
		t.Run(name, func(t *testing.T) {
			logos, err := FindLogos(buildImage(vol), LogoFileGUID)
			if err != nil {
				t.Fatal(err)
			}
			l := logos[0]
			s, err := newVolumeSlot(l.Node.Buf.Data, l.Volume, l.File)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.plan(l.Capacity); err != nil {
				t.Fatalf("capacity %d does not fit: %v", l.Capacity, err)
			}
			for size := l.Capacity + 1; size < l.Capacity+512; size++ {
				if pl, err := s.plan(size); err == nil {
					t.Fatalf("capacity %d, but %d fits (%v)", l.Capacity, size, pl.placement)
				}
			}
		})
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
	p, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := FindLogos(p.Data, LogoFileGUID)
	if err != nil || len(again) != len(logos) || !bytes.Equal(again[0].Image, img) {
		t.Fatalf("re-read failed: %v", err)
	}
}
