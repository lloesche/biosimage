package uefi

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// FFS file types.
const (
	FileTypeRaw      = 0x01
	FileTypeFreeform = 0x02
	FileTypeFVImage  = 0x0B
	FileTypePad      = 0xF0
)

const (
	ffsAttribLargeFile      = 0x01
	ffsAttribDataAlignment2 = 0x02
	ffsAttribFixed          = 0x04
	ffsAttribChecksum       = 0x40

	ffsHeaderSize      = 24
	ffsLargeHeaderSize = 32
	ffsMaxSize         = 0xFFFFFF

	// EFI_FILE_HEADER_CONSTRUCTION | EFI_FILE_HEADER_VALID | EFI_FILE_DATA_VALID
	fileStateValid = 0x07
	// IntegrityCheck.Checksum.File when FFS_ATTRIB_CHECKSUM is clear (FFS_FIXED_CHECKSUM).
	fileChecksumFixed = 0xAA
)

// File is an FFS file header located at Offset in the image.
type File struct {
	Offset     int
	Size       int // including the header
	HeaderSize int
	GUID       GUID
	Type       byte
	Attributes byte
}

// DataOffset is the absolute offset of the file contents.
func (f File) DataOffset() int { return f.Offset + f.HeaderSize }

// End is the absolute offset just past the file.
func (f File) End() int { return f.Offset + f.Size }

// Files lists the files in v and returns the (8-byte aligned) offset where free space begins.
func (v *Volume) Files(buf []byte) ([]File, int, error) {
	var files []File
	blank := bytes.Repeat([]byte{v.EraseByte()}, ffsHeaderSize)
	p := v.DataOffset
	for p+ffsHeaderSize <= v.End() {
		h := buf[p : p+ffsHeaderSize]
		if bytes.Equal(h, blank) {
			break
		}
		f := File{Offset: p, HeaderSize: ffsHeaderSize, Type: h[18], Attributes: h[19], Size: getUint24(h[20:])}
		copy(f.GUID[:], h[:16])
		if v.IsFFS3() && f.Attributes&ffsAttribLargeFile != 0 {
			if p+ffsLargeHeaderSize > v.End() {
				return nil, 0, fmt.Errorf("truncated file header at %#x", p)
			}
			size := binary.LittleEndian.Uint64(buf[p+24:])
			if size > uint64(v.End()-p) {
				return nil, 0, fmt.Errorf("corrupt file size at %#x", p)
			}
			f.HeaderSize, f.Size = ffsLargeHeaderSize, int(size)
		}
		if f.Size < f.HeaderSize || f.Size > v.End()-p {
			return nil, 0, fmt.Errorf("corrupt file header at %#x", p)
		}
		files = append(files, f)
		p = v.alignUp(f.End())
	}
	used := v.DataOffset
	if len(files) > 0 {
		used = min(v.alignUp(files[len(files)-1].End()), v.End())
	}
	return files, used, nil
}

// headerChecksumOK checks IntegrityCheck.Checksum.Header (State and the file checksum
// count as zero).
func headerChecksumOK(buf []byte, f File) bool {
	h := bytes.Clone(buf[f.Offset : f.Offset+f.HeaderSize])
	h[17], h[23] = 0, 0
	return sum8(h) == 0
}

// dataChecksumOK checks IntegrityCheck.Checksum.File for files that carry one.
func dataChecksumOK(buf []byte, f File) bool {
	if f.Attributes&ffsAttribChecksum == 0 {
		return true
	}
	return sum8(buf[f.DataOffset():f.End()])+buf[f.Offset+17] == 0
}

// dataAlignment returns the alignment the file's data requires (FFS_ATTRIB_DATA_ALIGNMENT).
func dataAlignment(attrs byte) int {
	idx := (attrs >> 3) & 7
	if attrs&ffsAttribDataAlignment2 != 0 {
		return 128 << 10 << idx
	}
	return [...]int{1, 16, 128, 512, 1 << 10, 4 << 10, 32 << 10, 64 << 10}[idx]
}

// BuildFile assembles an FFS file with a standard 24-byte header and valid checksums.
func BuildFile(g GUID, typ, attrs byte, data []byte, erase byte) ([]byte, error) {
	size := ffsHeaderSize + len(data)
	if size > ffsMaxSize {
		return nil, fmt.Errorf("file of %d bytes is too large for a standard FFS header", size)
	}
	attrs &^= ffsAttribLargeFile
	f := make([]byte, size)
	copy(f, g[:])
	f[18], f[19] = typ, attrs
	putUint24(f[20:], size)
	copy(f[ffsHeaderSize:], data)
	f[16] = -sum8(f[:ffsHeaderSize])
	if attrs&ffsAttribChecksum != 0 {
		f[17] = -sum8(data)
	} else {
		f[17] = fileChecksumFixed
	}
	f[23] = fileStateValid
	if erase == 0xFF {
		f[23] = ^f[23]
	}
	return f, nil
}

// buildPadFile returns a pad file of exactly size bytes (size >= 24).
func buildPadFile(size int, erase byte) []byte {
	f, _ := BuildFile(padFileGUID, FileTypePad, 0, bytes.Repeat([]byte{erase}, size-ffsHeaderSize), erase)
	return f
}

func getUint24(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }

func putUint24(b []byte, v int) { b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) }

func sum8(b []byte) byte {
	var s byte
	for _, c := range b {
		s += c
	}
	return s
}

func alignUp(n, a int) int { return (n + a - 1) / a * a }

// Alignment is the required alignment of the file's data, in bytes.
func (f File) Alignment() int { return dataAlignment(f.Attributes) }

// HasDataChecksum reports whether the file's header carries a checksum of its data.
func (f File) HasDataChecksum() bool { return f.Attributes&ffsAttribChecksum != 0 }
