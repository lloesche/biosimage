//go:build js && wasm

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/lloesche/biosimage/uefi"
)

// collapseAbove is the decompressed size above which a compressed section is sent to
// JavaScript without its children, unless it contains the logo; they follow on expandNode.
const collapseAbove = 1 << 20

// explorer presents the structure tree of an image (uefi.Parse) to the web app's firmware
// explorer. Nodes get IDs as they are sent to JavaScript.
type explorer struct {
	root   *uefi.Node
	views  []view
	ids    map[view]int
	onPath map[*uefi.Node]bool // logo files and everything that contains one
	logo   *uefi.Node          // the first logo file, if any
}

// view is a node as shown in the explorer. The decompressed contents of a compressed
// section get a level of their own (data set), so that they can be viewed as a whole.
type view struct {
	n    *uefi.Node
	data *uefi.Buffer
}

// newExplorer presents root, in which the logo search found logos (none if it found none); that
// search has already decompressed what is needed to reach them.
func newExplorer(root *uefi.Node, logos []*uefi.Logo) *explorer {
	e := &explorer{root: root, ids: map[view]int{}, onPath: map[*uefi.Node]bool{}}
	if len(logos) > 0 {
		e.logo = logos[0].Node
	}
	e.mark(e.root)
	return e
}

// mark records the logo files below n and their ancestors.
func (e *explorer) mark(n *uefi.Node) {
	n.Walk(func(c *uefi.Node) bool {
		if c.Kind == uefi.KindFile && c.File.GUID == uefi.LogoFileGUID {
			for _, p := range c.Path() {
				e.onPath[p] = true
			}
		}
		return true
	})
}

// logoPath returns the IDs from the root to the first logo file.
func (e *explorer) logoPath() []int {
	if e.logo == nil {
		return nil
	}
	var ids []int
	path := e.logo.Path()
	for i, n := range path {
		ids = append(ids, e.ids[view{n: n}])
		if i+1 < len(path) && path[i+1].Buf != n.Buf {
			ids = append(ids, e.ids[view{n: n, data: path[i+1].Buf}])
		}
	}
	return ids
}

func (e *explorer) id(v view) int {
	id, ok := e.ids[v]
	if !ok {
		id = len(e.views)
		e.views = append(e.views, v)
		e.ids[v] = id
	}
	return id
}

func (e *explorer) view(id int) (view, error) {
	if id < 0 || id >= len(e.views) {
		return view{}, fmt.Errorf("no node %d", id)
	}
	return e.views[id], nil
}

// expand decompresses a lazy section and returns it with its children.
func (e *explorer) expand(id int) (map[string]any, error) {
	v, err := e.view(id)
	if err != nil {
		return nil, err
	}
	if v.data == nil && v.n.Lazy() {
		if err := v.n.Expand(); err != nil {
			return nil, err
		}
		e.mark(v.n)
	}
	return e.nodeJS(v, true), nil
}

func (e *explorer) bytes(id, off, n int) []byte {
	v, err := e.view(id)
	if err != nil {
		return nil
	}
	b := v.n.Bytes()
	if v.data != nil {
		b = v.data.Data
	}
	if off < 0 || off >= len(b) {
		return nil
	}
	return bytes.Clone(b[off:min(off+n, len(b))])
}

// children returns what the explorer shows inside v.
func (e *explorer) children(v view) []view {
	n := v.n
	if v.data == nil && len(n.Children) > 0 && n.Children[0].Buf != n.Buf {
		return []view{{n: n, data: n.Children[0].Buf}}
	}
	out := make([]view, len(n.Children))
	for i, c := range n.Children {
		out[i] = view{n: c}
	}
	return out
}

// collapsed reports whether v is sent without its children: lazy sections, and large
// decompressed sections that do not contain the logo.
func (e *explorer) collapsed(v view) bool {
	if v.data != nil {
		return false
	}
	if v.n.Lazy() {
		return true
	}
	h, ok := v.n.LZMAHeader()
	return ok && h.Size > collapseAbove && !e.onPath[v.n]
}

