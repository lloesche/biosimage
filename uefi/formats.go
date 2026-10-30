package uefi

import "fmt"

// tseFileGUID is AMI's Text Setup Environment (AMITSE), the module that draws the boot logo.
var tseFileGUID = MustParseGUID("B1DA0ADF-4F77-4070-A88E-BFFE1C60529A")

// LogoFormat is an image format a new logo can be stored in.
type LogoFormat struct {
	Format   string // "BMP", "JPEG", "PNG" or "GIF"
	Current  bool   // the current logo uses it
	Detected bool   // the boot logo code reads it (TSECode.LogoFormats)
}

// LogoFormats returns the formats a new logo can use, in the order BMP, JPEG, PNG, GIF: BMP and
// the format of the current logo always, the others if AMITSE's boot logo code reads them
// (TSECode.LogoFormats). It may have to decompress the whole image.
func (n *Node) LogoFormats(logos []*Logo) []LogoFormat {
	detected := map[string]bool{}
	for _, f := range n.TSECode().LogoFormats {
		detected[f] = true
	}
	return logoFormats(logos, detected)
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

// Reason says why the format is offered: "current logo", "logo code reads it" or "always"
// (BMP).
func (f LogoFormat) Reason() string {
	switch {
	case f.Current:
		return "current logo"
	case f.Detected:
		return "logo code reads it"
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
	info.Warnings = append(info.Warnings, fmt.Sprintf("this BIOS's boot logo code was not found to read %s; it will most likely show no logo", info.Format))
}

// tse returns AMITSE's file, unpacking compressed sections if needed, or nil.
func (n *Node) tse() *Node {
	if tse := n.findTSE(); tse != nil {
		return tse
	}
	n.expandAll()
	return n.findTSE()
}

func (n *Node) findTSE() *Node {
	var tse *Node
	n.Walk(func(c *Node) bool {
		if c.Kind == KindFile && c.File.GUID == tseFileGUID {
			tse = c
		}
		return tse == nil
	})
	return tse
}
