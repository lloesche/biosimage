// Command biosimage inspects and replaces the boot logo in AMI Aptio / UEFI firmware images.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lloesche/biosimage/uefi"
)

var version = "dev"

const usage = `biosimage - replace the boot logo in AMI Aptio / UEFI firmware images

Usage:
  biosimage info    FIRMWARE
  biosimage extract [-o FILE] FIRMWARE
  biosimage replace [-o FILE] [-compress | -no-compress] [-free-space] [-force] [-animate-dxe] FIRMWARE IMAGE
  biosimage version

FIRMWARE is a BIOS file as downloaded from the vendor (e.g. ASUS .CAP) or a raw
flash dump. IMAGE is a BMP, PNG, JPEG or GIF file. All commands accept
-guid GUID to target a file other than the default logo
(7BB28B99-61BB-11D5-9A5D-0090273FC14D).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "info":
		err = cmdInfo(os.Stdout, args)
	case "extract":
		err = cmdExtract(os.Stdout, args)
	case "replace":
		err = cmdReplace(os.Stdout, args)
	case "version", "-version", "--version":
		fmt.Println("biosimage", version)
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
	default:
		err = usageError(fmt.Sprintf("unknown command %q", cmd))
	}
	var ue usageError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintf(os.Stderr, "biosimage: %s\n\n%s", err, usage)
		os.Exit(2)
	case err != nil:
		fmt.Fprintf(os.Stderr, "biosimage: %s\n", err)
		os.Exit(1)
	}
}

type usageError string

func (e usageError) Error() string { return string(e) }

// parse parses flags that may appear before, between or after positional arguments.
func parse(fs *flag.FlagSet, args []string, want int) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError(err.Error())
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != want {
		return nil, usageError(fmt.Sprintf("%s: expected %d argument(s), got %d", fs.Name(), want, len(pos)))
	}
	return pos, nil
}

func guidFlag(fs *flag.FlagSet) *string {
	return fs.String("guid", uefi.LogoFileGUID.String(), "GUID of the logo file")
}

func parseGUID(s string) (uefi.GUID, error) {
	g, err := uefi.ParseGUID(s)
	if err != nil {
		return g, usageError(err.Error())
	}
	return g, nil
}

func cmdInfo(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	guidStr := guidFlag(fs)
	pos, err := parse(fs, args, 1)
	if err != nil {
		return err
	}
	guid, err := parseGUID(*guidStr)
	if err != nil {
		return err
	}
	buf, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "File:     %s (%d bytes)\n", pos[0], len(buf))
	root := uefi.Parse(buf)
	c := root.Capsule
	if c != nil && c.HeaderSize > 0 {
		fmt.Fprintf(w, "Capsule:  %s, %#x-byte header\n", c.Kind, c.HeaderSize)
	}
	signed := "no"
	if c != nil && c.Signed {
		signed = "YES (" + c.Kind + ")"
	}
	fmt.Fprintf(w, "Signed:   %s\n%s\n", signed, wrap(c.FlashNote(), "          ", 90))
	fmt.Fprintf(w, "Volumes:  %d top-level firmware volume(s)\n", len(uefi.FindVolumes(buf)))
	logos, err := root.FindLogos(guid)
	if err != nil {
		return err
	}
	var formats []string
	for _, f := range root.LogoFormats(logos) {
		formats = append(formats, fmt.Sprintf("%s (%s)", f.Format, f.Reason()))
	}
	fmt.Fprintf(w, "Formats:  %s\n", strings.Join(formats, ", "))
	if free, err := root.FreeSpace(); err == nil {
		fmt.Fprintf(w, "Free:     %d bytes in the main volume at %#x for a logo moved there\n"+
			"          (experimental: replace -free-space)\n", free.Capacity, free.Volume.Offset)
	}
	if note := uefi.DetectBootGuard(buf).Note(); note != "" {
		fmt.Fprintf(w, "BootGuard: %s\n", strings.TrimSpace(wrap(note, "           ", 90)))
	}
	for i, l := range logos {
		fmt.Fprintf(w, "\nLogo %d:   file %s at %#x (%d bytes) in volume at %#x (%d KiB)\n",
			i+1, l.File.GUID, l.File.Offset, l.File.Size, l.Volume.Offset, l.Volume.Length>>10)
		for _, h := range l.Enclosing() {
			if h.Compressed {
				fmt.Fprintf(w, "  Inside:   LZMA-compressed volume in file %s at %#x (%d bytes); the offsets above are within it\n",
					h.File.GUID, h.File.Offset, h.File.Size)
			} else {
				fmt.Fprintf(w, "  Inside:   file %s at %#x\n", h.File.GUID, h.File.Offset)
			}
		}
		fmt.Fprintf(w, "  Stored:   %s\n", l.Storage())
		if l.Image != nil {
			if info, err := uefi.InspectImage(l.Image); err == nil {
				fmt.Fprintf(w, "  Image:    %s %dx%d, %s, %d bytes\n", info.Format, info.Width, info.Height, info.Detail, len(l.Image))
			} else {
				fmt.Fprintf(w, "  Image:    %d bytes (%s)\n", len(l.Image), err)
			}
		}
		fmt.Fprintf(w, "  Capacity: up to %d bytes for a replacement file\n", l.Capacity)
	}
	return nil
}

func cmdExtract(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	out := fs.String("o", "", "output file (default: logo.<ext>)")
	guidStr := guidFlag(fs)
	pos, err := parse(fs, args, 1)
	if err != nil {
		return err
	}
	guid, err := parseGUID(*guidStr)
	if err != nil {
		return err
	}
	buf, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	logos, err := uefi.FindLogos(buf, guid)
	if err != nil {
		return err
	}
	l := logos[0]
	if l.Image == nil {
		return fmt.Errorf("logo is stored in an %s (%s) that cannot be decoded", l.Encoding, l.Detail)
	}
	name := *out
	if name == "" {
		name = "logo.bin"
		if info, err := uefi.InspectImage(l.Image); err == nil {
			name = "logo" + info.Ext
		}
	}
	if err := os.WriteFile(name, l.Image, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s (%d bytes)\n", name, len(l.Image))
	if len(logos) > 1 {
		fmt.Fprintf(w, "note: the image contains %d logo files; extracted the first\n", len(logos))
	}
	return nil
}

func cmdReplace(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("replace", flag.ContinueOnError)
	out := fs.String("o", "", "output file (default: FIRMWARE with -logo added to the name)")
	compress := fs.Bool("compress", false, "store the image LZMA-compressed (default: like the original)")
	noCompress := fs.Bool("no-compress", false, "store the image uncompressed (default: like the original)")
	freeSpace := fs.Bool("free-space", false, "move the logo into the free space of the main volume (experimental)")
	animateDXE := fs.Bool("animate-dxe", false, "experimental animated GIF playback during POST (compatible firmware only)")
	force := fs.Bool("force", false, "continue despite image and Boot Guard warnings")
	guidStr := guidFlag(fs)
	pos, err := parse(fs, args, 2)
	if err != nil {
		return err
	}
	guid, err := parseGUID(*guidStr)
	if err != nil {
		return err
	}
	in, imgPath := pos[0], pos[1]
	dst := *out
	if dst == "" {
		ext := filepath.Ext(in)
		dst = strings.TrimSuffix(in, ext) + "-logo" + ext
	}
	if absIn, absOut := absPath(in), absPath(dst); strings.EqualFold(absIn, absOut) {
		return usageError("refusing to overwrite the input firmware; choose another -o")
	}

	buf, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	root := uefi.Parse(buf)
	logos, err := root.FindLogos(guid)
	if err != nil {
		return err
	}
	img, err := os.ReadFile(imgPath)
	if err != nil {
		return err
	}
	if *animateDXE && (guid != uefi.LogoFileGUID || *compress || *noCompress || *freeSpace) {
		return usageError("-animate-dxe requires the standard logo GUID and cannot be combined with -compress, -no-compress or -free-space")
	}
	if !*animateDXE {
		info, err := uefi.InspectImage(img)
		if err != nil {
			if !*force {
				return fmt.Errorf("%s: %w (use -force to insert it anyway)", imgPath, err)
			}
			fmt.Fprintf(w, "warning: %s: %s\n", imgPath, err)
		} else {
			fmt.Fprintf(w, "Image:  %s %dx%d, %s, %d bytes\n", info.Format, info.Width, info.Height, info.Detail, len(img))
			info.CheckSupported(root.LogoFormats(logos))
			for _, warn := range info.Warnings {
				fmt.Fprintf(w, "warning: %s\n", warn)
			}
			if len(info.Warnings) > 0 && !*force {
				return errors.New("image may not display correctly; fix it or use -force")
			}
		}
	}

	opt := uefi.ReplaceOptions{UseFreeSpace: *freeSpace}
	switch {
	case *compress && *noCompress:
		return usageError("-compress and -no-compress exclude each other")
	case *compress:
		opt.Compression = uefi.CompressLZMA
	case *noCompress:
		opt.Compression = uefi.CompressNone
	}
	var p *uefi.Patch
	if *animateDXE {
		// This path has strict compatibility/protection checks: -force cannot bypass them.
		p, err = root.ReplaceAnimatedLogo(img, guid)
	} else {
		p, err = uefi.ReplaceLogo(buf, img, guid, opt)
	}
	if err != nil {
		return err
	}
	if p.BootGuardWarning != "" {
		if *animateDXE {
			return fmt.Errorf("animation not written: %s", p.BootGuardWarning)
		}
		fmt.Fprintf(w, "warning: %s\n", strings.TrimSpace(wrap(p.BootGuardWarning, "         ", 90)))
		if !*force {
			return errors.New("not written; use -force if you have a recovery path")
		}
	}
	for _, c := range p.Changes {
		fmt.Fprintf(w, "Logo:   volume %#x: file at %#x, %d -> %d bytes, %s, %s\n",
			c.VolumeOffset, c.NewOffset, c.OldSize, c.NewSize, c.Storage, c.Placement)
		for _, r := range c.Recompressed {
			fmt.Fprintf(w, "        %s\n", r)
		}
	}
	if err := os.WriteFile(dst, p.Data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s (verified)\n", dst)
	if *animateDXE {
		fmt.Fprintln(w, "Experimental POST animation added; the first frame is the normal boot logo fallback. Keep USB BIOS FlashBack or another recovery method available.")
	}
	if c := root.Capsule; c != nil && c.Signed {
		fmt.Fprintf(w, "\nnote: the input is signed (%s).\n%s\n", c.Kind, wrap(c.FlashNote(), "", 90))
	}
	return nil
}

// wrap breaks text into lines of at most width characters, each starting with indent.
func wrap(text, indent string, width int) string {
	var lines []string
	line := indent
	for _, word := range strings.Fields(text) {
		if len(line) > len(indent) && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	return strings.Join(append(lines, line), "\n")
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
