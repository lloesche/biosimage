package uefi

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/arch/x86/x86asm"
)

// fv2ProtocolGUID is EFI_FIRMWARE_VOLUME2_PROTOCOL_GUID.
var fv2ProtocolGUID = MustParseGUID("220E73B6-6BDB-4413-8405-B974B108619A")

// TSECode is what AMITSE's machine code shows about how it loads and plays the logo. It is
// read from the x64 code with a disassembler, by the instruction sequences that do it.
type TSECode struct {
	// AnyVolumeLogo: AMITSE reads the logo from every firmware volume, not one in particular:
	// for each Firmware Volume 2 handle it calls HandleProtocol, then ReadSection for the raw
	// section (type 0x19) of the logo file. Moving the logo (Node.FreeSpace) relies on it.
	AnyVolumeLogo bool
	// GIFMinDelay: the GIF player raises frame delays from 10 to 90 ms to 100 ms.
	GIFMinDelay bool
}

// TSECode disassembles AMITSE in the image.
func (n *Node) TSECode() TSECode {
	var c TSECode
	tse := n.tse()
	if tse == nil {
		return c
	}
	tse.Walk(func(s *Node) bool {
		if s.Kind == KindSection && s.Section.Type == SectionPE32 {
			if pe := parsePE64(s.Section.Body()); pe != nil {
				c.AnyVolumeLogo = c.AnyVolumeLogo || pe.readsSectionFromEveryVolume()
				c.GIFMinDelay = c.GIFMinDelay || pe.clampsGIFDelay()
			}
		}
		return true
	})
	return c
}

// pe64 is an x64 PE image as stored in a PE32 section: sections at their file offsets.
type pe64 struct {
	b        []byte
	sections []peSection
}

type peSection struct {
	rva, size, raw int
	code           bool
}

