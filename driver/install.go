//go:build ignore

// Install the tested production driver after GoReleaser validates the checkout.
// Run from the repository root; uses only the Go standard library.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"os"
)

func main() {
	source := os.Getenv("ANIMLOGO_DRIVER")
	if source == "" {
		source = "driver/build/production/animlogo.efi"
	}
	b, err := os.ReadFile(source)
	if err != nil {
		log.Fatal(err)
	}
	if len(b) < 64 || string(b[:2]) != "MZ" {
		log.Fatal("invalid production driver")
	}
	pe := int(binary.LittleEndian.Uint32(b[60:]))
	if pe < 0 || pe > len(b)-94 || string(b[pe:pe+4]) != "PE\x00\x00" ||
		binary.LittleEndian.Uint16(b[pe+4:]) != 0x8664 || binary.LittleEndian.Uint16(b[pe+24:]) != 0x20b ||
		binary.LittleEndian.Uint16(b[pe+92:]) != 11 || bytes.Contains(b, []byte("animlogo:")) {
		log.Fatal("expected a production x64 EFI boot-service driver, not a test build")
	}
	if err := os.WriteFile("uefi/animlogo.efi", b, 0644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Installed production driver SHA256: %x\n", sha256.Sum256(b))
}
