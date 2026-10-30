package uefi

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

// pe64Image returns an x64 PE image with a code section (at RVA 0x1000) and a data section
// (at RVA 0x2000), stored at their file offsets like in a PE32 section.
func pe64Image(text, data []byte) []byte {
	const textRaw, textRVA, dataRVA = 0x200, 0x1000, 0x2000
	dataRaw := textRaw + alignUp(len(text), 0x200)
	b := make([]byte, dataRaw+alignUp(len(data), 0x200))
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x40)
	copy(b[0x40:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[0x44:], 0x8664)
	binary.LittleEndian.PutUint16(b[0x46:], 2)
	for i, s := range []struct {
		name          string
		rva, raw, len int
		flags         uint32
	}{{".text", textRVA, textRaw, len(text), 0x60000020}, {".data", dataRVA, dataRaw, len(data), 0xC0000040}} {
		h := b[0x58+40*i:]
		copy(h, s.name)
		binary.LittleEndian.PutUint32(h[8:], uint32(s.len))
		binary.LittleEndian.PutUint32(h[12:], uint32(s.rva))
		binary.LittleEndian.PutUint32(h[16:], uint32(s.len))
		binary.LittleEndian.PutUint32(h[20:], uint32(s.raw))
		binary.LittleEndian.PutUint32(h[36:], s.flags)
	}
	copy(b[textRaw:], text)
	copy(b[dataRaw:], data)
	return b
}

// Code like AMITSE's, at RVA 0x1000; the Firmware Volume 2 protocol GUID is at RVA 0x2000,
// followed by "GIF89a" at RVA 0x2010.
var (
	// lea rdx, [rip+GUID]; call [rax+0x98]; mov r8b, 0x19; call [rax+0x18]; ret
	anyVolumeCode = []byte{0x48, 0x8d, 0x15, 0xf9, 0x0f, 0x00, 0x00, 0xff, 0x90, 0x98, 0, 0, 0, 0x41, 0xb0, 0x19, 0xff, 0x50, 0x18, 0xc3}
	// cmp eax, 0xF9; lea eax, [rbx-1]; cmp eax, 0x62; cmovbe ebx, ecx; ret
	gifClampCode = []byte{0x3d, 0xf9, 0, 0, 0, 0x8d, 0x43, 0xff, 0x83, 0xf8, 0x62, 0x0f, 0x46, 0xd9, 0xc3}
)

// logoCode returns code like AMITSE's boot logo code, to be placed at offset at of the code
// section: it builds the logo file's GUID and calls a format test for JPEG, and for PNG and GIF
// if set.
func logoCode(at int, png, gif bool) []byte {
	gifRef := 0x2000 // the FV2 GUID instead of "GIF89a"
	if gif {
		gifRef = 0x2010
	}
	pngByte := byte(0x51)
	if png {
		pngByte = 0x50
	}
	b := []byte{
		0xc7, 0x45, 0xf0, 0x99, 0x8b, 0xb2, 0x7b, // mov dword [rbp-0x10], 0x7bb28b99
		0xe8, 1, 0, 0, 0, // call test
		0xc3,                   // ret
		0xb8, 0xff, 0xd8, 0, 0, // test: mov eax, 0xd8ff
		0x66, 0x3b, 0x01, // cmp ax, [rcx]
		0x80, 0x79, 0x01, pngByte, // cmp byte [rcx+1], 0x50
		0x48, 0x8d, 0x15, 0, 0, 0, 0, // lea rdx, [rip+GIF89a]
		0xc3, // ret
	}
	binary.LittleEndian.PutUint32(b[28:], uint32(gifRef-(0x1000+at+32)))
	return b
}

// tseFile returns an AMITSE file with code.
func tseFile(t *testing.T, code []byte) []byte {
	data := append(bytes.Clone(fv2ProtocolGUID[:]), "GIF89a"...)
	return mustFile(t, tseFileGUID, 0x07, NewSection(SectionPE32, pe64Image(code, data)), 0xFF)
}

func TestTSECode(t *testing.T) {
	noReadSection := bytes.Replace(anyVolumeCode, []byte{0xff, 0x50, 0x18}, []byte{0x90, 0x90, 0x90}, 1)
	noClamp := bytes.Replace(gifClampCode, []byte{0x83, 0xf8, 0x62}, []byte{0x83, 0xf8, 0x10}, 1)
	for _, c := range []struct {
		name string
		code []byte
		want TSECode
	}{
		{"both", append(bytes.Clone(anyVolumeCode), gifClampCode...), TSECode{AnyVolumeLogo: true, GIFMinDelay: true}},
		{"no ReadSection", append(bytes.Clone(noReadSection), gifClampCode...), TSECode{GIFMinDelay: true}},
		{"no clamp", append(bytes.Clone(anyVolumeCode), noClamp...), TSECode{AnyVolumeLogo: true}},
		{"JPEG", logoCode(0, false, false), TSECode{LogoFormats: []string{"JPEG"}}},
		{"JPEG PNG GIF", logoCode(0, true, true), TSECode{LogoFormats: []string{"JPEG", "PNG", "GIF"}}},
		{"after other code", append(bytes.Clone(anyVolumeCode), logoCode(len(anyVolumeCode), false, true)...),
			TSECode{AnyVolumeLogo: true, LogoFormats: []string{"JPEG", "GIF"}}},
		{"no logo GUID", bytes.Replace(logoCode(0, true, true), []byte{0x99, 0x8b}, []byte{0x98, 0x8b}, 1), TSECode{}},
	} {
		fw := buildImage(buildVolume(t, 0x10000, 0xFF, tseFile(t, c.code)))
		if got := Parse(fw).TSECode(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
