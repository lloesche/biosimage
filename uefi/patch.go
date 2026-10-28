package uefi

import (
	"bytes"
	"errors"
	"fmt"
)

// ReplaceOptions control how the new logo is stored.
type ReplaceOptions struct {
	// NoCompress stores the image uncompressed even if the original was compressed.
	NoCompress bool
}

// Storage says how the new image was stored in the logo file.
type Storage int

const (
	StoredRawFile                Storage = iota // the file is the image
	StoredUncompressed                          // raw section, as before
	StoredLZMA                                  // raw section inside LZMA, as before
	StoredUnwrapped                             // LZMA wrapper removed (ReplaceOptions.NoCompress)
	StoredLZMAFallback                          // was uncompressed, but only fits compressed
	StoredReplacingEncapsulation                // raw section in place of an undecodable one
)

func (s Storage) String() string {
	return [...]string{
		"stored as raw file",
		"stored uncompressed",
		"stored LZMA-compressed",
		"stored uncompressed (LZMA wrapper removed)",
		"stored LZMA-compressed (the original was uncompressed but the new image did not fit)",
		"stored uncompressed (replaces the undecodable encapsulation)",
	}[s]
}

// Recompression is an enclosing compressed volume that was rebuilt around the new logo.
type Recompression struct {
	File      File // the file holding the compressed section, before patching
	NewOffset int
	NewSize   int
	Placement Placement
}

func (r Recompression) String() string {
	return fmt.Sprintf("inside compressed volume file %s, recompressed %d -> %d bytes at %#x, %s",
		r.File.GUID, r.File.Size, r.NewSize, r.NewOffset, r.Placement)
}

// Change describes one patched logo file.
type Change struct {
	VolumeOffset int
	OldOffset    int
	NewOffset    int
	OldSize      int
	NewSize      int
	Placement    Placement
	Storage      Storage
	// Recompressed lists the compressed volumes rebuilt around the logo, innermost first.
	Recompressed []Recompression
}

// Patch is the result of ReplaceLogo.
type Patch struct {
	Data    []byte
	Changes []Change
	// Logos are the logo files read back from Data by a fresh parse.
	Logos []*Logo
}

// ReplaceLogo returns a copy of buf with every logo file (gEfiDefaultBmpLogoGuid unless guid is
// given) replaced by img. Each logo file is rebuilt and put back into its volume, then every
// enclosing file is updated bottom-up: checksums for uncompressed nesting, recompression and
// placement for compressed volumes. The result is verified before it is returned.
func ReplaceLogo(buf, img []byte, guid GUID, opt ReplaceOptions) (*Patch, error) {
	out := bytes.Clone(buf)
	logos, err := Parse(out).FindLogos(guid)
	if err != nil {
		return nil, err
	}
	if err := independent(logos); err != nil {
		return nil, err
	}
	patch := &Patch{Data: out}
	for _, l := range logos {
		p := &patcher{logo: l.Node, before: map[*Buffer][]byte{}}
		c, err := p.replace(l, img, opt)
		if err != nil {
			return nil, err
		}
		patch.Changes = append(patch.Changes, c)
	}
	if patch.Logos, err = verifyLogos(out, guid, img, len(logos)); err != nil {
		return nil, fmt.Errorf("verification failed: %w", err)
	}
	return patch, nil
}

// independent ensures that no two logo files share a volume: patching one may move files in
// it, which would invalidate the other's position.
func independent(logos []*Logo) error {
	owner := map[*Node]*Logo{}
	for _, l := range logos {
		for _, n := range l.Node.Path() {
			if o := owner[n]; n.Kind == KindVolume && o != nil && o != l {
				return fmt.Errorf("logo files at %#x and %#x are in the same volume; not supported", o.File.Offset, l.File.Offset)
			}
			owner[n] = l
		}
	}
	return nil
}

