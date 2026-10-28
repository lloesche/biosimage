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
  biosimage replace [-o FILE] [-no-compress] [-force] FIRMWARE IMAGE
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
	if c := uefi.DetectCapsule(buf); c != nil {
		signed := "unsigned"
		if c.Signed {
			signed = "signed - a modified file will fail signature checks"
		}
		fmt.Fprintf(w, "Capsule:  %s, %#x-byte header, %s\n", c.Kind, c.HeaderSize, signed)
	}
	fmt.Fprintf(w, "Volumes:  %d top-level firmware volume(s)\n", len(uefi.FindVolumes(buf)))
	logos, err := uefi.FindLogos(buf, guid)
	if err != nil {
		return err
	}
	for i, l := range logos {
		fmt.Fprintf(w, "\nLogo %d:   file %s at %#x (%d bytes) in volume at %#x (%d KiB)\n",
			i+1, l.File.GUID, l.File.Offset, l.File.Size, l.Volume.Offset, l.Volume.Length>>10)
		enc := l.Encoding.String()
		if l.Detail != "" {
			enc += ": " + l.Detail
		}
		fmt.Fprintf(w, "  Stored:   %s\n", enc)
		if l.Name != "" {
			fmt.Fprintf(w, "  Name:     %s\n", l.Name)
		}
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
	noCompress := fs.Bool("no-compress", false, "store the image uncompressed")
	force := fs.Bool("force", false, "continue despite image warnings")
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

	img, err := os.ReadFile(imgPath)
	if err != nil {
		return err
	}
	info, err := uefi.InspectImage(img)
	if err != nil {
		if !*force {
			return fmt.Errorf("%s: %w (use -force to insert it anyway)", imgPath, err)
		}
		fmt.Fprintf(w, "warning: %s: %s\n", imgPath, err)
	} else {
		fmt.Fprintf(w, "Image:  %s %dx%d, %s, %d bytes\n", info.Format, info.Width, info.Height, info.Detail, len(img))
		for _, warn := range info.Warnings {
			fmt.Fprintf(w, "warning: %s\n", warn)
		}
		if len(info.Warnings) > 0 && !*force {
			return errors.New("image may not display correctly; fix it or use -force")
		}
	}

	buf, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	patched, changes, err := uefi.ReplaceLogo(buf, img, guid, uefi.ReplaceOptions{NoCompress: *noCompress})
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Fprintf(w, "Logo:   volume %#x: file at %#x, %d -> %d bytes, %s, %s\n",
			c.VolumeOffset, c.NewOffset, c.OldSize, c.NewSize, c.Storage, c.Placement)
	}
	if err := os.WriteFile(dst, patched, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s (verified)\n", dst)
	if c := uefi.DetectCapsule(buf); c != nil && c.Signed {
		fmt.Fprintln(w, "\nnote: the input is a signed capsule. The modified file no longer matches its signature,")
		fmt.Fprintln(w, "so the BIOS's built-in updater (e.g. ASUS EZ Flash) will reject it. Flash it with a")
		fmt.Fprintln(w, "method that skips verification, such as ASUS USB BIOS FlashBack.")
	}
	return nil
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