func (e *explorer) nodeJS(v view, open bool) map[string]any {
	d := describe(v)
	lazy := !open && e.collapsed(v)
	children := make([]any, 0)
	if !lazy {
		for _, c := range e.children(v) {
			children = append(children, e.nodeJS(c, false))
		}
	}
	info := make([]any, len(d.info))
	for i, kv := range d.info {
		info[i] = []any{kv[0], kv[1]}
	}
	logo := v.data == nil && v.n.Kind == uefi.KindFile && v.n.File.GUID == uefi.LogoFileGUID
	offset, size := v.n.Offset, v.n.Size
	if v.data != nil {
		offset, size = 0, len(v.data.Data)
	}
	return map[string]any{
		"id": e.id(v), "kind": d.kind, "name": d.name, "type": d.typ, "guid": d.guid,
		"offset": offset, "size": size, "decompressed": v.data != nil || v.n.Decompressed(), "info": info,
		"logo": logo, "hasLogo": e.onPath[v.n], "image": d.image, "imageOffset": d.imageOffset,
		"lazy": lazy, "children": children,
	}
}

// description is how a node is presented.
type description struct {
	kind, name, typ, guid string
	info                  [][2]string
	image                 string // format of the image the node holds, if any
	imageOffset           int
}

func (d *description) add(k, v string) { d.info = append(d.info, [2]string{k, v}) }

func describe(v view) description {
	n := v.n
	var d description
	switch {
	case v.data != nil:
		d = description{kind: "section", name: "Decompressed data", typ: "decompressed"}
		d.add("Size", fmt.Sprintf("%d bytes", len(v.data.Data)))
		d.add("Note", "offsets inside are relative to the decompressed data")
		return d
	case n.Kind == uefi.KindImage:
		d = description{kind: "image", name: "Firmware image"}
		if c := n.Capsule; c != nil {
			d.add("Format", c.Kind)
			d.add("Signed", map[bool]string{true: "yes", false: "no"}[c.Signed])
		}
		vols := 0
		for _, c := range n.Children {
			if c.Kind == uefi.KindVolume {
				vols++
			}
		}
		d.add("Size", fmt.Sprintf("%d bytes", n.Size))
		d.add("Volumes", fmt.Sprintf("%d top-level", vols))
	case n.Kind == uefi.KindCapsule:
		d = description{kind: "capsule", name: "Capsule header", typ: n.Capsule.Kind}
	case n.Kind == uefi.KindRegion:
		d = description{kind: "region", name: "Data", typ: "non-volume data"}
		switch n.RegionContents() {
		case "erased":
			d.name, d.typ = "Empty", "erased"
		case "Intel flash descriptor":
			d.name, d.typ = "Intel flash descriptor region", "flash descriptor"
		}
	case n.Kind == uefi.KindVolume:
		d = describeVolume(n)
	case n.Kind == uefi.KindFree:
		d = description{kind: "free", name: "Free space", typ: "free"}
		if erase := n.Parent.Volume.EraseByte(); bytes.Count(n.Bytes(), []byte{erase}) != n.Size {
			d.add("Warning", "not erased")
		}
	case n.Kind == uefi.KindFile:
		d = describeFile(n)
	default:
		d = describeSection(n)
	}
	if err := n.Err(); err != nil {
		d.add("Error", err.Error())
	}
	return d
}

func describeVolume(n *uefi.Node) description {
	v := n.Volume
	fs := "FFS2"
	if v.IsFFS3() {
		fs = "FFS3"
	}
	d := description{kind: "volume", name: "Firmware volume", typ: fs + " volume", guid: v.FileSystem.String()}
	d.add("File system", fmt.Sprintf("%s (%s)", fs, v.FileSystem))
	d.add("Attributes", fmt.Sprintf("%#08x, erase byte %#02x", v.Attributes, v.EraseByte()))
	d.add("Header", fmt.Sprintf("%d bytes, checksum valid", v.DataOffset-v.Offset))
	if name, ok := n.VolumeName(); ok {
		d.add("Volume name", name.String())
		d.name = "Volume " + name.String()[:8]
	}
	if n.Err() == nil {
		files, free := 0, 0
		for _, c := range n.Children {
			switch c.Kind {
			case uefi.KindFile:
				files++
			case uefi.KindFree:
				free = c.Size
			}
		}
		d.add("Files", fmt.Sprintf("%d, %d bytes free", files, free))
	}
	return d
}

