package uefi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Node is one element of a firmware image's structure, as shown by an explorer: the image
// itself, a capsule header, a firmware volume, an FFS file, a section, or unused space.
type Node struct {
	ID     int
	Kind   string // image, capsule, region, volume, file, pad, section, free
	Name   string
	Type   string
	GUID   string
	Offset int // within Buffer
	Size   int
	// Buffer is 0 for the image itself; other values are decompressed sections.
	Buffer int
	Info   [][2]string
	// Logo marks the logo file; HasLogo marks every node on the path to it.
	Logo    bool
	HasLogo bool
	// Image is set for raw sections and files that hold a BMP, PNG, JPEG or GIF image,
	// which starts ImageOffset bytes into the node.
	Image       string
	ImageOffset int
	// Lazy nodes are large compressed sections; Expand decompresses them.
	Lazy     bool
	Children []*Node

	parent *Node
	expand func() error
}

// Explorer builds and expands the structure tree of a firmware image.
type Explorer struct {
	bufs  [][]byte
	nodes []*Node
	logo  GUID
	// decompressed holds container data already unpacked by the logo search, keyed by the
	// offset of the file holding it, so opening it does not decompress it again.
	decompressed map[int][]byte
	// containers are the offsets of files whose compressed volume holds a logo.
	containers map[int]bool
}

// lazyThreshold is the decompressed size above which compressed sections are only
// unpacked on request.
const lazyThreshold = 1 << 20

// NewExplorer parses img and returns an explorer for it.
func NewExplorer(img []byte) *Explorer {
	e := &Explorer{bufs: [][]byte{img}, logo: LogoFileGUID, decompressed: map[int][]byte{}, containers: map[int]bool{}}
	if logos, err := FindLogos(img, e.logo); err == nil {
		for _, l := range logos {
			if c := l.Container; c != nil && !e.containers[c.File.Offset] {
				e.containers[c.File.Offset] = true
				e.decompressed[c.File.Offset] = c.data
			}
		}
	}
	root := e.node(nil, "image", "Firmware image", 0, 0, len(img))
	if c := DetectCapsule(img); c != nil {
		signed := "no"
		if c.Signed {
			signed = "yes"
		}
		root.Info = append(root.Info, [2]string{"Format", c.Kind}, [2]string{"Signed", signed})
		if c.HeaderSize > 0 {
			n := e.node(root, "capsule", "Capsule header", 0, 0, c.HeaderSize)
			n.Type = c.Kind
		}
	}
	start := 0
	if len(root.Children) > 0 {
		start = root.Children[0].Size
	}
	vols := FindVolumes(img)
	for _, v := range vols {
		if v.Offset > start {
			e.region(root, start, v.Offset)
		}
		e.volume(root, v)
		start = v.End()
	}
	if start < len(img) {
		e.region(root, start, len(img))
	}
	root.Info = append(root.Info, [2]string{"Size", fmt.Sprintf("%d bytes", len(img))},
		[2]string{"Volumes", fmt.Sprintf("%d top-level", len(vols))})
	e.markLogos()
	return e
}

// Root returns the image node.
func (e *Explorer) Root() *Node { return e.nodes[0] }

// Node returns the node with the given ID, or nil.
func (e *Explorer) Node(id int) *Node {
	if id < 0 || id >= len(e.nodes) {
		return nil
	}
	return e.nodes[id]
}

// Expand decompresses a lazy node and builds its children.
func (e *Explorer) Expand(id int) (*Node, error) {
	n := e.Node(id)
	if n == nil {
		return nil, fmt.Errorf("no node %d", id)
	}
	if n.Lazy && n.expand != nil {
		if err := n.expand(); err != nil {
			return nil, err
		}
		n.Lazy, n.expand = false, nil
		e.markLogos()
	}
	return n, nil
}

// Bytes returns up to n bytes of the node, starting at off.
func (e *Explorer) Bytes(id, off, n int) []byte {
	nd := e.Node(id)
	if nd == nil || off < 0 || off >= nd.Size {
		return nil
	}
	buf := e.bufs[nd.Buffer]
	start := nd.Offset + off
	return bytes.Clone(buf[start:min(start+n, nd.Offset+nd.Size, len(buf))])
}

