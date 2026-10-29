package uefi

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// tseFileGUID is AMI's Text Setup Environment (AMITSE), the module that draws the boot logo.
var tseFileGUID = MustParseGUID("B1DA0ADF-4F77-4070-A88E-BFFE1C60529A")

// LogoFormat is an image format a new logo can be stored in.
type LogoFormat struct {
	Format   string // "BMP", "JPEG", "PNG" or "GIF"
	Current  bool   // the current logo uses it
	Detected bool   // a decoder for it was found in the firmware
}

// decoderFingerprints are data that AMITSE's image decoders contain, found in the firmware of
// all tested vendors. BMP is always supported.
var decoderFingerprints = map[string][][]byte{
	// The first entries of the JPEG zig-zag order table, as 16-bit values.
	"JPEG": {u16le(0, 1, 5, 6, 14, 15, 27, 28, 2, 4, 7, 13, 16, 26, 29, 42)},
	// The header chunk type, compared as a string or as a big-endian constant.
	"PNG": {[]byte("IHDR"), []byte("RDHI")},
	"GIF": {[]byte("GIF8")},
}

func u16le(v ...uint16) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], x)
	}
	return b
}

// LogoFormats returns the formats a new logo can use, in the order BMP, JPEG, PNG, GIF: BMP and
// the format of the current logo always, the others if AMITSE has a decoder for them. Finding
// decoders is best effort: it looks for their tables and signatures, and may have to
// decompress the whole image.
func (n *Node) LogoFormats(logos []*Logo) []LogoFormat {
	return logoFormats(logos, n.decoders())
}

// CurrentFormats returns the formats a new logo can use without looking for decoders: BMP and
// the format of the current logo. It is quick, unlike Node.LogoFormats.
func CurrentFormats(logos []*Logo) []LogoFormat {
	return logoFormats(logos, nil)
}

func logoFormats(logos []*Logo, detected map[string]bool) []LogoFormat {
	current := map[string]bool{}
	for _, l := range logos {
		if info, err := InspectImage(l.Image); err == nil {
			current[info.Format] = true
		}
	}
	var out []LogoFormat
	for _, f := range []string{"BMP", "JPEG", "PNG", "GIF"} {
		if f == "BMP" || current[f] || detected[f] {
			out = append(out, LogoFormat{Format: f, Current: current[f], Detected: detected[f]})
		}
	}
	return out
}

// Reason says why the format is offered: "current logo", "decoder found" or "always" (BMP).
func (f LogoFormat) Reason() string {
	switch {
	case f.Current:
		return "current logo"
	case f.Detected:
		return "decoder found"
	}
	return "always"
}

// CheckSupported adds a warning to info if its format is not among the formats the firmware
// can show.
func (info *ImageInfo) CheckSupported(formats []LogoFormat) {
	for _, f := range formats {
		if f.Format == info.Format {
			return
		}
	}
	info.Warnings = append(info.Warnings, fmt.Sprintf("no %s decoder was found in this BIOS; it may not show the logo", info.Format))
}

// decoders returns the image formats AMITSE has a decoder for; none if it is not found.
func (n *Node) decoders() map[string]bool {
	tse := n.tse()
	if tse == nil {
		n.expandAll()
		tse = n.tse()
	}
	found := map[string]bool{}
	if tse == nil {
		return found
	}
	tse.Walk(func(c *Node) bool {
		if c.Kind == KindSection && (c.Section.Type == SectionPE32 || c.Section.Type == SectionTE) {
			for f, prints := range decoderFingerprints {
				for _, p := range prints {
					found[f] = found[f] || bytes.Contains(c.Section.Body(), p)
				}
			}
		}
		return true
	})
	return found
}

func (n *Node) tse() *Node {
	var tse *Node
	n.Walk(func(c *Node) bool {
		if c.Kind == KindFile && c.File.GUID == tseFileGUID {
			tse = c
		}
		return tse == nil
	})
	return tse
}
