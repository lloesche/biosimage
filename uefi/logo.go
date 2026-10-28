package uefi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Encoding describes how the logo image is stored inside its FFS file.
type Encoding int

const (
	// EncodingRawFile: an EFI_FV_FILETYPE_RAW file whose contents are the image.
	EncodingRawFile Encoding = iota
	// EncodingRawSection: an uncompressed EFI_SECTION_RAW in the file.
	EncodingRawSection
	// EncodingLZMA: an EFI_SECTION_RAW inside an LZMA GUID-defined section.
	EncodingLZMA
	// EncodingUnsupported: wrapped in an encapsulation this tool cannot decode
	// (EFI/Tiano compression, Brotli, ...). It can still be replaced, uncompressed.
	EncodingUnsupported
)

func (e Encoding) String() string {
	return [...]string{"raw file", "uncompressed section", "LZMA-compressed section", "unsupported encapsulation"}[e]
}

// ErrLogoNotFound is returned when no logo file is present in a top-level volume.
var ErrLogoNotFound = errors.New("logo file not found in any top-level firmware volume")

// Logo is a logo file found in an image.
type Logo struct {
	Volume   *Volume
	File     File
	Encoding Encoding
	Detail   string // human-readable encapsulation info for EncodingUnsupported
	Image    []byte // nil when the encoding cannot be decoded
	Name     string // UI section name, if any
	// Capacity is the largest replacement file (header included) that fits.
	Capacity int

	top      []Section
	topIdx   int // section holding the image (raw or encapsulation)
	inner    []Section
	innerIdx int
	lzma     lzmaParams
	gAttrs   uint16
}

// FindLogos returns every file with the given GUID in the top-level volumes of buf.
func FindLogos(buf []byte, guid GUID) ([]*Logo, error) {
	var logos []*Logo
	for _, v := range FindVolumes(buf) {
		files, used, err := v.Files(buf)
		if err != nil {
			continue // not something we could patch safely anyway
		}
		for i, f := range files {
			if f.GUID != guid {
				continue
			}
			l, err := decodeLogo(buf, v, f)
			if err != nil {
				return nil, fmt.Errorf("logo file at %#x: %w", f.Offset, err)
			}
			if i == len(files)-1 {
				l.Capacity = v.End() - f.Offset
			} else {
				l.Capacity = max(v.alignUp(f.End())-f.Offset, v.End()-used)
			}
			logos = append(logos, l)
		}
	}
	if len(logos) == 0 {
		if bytes.Contains(buf, guid[:]) {
			return nil, fmt.Errorf("%w (the GUID does occur in the image, probably inside a compressed volume, which is not supported)", ErrLogoNotFound)
		}
		return nil, ErrLogoNotFound
	}
	return logos, nil
}

func decodeLogo(buf []byte, v *Volume, f File) (*Logo, error) {
	l := &Logo{Volume: v, File: f, topIdx: -1, innerIdx: -1}
	data := buf[f.DataOffset():f.End()]
	if f.Type == FileTypeRaw {
		l.Encoding, l.Image = EncodingRawFile, bytes.Clone(data)
		return l, nil
	}
	secs, err := ParseSections(data)
	if err != nil {
		return nil, err
	}
	l.top = secs
	for i, s := range secs {
		switch s.Type {
		case SectionUI:
			l.Name = s.UIName()
		case SectionRaw:
			if l.topIdx < 0 {
				l.topIdx, l.Encoding, l.Image = i, EncodingRawSection, bytes.Clone(s.Body())
			}
		}
	}
	if l.topIdx >= 0 {
		return l, nil
	}
	for i, s := range secs {
		if s.Type != SectionCompression && s.Type != SectionGUIDDefined {
			continue
		}
		l.topIdx, l.Encoding = i, EncodingUnsupported
		if s.Type == SectionCompression {
			l.Detail = "EFI_SECTION_COMPRESSION"
			return l, nil
		}
		g, off, attrs, err := s.guidedHeader()
		if err != nil {
			return nil, err
		}
		l.gAttrs = attrs
		if g != LZMASectionGUID {
			l.Detail = "GUID-defined section " + g.String()
			if n, ok := guidNames[g]; ok {
				l.Detail += " (" + n + ")"
			}
			return l, nil
		}
		payload := s.Raw[off:]
		innerData, err := lzmaDecompress(payload)
		if err != nil {
			return nil, fmt.Errorf("LZMA section: %w", err)
		}
		inner, err := ParseSections(innerData)
		if err != nil {
			return nil, fmt.Errorf("LZMA section contents: %w", err)
		}
		l.lzma = lzmaParams{props: payload[0], dict: binary.LittleEndian.Uint32(payload[1:])}
		for j, is := range inner {
			switch is.Type {
			case SectionUI:
				l.Name = is.UIName()
			case SectionRaw:
				if l.innerIdx < 0 {
					l.innerIdx, l.Image = j, bytes.Clone(is.Body())
				}
			}
		}
		if l.innerIdx < 0 {
			return nil, errors.New("LZMA section contains no raw image section")
		}
		l.inner, l.Encoding = inner, EncodingLZMA
		return l, nil
	}
	return nil, errors.New("file contains no image section")
}

