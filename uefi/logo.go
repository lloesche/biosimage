package uefi

import (
	"bytes"
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

// Logo is a logo file found in an image.
type Logo struct {
	Node     *Node // the logo file in the image's tree
	File     File
	Volume   *Volume
	Encoding Encoding
	Detail   string // human-readable encapsulation info for EncodingUnsupported
	Image    []byte // nil when the encoding cannot be decoded
	Name     string // UI section name, if any
	// Capacity is the largest replacement file (header included) that fits its volume.
	Capacity int

	image *Node // node holding the image: raw section, raw file or undecodable encapsulation
}

// FindLogos returns every file with the given GUID in buf.
func FindLogos(buf []byte, guid GUID) ([]*Logo, error) {
	return Parse(buf).FindLogos(guid)
}

// FindLogos returns every file with the given GUID below n. Large compressed sections are
// only unpacked when there is no such file elsewhere (MSI keeps the logo only in its
// compressed main volume on some boards).
func (n *Node) FindLogos(guid GUID) ([]*Logo, error) {
	logos, err := n.logoFiles(guid)
	if err == nil && len(logos) == 0 {
		n.expandAll()
		logos, err = n.logoFiles(guid)
	}
	if err != nil {
		return nil, err
	}
	if len(logos) == 0 {
		return nil, ErrLogoNotFound
	}
	return logos, nil
}

func (n *Node) logoFiles(guid GUID) ([]*Logo, error) {
	var logos []*Logo
	var err error
	n.Walk(func(c *Node) bool {
		if err != nil || c.Kind != KindFile || c.File.GUID != guid {
			return err == nil
		}
		var l *Logo
		if l, err = newLogo(c); err != nil {
			err = fmt.Errorf("logo file at %#x: %w", c.Offset, err)
		} else {
			logos = append(logos, l)
		}
		return false
	})
	return logos, err
}

func newLogo(n *Node) (*Logo, error) {
	vol := n.Parent
	slot, err := newVolumeSlot(n.Buf.Data, vol.Volume, n.File)
	if err != nil {
		return nil, err
	}
	l := &Logo{Node: n, File: n.File, Volume: vol.Volume, Capacity: slot.capacity()}
	if n.File.Type == FileTypeRaw {
		l.Encoding, l.image, l.Image = EncodingRawFile, n, bytes.Clone(n.Bytes()[n.File.HeaderSize:])
		return l, nil
	}
	if n.err != nil {
		return nil, n.err
	}
	l.Name = sectionName(n.Children, l.Name)
	if raw := firstSection(n.Children, SectionRaw); raw != nil {
		l.Encoding, l.image, l.Image = EncodingRawSection, raw, bytes.Clone(raw.Section.Body())
		return l, nil
	}
	for _, s := range n.Children {
		if s.Section.Type != SectionCompression && s.Section.Type != SectionGUIDDefined {
			continue
		}
		l.Encoding, l.image = EncodingUnsupported, s
		if s.Section.Type == SectionCompression {
			l.Detail = "EFI_SECTION_COMPRESSION"
			return l, nil
		}
		g, _, _, ok := s.GUIDDefinition()
		if !ok {
			return nil, errors.New("bad GUID-defined section")
		}
		if g != LZMASectionGUID {
			l.Detail = "GUID-defined section " + g.String()
			if name, ok := guidNames[g]; ok {
				l.Detail += " (" + name + ")"
			}
			return l, nil
		}
		if err := s.Expand(); err != nil {
			return nil, err
		}
		raw := firstSection(s.Children, SectionRaw)
		if raw == nil {
			return nil, errors.New("LZMA section contains no raw image section")
		}
		l.Encoding, l.image, l.Image = EncodingLZMA, raw, bytes.Clone(raw.Section.Body())
		l.Name = sectionName(s.Children, l.Name)
		return l, nil
	}
	return nil, errors.New("file contains no image section")
}

func firstSection(secs []*Node, typ byte) *Node {
	for _, s := range secs {
		if s.Section.Type == typ {
			return s
		}
	}
	return nil
}

// sectionName returns the name from the last UI section in secs, or name if there is none.
func sectionName(secs []*Node, name string) string {
	for _, s := range secs {
		if s.Section.Type == SectionUI {
			name = s.Section.UIName()
		}
	}
	return name
}

// Header returns up to n bytes from the start of the logo file.
func (l *Logo) Header(n int) []byte {
	b := l.Node.Bytes()
	return bytes.Clone(b[:min(n, len(b))])
}

// Compressed reports whether the logo is inside a compressed section, so that its offsets
// are relative to the decompressed data.
func (l *Logo) Compressed() bool { return l.Node.Decompressed() }

// Hop is a file that encloses the logo.
type Hop struct {
	File File
	// Compressed is set when the logo is inside a compressed section of this file.
	Compressed bool
}

// Enclosing returns the files that contain the logo file, outermost first.
func (l *Logo) Enclosing() []Hop {
	var hops []Hop
	path := l.Node.Path()
	for i, n := range path[:len(path)-1] {
		if n.Kind != KindFile {
			continue
		}
		hop := Hop{File: n.File}
		for _, m := range path[i+1:] {
			if m.Kind == KindFile {
				break
			}
			hop.Compressed = hop.Compressed || m.Buf != n.Buf
		}
		hops = append(hops, hop)
	}
	return hops
}

// Storage describes how the image is stored, e.g. "LZMA section → RAW (Logo.bmp)".
func (l *Logo) Storage() string {
	name := ""
	if l.Name != "" {
		name = " (" + l.Name + ")"
	}
	switch l.Encoding {
	case EncodingRawFile:
		return "RAW file"
	case EncodingRawSection:
		return "RAW section" + name
	case EncodingLZMA:
		return "LZMA section → RAW" + name
	}
	return l.Detail
}
