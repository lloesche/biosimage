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

// ErrLogoNotFound is returned when no logo file is present in the image.
var ErrLogoNotFound = errors.New("logo file not found")

// Container is an LZMA-compressed section of a firmware-volume file whose contents hold
// another firmware volume. Some vendors (e.g. MSI on Intel boards) keep the logo only in
// such a compressed main volume.
type Container struct {
	Volume *Volume // volume in the image that holds File
	File   File
	secs   []Section
	idx    int // index of the LZMA section in secs
	lzma   lzmaParams
	attrs  uint16
	data   []byte // decompressed section contents
}

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
	// Container is set when the logo is inside a compressed volume. Volume and File
	// offsets are then relative to the container's decompressed data.
	Container *Container

	top      []Section
	topIdx   int // section holding the image (raw or encapsulation)
	inner    []Section
	innerIdx int
	lzma     lzmaParams
	gAttrs   uint16
}

// FindLogos returns every file with the given GUID in the volumes of buf, including
// uncompressed volumes nested inside files. Only if there is none, it also looks inside
// LZMA-compressed volumes.
func FindLogos(buf []byte, guid GUID) ([]*Logo, error) {
	logos, err := findLogos(buf, guid)
	if err != nil {
		return nil, err
	}
	if len(logos) == 0 {
		for _, c := range compressedVolumes(buf) {
			inner, err := findLogos(c.data, guid)
			if err != nil {
				return nil, fmt.Errorf("compressed volume in file at %#x: %w", c.File.Offset, err)
			}
			for _, l := range inner {
				l.Container = c
			}
			logos = append(logos, inner...)
		}
	}
	if len(logos) == 0 {
		return nil, ErrLogoNotFound
	}
	return logos, nil
}

func findLogos(buf []byte, guid GUID) ([]*Logo, error) {
	var logos []*Logo
	for _, v := range AllVolumes(buf) {
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
	return logos, nil
}

// compressedVolumes returns the LZMA sections of firmware-volume files that hold a volume.
func compressedVolumes(buf []byte) []*Container {
	var out []*Container
	for _, v := range AllVolumes(buf) {
		files, _, err := v.Files(buf)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Type != FileTypeFVImage {
				continue
			}
			secs, err := ParseSections(buf[f.DataOffset():f.End()])
			if err != nil {
				continue
			}
			for i, s := range secs {
				if s.Type != SectionGUIDDefined {
					continue
				}
				g, off, attrs, err := s.guidedHeader()
				if err != nil || g != LZMASectionGUID {
					continue
				}
				payload := s.Raw[off:]
				data, err := lzmaDecompress(payload)
				if err != nil || len(FindVolumes(data)) == 0 {
					continue
				}
				out = append(out, &Container{
					Volume: v, File: f, secs: secs, idx: i, attrs: attrs, data: data,
					lzma: lzmaParams{props: payload[0], dict: binary.LittleEndian.Uint32(payload[1:])},
				})
			}
		}
	}
	return out
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
	// Container describes the compressed volume the logo was in, if any.
	Container string
}

// ReplaceLogo returns a copy of buf with every logo file (gEfiDefaultBmpLogoGuid unless
// guid is given) replaced by img. The result is re-parsed and verified before returning.
func ReplaceLogo(buf, img []byte, guid GUID, opt ReplaceOptions) ([]byte, []Change, error) {
	logos, err := FindLogos(buf, guid)
	if err != nil {
		return nil, nil, err
	}
	var direct []*Logo
	var containers []*Container
	inside := map[*Container][]*Logo{}
	for _, l := range logos {
		if l.Container == nil {
			direct = append(direct, l)
			continue
		}
		if inside[l.Container] == nil {
			containers = append(containers, l.Container)
		}
		inside[l.Container] = append(inside[l.Container], l)
	}

	out := bytes.Clone(buf)
	changes, err := replaceIn(out, direct, img, guid, opt)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range containers {
		data := bytes.Clone(c.data)
		inner, err := replaceIn(data, inside[c], img, guid, opt)
		if err != nil {
			return nil, nil, err
		}
		payload, err := lzmaCompress(data, c.lzma)
		if err != nil {
			return nil, nil, err
		}
		parts := make([][]byte, len(c.secs))
		for i, s := range c.secs {
			parts[i] = s.Raw
		}
		parts[c.idx] = newGuidedSection(LZMASectionGUID, c.attrs, payload)
		nf, err := BuildFile(c.File.GUID, c.File.Type, c.File.Attributes, JoinSections(parts...), c.Volume.EraseByte())
		if err != nil {
			return nil, nil, err
		}
		before := bytes.Clone(out)
		pos, placement, err := placeFile(out, c.Volume, c.File, nf)
		if err != nil {
			return nil, nil, fmt.Errorf("compressed volume in file at %#x: %w", c.File.Offset, err)
		}
		if err := verifyVolume(before, out, c.Volume, c.File.GUID); err != nil {
			return nil, nil, fmt.Errorf("verification of volume at %#x failed: %w", c.Volume.Offset, err)
		}
		if err := fixParents(before, out, c.Volume); err != nil {
			return nil, nil, fmt.Errorf("volume at %#x: %w", c.Volume.Offset, err)
		}
		desc := fmt.Sprintf("inside compressed volume file %s at %#x, recompressed %d -> %d bytes, %s",
			c.File.GUID, pos, c.File.Size, len(nf), placement)
		for i := range inner {
			inner[i].Container = desc
		}
		changes = append(changes, inner...)
	}

	// Final check: parse the patched image from scratch; every logo must decode to img.
	check, err := FindLogos(out, guid)
	if err != nil {
		return nil, nil, fmt.Errorf("verification failed: %w", err)
	}
	if len(check) != len(logos) {
		return nil, nil, fmt.Errorf("verification failed: %d logo files after patching, %d before", len(check), len(logos))
	}
	for _, l := range check {
		if !bytes.Equal(l.Image, img) {
			return nil, nil, fmt.Errorf("verification failed: logo at %#x does not decode to the new image", l.File.Offset)
		}
	}
	return out, changes, nil
}