// ReplaceOptions control how the new logo is stored.
type ReplaceOptions struct {
	// NoCompress stores the image uncompressed even if the original was compressed.
	NoCompress bool
}

// rebuildData returns the new file contents holding img, keeping the original layout
// (other sections, compression) where possible.
func (l *Logo) rebuildData(img []byte, opt ReplaceOptions) ([]byte, string, error) {
	raw := NewSection(SectionRaw, img)
	join := func(secs []Section, idx int, repl ...[]byte) []byte {
		var parts [][]byte
		for i, s := range secs {
			if i == idx {
				parts = append(parts, repl...)
			} else {
				parts = append(parts, s.Raw)
			}
		}
		return JoinSections(parts...)
	}
	switch l.Encoding {
	case EncodingRawFile:
		return img, "stored as raw file", nil
	case EncodingRawSection:
		return join(l.top, l.topIdx, raw), "stored uncompressed", nil
	case EncodingLZMA:
		inner := make([][]byte, 0, len(l.inner))
		for j, s := range l.inner {
			if j == l.innerIdx {
				inner = append(inner, raw)
			} else {
				inner = append(inner, s.Raw)
			}
		}
		if opt.NoCompress {
			return join(l.top, l.topIdx, inner...), "stored uncompressed (LZMA wrapper removed)", nil
		}
		payload, err := lzmaCompress(JoinSections(inner...), l.lzma)
		if err != nil {
			return nil, "", err
		}
		return join(l.top, l.topIdx, newGuidedSection(LZMASectionGUID, l.gAttrs, payload)), "stored LZMA-compressed", nil
	default:
		return join(l.top, l.topIdx, raw), "stored uncompressed (replaces " + l.Detail + ")", nil
	}
}

// Change describes one patched logo file.
type Change struct {
	VolumeOffset int
	OldOffset    int
	NewOffset    int
	OldSize      int
	NewSize      int
	Placement    string
	Storage      string
}

// ReplaceLogo returns a copy of buf with every logo file (gEfiDefaultBmpLogoGuid unless
// guid is given) replaced by img. The result is re-parsed and verified before returning.
func ReplaceLogo(buf, img []byte, guid GUID, opt ReplaceOptions) ([]byte, []Change, error) {
	logos, err := FindLogos(buf, guid)
	if err != nil {
		return nil, nil, err
	}
	seen := map[int]bool{}
	for _, l := range logos {
		if seen[l.Volume.Offset] {
			return nil, nil, fmt.Errorf("volume at %#x contains more than one logo file; not supported", l.Volume.Offset)
		}
		seen[l.Volume.Offset] = true
	}
	out := bytes.Clone(buf)
	var changes []Change
	for _, l := range logos {
		data, storage, err := l.rebuildData(img, opt)
		if err != nil {
			return nil, nil, err
		}
		nf, err := BuildFile(l.File.GUID, l.File.Type, l.File.Attributes, data, l.Volume.EraseByte())
		if err != nil {
			return nil, nil, err
		}
		before := bytes.Clone(out)
		pos, placement, err := placeFile(out, l.Volume, l.File, nf)
		if err != nil {
			return nil, nil, fmt.Errorf("volume at %#x: %w", l.Volume.Offset, err)
		}
		if err := verifyVolume(before, out, l.Volume, guid, img); err != nil {
			return nil, nil, fmt.Errorf("verification of volume at %#x failed: %w", l.Volume.Offset, err)
		}
		changes = append(changes, Change{
			VolumeOffset: l.Volume.Offset, OldOffset: l.File.Offset, NewOffset: pos,
			OldSize: l.File.Size, NewSize: len(nf), Placement: placement, Storage: storage,
		})
	}
	return out, changes, nil
}

