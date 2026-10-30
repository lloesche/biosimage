package uefi

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// AMI Aptio firmware describes the flash areas in a ROM layout table ("ROML"): offset, size,
// type and attributes of each area. Areas with romAreaDXE are published as firmware volumes
// for the DXE phase, where AMITSE looks for the logo in every volume (by file GUID).

// romLayoutSectionGUID is the freeform subtype GUID of the section holding the table.
var romLayoutSectionGUID = MustParseGUID("00781CA1-5DE3-405F-ABB8-379C3C076984")

// mainVolumeGUID names AMI's main firmware volume (FV_MAIN).
var mainVolumeGUID = MustParseGUID("4F1C52D3-D824-4D2A-A2F0-EC40C23C5916")

// romAreaDXE is the attribute of areas published for DXE: it is set on the volumes the logo
// is read from on all tested boards (on ASUS a volume of its own, elsewhere FV_MAIN).
const romAreaDXE = 0x8

// romArea is an entry of the ROM layout table.
type romArea struct {
	Name         GUID // of the firmware volume, if the area is one
	Offset, Size int  // in the BIOS region
	Type         uint32
	Attributes   uint32
}

// romLayout returns the ROM layout table in buf, or nil if there is none.
func romLayout(buf []byte) []romArea {
	i := bytes.Index(buf, append(romLayoutSectionGUID[:], "ROML"...))
	if i < 0 {
		return nil
	}
	h := buf[i+16:]
	if len(h) < 16 {
		return nil
	}
	size, n := int(binary.LittleEndian.Uint32(h[8:])), int(binary.LittleEndian.Uint32(h[12:]))
	if size < 40 || n > 256 || len(h) < 16+n*size {
		return nil
	}
	areas := make([]romArea, n)
	for k := range areas {
		e := h[16+k*size:]
		copy(areas[k].Name[:], e)
		areas[k].Offset = int(binary.LittleEndian.Uint32(e[24:]))
		areas[k].Size = int(binary.LittleEndian.Uint32(e[28:]))
		areas[k].Type = binary.LittleEndian.Uint32(e[32:])
		areas[k].Attributes = binary.LittleEndian.Uint32(e[36:])
	}
	return areas
}

// FreeSpace is a volume a logo file can be moved to (ReplaceOptions.UseFreeSpace).
type FreeSpace struct {
	Volume *Node
	// Capacity is the largest logo file (header included) that fits its free space.
	Capacity int
}

// ErrNoFreeSpace is returned by FreeSpace for images without a suitable volume.
var ErrNoFreeSpace = errors.New("no main volume with free space for the logo")

// FreeSpace returns the main volume (FV_MAIN) of an AMI image if the logo can be moved there:
// the ROM layout must publish it for DXE, and AMITSE's code must read the logo from any volume
// (TSECode). Moving the logo is experimental: this is verified in the firmware code, not on a
// running board.
func (n *Node) FreeSpace() (*FreeSpace, error) {
	return n.freeSpace(n.TSECode())
}

// freeSpace is FreeSpace with the facts c of n's AMITSE code, so that callers who have them need
// not disassemble it again.
func (n *Node) freeSpace(c TSECode) (*FreeSpace, error) {
	if !c.AnyVolumeLogo {
		return nil, ErrNoFreeSpace
	}
	vol := n.layoutVolume(mainVolumeGUID)
	if vol == nil {
		return nil, ErrNoFreeSpace
	}
	return freeSpaceIn(vol)
}

// freeSpaceIn returns the free space after the last file of volume vol, which must be erased.
// It reads the volume's current bytes, so it also measures what is left after files were added.
func freeSpaceIn(vol *Node) (*FreeSpace, error) {
	_, used, err := vol.Volume.Files(vol.Buf.Data)
	if err != nil {
		return nil, err
	}
	pos := alignedFilePos(vol.Volume, used, ffsHeaderSize, 8)
	end := vol.Volume.End()
	for end > pos && vol.Buf.Data[end-1] == vol.Volume.EraseByte() {
		end--
	}
	if end > pos { // not erased: something unknown lives there
		return nil, ErrNoFreeSpace
	}
	return &FreeSpace{Volume: vol, Capacity: vol.Volume.End() - pos}, nil
}

// layoutVolume returns the top-level volume named name that the ROM layout publishes for
// DXE. The layout's offsets are relative to the BIOS region; the volume whose offset differs
// from its area's by the same amount as most other volumes is the one meant.
func (n *Node) layoutVolume(name GUID) *Node {
	areas := romLayout(n.Buf.Data)
	byName := map[GUID][]*Node{}
	for _, c := range n.Children {
		if g, ok := c.VolumeName(); ok {
			byName[g] = append(byName[g], c)
		}
	}
	votes := map[int]int{}
	for _, a := range areas {
		for _, v := range byName[a.Name] {
			votes[v.Offset-a.Offset]++
		}
	}
	base, most := 0, 0
	for d, c := range votes {
		if c > most || c == most && d < base {
			base, most = d, c
		}
	}
	for _, a := range areas {
		if a.Name != name || a.Type != 0 || a.Attributes&romAreaDXE == 0 {
			continue
		}
		for _, v := range byName[name] {
			if v.Offset == base+a.Offset && v.Volume.Length == a.Size {
				return v
			}
		}
	}
	return nil
}
