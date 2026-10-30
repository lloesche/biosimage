package uefi

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"golang.org/x/arch/x86/x86asm"
	"testing"
)

// Synthetic PE with an installed post table, getter, timer and linked placement
// routine. No vendor image or vendor module is included in the repository.
func animationGatePE(t *testing.T) []byte {
	t.Helper()
	code := bytes.Repeat([]byte{0xcc}, 0x600)
	data := make([]byte, 0x200)
	copy(data, postManagerGUID[:])
	binary.LittleEndian.PutUint64(data[0x20+0x50:], 0x1100)
	put := func(at int, h string) {
		b, e := hex.DecodeString(h)
		if e != nil {
			t.Fatal(e)
		}
		copy(code[at:], b)
	}
	rip := func(at, target int) { binary.LittleEndian.PutUint32(code[at:], uint32(target-(0x1000+at+4))) }
	// InstallMultipleProtocolInterfaces(handle, post GUID, table, NULL).
	put(0, "4c8d05000000004533c9488d1500000000488d0d00000000ff9048010000c3")
	rip(3, 0x2020)
	rip(13, 0x2000)
	rip(20, 0x2080)
	// CreateEvent(timer|signal, TPL_NOTIFY, callback, ...).
	put(0x50, "4c8d0500000000ba10000000b900020080ff5050c3")
	rip(0x53, 0x1200)
	// No-argument getter with an optional tail delegate, then mov eax,[state];ret.
	put(0x100, "803d00000000017510488b05000000004885c0740448ff60188b0500000000c3")
	rip(0x102, 0x2090)
	rip(0x10c, 0x2098)
	rip(0x11b, 0x20a0)
	// Timer: if state>3 return; otherwise call placement.
	put(0x200, "48833d00000000037705e800000000c3")
	rip(0x203, 0x209f) // CMP has one immediate byte after its RIP displacement.
	rip(0x20b, 0x1300)
	// x64 arithmetic: screen*40/100 twice, both JA to one centering path;
	// otherwise screenHeight*382/1000 - imageHeight/2. Fixed register allocation
	// is intentional: unrecognized compiler variants must remain unsupported.
	put(0x300, "4b8d0c8048bb15ae47e17a14ae4748c1e103488bc348f7e1482bca48d1e94803ca48c1e9064c3bf177694b8d0c89488bc348c1e10348f7e1482bca48d1e94803ca48c1e906483bf177494969c97e010000498bc0492bc64899482bc248d1f8488bf848b8cff753e3a59bc42048f7e9488bc6488bda489948c1fb07482bc2488bcb48d1f848c1e93f4803d9482bd8e9db000000")
	put(0x393, "909090c3")
	put(0x46e, "c3")
	return pe64Image(code, data)
}

