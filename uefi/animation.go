package uefi

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
)

// animlogo.efi is our own freestanding DXE driver, built from driver/animlogo.c.
// Never replace this asset with a vendor module. Rebuild with driver/build.ps1.
//
//go:embed animlogo.efi
var animationDriver []byte

var AnimationDriverGUID = MustParseGUID("2C0B04B4-77B3-4477-9F9D-FE8F7DB29B4D")
var AnimationDataGUID = MustParseGUID("6EDB8A32-70A0-4B5D-B131-0277F602E529")

const animationHeader = 12 + 256*4
const maxAnimationData = 16 << 20

// EncodeBootAnimation returns our bounded BIA1 frame stream and a first-frame BMP.
// Each record stores a delay in 10ms units and a changed rectangle of palette indices.
// Delays belong to the frame being displayed (no AMITSE timing rotation).
func EncodeBootAnimation(a *Animation) ([]byte, []byte, error) {
	if a == nil || len(a.Frames) < 2 || len(a.Frames) > 3000 || len(a.Delays) != len(a.Frames) {
		return nil, nil, errors.New("animation needs 2–3000 frames and matching delays")
	}
	if a.Frames[0] == nil {
		return nil, nil, errors.New("missing first animation frame")
	}
	r := a.Frames[0].Bounds()
	w, h := r.Dx(), r.Dy()
	if w < 1 || h < 1 || w > 1920 || h > 1080 || uint64(w)*uint64(h)*uint64(len(a.Frames)) > maxAnimationPixels {
		return nil, nil, errors.New("animation dimensions or decoded size exceed limits")
	}
	for _, f := range a.Frames {
		if f == nil || f.Bounds().Size() != r.Size() {
			return nil, nil, errors.New("animation frames differ in size")
		}
	}
	pal := gifPalette(a.Frames)
	index := paletteIndex(pal)
	out := make([]byte, animationHeader)
	copy(out, "BIA1")
	binary.LittleEndian.PutUint16(out[4:], uint16(w))
	binary.LittleEndian.PutUint16(out[6:], uint16(h))
	binary.LittleEndian.PutUint32(out[8:], uint32(len(a.Frames)))
	for i, c := range pal {
		cr, cg, cb, _ := c.RGBA()
		copy(out[12+4*i:], []byte{byte(cb >> 8), byte(cg >> 8), byte(cr >> 8), 0})
	}
	var last *image.Paletted
	for i, f := range a.Frames {
		delay := max(MinFrameDelay, a.Delays[i])
		if delay > 6000 {
			return nil, nil, errors.New("animation frame delay exceeds 60 seconds")
		}
		p := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		rgba := toRGBA(f)
		for j := range p.Pix {
			p.Pix[j] = index(rgbKey(rgba.Pix[4*j:]))
		}
		rect := p.Rect
		if last != nil {
			rect = changedRect(last, p)
			if rect.Empty() {
				rect = image.Rect(0, 0, 1, 1)
			}
		}
		hdr := make([]byte, 12)
		for j, v := range []int{delay, rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy(), 0} {
			binary.LittleEndian.PutUint16(hdr[2*j:], uint16(v))
		}
		if len(out)+len(hdr)+rect.Dx()*rect.Dy() > maxAnimationData {
			return nil, nil, errors.New("animation data exceeds 16 MiB")
		}
		out = append(out, hdr...)
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			out = append(out, p.Pix[y*p.Stride+rect.Min.X:y*p.Stride+rect.Max.X]...)
		}
		last = p
	}
	// Quantized frame zero exactly matches the first driver frame.
	first := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			j := int(out[animationHeader+12+y*w+x])
			base := 12 + 4*j
			first.SetRGBA(x, y, keyColor(uint32(out[base+2])<<16|uint32(out[base+1])<<8|uint32(out[base])))
		}
	}
	bmp, err := EncodeImage(first, "BMP", 100)
	return out, bmp, err
}

// AnimationSupport reports whether image n supports the driver animation. It fails closed: the
// image must prove all compatibility requirements. A force/recovery checkbox cannot override
// a Boot Guard protection or missing evidence. It reuses what is already unpacked of n:
// AMITSE may be in a compressed volume.
func (n *Node) AnimationSupport() error {
	_, err := n.animationTarget()
	return err
}

// animationTarget checks that image n supports the driver animation and returns the free space
// of the main volume, where its files go.
func (n *Node) animationTarget() (*FreeSpace, error) {
	bg := DetectBootGuard(n.Bytes())
	if bg.Manifests || len(bg.Hashed) > 0 {
		return nil, errors.New("driver animation is unavailable on firmware with Boot Guard evidence")
	}
	c := n.TSECode()
	if !c.AnyVolumeLogo || !c.PostManagerState || !c.LogoPlacement {
		return nil, errors.New("experimental animation requires proven any-volume loading, post-manager state getter and logo placement")
	}
	fs, err := n.freeSpace(c)
	if err != nil {
		return nil, err
	}
	var duplicate bool
	n.expandAll()
	n.Walk(func(n *Node) bool {
		if n.Kind == KindFile && (n.File.GUID == AnimationDriverGUID || n.File.GUID == AnimationDataGUID) {
			duplicate = true
		}
		return true
	})
	if duplicate {
		return nil, errors.New("firmware already contains an animation driver or data file; start from the original BIOS")
	}
	return fs, nil
}