// verifyLogos parses the patched image from scratch: there must be as many logo files as
// before, all holding img.
func verifyLogos(out []byte, guid GUID, img []byte, want int) ([]*Logo, error) {
	logos, err := FindLogos(out, guid)
	if err != nil {
		return nil, err
	}
	if len(logos) != want {
		return nil, fmt.Errorf("%d logo files after patching, %d before", len(logos), want)
	}
	for _, l := range logos {
		if !bytes.Equal(l.Image, img) {
			return nil, fmt.Errorf("logo at %#x does not decode to the new image", l.File.Offset)
		}
	}
	return logos, nil
}

// patcher rebuilds one logo file and everything that encloses it.
type patcher struct {
	logo   *Node
	before map[*Buffer][]byte // buffer contents before this patch, for verification
	change Change
}

func (p *patcher) replace(l *Logo, img []byte, opt ReplaceOptions) (Change, error) {
	p.change = Change{VolumeOffset: l.Volume.Offset, OldOffset: l.File.Offset, OldSize: l.File.Size}
	raw := NewSection(SectionRaw, img)
	var err error
	switch l.Encoding {
	case EncodingRawFile:
		p.change.Storage = StoredRawFile
		var nf []byte
		if nf, err = BuildFile(l.File.GUID, l.File.Type, l.File.Attributes, img, l.Volume.EraseByte()); err == nil {
			err = p.placeFile(l.Node, nf)
		}
	case EncodingRawSection:
		p.change.Storage = StoredUncompressed
		err = p.replaceSection(l.image, raw)
		if errors.Is(err, errTooBig) && !opt.NoCompress {
			// It only fits compressed. The firmware reads the raw section through any
			// encapsulation, so wrap it in LZMA (nothing was written yet).
			p.change.Storage = StoredLZMAFallback
			var payload []byte
			if payload, err = lzmaCompress(raw, lzmaParams{props: lzmaDefaultProps, dict: lzmaDefaultDict}); err == nil {
				err = p.replaceSection(l.image, newGuidedSection(LZMASectionGUID, guidedProcessingRequired, payload))
			}
		}
	case EncodingLZMA:
		p.change.Storage = StoredLZMA
		if opt.NoCompress {
			p.change.Storage = StoredUnwrapped
			wrapper := l.image.Parent
			err = p.replaceSection(wrapper, joinWith(wrapper.Children, l.image, raw))
		} else {
			err = p.replaceSection(l.image, raw)
		}
	default:
		p.change.Storage = StoredReplacingEncapsulation
		err = p.replaceSection(l.image, raw)
	}
	return p.change, err
}

// placeFile puts nf in place of file n in its volume, then propagates the change upwards.
func (p *patcher) placeFile(n *Node, nf []byte) error {
	vol := n.Parent
	slot, err := newVolumeSlot(vol.Buf.Data, vol.Volume, n.File)
	if err != nil {
		return err
	}
	l, err := slot.plan(len(nf))
	if err != nil {
		return fmt.Errorf("volume at %#x: %w", vol.Offset, err)
	}
	p.snapshot(vol.Buf)
	l.apply(vol.Buf.Data, vol.Volume.EraseByte(), nf)
	if n == p.logo {
		p.change.NewOffset, p.change.NewSize, p.change.Placement = l.pos, len(nf), l.placement
	} else {
		p.change.Recompressed = append(p.change.Recompressed,
			Recompression{File: n.File, NewOffset: l.pos, NewSize: len(nf), Placement: l.placement})
	}
	if err := p.verifyVolume(vol, n.File.GUID); err != nil {
		return err
	}
	return p.changed(vol)
}

// replaceSection replaces section n by b (one or more sections of any size) and rebuilds
// what contains it.
func (p *patcher) replaceSection(n *Node, b []byte) error {
	parent := n.Parent
	data := joinWith(parent.Children, n, b)
	switch {
	case parent.Kind == KindFile:
		f := parent.File
		nf, err := BuildFile(f.GUID, f.Type, f.Attributes, data, parent.Parent.Volume.EraseByte())
		if err != nil {
			return err
		}
		return p.placeFile(parent, nf)
	case parent.Buf != n.Buf:
		return p.recompress(parent, data)
	}
	return fmt.Errorf("%w: %s", errNotRebuildable, SectionTypeName(parent.Section.Type))
}

