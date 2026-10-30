package uefi

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
)

// bootGuardHashGUID names AMI's ReserveBootGuardFvMainHashKey file: a SHA-256 of FV_MAIN
// (up to three segments) in the boot block, which Intel Boot Guard verifies by signature.
// The boot block checks FV_MAIN against it before running it.
var bootGuardHashGUID = MustParseGUID("CBC91F44-A4BC-4A5B-8696-703451D0B053")

// Range is a byte range of an image.
type Range struct{ Offset, Size int }

// End is the offset just past the range.
func (r Range) End() int { return r.Offset + r.Size }

// BootGuard is what an image shows of Intel Boot Guard. Whether a board enforces it is fused
// into the chipset and cannot be read from the image.
type BootGuard struct {
	// Manifests is set if the image has a key manifest and a boot policy manifest: the vendor
	// set Boot Guard up for the board.
	Manifests bool
	// Hashed are the parts of the main volume that the boot block verifies against a hash,
	// if the image has one that matches them.
	Hashed []Range
}

// DetectBootGuard returns what img shows of Intel Boot Guard.
func DetectBootGuard(img []byte) BootGuard {
	b := BootGuard{Manifests: bytes.Contains(img, []byte("__KEYM__")) && bytes.Contains(img, []byte("__ACBP__"))}
	Parse(img).Walk(func(n *Node) bool {
		if b.Hashed != nil || n.Kind != KindFile || n.File.GUID != bootGuardHashGUID {
			return b.Hashed == nil
		}
		b.Hashed = bootGuardHash(img, n)
		return false
	})
	return b
}

// bootGuardHash returns the ranges of img hashed in the FV_MAIN hash file, or nil if the hash
// does not match them (unused hash files are erased).
func bootGuardHash(img []byte, file *Node) []Range {
	var body []byte
	for _, s := range file.Children {
		if s.Kind == KindSection && s.Section.Type == SectionRaw {
			body = s.Section.Body()
		}
	}
	if len(body) < 56 {
		return nil
	}
	// The image ends at 4 GiB in the CPU's address space.
	base := 1<<32 - len(img)
	h := sha256.New()
	var ranges []Range
	for i := range 3 {
		addr := int(binary.LittleEndian.Uint32(body[32+4*i:]))
		size := int(binary.LittleEndian.Uint32(body[44+4*i:]))
		if size == 0 {
			continue
		}
		r := Range{addr - base, size}
		if r.Offset < 0 || r.End() > len(img) {
			return nil
		}
		h.Write(img[r.Offset:r.End()])
		ranges = append(ranges, r)
	}
	if ranges == nil || !bytes.Equal(h.Sum(nil), body[:32]) {
		return nil
	}
	return ranges
}

// Note describes Boot Guard in the image, or returns "" if it shows no sign of it.
func (b BootGuard) Note() string {
	switch {
	case b.Manifests && b.Hashed != nil:
		return "Intel Boot Guard is set up for this board, and the main volume (FV_MAIN) is verified at boot against a hash: a BIOS whose main volume changed will very likely not start."
	case b.Manifests:
		return "Intel Boot Guard is set up for this board, and it is unknown what it verifies: a changed BIOS may not start."
	case b.Hashed != nil:
		return "The main volume (FV_MAIN) is verified at boot against an Intel Boot Guard hash: a board that enforces Boot Guard does not start a BIOS whose main volume changed."
	}
	return ""
}

// warningIf describes the risk of a change that touches the ranges for which changed is true,
// or returns "" if there is none.
func (b BootGuard) warningIf(changed func(Range) bool) string {
	switch {
	case b.Hashed != nil && !slices.ContainsFunc(b.Hashed, changed):
		return ""
	case b.Hashed != nil && b.Manifests:
		return "Intel Boot Guard is set up for this board and verifies its main volume (FV_MAIN), which this patch changes: the board will very likely not start this BIOS."
	case b.Hashed != nil:
		return "This patch changes the main volume (FV_MAIN), which is verified against an Intel Boot Guard hash: a board that enforces Boot Guard does not start this BIOS."
	case b.Manifests:
		return "Intel Boot Guard is set up for this board, and it is unknown what it verifies: the board may not start this BIOS."
	}
	return ""
}

// Warning describes the risk that Intel Boot Guard keeps the board from starting the image
// after ReplaceLogo has patched logos, or returns "" if there is none, the same as
// Patch.BootGuardWarning. The patch changes the top-level volumes holding the logos, and the
// volume of free if the logo is moved there (UseFreeSpace; nil if not).
func (b BootGuard) Warning(logos []*Logo, free *FreeSpace) string {
	var vols []*Node
	for _, l := range logos {
		vols = append(vols, l.Node.Path()[1])
	}
	if free != nil {
		vols = append(vols, free.Volume)
	}
	return b.warningIf(func(r Range) bool {
		return slices.ContainsFunc(vols, func(v *Node) bool { return v.Offset < r.End() && r.Offset < v.Offset+v.Size })
	})
}