// ReplaceAnimatedLogo returns a copy of image n with a first-frame fallback for the logo, our
// DXE driver, and a separate compressed frame-data file in FV_MAIN. It reuses what is already
// unpacked of n, and does not modify it.
func (n *Node) ReplaceAnimatedLogo(gif []byte, guid GUID) (*Patch, error) {
	if guid != LogoFileGUID {
		return nil, errors.New("driver animation only supports the standard boot-logo GUID")
	}
	target, err := n.animationTarget()
	if err != nil {
		return nil, err
	}
	info, err := InspectImage(gif)
	if err != nil || info.Format != "GIF" || info.Width > 1920 || info.Height > 1080 || info.Frames < 2 || info.Frames > 3000 || uint64(info.Width)*uint64(info.Height)*uint64(info.Frames) > maxAnimationPixels {
		return nil, errors.New("requires a bounded animated GIF of at most 1920×1080")
	}
	a, err := DecodeAnimation(gif)
	if err != nil {
		return nil, err
	}
	data, first, err := EncodeBootAnimation(a)
	if err != nil {
		return nil, err
	}
	if err := validateAnimationDriver(animationDriver); err != nil {
		return nil, err
	}
	p, err := ReplaceLogo(n.Bytes(), first, guid, ReplaceOptions{})
	if err != nil {
		return nil, fmt.Errorf("first-frame fallback: %w", err)
	}
	// The fallback may have changed the main volume (if the logo is inside it), but not where
	// it is: take it from the patched image, which needs nothing unpacked.
	vol := topVolumeAt(p.Root, target.Volume.Offset)
	if vol == nil {
		return nil, errors.New("main volume not found after writing the first-frame fallback")
	}
	fs, err := freeSpaceIn(vol)
	if err != nil {
		return nil, err
	}
	p.AnimationRoom = fs.Capacity
	// PUSH GOP, END: dispatch only after graphics is available. Still retry GOP and the
	// AMI protocol at runtime; GOP mode information can change during POST.
	gopGUID := MustParseGUID("9042A9DE-23DC-4A38-96FB-7ADED080516A")
	depex := append([]byte{0x02}, gopGUID[:]...)
	depex = append(depex, 0x08)
	driverSections := JoinSections(NewSection(0x13, depex), NewSection(SectionPE32, animationDriver), NewUISection("biosimage experimental animation"))
	df, err := BuildFile(AnimationDriverGUID, 0x07, ffsAttribChecksum, driverSections, fs.Volume.Volume.EraseByte())
	if err != nil {
		return nil, err
	}
	compressed, err := lzmaSection(NewSection(SectionRaw, data))
	if err != nil {
		return nil, err
	}
	af, err := BuildFile(AnimationDataGUID, FileTypeFreeform, ffsAttribChecksum, compressed, fs.Volume.Volume.EraseByte())
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		guid  GUID
		bytes []byte
	}{{AnimationDataGUID, af}, {AnimationDriverGUID, df}} {
		// addFile reads the volume's current files, so both go into vol one after the other;
		// each addition verifies that the files before it are unchanged.
		patcher := &patcher{before: map[*Buffer][]byte{}}
		if _, err = patcher.addFile(vol, f.bytes, f.guid, 8); err != nil {
			return nil, err
		}
	}
	p.AnimationSize = p.AnimationRoom
	if left, err := freeSpaceIn(vol); err == nil {
		p.AnimationSize -= left.Capacity
	}
	p.Root = Parse(p.Data)
	if err := hasFiles(topVolumeAt(p.Root, vol.Offset), AnimationDataGUID, AnimationDriverGUID); err != nil {
		return nil, err
	}
	p.Logos, err = verifyLogos(p.Root, guid, first, len(p.Changes))
	if err != nil {
		return nil, err
	}
	if p.BootGuardWarning != "" {
		return nil, errors.New("animation changed a protected firmware range")
	}
	return p, nil
}

// topVolumeAt returns the top-level volume of image root at offset off, or nil.
func topVolumeAt(root *Node, off int) *Node {
	for _, c := range root.Children {
		if c.Kind == KindVolume && c.Offset == off {
			return c
		}
	}
	return nil
}

// hasFiles checks that volume vol, read afresh, has a file with each of guids.
func hasFiles(vol *Node, guids ...GUID) error {
	if vol == nil {
		return errors.New("main volume not found after adding the animation")
	}
	files, _, err := vol.Volume.Files(vol.Buf.Data)
	if err != nil {
		return err
	}
	for _, g := range guids {
		if !slices.ContainsFunc(files, func(f File) bool { return f.GUID == g }) {
			return fmt.Errorf("file %s missing from the main volume after adding it", g)
		}
	}
	return nil
}

func validateAnimationDriver(b []byte) error {
	if bytes.Contains(b, []byte("animlogo:")) {
		return errors.New("refusing a test-instrumented animation driver")
	}
	if len(b) < 0x40 || string(b[:2]) != "MZ" {
		return errors.New("animation driver asset missing or invalid")
	}
	p := int(binary.LittleEndian.Uint32(b[0x3c:]))
	if p < 0 || p+94 > len(b) || !bytes.Equal(b[p:p+4], []byte{'P', 'E', 0, 0}) || binary.LittleEndian.Uint16(b[p+4:]) != 0x8664 || binary.LittleEndian.Uint16(b[p+24:]) != 0x20b || binary.LittleEndian.Uint16(b[p+92:]) != 11 {
		return errors.New("animation driver must be an x64 EFI boot-service driver")
	}
	return nil
}