func TestAnimationCompatibilityGates(t *testing.T) {
	base := animationGatePE(t)
	if p, l := parsePE64(base).animationContract(); !p || !l {
		t.Fatal("complete synthetic contract not recognized")
	}
	for _, c := range []struct {
		name     string
		old, new []byte
	}{
		{"cleared protocol table", []byte{0x45, 0x33, 0xc9}, []byte{0x45, 0x33, 0xc0}},
		{"partial table overwrite", []byte{0x45, 0x33, 0xc9}, []byte{0x41, 0xb0, 0}},
		{"wrong height register", []byte{0x48, 0x8b, 0xc6}, []byte{0x48, 0x8b, 0xc7}},
		{"wrong GUID", postManagerGUID[:], make([]byte, 16)},
		{"wrong installed slot", []byte{0xff, 0x90, 0x48, 1, 0, 0}, []byte{0xff, 0x90, 0x40, 1, 0, 0}},
		{"not a timer", []byte{0xb9, 0, 2, 0, 0x80}, []byte{0xb9, 0, 2, 0, 0}},
		{"wrong threshold", []byte{0x48, 0x83, 0x3d, 0x98, 0x0e, 0, 0, 3}, []byte{0x48, 0x83, 0x3d, 0x98, 0x0e, 0, 0, 4}},
		{"wrong state comparison", []byte{0x77, 5, 0xe8}, []byte{0x76, 5, 0xe8}},
		{"branch continues playing", []byte{0x77, 5, 0xe8}, []byte{0x77, 0, 0xe8}},
		{"wrong placement fraction", []byte{0x7e, 1, 0, 0}, []byte{0x7f, 1, 0, 0}},
		{"wrong percent division", []byte{0x15, 0xae, 0x47, 0xe1}, []byte{0x14, 0xae, 0x47, 0xe1}},
		{"no placement call", []byte{0xe8, 0xf1, 0, 0, 0}, []byte{0x90, 0x90, 0x90, 0x90, 0x90}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !bytes.Contains(base, c.old) {
				t.Fatal("mutation was not applied")
			}
			b := bytes.Replace(bytes.Clone(base), c.old, c.new, 1)
			if p, l := parsePE64(b).animationContract(); p || l {
				t.Fatal("incomplete or changed contract accepted")
			}
		})
	}
	b := bytes.Clone(base)
	p := parsePE64(b)
	binary.LittleEndian.PutUint64(b[p.offset(0x2070):], 0x1101)
	if a, l := p.animationContract(); a || l {
		t.Fatal("misaligned getter accepted")
	}
}

// Destroy each arithmetic instruction independently, including the register moves
// the old matcher skipped. None of these altered data flows proves placement.
func TestAnimationPlacementRejectsEveryInstructionMutation(t *testing.T) {
	base := animationGatePE(t)
	pe := parsePE64(base)
	pc := 0x1300
	for index, inst := range pe.code(pe.offset(pc), len(placementPattern)) {
		off := pe.offset(pc)
		t.Run(fmt.Sprintf("%02d-%s", index, inst.Op), func(t *testing.T) {
			b := bytes.Clone(base)
			for j := 0; j < inst.Len; j++ {
				b[off+j] = 0x90
			}
			if p, l := parsePE64(b).animationContract(); p || l {
				t.Fatal("altered placement accepted")
			}
		})
		pc += inst.Len
	}
}

func TestAnimationCallArgumentsRejectClobbers(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"table cleared", "4533c0"}, {"table low byte", "41b001"},
		{"table low word", "6641b80100"}, {"table unknown move", "4d8bc1"},
		{"guid low byte", "b201"}, {"guid high byte", "b601"},
		{"guid low word", "66ba0100"}, {"guid cleared", "33d2"},
		{"branch", "eb00"}, {"unknown arithmetic", "49ffc0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := animationGatePE(t)
			p := parsePE64(b)
			code, _ := hex.DecodeString("4c8d0519000000488d1512000000" + tc.code + "ff9048010000")
			copy(b[p.offset(0x1000):], code)
			args := p.callArguments(p.offset(0x1000), 0x148)
			if args[x86asm.R8].address && args[x86asm.RDX].address {
				t.Fatal("clobbered arguments retained")
			}
		})
	}
}

func TestAnimationStackVarargsRequireProvenBase(t *testing.T) {
	for _, tc := range []struct {
		name, prologue string
		want           bool
	}{
		{"stack copy", "4c8bdc55488bec4883ec60", true},
		{"unproven base", "90909055488bec4883ec60", false},
		{"clobbered base", "4c8bdc4533db9090909090", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := animationGatePE(t)
			p := parsePE64(b)
			prefix, _ := hex.DecodeString(tc.prologue)
			copy(b[p.offset(0x1000):], prefix)
			code, _ := hex.DecodeString("4c8d0519000000488d1512000000498943c0ff9048010000")
			off := p.offset(0x1000 + len(prefix))
			copy(b[off:], code)
			args := p.callArguments(off, 0x148)
			if got := args[x86asm.R8].address && args[x86asm.RDX].address; got != tc.want {
				t.Fatalf("accepted stack varargs = %v, want %v", got, tc.want)
			}
		})
	}
}