func (e *Explorer) node(parent *Node, kind, name string, buffer, off, size int) *Node {
	n := &Node{ID: len(e.nodes), Kind: kind, Name: name, Buffer: buffer, Offset: off, Size: size, parent: parent}
	e.nodes = append(e.nodes, n)
	if parent != nil {
		parent.Children = append(parent.Children, n)
	}
	return n
}

func (e *Explorer) region(parent *Node, start, end int) {
	buf := e.bufs[0]
	name, typ := "Data", "non-volume data"
	switch {
	case isErased(buf[start:end], 0xFF) || isErased(buf[start:end], 0x00):
		name, typ = "Empty", "erased"
	case end-start > 0x14 && binary.LittleEndian.Uint32(buf[start+0x10:]) == 0x0FF0A55A:
		name, typ = "Intel flash descriptor region", "flash descriptor"
	}
	n := e.node(parent, "region", name, 0, start, end-start)
	n.Type = typ
}

func (e *Explorer) volume(parent *Node, v *Volume) *Node {
	buf := e.bufs[e.bufOf(parent)]
	fs := "FFS2"
	if v.IsFFS3() {
		fs = "FFS3"
	}
	n := e.node(parent, "volume", "Firmware volume", e.bufOf(parent), v.Offset, v.Length)
	n.Type = fs + " volume"
	n.GUID = v.FileSystem.String()
	n.Info = [][2]string{
		{"File system", fmt.Sprintf("%s (%s)", fs, v.FileSystem)},
		{"Attributes", fmt.Sprintf("%#08x, erase byte %#02x", v.Attributes, v.EraseByte())},
		{"Header", fmt.Sprintf("%d bytes, checksum valid", v.DataOffset-v.Offset)},
	}
	if ext := int(binary.LittleEndian.Uint16(buf[v.Offset+52:])); ext != 0 {
		var name GUID
		copy(name[:], buf[v.Offset+ext:])
		n.Info = append(n.Info, [2]string{"Volume name", name.String()})
		n.Name = "Volume " + name.String()[:8]
	}
	files, used, err := v.Files(buf)
	if err != nil {
		n.Info = append(n.Info, [2]string{"Error", err.Error()})
		return n
	}
	for _, f := range files {
		e.file(n, v, f)
	}
	if used < v.End() {
		free := e.node(n, "free", "Free space", n.Buffer, used, v.End()-used)
		free.Type = "free"
		if !isErased(buf[used:v.End()], v.EraseByte()) {
			free.Info = append(free.Info, [2]string{"Warning", "not erased"})
		}
	}
	n.Info = append(n.Info, [2]string{"Files", fmt.Sprintf("%d, %d bytes free", len(files), v.End()-used)})
	return n
}

func (e *Explorer) bufOf(n *Node) int {
	if n == nil {
		return 0
	}
	return n.Buffer
}

func (e *Explorer) file(parent *Node, v *Volume, f File) {
	buf := e.bufs[parent.Buffer]
	kind := "file"
	if f.Type == FileTypePad {
		kind = "pad"
	}
	n := e.node(parent, kind, f.GUID.String(), parent.Buffer, f.Offset, f.Size)
	n.GUID = f.GUID.String()
	n.Type = fileTypeName(f.Type)
	ok := func(b bool) string {
		if b {
			return "valid"
		}
		return "INVALID"
	}
	n.Info = [][2]string{
		{"GUID", f.GUID.String()},
		{"Type", fmt.Sprintf("%s (%#02x)", n.Type, f.Type)},
		{"Attributes", fmt.Sprintf("%#02x", f.Attributes)},
		{"Header checksum", ok(headerChecksumOK(buf, f))},
	}
	if f.Attributes&ffsAttribChecksum != 0 {
		n.Info = append(n.Info, [2]string{"Data checksum", ok(dataChecksumOK(buf, f))})
	}
	if a := dataAlignment(f.Attributes); a > 1 {
		n.Info = append(n.Info, [2]string{"Data alignment", fmt.Sprintf("%d bytes", a)})
	}
	if f.GUID == e.logo {
		n.Logo = true
		n.Name = "Boot logo"
	} else if name, ok := knownFiles[f.GUID]; ok {
		n.Name = name
	}
	switch f.Type {
	case FileTypePad:
		n.Name = "Pad file"
	case FileTypeRaw:
		if nv, err := ParseVolume(buf, f.DataOffset()); err == nil && nv.End() <= f.End() {
			e.volume(n, nv)
		} else if info, err := InspectImage(buf[f.DataOffset():f.End()]); err == nil {
			n.Image, n.ImageOffset = info.Format, f.HeaderSize
			n.Info = append(n.Info, [2]string{"Image", fmt.Sprintf("%s %dx%d, %s", info.Format, info.Width, info.Height, info.Detail)})
		}
	default:
		e.sections(n, f.DataOffset(), buf[f.DataOffset():f.End()])
	}
	if ui := findUIName(n); ui != "" && !n.Logo {
		n.Name = ui
	}
}