func parsePE64(b []byte) *pe64 {
	if len(b) < 0x40 || string(b[:2]) != "MZ" {
		return nil
	}
	h := int(binary.LittleEndian.Uint32(b[0x3c:]))
	if h+24 > len(b) || string(b[h:h+4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(b[h+4:]) != 0x8664 {
		return nil
	}
	n := int(binary.LittleEndian.Uint16(b[h+6:]))
	p := h + 24 + int(binary.LittleEndian.Uint16(b[h+20:]))
	pe := &pe64{b: b}
	for i := 0; i < n && p+40 <= len(b); i, p = i+1, p+40 {
		s := peSection{
			rva:  int(binary.LittleEndian.Uint32(b[p+12:])),
			size: int(binary.LittleEndian.Uint32(b[p+16:])),
			raw:  int(binary.LittleEndian.Uint32(b[p+20:])),
			code: binary.LittleEndian.Uint32(b[p+36:])&0x20000000 != 0, // IMAGE_SCN_MEM_EXECUTE
		}
		if s.raw+s.size <= len(b) {
			pe.sections = append(pe.sections, s)
		}
	}
	return pe
}

// rva returns the address of file offset off, or -1.
func (pe *pe64) rva(off int) int {
	for _, s := range pe.sections {
		if off >= s.raw && off < s.raw+s.size {
			return off - s.raw + s.rva
		}
	}
	return -1
}

// code returns the instructions from file offset off on, at most n, stopping at one that
// cannot be decoded. Their addresses follow from the first one's.
func (pe *pe64) code(off, n int) []x86asm.Inst {
	var insts []x86asm.Inst
	for len(insts) < n && off < len(pe.b) {
		inst, err := x86asm.Decode(pe.b[off:], 64)
		if err != nil {
			break
		}
		insts = append(insts, inst)
		off += inst.Len
	}
	return insts
}

// refsTo returns the file offsets of the instructions in code sections that address target
// (an RVA) RIP-relatively. It looks for 32-bit displacements that resolve to target at the end
// of an instruction without an immediate, then decodes the instruction that holds them.
func (pe *pe64) refsTo(target int) []int {
	var refs []int
	for _, s := range pe.sections {
		if !s.code {
			continue
		}
		for i := s.raw; i+4 <= s.raw+s.size; i++ {
			end := i + 4 - s.raw + s.rva
			if end+int(int32(binary.LittleEndian.Uint32(pe.b[i:]))) != target {
				continue
			}
			for _, start := range []int{i - 3, i - 2} { // REX, opcode, ModRM; or opcode, ModRM
				if start < s.raw {
					continue
				}
				inst, err := x86asm.Decode(pe.b[start:], 64)
				if err == nil && start+inst.Len == i+4 && ripTarget(inst, pe.rva(start)) == target {
					refs = append(refs, start)
					break
				}
			}
		}
	}
	return refs
}

// ripTarget returns the address that inst at pc addresses RIP-relatively, or -1.
func ripTarget(inst x86asm.Inst, pc int) int {
	for _, a := range inst.Args {
		if m, ok := a.(x86asm.Mem); ok && m.Base == x86asm.RIP {
			return pc + inst.Len + int(m.Disp)
		}
	}
	return -1
}

// readsSectionFromEveryVolume reports whether, after loading the Firmware Volume 2 protocol
// GUID, the code calls HandleProtocol (boot services +0x98) and then a protocol's ReadSection
// (+0x18) with EFI_SECTION_RAW (0x19) in r8, the way AMITSE loops over all volumes.
func (pe *pe64) readsSectionFromEveryVolume() bool {
	at := bytes.Index(pe.b, fv2ProtocolGUID[:])
	for at >= 0 {
		if target := pe.rva(at); target >= 0 { // else outside the sections, not addressable
			for _, ref := range pe.refsTo(target) {
				if sequence(pe.code(ref, 48), slotUse(0x98), rawSectionType, slotUse(0x18)) {
					return true
				}
			}
		}
		next := bytes.Index(pe.b[at+1:], fv2ProtocolGUID[:])
		if next < 0 {
			break
		}
		at += 1 + next
	}
	return false
}

// sequence reports whether insts contain instructions matching each of match, in order.
func sequence(insts []x86asm.Inst, match ...func(x86asm.Inst) bool) bool {
	for _, inst := range insts {
		if len(match) > 0 && match[0](inst) {
			match = match[1:]
		}
	}
	return len(match) == 0
}

// slotUse matches a call through, or a load of, the function pointer at offset disp of a
// table (a protocol or boot services): call [reg+disp] or mov reg, [reg+disp].
func slotUse(disp int64) func(x86asm.Inst) bool {
	return func(inst x86asm.Inst) bool {
		if inst.Op != x86asm.CALL && inst.Op != x86asm.MOV {
			return false
		}
		for _, a := range inst.Args {
			if m, ok := a.(x86asm.Mem); ok && m.Base != x86asm.RIP && m.Base != x86asm.RSP && m.Base != x86asm.RBP && m.Index == 0 && m.Disp == disp {
				return true
			}
		}
		return false
	}
}

// rawSectionType matches setting the third argument (r8) to EFI_SECTION_RAW.
func rawSectionType(inst x86asm.Inst) bool {
	if inst.Op != x86asm.MOV {
		return false
	}
	r, ok := inst.Args[0].(x86asm.Reg)
	v, isImm := inst.Args[1].(x86asm.Imm)
	return ok && isImm && v == 0x19 && (r == x86asm.R8B || r == x86asm.R8W || r == x86asm.R8L || r == x86asm.R8)
}

// clampsGIFDelay reports whether the GIF player raises frame delays below 100 ms to 100 ms.
// After recognizing a graphic control extension (cmp reg, 0xF9) it clamps the delay: compiled
// as cmp ms-1, 98 followed by a conditional move or branch, or as mov reg, 100 / cmp ms, reg /
// cmovb.
func (pe *pe64) clampsGIFDelay() bool {
	for _, s := range pe.sections {
		if !s.code {
			continue
		}
		for i := s.raw; i+6 <= s.raw+s.size; i++ {
			// cmp eax, imm32 0xF9 or cmp r32, imm32 0xF9
			b := pe.b[i:]
			if !(b[0] == 0x3d && bytes.Equal(b[1:5], []byte{0xf9, 0, 0, 0})) &&
				!(b[0] == 0x81 && b[1]&0xf8 == 0xf8 && bytes.Equal(b[2:6], []byte{0xf9, 0, 0, 0})) {
				continue
			}
			hundred := false
			for _, inst := range pe.code(i, 100) {
				v, imm := inst.Args[1].(x86asm.Imm)
				switch {
				case inst.Op == x86asm.CMP && imm && v == 98:
					return true
				case inst.Op == x86asm.MOV && imm && v == 100:
					hundred = true
				case inst.Op == x86asm.CMOVB && hundred:
					return true
				}
			}
		}
	}
	return false
}
