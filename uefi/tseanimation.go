package uefi

import (
	"bytes"
	"encoding/binary"
	"golang.org/x/arch/x86/x86asm"
	"slices"
)

var postManagerGUID = MustParseGUID("8A91B1E1-56C7-4ADC-ABEB-1C2CA1729EFF")

// animationContract deliberately recognizes a narrow compiler layout. A GUID,
// getter or arithmetic constant alone is not evidence: the installed protocol's
// getter must name the state tested by a registered timer, which in turn calls
// the recognized placement routine. Unknown layouts disable the feature.
func (pe *pe64) animationContract() (bool, bool) {
	for at := 0; at+16 <= len(pe.b); at++ {
		if !bytes.Equal(pe.b[at:at+16], postManagerGUID[:]) {
			continue
		}
		for _, ref := range pe.refsTo(pe.rva(at)) {
			// InstallMultipleProtocolInterfaces(handle, GUID, table, NULL).
			for start := max(0, ref-40); start <= ref; start++ {
				args := pe.callArguments(start, 0x148)
				table := args[x86asm.R8]
				if !table.address || args[x86asm.RDX] != (callArgument{int64(pe.rva(at)), true}) {
					continue
				}
				off := pe.offset(int(table.value) + 0x50)
				if off < 0 || off+8 > len(pe.b) {
					continue
				}
				entry := binary.LittleEndian.Uint64(pe.b[off:])
				if entry <= uint64(^uint(0)>>1) {
					state := pe.stateGetter(int(entry))
					if state >= 0 && pe.animationTimer(state) {
						return true, true
					}
				}
			}
		}
	}
	return false, false
}

// Only the observed straight-line argument setup is supported. Every write to an
// argument register (including a partial register) discards its previous value.
// Unknown operations or control flow fail closed instead of retaining stale facts.
type callArgument struct {
	value   int64
	address bool
}

func argumentRegister(r x86asm.Arg) x86asm.Reg {
	switch r {
	case x86asm.CL, x86asm.CH, x86asm.CX, x86asm.ECX, x86asm.RCX:
		return x86asm.RCX
	case x86asm.DL, x86asm.DH, x86asm.DX, x86asm.EDX, x86asm.RDX:
		return x86asm.RDX
	case x86asm.R8B, x86asm.R8W, x86asm.R8L, x86asm.R8:
		return x86asm.R8
	case x86asm.R9B, x86asm.R9W, x86asm.R9L, x86asm.R9:
		return x86asm.R9
	}
	return 0
}

func (pe *pe64) callArguments(off int, slot int64) map[x86asm.Reg]callArgument {
	args := map[x86asm.Reg]callArgument{}
	// Some AMI builds stage additional varargs via an R11 copy of RSP.
	stackR11 := pe.stackR11At(off)
	pc := pe.rva(off)
	for _, inst := range pe.code(off, 16) {
		if inst.Op == x86asm.CALL && slotUse(slot)(inst) {
			return args
		}
		if inst.Op != x86asm.MOV && inst.Op != x86asm.LEA && inst.Op != x86asm.XOR {
			return nil
		}
		if m, ok := inst.Args[0].(x86asm.Mem); ok && (inst.Op != x86asm.MOV || m.Index != 0 ||
			(m.Base != x86asm.RSP && !(m.Base == x86asm.R11 && stackR11))) {
			return nil
		}
		if isR11(inst.Args[0]) {
			stackR11 = false
		}
		r := argumentRegister(inst.Args[0])
		delete(args, r)
		// Partial writes invalidate the value; only complete 32/64-bit assignments
		// can establish a new immediate, and addresses must be 64-bit LEAs.
		full := inst.Args[0] == r || inst.Args[0] == x86asm.ECX || inst.Args[0] == x86asm.EDX || inst.Args[0] == x86asm.R8L || inst.Args[0] == x86asm.R9L
		if r != 0 && full {
			switch inst.Op {
			case x86asm.LEA:
				if target := ripTarget(inst, pc); inst.Args[0] == r && target >= 0 {
					args[r] = callArgument{int64(target), true}
				}
			case x86asm.MOV:
				if value, ok := inst.Args[1].(x86asm.Imm); ok {
					v := int64(value)
					if inst.Args[0] != r {
						v = int64(uint32(v))
					}
					args[r] = callArgument{v, false}
				}
			case x86asm.XOR:
				if inst.Args[0] == inst.Args[1] {
					args[r] = callArgument{0, false}
				}
			}
		}
		pc += inst.Len
	}
	return nil
}

func isR11(arg x86asm.Arg) bool {
	return arg == x86asm.R11 || arg == x86asm.R11L || arg == x86asm.R11W || arg == x86asm.R11B
}

