package uefi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	fvSignature        = "_FVH"
	fvSignatureOffset  = 40
	fvHeaderMinSize    = 56
	fvb2ErasePolarity  = 0x00000800
	fvExtHeaderMinSize = 20
)

// Volume is a firmware volume (EFI_FIRMWARE_VOLUME_HEADER) inside an image.
// All offsets are absolute offsets into the image buffer.
type Volume struct {
	Offset     int
	Length     int
	FileSystem GUID
	Attributes uint32
	DataOffset int // first file
}

// End returns the offset just past the volume.
func (v *Volume) End() int { return v.Offset + v.Length }

// EraseByte is the value of erased flash (and free space) in this volume.
func (v *Volume) EraseByte() byte {
	if v.Attributes&fvb2ErasePolarity != 0 {
		return 0xFF
	}
	return 0x00
}

// IsFFS3 reports whether the volume uses the FFS3 file system (large files allowed).
func (v *Volume) IsFFS3() bool { return v.FileSystem == FFS3GUID }

// alignUp rounds an absolute offset up to the next 8-byte boundary relative to the volume start.
func (v *Volume) alignUp(abs int) int { return v.Offset + alignUp(abs-v.Offset, 8) }

// ParseVolume validates and parses the volume header at off.
func ParseVolume(buf []byte, off int) (*Volume, error) {
	if off < 0 || off+fvHeaderMinSize > len(buf) {
		return nil, errors.New("truncated volume header")
	}
	h := buf[off:]
	if string(h[fvSignatureOffset:fvSignatureOffset+4]) != fvSignature {
		return nil, errors.New("missing _FVH signature")
	}
	var fs GUID
	copy(fs[:], h[16:32])
	if fs != FFS2GUID && fs != FFS3GUID {
		return nil, fmt.Errorf("unsupported file system %s", fs)
	}
	length64 := binary.LittleEndian.Uint64(h[32:40])
	if length64 > uint64(len(h)) {
		return nil, fmt.Errorf("volume length %#x exceeds image", length64)
	}
	length := int(length64)
	hdrLen := int(binary.LittleEndian.Uint16(h[48:50]))
	if hdrLen < fvHeaderMinSize || hdrLen%2 != 0 || hdrLen > length {
		return nil, fmt.Errorf("bad header length %#x", hdrLen)
	}
	var sum uint16
	for i := 0; i < hdrLen; i += 2 {
		sum += binary.LittleEndian.Uint16(h[i:])
	}
	if sum != 0 {
		return nil, errors.New("header checksum mismatch")
	}
	data := hdrLen
	if ext := int(binary.LittleEndian.Uint16(h[52:54])); ext != 0 {
		if ext+fvExtHeaderMinSize > length {
			return nil, errors.New("bad extended header offset")
		}
		extSize := int(binary.LittleEndian.Uint32(h[ext+16:]))
		if extSize < fvExtHeaderMinSize || ext+extSize > length {
			return nil, errors.New("bad extended header size")
		}
		data = ext + extSize
	}
	return &Volume{
		Offset:     off,
		Length:     length,
		FileSystem: fs,
		Attributes: binary.LittleEndian.Uint32(h[44:48]),
		DataOffset: off + alignUp(data, 8),
	}, nil
}

// FindVolumes returns the top-level firmware volumes in buf. Signatures that do not
// belong to a valid header (e.g. inside compressed data) are skipped, as are volumes
// nested inside another volume.
func FindVolumes(buf []byte) []*Volume {
	var vols []*Volume
	for pos := 0; ; {
		i := bytes.Index(buf[pos:], []byte(fvSignature))
		if i < 0 {
			return vols
		}
		sig := pos + i
		if v, err := ParseVolume(buf, sig-fvSignatureOffset); err == nil {
			vols = append(vols, v)
			pos = v.End()
			continue
		}
		pos = sig + 1
	}
}
