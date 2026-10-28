package uefi

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// CRC32SectionGUID is gEfiCrc32GuidedSectionExtractionGuid.
var CRC32SectionGUID = MustParseGUID("FC1BCDB0-7D31-49AA-936A-A4600D9DD083")

var knownFiles = map[GUID]string{
	MustParseGUID("9E21FD93-9C72-4C15-8C4B-E77F1DB2D792"): "Compressed main volume (FV_MAIN)",
	MustParseGUID("414D94AD-998D-47D2-BFCD-4E882241DE32"): "AMI firmware signature",
	MustParseGUID("05CA020B-0FC1-11DC-9011-00173153EBA8"): "Nested volume (AMI)",
}

// KnownFileName returns a description of well-known files, such as AMI's nested volumes.
func KnownFileName(g GUID) (string, bool) {
	name, ok := knownFiles[g]
	return name, ok
}

// GUIDName returns the name of a well-known GUID, such as a section definition.
func GUIDName(g GUID) (string, bool) {
	if g == CRC32SectionGUID {
		return "CRC32", true
	}
	name, ok := guidNames[g]
	return name, ok
}

// FileTypeName returns the name of an FFS file type, e.g. "FV_IMAGE".
func FileTypeName(t byte) string {
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

// SectionTypeName returns the name of a section type, e.g. "User interface".
func SectionTypeName(t byte) string {
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

// UTF16String decodes a NUL-terminated UTF-16LE string.
func UTF16String(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}