// Recognize a nearby straight-line prologue establishing R11 as the stack base.
// Do not assume that an arbitrary non-argument register points at the stack.
func (pe *pe64) stackR11At(off int) bool {
	for start := max(0, off-96); start < off; start++ {
		insts := pe.code(start, 32)
		if len(insts) == 0 || insts[0].Op != x86asm.MOV || insts[0].Args != (x86asm.Args{x86asm.R11, x86asm.RSP}) {
			continue
		}
		pc := start + insts[0].Len
		for _, inst := range insts[1:] {
			if pc == off {
				return true
			}
			if pc > off || isR11(inst.Args[0]) {
				break
			}
			switch inst.Op {
			case x86asm.MOV, x86asm.LEA, x86asm.PUSH, x86asm.SUB, x86asm.AND:
				pc += inst.Len
			default:
				pc = off + 1 // unknown effects or control flow: reject this prologue
			}
		}
	}
	return false
}

// The observed getter takes no arguments, returns EAX from RIP-relative state,
// and optionally tail-delegates to another protocol's +0x18 getter. Both forward
// branches must reach the same final load; accepting an arbitrary MOV/RET inside
// another function would not establish the ABI.
func (pe *pe64) stateGetter(entry int) int {
	off := pe.offset(entry)
	if off < 0 {
		return -1
	}
	i := pe.code(off, 8)
	if len(i) != 8 || i[0].Op != x86asm.CMP || !hasImm(i[0], 1) || i[1].Op != x86asm.JNE ||
		i[2].Op != x86asm.MOV || i[2].Args[0] != x86asm.RAX || ripTarget(i[2], entry+i[0].Len+i[1].Len) < 0 ||
		i[3].Op != x86asm.TEST || i[3].Args[0] != x86asm.RAX || i[3].Args[1] != x86asm.RAX ||
		i[4].Op != x86asm.JE || i[5].Op != x86asm.JMP || i[5].Args[0] != (x86asm.Mem{Base: x86asm.RAX, Disp: 0x18}) ||
		i[6].Op != x86asm.MOV || i[6].Args[0] != x86asm.EAX || i[7].Op != x86asm.RET {
		return -1
	}
	pc, load := entry, entry
	for n := 0; n < 6; n++ {
		load += i[n].Len
	}
	for n, inst := range i {
		if n == 1 || n == 4 {
			rel, ok := inst.Args[0].(x86asm.Rel)
			if !ok || pc+inst.Len+int(rel) != load {
				return -1
			}
		}
		pc += inst.Len
	}
	return ripTarget(i[6], load)
}

func (pe *pe64) animationTimer(state int) bool {
	for _, s := range pe.sections {
		if !s.code {
			continue
		}
		for off := s.raw; off < s.raw+s.size; off++ {
			// lea r8, [rip+callback], then CreateEvent(EVT_TIMER|NOTIFY_SIGNAL,TPL_NOTIFY,...).
			inst, e := x86asm.Decode(pe.b[off:], 64)
			if e != nil || inst.Op != x86asm.LEA || inst.Args[0] != x86asm.R8 {
				continue
			}
			callback := ripTarget(inst, pe.rva(off))
			if callback < 0 {
				continue
			}
			args := pe.callArguments(off, 0x50)
			if args[x86asm.R8] != (callArgument{int64(callback), true}) ||
				args[x86asm.RCX] != (callArgument{0x80000200, false}) ||
				args[x86asm.RDX] != (callArgument{16, false}) {
				continue
			}
			flow, _ := pe.flow(callback, 256)
			for n, x := range flow {
				if x.Op != x86asm.CMP || !hasImm(x.Inst, 3) || ripTarget(x.Inst, x.pc) != state {
					continue
				}
				for j := n + 1; j < min(n+5, len(flow)); j++ {
					if flow[j].Op == x86asm.JA {
						branch := flow[j]
						rel, ok := branch.Args[0].(x86asm.Rel)
						if ok && pe.timerBranches(branch.pc+branch.Len, branch.pc+branch.Len+int(rel)) {
							return true
						}
						break
					}
					// Only flag-preserving argument shuffles may separate CMP and JA.
					if flow[j].Op != x86asm.MOV && flow[j].Op != x86asm.LEA {
						break
					}
				}
			}
		}
	}
	return false
}

// State>3 must reach a return without the placement call. The fallthrough must
// still call the recognized placement routine. This rejects a JA pointing back
// into the playing path or an unrelated later JA whose flags came from elsewhere.
func (pe *pe64) timerBranches(play, stop int) bool {
	stopped, stopCalls := pe.flow(stop, 64)
	if !slices.ContainsFunc(stopped, func(i placed) bool { return i.Op == x86asm.RET }) {
		return false
	}
	for _, callee := range stopCalls {
		f, _ := pe.flow(callee, 800)
		if placementRule(f) {
			return false
		}
	}
	_, playCalls := pe.flow(play, 256)
	for _, callee := range playCalls {
		f, _ := pe.flow(callee, 800)
		if placementRule(f) {
			return true
		}
	}
	return false
}

