package uefi

import (
	"bytes"
	"encoding/binary"
)

// Capsule describes a capsule header in front of the flash image.
type Capsule struct {
	Kind       string
	HeaderSize int
	Signed     bool
}

// DetectCapsule recognises AMI Aptio and UEFI capsule headers, and AMI signature blocks
// embedded inside a flash image (used by e.g. Gigabyte and MSI instead of a capsule header).
func DetectCapsule(buf []byte) *Capsule {
	if len(buf) < 28 {
		return nil
	}
	var g GUID
	copy(g[:], buf)
	kind, ok := map[GUID]string{
		AMICapsuleGUID: "AMI Aptio capsule",
		EFICapsuleGUID: "UEFI capsule",
		FMPCapsuleGUID: "UEFI FMP capsule",
	}[g]
	if !ok {
		return detectEmbeddedSignature(buf)
	}
	hs := int(binary.LittleEndian.Uint32(buf[16:]))
	if hs > len(buf) {
		hs = len(buf)
	}
	hdr := buf[:hs]
	return &Capsule{
		Kind:       kind,
		HeaderSize: hs,
		Signed:     bytes.Contains(hdr, CertRSA2048GUID[:]) || bytes.Contains(hdr, CertPKCS7GUID[:]),
	}
}

// detectEmbeddedSignature looks for an AMI capsule header inside the image whose
// certificate follows within its first bytes.
func detectEmbeddedSignature(buf []byte) *Capsule {
	for pos := 0; ; {
		i := bytes.Index(buf[pos:], AMICapsuleGUID[:])
		if i < 0 {
			return nil
		}
		hdr := buf[pos+i : min(len(buf), pos+i+0x80)]
		if bytes.Contains(hdr, CertRSA2048GUID[:]) || bytes.Contains(hdr, CertPKCS7GUID[:]) {
			return &Capsule{Kind: "AMI signature embedded in the image", Signed: true}
		}
		pos += i + 1
	}
}

// FlashNote explains how a patched copy of the image can be flashed. c may be nil.
func (c *Capsule) FlashNote() string {
	if c == nil || !c.Signed {
		return "No signature detected: your usual BIOS update method should accept the patched file. Keep the original at hand."
	}
	return "A patched file can only be flashed with a method that skips the signature check: ASUS USB BIOS FlashBack, " +
		"MSI Flash BIOS Button, Gigabyte Q-Flash Plus or an SPI programmer. The BIOS's own updater (ASUS EZ Flash, " +
		"MSI M-Flash, Gigabyte Q-Flash, ASRock Instant Flash) rejects it because it no longer matches the signature."
}