// changed propagates an in-place change of n (same size) to its ancestors.
func (p *patcher) changed(n *Node) error {
	parent := n.Parent
	switch {
	case parent == nil || parent.Kind == KindImage:
		return nil
	case parent.Kind == KindVolume:
		if err := p.verifyVolume(parent, n.File.GUID); err != nil {
			return err
		}
	case parent.Kind == KindFile: // e.g. AMI's nested volume inside a RAW file
		p.snapshot(parent.Buf)
		buf, f := parent.Buf.Data, parent.File
		if f.Attributes&ffsAttribChecksum != 0 {
			buf[f.Offset+17] = -sum8(buf[f.DataOffset():f.End()])
		}
		if header, data := parent.Checksums(); !header || !data {
			return fmt.Errorf("checksum of enclosing file at %#x is invalid", f.Offset)
		}
	case parent.Buf != n.Buf: // inside a compressed section
		return p.recompress(parent, n.Buf.Data)
	case parent.Section.Type == SectionGUIDDefined: // e.g. CRC32, whose check value would change
		return fmt.Errorf("%w: %s", errNotRebuildable, SectionTypeName(parent.Section.Type))
	}
	return p.changed(parent)
}

// recompress replaces the contents of LZMA section n with data, keeping its LZMA settings.
func (p *patcher) recompress(n *Node, data []byte) error {
	h, ok := n.LZMAHeader()
	_, _, attrs, _ := n.GUIDDefinition()
	if !ok {
		return fmt.Errorf("%w: compressed section at %#x", errNotRebuildable, n.Offset)
	}
	payload, err := lzmaCompress(data, lzmaParams{props: h.Props, dict: h.Dict})
	if err != nil {
		return err
	}
	return p.replaceSection(n, newGuidedSection(LZMASectionGUID, attrs, payload))
}

func (p *patcher) snapshot(b *Buffer) {
	if _, ok := p.before[b]; !ok {
		p.before[b] = bytes.Clone(b.Data)
	}
}

// joinWith joins the sections in secs, with old replaced by repl.
func joinWith(secs []*Node, old *Node, repl []byte) []byte {
	parts := make([][]byte, len(secs))
	for i, s := range secs {
		parts[i] = s.Bytes()
		if s == old {
			parts[i] = repl
		}
	}
	return JoinSections(parts...)
}

// verifyVolume re-parses a patched volume and checks that it is consistent, that nothing
// outside it changed and that every file except the replaced one (by GUID) is untouched.
func (p *patcher) verifyVolume(vol *Node, guid GUID) error {
	before, after, v := p.before[vol.Buf], vol.Buf.Data, vol.Volume
	fail := func(err error) error { return fmt.Errorf("verification of volume at %#x failed: %w", v.Offset, err) }
	if !bytes.Equal(before[:v.Offset], after[:v.Offset]) || !bytes.Equal(before[v.End():], after[v.End():]) {
		return fail(errors.New("bytes outside the volume changed"))
	}
	nv, err := ParseVolume(after, v.Offset)
	if err != nil {
		return fail(err)
	}
	others := func(buf []byte) (kept [][]byte, bad int, err error) {
		files, _, err := nv.Files(buf)
		for _, f := range files {
			if !headerChecksumOK(buf, f) || !dataChecksumOK(buf, f) {
				bad++
			}
			if f.GUID != guid && f.Type != FileTypePad {
				kept = append(kept, buf[f.Offset:f.End()])
			}
		}
		return kept, bad, err
	}
	keptBefore, badBefore, err := others(before)
	if err != nil {
		return fail(err)
	}
	keptAfter, badAfter, err := others(after)
	if err != nil {
		return fail(err)
	}
	if badAfter > badBefore {
		return fail(fmt.Errorf("%d file checksum(s) invalid after patching (was %d)", badAfter, badBefore))
	}
	if len(keptBefore) != len(keptAfter) {
		return fail(errors.New("number of files changed"))
	}
	for i := range keptBefore {
		if !bytes.Equal(keptBefore[i], keptAfter[i]) {
			return fail(errors.New("another file in the volume changed"))
		}
	}
	return nil
}
