package uefi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Section types.
const (
	SectionCompression = 0x01
	SectionGUIDDefined = 0x02
	SectionUI          = 0x15
	SectionRaw         = 0x19
)

const (
	guidedHeaderSize         = 20 // SectionDefinitionGuid + DataOffset + Attributes
	guidedProcessingRequired = 0x01
	sectionExtendedSize      = 0xFFFFFF
)

// Section is one EFI_COMMON_SECTION_HEADER-prefixed section; Raw includes the header.
type Section struct {
	Type       byte
	HeaderSize int
	Raw        []byte
}

// Body returns the section contents after the common header.
func (s Section) Body() []byte { return s.Raw[s.HeaderSize:] }

// ParseSections splits a file's (or an encapsulation's) data into sections.
func ParseSections(b []byte) ([]Section, error) {
	var secs []Section
	for p := 0; p+4 <= len(b); {
		size, hs := getUint24(b[p:]), 4
		if size == sectionExtendedSize {
			if p+8 > len(b) {
				return nil, fmt.Errorf("truncated section header at %#x", p)
			}
			size, hs = int(binary.LittleEndian.Uint32(b[p+4:])), 8
		}
		if size < hs || size > len(b)-p {
			return nil, fmt.Errorf("corrupt section header at %#x", p)
		}
		secs = append(secs, Section{Type: b[p+3], HeaderSize: hs, Raw: b[p : p+size]})
		p = alignUp(p+size, 4)
	}
	return secs, nil
}

// NewSection builds a section, using the extended size header when needed.
func NewSection(typ byte, body []byte) []byte {
	if size := 4 + len(body); size < sectionExtendedSize {
		s := make([]byte, 4, size)
		putUint24(s, size)
		s[3] = typ
		return append(s, body...)
	}
	s := make([]byte, 8, 8+len(body))
	putUint24(s, sectionExtendedSize)
	s[3] = typ
	binary.LittleEndian.PutUint32(s[4:], uint32(8+len(body)))
	return append(s, body...)
}

// JoinSections concatenates sections, padding each to a 4-byte boundary.
func JoinSections(secs ...[]byte) []byte {
	var out []byte
	for _, s := range secs {
		out = append(out, make([]byte, alignUp(len(out), 4)-len(out))...)
		out = append(out, s...)
	}
	return out
}

// NewUISection builds a user interface (name) section.
func NewUISection(name string) []byte {
	u := utf16.Encode([]rune(name + "\x00"))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return NewSection(SectionUI, b)
}

// UIName decodes a user interface section.
func (s Section) UIName() string {
	b := s.Body()
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

// guidedHeader parses an EFI_GUID_DEFINED_SECTION header.
func (s Section) guidedHeader() (g GUID, dataOffset int, attrs uint16, err error) {
	body := s.Body()
	if len(body) < guidedHeaderSize {
		return g, 0, 0, errors.New("truncated GUID-defined section")
	}
	copy(g[:], body)
	dataOffset = int(binary.LittleEndian.Uint16(body[16:]))
	attrs = binary.LittleEndian.Uint16(body[18:])
	if dataOffset < s.HeaderSize+guidedHeaderSize || dataOffset > len(s.Raw) {
		return g, 0, 0, errors.New("bad DataOffset in GUID-defined section")
	}
	return g, dataOffset, attrs, nil
}

// newGuidedSection builds a GUID-defined section around payload.
func newGuidedSection(g GUID, attrs uint16, payload []byte) []byte {
	hdr := 4
	if 4+guidedHeaderSize+len(payload) >= sectionExtendedSize {
		hdr = 8
	}
	body := make([]byte, guidedHeaderSize, guidedHeaderSize+len(payload))
	copy(body, g[:])
	binary.LittleEndian.PutUint16(body[16:], uint16(hdr+guidedHeaderSize))
	binary.LittleEndian.PutUint16(body[18:], attrs)
	return NewSection(SectionGUIDDefined, append(body, payload...))
}
