package uefi

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Kind identifies what a tree node is.
type Kind int

const (
	KindImage   Kind = iota // the whole firmware image
	KindCapsule             // capsule header in front of the flash image
	KindRegion              // data between top-level volumes
	KindVolume              // firmware volume
	KindFile                // FFS file
	KindSection             // file section
	KindFree                // unused space at the end of a volume
)

func (k Kind) String() string {
	return [...]string{"image", "capsule", "region", "volume", "file", "section", "free"}[k]
}

// Buffer is the byte slice nodes point into: the image itself, or the decompressed contents
// of a compressed section (its Owner).
type Buffer struct {
	Data  []byte
	Owner *Node
}

// Node is one element of a firmware image's structure. It is the single model of the image
// used for finding, patching and exploring.
type Node struct {
	Kind     Kind
	Buf      *Buffer
	Offset   int // within Buf.Data
	Size     int
	Parent   *Node
	Children []*Node

	Capsule *Capsule // KindImage and KindCapsule, if the image is a capsule
	Volume  *Volume  // KindVolume
	File    File     // KindFile
	Section Section  // KindSection

	lazy bool  // compressed section that has not been decompressed yet
	err  error // why the node's contents could not be parsed
}

// lazyThreshold is the decompressed size above which compressed sections are only unpacked
// by Expand; sections of logo files are always unpacked.
const lazyThreshold = 1 << 20

// Parse builds the structure tree of img. Nodes refer to img directly, so changes to img
// show through.
func Parse(img []byte) *Node {
	buf := &Buffer{Data: img}
	root := newNode(nil, KindImage, buf, 0, len(img))
	root.Capsule = DetectCapsule(img)
	start := 0
	if c := root.Capsule; c != nil && c.HeaderSize > 0 {
		newNode(root, KindCapsule, buf, 0, c.HeaderSize).Capsule = c
		start = c.HeaderSize
	}
	for _, v := range FindVolumes(img) {
		if v.Offset > start {
			newNode(root, KindRegion, buf, start, v.Offset-start)
		}
		parseVolume(root, buf, v)
		start = v.End()
	}
	if start < len(img) {
		newNode(root, KindRegion, buf, start, len(img)-start)
	}
	return root
}

func newNode(parent *Node, kind Kind, buf *Buffer, off, size int) *Node {
	n := &Node{Kind: kind, Buf: buf, Offset: off, Size: size, Parent: parent}
	if parent != nil {
		parent.Children = append(parent.Children, n)
	}
	return n
}

func parseVolume(parent *Node, buf *Buffer, v *Volume) {
	n := newNode(parent, KindVolume, buf, v.Offset, v.Length)
	n.Volume = v
	files, used, err := v.Files(buf.Data)
	if err != nil {
		n.err = err
		return
	}
	for _, f := range files {
		parseFile(n, buf, f)
	}
	if used < v.End() {
		newNode(n, KindFree, buf, used, v.End()-used)
	}
}

func parseFile(parent *Node, buf *Buffer, f File) {
	n := newNode(parent, KindFile, buf, f.Offset, f.Size)
	n.File = f
	switch f.Type {
	case FileTypePad:
	case FileTypeRaw:
		// AMI keeps small volumes (e.g. the logo's) inside RAW files.
		parseNestedVolume(n, buf, f.DataOffset(), f.End())
	default:
		parseSections(n, buf, f.DataOffset(), buf.Data[f.DataOffset():f.End()])
	}
}

func parseNestedVolume(parent *Node, buf *Buffer, off, end int) {
	if v, err := ParseVolume(buf.Data, off); err == nil && v.End() <= end {
		parseVolume(parent, buf, v)
	}
}

func parseSections(parent *Node, buf *Buffer, base int, data []byte) {
	secs, err := ParseSections(data)
	if err != nil {
		parent.err = err
		return
	}
	for _, s := range secs {
		parseSection(parent, buf, base, s)
	}
}

func parseSection(parent *Node, buf *Buffer, base int, s Section) {
	n := newNode(parent, KindSection, buf, base+s.Offset, len(s.Raw))
	n.Section = s
	body := n.Offset + s.HeaderSize
	switch s.Type {
	case SectionRaw, SectionFVImage:
		parseNestedVolume(n, buf, body, n.Offset+n.Size)
	case SectionCompression:
		if b := s.Body(); len(b) >= 5 && b[4] == 0 { // "not compressed"
			parseSections(n, buf, body+5, b[5:])
		}
	case SectionGUIDDefined:
		g, off, _, err := s.guidedHeader()
		switch {
		case err != nil:
			n.err = err
		case g == CRC32SectionGUID:
			parseSections(n, buf, n.Offset+off, s.Raw[off:])
		case g == LZMASectionGUID:
			n.lazy = true
			if h, ok := n.LZMAHeader(); ok && (h.Size <= lazyThreshold || n.inLogoFile()) {
				n.Expand()
			}
		}
	}
}

// Expand decompresses a lazy compressed section and parses its contents into a new buffer.
func (n *Node) Expand() error {
	if !n.lazy {
		return n.err
	}
	n.lazy = false
	_, off, _, _ := n.Section.guidedHeader()
	data, err := lzmaDecompress(n.Section.Raw[off:])
	if err != nil {
		n.err = fmt.Errorf("LZMA section: %w", err)
		return n.err
	}
	parseSections(n, &Buffer{Data: data, Owner: n}, 0, data)
	return n.err
}