// placementRule recognizes the arithmetic/data flow of the observed x64 AMI
// placement routine, including both >40% branches to the same centering path.
// Register choices are intentionally conservative rather than inferred from
// loose constants. See driver/README.md for the runtime placement policy.
// Every instruction in this arithmetic block is significant. In particular the
// loads and sign corrections between the multiplications establish which image
// dimension is subtracted; matching constants alone cannot prove placement.
var placementPattern = []struct {
	op   x86asm.Op
	args x86asm.Args
}{
	{x86asm.LEA, x86asm.Args{x86asm.RCX, x86asm.Mem{Base: x86asm.R8, Index: x86asm.R8, Scale: 4}}},
	{x86asm.MOV, x86asm.Args{x86asm.RBX, x86asm.Imm(0x47ae147ae147ae15)}},
	{x86asm.SHL, x86asm.Args{x86asm.RCX, x86asm.Imm(3)}},
	{x86asm.MOV, x86asm.Args{x86asm.RAX, x86asm.RBX}},
	{x86asm.MUL, x86asm.Args{x86asm.RCX}},
	{x86asm.SUB, x86asm.Args{x86asm.RCX, x86asm.RDX}},
	{x86asm.SHR, x86asm.Args{x86asm.RCX, x86asm.Imm(1)}},
	{x86asm.ADD, x86asm.Args{x86asm.RCX, x86asm.RDX}},
	{x86asm.SHR, x86asm.Args{x86asm.RCX, x86asm.Imm(6)}},
	{x86asm.CMP, x86asm.Args{x86asm.R14, x86asm.RCX}},
	{x86asm.JA, x86asm.Args{}}, // width exceeds 40%: same centering target as height
	{x86asm.LEA, x86asm.Args{x86asm.RCX, x86asm.Mem{Base: x86asm.R9, Index: x86asm.R9, Scale: 4}}},
	{x86asm.MOV, x86asm.Args{x86asm.RAX, x86asm.RBX}},
	{x86asm.SHL, x86asm.Args{x86asm.RCX, x86asm.Imm(3)}},
	{x86asm.MUL, x86asm.Args{x86asm.RCX}},
	{x86asm.SUB, x86asm.Args{x86asm.RCX, x86asm.RDX}},
	{x86asm.SHR, x86asm.Args{x86asm.RCX, x86asm.Imm(1)}},
	{x86asm.ADD, x86asm.Args{x86asm.RCX, x86asm.RDX}},
	{x86asm.SHR, x86asm.Args{x86asm.RCX, x86asm.Imm(6)}},
	{x86asm.CMP, x86asm.Args{x86asm.RSI, x86asm.RCX}},
	{x86asm.JA, x86asm.Args{}},
	{x86asm.IMUL, x86asm.Args{x86asm.RCX, x86asm.R9, x86asm.Imm(382)}},
	// x = (screenWidth - imageWidth) / 2, retained in RDI.
	{x86asm.MOV, x86asm.Args{x86asm.RAX, x86asm.R8}},
	{x86asm.SUB, x86asm.Args{x86asm.RAX, x86asm.R14}},
	{x86asm.CQO, x86asm.Args{}},
	{x86asm.SUB, x86asm.Args{x86asm.RAX, x86asm.RDX}},
	{x86asm.SAR, x86asm.Args{x86asm.RAX, x86asm.Imm(1)}},
	{x86asm.MOV, x86asm.Args{x86asm.RDI, x86asm.RAX}},
	// y = screenHeight * 382 / 1000 - imageHeight / 2, retained in RBX.
	{x86asm.MOV, x86asm.Args{x86asm.RAX, x86asm.Imm(0x20c49ba5e353f7cf)}},
	{x86asm.IMUL, x86asm.Args{x86asm.RCX}},
	{x86asm.MOV, x86asm.Args{x86asm.RAX, x86asm.RSI}},
	{x86asm.MOV, x86asm.Args{x86asm.RBX, x86asm.RDX}},
	{x86asm.CQO, x86asm.Args{}},
	{x86asm.SAR, x86asm.Args{x86asm.RBX, x86asm.Imm(7)}},
	{x86asm.SUB, x86asm.Args{x86asm.RAX, x86asm.RDX}},
	{x86asm.MOV, x86asm.Args{x86asm.RCX, x86asm.RBX}},
	{x86asm.SAR, x86asm.Args{x86asm.RAX, x86asm.Imm(1)}},
	{x86asm.SHR, x86asm.Args{x86asm.RCX, x86asm.Imm(63)}},
	{x86asm.ADD, x86asm.Args{x86asm.RBX, x86asm.RCX}},
	{x86asm.SUB, x86asm.Args{x86asm.RBX, x86asm.RAX}},
}

func placementRule(flow []placed) bool {
	for n := 0; n+len(placementPattern) <= len(flow); n++ {
		block := flow[n : n+len(placementPattern)]
		center, matched := -1, true
		for i, want := range placementPattern {
			got := block[i]
			if got.Op != want.op || i > 0 && got.pc != block[i-1].pc+block[i-1].Len {
				matched = false
				break
			}
			if want.op == x86asm.JA {
				rel, ok := got.Args[0].(x86asm.Rel)
				target := got.pc + got.Len + int(rel)
				if !ok || target <= block[len(block)-1].pc || center >= 0 && target != center {
					matched = false
					break
				}
				center = target
			} else if got.Args != want.args {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