func (e *Explorer) sections(parent *Node, base int, data []byte) {
	secs, err := ParseSections(data)
	if err != nil {
		parent.Info = append(parent.Info, [2]string{"Error", err.Error()})
		return
	}
	for _, s := range secs {
		e.section(parent, base, s)
	}
}

func (e *Explorer) section(parent *Node, base int, s Section) {
	n := e.node(parent, "section", sectionTypeName(s.Type), parent.Buffer, base+s.Offset, len(s.Raw))
	n.Type = fmt.Sprintf("section %#02x", s.Type)
	n.Info = [][2]string{{"Type", fmt.Sprintf("%s (%#02x)", n.Name, s.Type)}, {"Header", fmt.Sprintf("%d bytes", s.HeaderSize)}}
	body := s.Body()
	bodyOff := n.Offset + s.HeaderSize
	switch s.Type {
	case SectionUI:
		n.Info = append(n.Info, [2]string{"Name", s.UIName()})
		n.Name = "Name: " + s.UIName()
	case 0x14: // version
		if len(body) >= 2 {
			n.Info = append(n.Info, [2]string{"Build", fmt.Sprint(binary.LittleEndian.Uint16(body))},
				[2]string{"Version", utf16String(body[2:])})
		}
	case SectionRaw:
		if info, err := InspectImage(body); err == nil {
			n.Image, n.ImageOffset = info.Format, s.HeaderSize
			n.Name = fmt.Sprintf("Image: %s %d×%d", info.Format, info.Width, info.Height)
			n.Info = append(n.Info, [2]string{"Image", fmt.Sprintf("%s %dx%d, %s", info.Format, info.Width, info.Height, info.Detail)})
		} else if nv, err := ParseVolume(e.bufs[n.Buffer], bodyOff); err == nil && nv.End() <= n.Offset+n.Size {
			e.volume(n, nv)
		}
	case SectionFVImage:
		if nv, err := ParseVolume(e.bufs[n.Buffer], bodyOff); err == nil && nv.End() <= n.Offset+n.Size {
			e.volume(n, nv)
		}
	case SectionCompression:
		if len(body) >= 5 {
			n.Info = append(n.Info, [2]string{"Uncompressed size", fmt.Sprintf("%d bytes", binary.LittleEndian.Uint32(body))},
				[2]string{"Compression", map[byte]string{0: "none", 1: "EFI standard (Tiano)"}[body[4]]})
			if body[4] == 0 {
				e.sections(n, bodyOff+5, body[5:])
			}
		}
	case SectionGUIDDefined:
		g, off, attrs, err := s.guidedHeader()
		if err != nil {
			break
		}
		name := g.String()
		if gn, ok := guidNames[g]; ok {
			name = gn
		}
		n.Name = "GUID-defined: " + name
		n.GUID = g.String()
		n.Info = append(n.Info, [2]string{"Definition", g.String()}, [2]string{"Data offset", fmt.Sprintf("%#x", off)},
			[2]string{"Attributes", fmt.Sprintf("%#04x", attrs)})
		if g != LZMASectionGUID || len(s.Raw)-off < lzmaHeaderSize {
			if g == CRC32SectionGUID {
				e.sections(n, n.Offset+off, s.Raw[off:])
			}
			break
		}
		payload := s.Raw[off:]
		size := binary.LittleEndian.Uint64(payload[5:])
		n.Info = append(n.Info, [2]string{"LZMA", fmt.Sprintf("props %#02x, dictionary %d KiB", payload[0], binary.LittleEndian.Uint32(payload[1:])>>10)},
			[2]string{"Compressed", fmt.Sprintf("%d bytes", len(payload))}, [2]string{"Uncompressed", fmt.Sprintf("%d bytes", size)})
		fileOffset := -1
		if parent.Kind == "file" && parent.Buffer == 0 {
			fileOffset = parent.Offset
		}
		n.expand = func() error {
			data, ok := e.decompressed[fileOffset]
			if !ok {
				var err error
				if data, err = lzmaDecompress(payload); err != nil {
					return err
				}
			}
			delete(e.decompressed, fileOffset)
			e.bufs = append(e.bufs, data)
			inner := e.node(n, "section", "Decompressed data", len(e.bufs)-1, 0, len(data))
			inner.Type = "decompressed"
			inner.Info = [][2]string{{"Size", fmt.Sprintf("%d bytes", len(data))}, {"Note", "offsets inside are relative to the decompressed data"}}
			e.sections(inner, 0, data)
			return nil
		}
		if size > lazyThreshold && !inLogoFile(n) {
			n.Lazy = true
		} else if err := n.expand(); err == nil {
			n.expand = nil
		} else {
			n.Info = append(n.Info, [2]string{"Error", err.Error()})
		}
	}
}