// placeFile writes nf in place of old, in the cheapest way that keeps the volume valid:
//   - old is the last file: rewrite it in place, growing into the free space;
//   - nf fits into old's slot: rewrite in place, filling the remainder with a pad file;
//   - otherwise: turn old's slot into a pad file and append nf after the last file.
func placeFile(buf []byte, v *Volume, old File, nf []byte) (int, string, error) {
	files, used, err := v.Files(buf)
	if err != nil {
		return 0, "", err
	}
	idx := -1
	for i, f := range files {
		if f.Offset == old.Offset {
			idx = i
		}
	}
	if idx < 0 {
		return 0, "", errors.New("logo file vanished")
	}
	erase := v.EraseByte()
	fill := func(from, to int) {
		for i := from; i < to; i++ {
			buf[i] = erase
		}
	}
	slotEnd := v.alignUp(old.End())
	newEnd := old.Offset + len(nf)
	align := max(dataAlignment(old.Attributes), 8)

	if idx == len(files)-1 {
		if newEnd > v.End() {
			return 0, "", tooBig(len(nf), v.End()-old.Offset)
		}
		if !isErased(buf[slotEnd:max(slotEnd, min(v.alignUp(newEnd), v.End()))], erase) {
			return 0, "", errors.New("space after the last file is not empty")
		}
		copy(buf[old.Offset:], nf)
		fill(newEnd, max(newEnd, slotEnd))
		return old.Offset, "rewritten in place (last file in volume)", nil
	}

	if gap := slotEnd - v.alignUp(newEnd); gap == 0 || gap >= ffsHeaderSize {
		copy(buf[old.Offset:], nf)
		fill(newEnd, v.alignUp(newEnd))
		if gap > 0 {
			copy(buf[v.alignUp(newEnd):], buildPadFile(gap, erase))
			return old.Offset, "rewritten in place, remainder padded", nil
		}
		return old.Offset, "rewritten in place", nil
	}

	pos := used
	for (pos+ffsHeaderSize-v.Offset)%align != 0 || (pos != used && pos-used < ffsHeaderSize) {
		pos += 8
	}
	if pos+len(nf) > v.End() {
		return 0, "", tooBig(len(nf), v.End()-used)
	}
	if !isErased(buf[used:min(v.alignUp(pos+len(nf)), v.End())], erase) {
		return 0, "", errors.New("free space at the end of the volume is not empty")
	}
	copy(buf[old.Offset:], buildPadFile(slotEnd-old.Offset, erase))
	if pos > used {
		copy(buf[used:], buildPadFile(pos-used, erase))
	}
	copy(buf[pos:], nf)
	return pos, "moved to end of volume, old slot padded", nil
}

func tooBig(need, have int) error {
	return fmt.Errorf("new logo file needs %d bytes but only %d are available in its volume; use a smaller or simpler image", need, have)
}

func isErased(b []byte, erase byte) bool {
	for _, c := range b {
		if c != erase {
			return false
		}
	}
	return true
}

// verifyVolume re-parses the patched volume and checks that it is consistent, that every
// other file is untouched and that the logo decodes to img.
func verifyVolume(before, after []byte, v *Volume, guid GUID, img []byte) error {
	if !bytes.Equal(before[:v.Offset], after[:v.Offset]) || !bytes.Equal(before[v.End():], after[v.End():]) {
		return errors.New("bytes outside the volume changed")
	}
	nv, err := ParseVolume(after, v.Offset)
	if err != nil {
		return err
	}
	others := func(buf []byte) (kept [][]byte, bad int, err error) {
		files, _, err := nv.Files(buf)
		if err != nil {
			return nil, 0, err
		}
		for _, f := range files {
			if !headerChecksumOK(buf, f) || !dataChecksumOK(buf, f) {
				bad++
			}
			if f.GUID != guid && f.Type != FileTypePad {
				kept = append(kept, buf[f.Offset:f.End()])
			}
		}
		return kept, bad, nil
	}
	keptBefore, badBefore, err := others(before)
	if err != nil {
		return err
	}
	keptAfter, badAfter, err := others(after)
	if err != nil {
		return err
	}
	if badAfter > badBefore {
		return fmt.Errorf("%d file checksum(s) invalid after patching (was %d)", badAfter, badBefore)
	}
	if len(keptBefore) != len(keptAfter) {
		return errors.New("number of files changed")
	}
	for i := range keptBefore {
		if !bytes.Equal(keptBefore[i], keptAfter[i]) {
			return errors.New("a non-logo file changed")
		}
	}
	logos, err := FindLogos(after[:v.End()], guid)
	if err != nil {
		return err
	}
	for _, l := range logos {
		if l.Volume.Offset == v.Offset && !bytes.Equal(l.Image, img) {
			return errors.New("logo does not decode to the new image")
		}
	}
	return nil
}