// replaceIn patches logos that all live directly in buf, which is either the image or the
// decompressed contents of a container.
func replaceIn(buf []byte, logos []*Logo, img []byte, guid GUID, opt ReplaceOptions) ([]Change, error) {
	seen := map[int]bool{}
	for _, l := range logos {
		if seen[l.Volume.Offset] {
			return nil, fmt.Errorf("volume at %#x contains more than one logo file; not supported", l.Volume.Offset)
		}
		seen[l.Volume.Offset] = true
	}
	var changes []Change
	for _, l := range logos {
		data, storage, err := l.rebuildData(img, opt)
		if err != nil {
			return nil, err
		}
		nf, err := BuildFile(l.File.GUID, l.File.Type, l.File.Attributes, data, l.Volume.EraseByte())
		if err != nil {
			return nil, err
		}
		before := bytes.Clone(buf)
		pos, placement, err := placeFile(buf, l.Volume, l.File, nf)
		if err != nil {
			return nil, fmt.Errorf("volume at %#x: %w", l.Volume.Offset, err)
		}
		if err := verifyVolume(before, buf, l.Volume, guid); err != nil {
			return nil, fmt.Errorf("verification of volume at %#x failed: %w", l.Volume.Offset, err)
		}
		if err := fixParents(before, buf, l.Volume); err != nil {
			return nil, fmt.Errorf("volume at %#x: %w", l.Volume.Offset, err)
		}
		changes = append(changes, Change{
			VolumeOffset: l.Volume.Offset, OldOffset: l.File.Offset, NewOffset: pos,
			OldSize: l.File.Size, NewSize: len(nf), Placement: placement, Storage: storage,
		})
	}
	return changes, nil
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

// verifyVolume re-parses the patched volume and checks that it is consistent and that every
// file except the replaced one (identified by guid) is untouched.
func verifyVolume(before, after []byte, v *Volume, guid GUID) error {
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
			return errors.New("another file in the volume changed")
		}
	}
	return nil
}

// fixParents updates the data checksums of the files that contain a nested volume (innermost
// first, since an outer checksum covers the inner one) and checks that nothing else outside
// the volume changed.
func fixParents(before, after []byte, v *Volume) error {
	allowed := map[int]bool{}
	for i := len(v.Parents) - 1; i >= 0; i-- {
		p := v.Parents[i]
		if p.Attributes&ffsAttribChecksum != 0 {
			after[p.Offset+17] = -sum8(after[p.DataOffset():p.End()])
			allowed[p.Offset+17] = true
		}
	}
	for i := range before {
		if (i < v.Offset || i >= v.End()) && before[i] != after[i] && !allowed[i] {
			return fmt.Errorf("unexpected change at %#x outside the volume", i)
		}
	}
	for _, p := range v.Parents {
		if !headerChecksumOK(after, p) || !dataChecksumOK(after, p) {
			return fmt.Errorf("checksum of enclosing file at %#x is invalid", p.Offset)
		}
	}
	return nil
}

// Header returns up to n bytes from the start of the logo file, read from the image or,
// for a logo inside a compressed volume, from the decompressed data.
func (l *Logo) Header(buf []byte, n int) []byte {
	if l.Container != nil {
		buf = l.Container.data
	}
	return bytes.Clone(buf[l.File.Offset:min(len(buf), l.File.Offset+n)])
}