// markLogos sets HasLogo on every ancestor of a logo file, and on lazy sections that are
// known to contain one.
func (e *Explorer) markLogos() {
	for _, n := range e.nodes {
		mark := n.Logo || (n.Lazy && n.Buffer == 0 && n.parent != nil && e.containers[n.parent.Offset])
		for p := n; mark && p != nil; p = p.parent {
			p.HasLogo = true
		}
	}
}

// LogoPath returns the IDs from the root to the first logo file, expanding lazy nodes.
func (e *Explorer) LogoPath() []int {
	var find func(n *Node) []int
	find = func(n *Node) []int {
		if n.Logo {
			return []int{n.ID}
		}
		if n.Lazy && n.HasLogo {
			if _, err := e.Expand(n.ID); err != nil {
				return nil
			}
		}
		for _, c := range n.Children {
			if c.HasLogo || c.Lazy {
				if p := find(c); p != nil {
					return append([]int{n.ID}, p...)
				}
			}
		}
		return nil
	}
	return find(e.Root())
}

func findUIName(n *Node) string {
	for _, c := range n.Children {
		if c.Kind != "section" {
			continue
		}
		if name, ok := strings.CutPrefix(c.Name, "Name: "); ok {
			return name
		}
		if name := findUIName(c); name != "" {
			return name
		}
	}
	return ""
}

func utf16String(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

// CRC32SectionGUID is gEfiCrc32GuidedSectionExtractionGuid.
var CRC32SectionGUID = MustParseGUID("FC1BCDB0-7D31-49AA-936A-A4600D9DD083")

var knownFiles = map[GUID]string{
	MustParseGUID("9E21FD93-9C72-4C15-8C4B-E77F1DB2D792"): "Compressed main volume (FV_MAIN)",
	MustParseGUID("414D94AD-998D-47D2-BFCD-4E882241DE32"): "AMI firmware signature",
	MustParseGUID("05CA020B-0FC1-11DC-9011-00173153EBA8"): "Nested volume (AMI)",
}

func fileTypeName(t byte) string {
	names := map[byte]string{
		0x01: "RAW", 0x02: "FREEFORM", 0x03: "SEC_CORE", 0x04: "PEI_CORE", 0x05: "DXE_CORE",
		0x06: "PEIM", 0x07: "DRIVER", 0x08: "COMBINED", 0x09: "APPLICATION", 0x0A: "MM",
		0x0B: "FV_IMAGE", 0x0C: "COMBINED_MM_DXE", 0x0D: "MM_CORE", 0x0E: "MM_STANDALONE",
		0x0F: "MM_CORE_STANDALONE", 0xF0: "PAD",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return fmt.Sprintf("type %#02x", t)
}

func sectionTypeName(t byte) string {
	names := map[byte]string{
		0x01: "Compression", 0x02: "GUID-defined", 0x03: "Disposable", 0x10: "PE32 image",
		0x11: "PIC image", 0x12: "TE image", 0x13: "DXE dependency", 0x14: "Version",
		0x15: "User interface", 0x16: "Compatibility16", 0x17: "Firmware volume image",
		0x18: "Freeform subtype GUID", 0x19: "Raw", 0x1B: "PEI dependency", 0x1C: "MM dependency",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return fmt.Sprintf("Section %#02x", t)
}

// inLogoFile reports whether n is a section of a logo file. Those are always decompressed,
// so the logo can be previewed even when it is large.
func inLogoFile(n *Node) bool {
	for p := n.parent; p != nil; p = p.parent {
		if p.Kind == "file" {
			return p.Logo
		}
	}
	return false
}