func describeFile(n *uefi.Node) description {
	f := n.File
	d := description{kind: "file", name: f.GUID.String(), typ: uefi.FileTypeName(f.Type), guid: f.GUID.String()}
	valid := func(ok bool) string { return map[bool]string{true: "valid", false: "INVALID"}[ok] }
	header, data := n.Checksums()
	d.add("GUID", f.GUID.String())
	d.add("Type", fmt.Sprintf("%s (%#02x)", d.typ, f.Type))
	d.add("Attributes", fmt.Sprintf("%#02x", f.Attributes))
	d.add("Header checksum", valid(header))
	if f.HasDataChecksum() {
		d.add("Data checksum", valid(data))
	}
	if a := f.Alignment(); a > 1 {
		d.add("Data alignment", fmt.Sprintf("%d bytes", a))
	}
	if name, ok := uefi.KnownFileName(f.GUID); ok {
		d.name = name
	}
	switch {
	case f.GUID == uefi.LogoFileGUID:
		d.name = "Boot logo"
	case f.Type == uefi.FileTypePad:
		d.kind, d.name = "pad", "Pad file"
	default:
		if ui := n.UIName(); ui != "" {
			d.name = ui
		}
	}
	if info, off, ok := n.Image(); ok && len(n.Children) == 0 {
		d.image, d.imageOffset = info.Format, off
		d.add("Image", fmt.Sprintf("%s %dx%d, %s", info.Format, info.Width, info.Height, info.Detail))
	}
	return d
}

func describeSection(n *uefi.Node) description {
	s := n.Section
	d := description{kind: "section", name: uefi.SectionTypeName(s.Type), typ: fmt.Sprintf("section %#02x", s.Type)}
	d.add("Type", fmt.Sprintf("%s (%#02x)", d.name, s.Type))
	d.add("Header", fmt.Sprintf("%d bytes", s.HeaderSize))
	body := s.Body()
	switch s.Type {
	case uefi.SectionUI:
		d.add("Name", s.UIName())
		d.name = "Name: " + s.UIName()
	case uefi.SectionVersion:
		if len(body) >= 2 {
			d.add("Build", fmt.Sprint(binary.LittleEndian.Uint16(body)))
			d.add("Version", uefi.UTF16String(body[2:]))
		}
	case uefi.SectionRaw:
		if info, off, ok := n.Image(); ok {
			d.image, d.imageOffset = info.Format, off
			d.name = fmt.Sprintf("Image: %s %d×%d", info.Format, info.Width, info.Height)
			d.add("Image", fmt.Sprintf("%s %dx%d, %s", info.Format, info.Width, info.Height, info.Detail))
		}
	case uefi.SectionCompression:
		if len(body) >= 5 {
			d.add("Uncompressed size", fmt.Sprintf("%d bytes", binary.LittleEndian.Uint32(body)))
			d.add("Compression", map[byte]string{0: "none", 1: "EFI standard (Tiano)"}[body[4]])
		}
	case uefi.SectionGUIDDefined:
		g, off, attrs, ok := n.GUIDDefinition()
		if !ok {
			break
		}
		name, known := uefi.GUIDName(g)
		if !known {
			name = g.String()
		}
		d.name, d.guid = "GUID-defined: "+name, g.String()
		d.add("Definition", g.String())
		d.add("Data offset", fmt.Sprintf("%#x", off))
		d.add("Attributes", fmt.Sprintf("%#04x", attrs))
		if h, ok := n.LZMAHeader(); ok {
			d.add("LZMA", fmt.Sprintf("props %#02x, dictionary %d KiB", h.Props, h.Dict>>10))
			d.add("Compressed", fmt.Sprintf("%d bytes", len(s.Raw)-off))
			d.add("Uncompressed", fmt.Sprintf("%d bytes", h.Size))
		}
	}
	return d
}
