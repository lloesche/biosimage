package uefi

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
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

// Repeat says how often the driver plays an animation.
type Repeat int

const (
	RepeatAsGIF   Repeat = iota // as often as the GIF's loop count says
	RepeatForever               // until the board leaves the logo screen
	RepeatOnce                  // once, then the last frame stays
)

// AnimationOptions control how the driver animation plays.
type AnimationOptions struct {
	Repeat Repeat
	// LogoFrame is the frame of the GIF, from 0, stored as the regular boot logo: the BIOS
	// shows it if the driver does not run.
	LogoFrame int
}

// EncodeBootAnimation returns our bounded BIA2 frame stream and frame logoFrame as a BMP.
// The header holds a.Plays; each record stores a delay in 10ms units and a changed rectangle
// of palette indices. Delays belong to the frame being displayed (no AMITSE timing rotation).
func EncodeBootAnimation(a *Animation, logoFrame int) ([]byte, []byte, error) {
	if a == nil || len(a.Frames) < 2 || len(a.Frames) > 3000 || len(a.Delays) != len(a.Frames) {
		return nil, nil, errors.New("animation needs 2–3000 frames and matching delays")
	}
	if a.Plays < 0 || a.Plays > maxAnimationPlays {
		return nil, nil, errors.New("animation play count out of range")
	}
	if logoFrame < 0 || logoFrame >= len(a.Frames) {
		return nil, nil, fmt.Errorf("no frame %d for the logo: the animation has %d", logoFrame, len(a.Frames))
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
	copy(out, "BIA2")
	binary.LittleEndian.PutUint16(out[4:], uint16(w))
	binary.LittleEndian.PutUint16(out[6:], uint16(h))
	binary.LittleEndian.PutUint16(out[8:], uint16(len(a.Frames)))
	binary.LittleEndian.PutUint16(out[10:], uint16(a.Plays))
	for i, c := range pal {
		cr, cg, cb, _ := c.RGBA()
		copy(out[12+4*i:], []byte{byte(cb >> 8), byte(cg >> 8), byte(cr >> 8), 0})
	}
	var last, logo *image.Paletted
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
		if i == logoFrame {
			logo = p
		}
	}
	// The quantized frame exactly matches the driver's frame.
	rgba := image.NewRGBA(logo.Rect)
	draw.Draw(rgba, rgba.Rect, logo, image.Point{}, draw.Src)
	bmp, err := EncodeImage(rgba, "BMP", 100)
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

// ReplaceAnimatedLogo returns a copy of image n with a frame of gif as the logo, our
// DXE driver, and a separate compressed frame-data file in FV_MAIN. It reuses what is already
// unpacked of n, and does not modify it.
func (n *Node) ReplaceAnimatedLogo(gif []byte, guid GUID, opt AnimationOptions) (*Patch, error) {
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
	switch opt.Repeat {
	case RepeatForever:
		a.Plays = 0
	case RepeatOnce:
		a.Plays = 1
	}
	data, logo, err := EncodeBootAnimation(a, opt.LogoFrame)
	if err != nil {
		return nil, err
	}
	if err := validateAnimationDriver(animationDriver); err != nil {
		return nil, err
	}
	p, err := ReplaceLogo(n.Bytes(), logo, guid, ReplaceOptions{})
	if err != nil {
		return nil, fmt.Errorf("fallback logo: %w", err)
	}
	// The logo may have changed the main volume (if the logo is inside it), but not where
	// it is: take it from the patched image, which needs nothing unpacked.
	vol := topVolumeAt(p.Root, target.Volume.Offset)
	if vol == nil {
		return nil, errors.New("main volume not found after writing the fallback logo")
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
	if err := checkAnimation(topVolumeAt(p.Root, vol.Offset), data); err != nil {
		return nil, err
	}
	p.Logos, err = verifyLogos(p.Root, guid, logo, len(p.Changes))
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

// checkAnimation checks that volume vol of a freshly parsed image has the animation driver,
// and an animation data file that reads back as data.
func checkAnimation(vol *Node, data []byte) error {
	if vol == nil {
		return errors.New("main volume not found after adding the animation")
	}
	var driver, frames *Node
	for _, c := range vol.Children {
		if c.Kind == KindFile && c.File.GUID == AnimationDriverGUID {
			driver = c
		}
		if c.Kind == KindFile && c.File.GUID == AnimationDataGUID {
			frames = c
		}
	}
	if driver == nil || frames == nil {
		return errors.New("animation driver or data file missing from the main volume after adding it")
	}
	got, err := frames.BootAnimationData()
	if err != nil || !bytes.Equal(got, data) {
		return fmt.Errorf("animation data does not read back from the main volume: %v", err)
	}
	return nil
}

// BootAnimationData returns the frame data (EncodeBootAnimation) in file n, an animation data
// file added by Node.ReplaceAnimatedLogo, unpacking its compressed section.
func (n *Node) BootAnimationData() ([]byte, error) {
	if n.Kind != KindFile || n.File.GUID != AnimationDataGUID {
		return nil, errors.New("not an animation data file")
	}
	var raw *Node
	var err error
	n.Walk(func(c *Node) bool {
		if c.Lazy() {
			err = c.Expand()
		}
		if c.Kind == KindSection && c.Section.Type == SectionRaw {
			raw = c
		}
		return raw == nil && err == nil
	})
	switch {
	case err != nil:
		return nil, err
	case raw == nil:
		return nil, errors.New("animation data file without a raw section")
	}
	return raw.Section.Body(), nil
}

// DecodeBootAnimation returns the complete frames of frame data b (EncodeBootAnimation), each
// with its delay, as the driver shows them.
func DecodeBootAnimation(b []byte) (*Animation, error) {
	if len(b) < animationHeader || string(b[:4]) != "BIA2" {
		return nil, errors.New("not biosimage animation data")
	}
	w, h := int(binary.LittleEndian.Uint16(b[4:])), int(binary.LittleEndian.Uint16(b[6:]))
	count := int(binary.LittleEndian.Uint16(b[8:]))
	if w < 1 || h < 1 || w > 1920 || h > 1080 || count < 1 || count > 3000 || uint64(w)*uint64(h)*uint64(count) > maxAnimationPixels {
		return nil, errors.New("animation data exceeds the limits")
	}
	var pal [256]color.RGBA
	for i := range pal {
		c := b[12+4*i:] // blue, green, red, 0
		pal[i] = color.RGBA{c[2], c[1], c[0], 0xFF}
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	a := &Animation{Plays: int(binary.LittleEndian.Uint16(b[10:]))}
	p := animationHeader
	for i := range count {
		if p+12 > len(b) {
			return nil, fmt.Errorf("animation data truncated at frame %d", i)
		}
		var v [5]int // delay, x, y, width, height
		for j := range v {
			v[j] = int(binary.LittleEndian.Uint16(b[p+2*j:]))
		}
		r := image.Rect(v[1], v[2], v[1]+v[3], v[2]+v[4])
		p += 12
		if r.Empty() || !r.In(canvas.Rect) || p+r.Dx()*r.Dy() > len(b) {
			return nil, fmt.Errorf("animation frame %d out of bounds", i)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				canvas.SetRGBA(x, y, pal[b[p]])
				p++
			}
		}
		a.Frames = append(a.Frames, clone(canvas))
		a.Delays = append(a.Delays, v[0])
	}
	return a, nil
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
