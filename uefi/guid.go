// Package uefi parses UEFI/PI firmware images (capsules, firmware volumes, FFS files and
// sections) far enough to find and replace the boot logo.
package uefi

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// GUID is an EFI_GUID in its on-disk byte order (the first three fields are little-endian).
type GUID [16]byte

// ParseGUID parses the canonical text form, e.g. "7BB28B99-61BB-11D5-9A5D-0090273FC14D".
func ParseGUID(s string) (GUID, error) {
	var g GUID
	parts := strings.Split(strings.Trim(s, "{}"), "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 ||
		len(parts[3]) != 4 || len(parts[4]) != 12 {
		return g, fmt.Errorf("invalid GUID %q", s)
	}
	b, err := hex.DecodeString(strings.Join(parts, ""))
	if err != nil {
		return g, fmt.Errorf("invalid GUID %q: %w", s, err)
	}
	g[0], g[1], g[2], g[3] = b[3], b[2], b[1], b[0]
	g[4], g[5] = b[5], b[4]
	g[6], g[7] = b[7], b[6]
	copy(g[8:], b[8:])
	return g, nil
}

// MustParseGUID is like ParseGUID but panics on malformed input.
func MustParseGUID(s string) GUID {
	g, err := ParseGUID(s)
	if err != nil {
		panic(err)
	}
	return g
}

func (g GUID) String() string {
	return fmt.Sprintf("%08X-%04X-%04X-%X-%X",
		binary.LittleEndian.Uint32(g[0:4]), binary.LittleEndian.Uint16(g[4:6]),
		binary.LittleEndian.Uint16(g[6:8]), g[8:10], g[10:16])
}

var (
	// LogoFileGUID is gEfiDefaultBmpLogoGuid, the FFS file that holds the boot logo in
	// EDK2- and AMI Aptio-based firmware.
	LogoFileGUID = MustParseGUID("7BB28B99-61BB-11D5-9A5D-0090273FC14D")
	// LZMASectionGUID is gLzmaCustomDecompressGuid (EDK2 LzmaCustomDecompressLib).
	LZMASectionGUID = MustParseGUID("EE4E5898-3914-4259-9D6E-DC7BD79403CF")
	// LZMAF86SectionGUID is gLzmaF86CustomDecompressGuid (LZMA with x86 branch filter).
	LZMAF86SectionGUID = MustParseGUID("D42AE6BD-1352-4BFB-909A-CA72A6EAE889")
	// BrotliSectionGUID is gBrotliCustomDecompressGuid.
	BrotliSectionGUID = MustParseGUID("3D532050-5CDA-4FD0-879E-0F7F630D5AFB")

	// FFS2GUID and FFS3GUID identify the firmware file system of a volume.
	FFS2GUID = MustParseGUID("8C8CE578-8A3D-4F1C-9935-896185C32DD3")
	FFS3GUID = MustParseGUID("5473C07A-3DCB-4DCA-BD6F-1E9689E7349A")

	AMICapsuleGUID  = MustParseGUID("4A3CA68B-7723-48FB-803D-578CC1FEC44D")
	EFICapsuleGUID  = MustParseGUID("3B6686BD-0D76-4030-B70E-B5519E2FC5A0")
	FMPCapsuleGUID  = MustParseGUID("6DCBD5ED-E82D-4C44-BDA1-7194199AD92A")
	CertRSA2048GUID = MustParseGUID("A7717414-C616-4977-9420-844712A735BF") // EFI_CERT_TYPE_RSA2048_SHA256_GUID
	CertPKCS7GUID   = MustParseGUID("4AAFD29D-68DF-49EE-8AA9-347D375665A7") // EFI_CERT_TYPE_PKCS7_GUID

	padFileGUID = GUID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
)

var guidNames = map[GUID]string{
	LZMASectionGUID:    "LZMA",
	LZMAF86SectionGUID: "LZMA+x86",
	BrotliSectionGUID:  "Brotli",
}