// Bytes returns the node's bytes (a view into its buffer).
func (n *Node) Bytes() []byte { return n.Buf.Data[n.Offset : n.Offset+n.Size] }

// Decompressed reports whether the node lives inside decompressed data, so that its offset
// is relative to that data rather than to the image.
func (n *Node) Decompressed() bool { return n.Buf.Owner != nil }

// Lazy reports whether the node is a compressed section that Expand has not unpacked yet.
func (n *Node) Lazy() bool { return n.lazy }

// Err returns why the node's contents could not be parsed, if they could not.
func (n *Node) Err() error { return n.err }

// Walk calls fn for n and its descendants in order; returning false skips the children.
func (n *Node) Walk(fn func(*Node) bool) {
	if fn(n) {
		for _, c := range n.Children {
			c.Walk(fn)
		}
	}
}

// Path returns the nodes from the root to n.
func (n *Node) Path() []*Node {
	var path []*Node
	for p := n; p != nil; p = p.Parent {
		path = append([]*Node{p}, path...)
	}
	return path
}

// expandAll unpacks every lazy section below n, including ones that appear in unpacked data.
func (n *Node) expandAll() {
	for {
		var lazy []*Node
		n.Walk(func(c *Node) bool {
			if c.lazy {
				lazy = append(lazy, c)
			}
			return true
		})
		if len(lazy) == 0 {
			return
		}
		for _, c := range lazy {
			c.Expand()
		}
	}
}

// file returns the nearest enclosing file, or nil.
func (n *Node) file() *Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Kind == KindFile {
			return p
		}
	}
	return nil
}

func (n *Node) inLogoFile() bool {
	f := n.file()
	return f != nil && f.File.GUID == LogoFileGUID
}

// ---------- typed views used by the explorer ----------

// LZMAInfo is the header of an LZMA stream.
type LZMAInfo struct {
	Props byte
	Dict  uint32
	Size  uint64 // uncompressed
}

// LZMAHeader returns the header of an LZMA GUID-defined section.
func (n *Node) LZMAHeader() (LZMAInfo, bool) {
	g, off, _, err := n.Section.guidedHeader()
	if n.Kind != KindSection || err != nil || g != LZMASectionGUID || len(n.Section.Raw)-off < lzmaHeaderSize {
		return LZMAInfo{}, false
	}
	p := n.Section.Raw[off:]
	return LZMAInfo{Props: p[0], Dict: binary.LittleEndian.Uint32(p[1:]), Size: binary.LittleEndian.Uint64(p[5:])}, true
}

// GUIDDefinition returns the definition GUID, data offset and attributes of a GUID-defined
// section.
func (n *Node) GUIDDefinition() (GUID, int, uint16, bool) {
	if n.Kind != KindSection || n.Section.Type != SectionGUIDDefined {
		return GUID{}, 0, 0, false
	}
	g, off, attrs, err := n.Section.guidedHeader()
	return g, off, attrs, err == nil
}

// Checksums reports whether a file's header checksum and, if it has one, its data checksum
// are valid.
func (n *Node) Checksums() (header, data bool) {
	return headerChecksumOK(n.Buf.Data, n.File), dataChecksumOK(n.Buf.Data, n.File)
}

// Image returns the image stored in a raw section or raw file, and its offset in the node.
func (n *Node) Image() (*ImageInfo, int, bool) {
	off := -1
	switch {
	case n.Kind == KindSection && n.Section.Type == SectionRaw:
		off = n.Section.HeaderSize
	case n.Kind == KindFile && n.File.Type == FileTypeRaw:
		off = n.File.HeaderSize
	}
	if off < 0 {
		return nil, 0, false
	}
	info, err := InspectImage(n.Bytes()[off:])
	return info, off, err == nil
}

// UIName returns the name from a file's user interface section, if it has one.
func (n *Node) UIName() string {
	name := ""
	n.Walk(func(c *Node) bool {
		if name != "" || (c != n && c.Kind == KindFile) {
			return false
		}
		if c.Kind == KindSection && c.Section.Type == SectionUI {
			name = c.Section.UIName()
		}
		return true
	})
	return name
}

// VolumeName returns the name GUID from a volume's extended header, if it has one.
func (n *Node) VolumeName() (GUID, bool) {
	var g GUID
	if n.Kind != KindVolume {
		return g, false
	}
	ext := int(binary.LittleEndian.Uint16(n.Buf.Data[n.Offset+52:]))
	if ext == 0 {
		return g, false
	}
	copy(g[:], n.Buf.Data[n.Offset+ext:])
	return g, true
}

// RegionContents describes data between volumes: "erased", "Intel flash descriptor" or "data".
func (n *Node) RegionContents() string {
	b := n.Bytes()
	switch {
	case isErased(b, 0xFF) || isErased(b, 0x00):
		return "erased"
	case len(b) > 0x14 && binary.LittleEndian.Uint32(b[0x10:]) == 0x0FF0A55A:
		return "Intel flash descriptor"
	}
	return "data"
}

var errNotRebuildable = errors.New("cannot rebuild this encapsulation")
