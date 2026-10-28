# biosimage

Replace the boot logo in AMI Aptio / UEFI firmware images, such as the `.CAP` BIOS files
ASUS publishes for its mainboards.

**Use it in your browser: <https://lloesche.github.io/biosimage/>**. Drop in your BIOS file
and an image (PNG, SVG, JPEG, ...), position the logo, and download the patched BIOS.
Everything runs locally in WebAssembly; nothing is uploaded.

There is also a command-line tool:

```console
$ biosimage info A5766.CAP
File:     A5766.CAP (33558528 bytes)
Capsule:  AMI Aptio capsule, 0x1000-byte header, signed - a modified file will fail signature checks
Volumes:  5 top-level firmware volume(s)

Logo 1:   file 7BB28B99-61BB-11D5-9A5D-0090273FC14D at 0x1b830a0 (4729 bytes) in volume at 0x1b81000 (256 KiB)
  Stored:   LZMA-compressed section
  Name:     Logo.bmp
  Image:    BMP 672x378, 24 bpp, 762104 bytes
  Capacity: up to 253792 bytes for a replacement file

$ biosimage replace A5766.CAP mylogo.bmp -o A5766-mylogo.CAP
```

> [!WARNING]
> Flashing modified firmware can brick your board. Only use this if you have a recovery
> path (e.g. ASUS USB BIOS FlashBack, a dual BIOS, or an SPI programmer) and keep the
> original file. You use this tool at your own risk.

## Install

Download a binary from the [releases](https://github.com/lloesche/biosimage/releases), or

```sh
go install github.com/lloesche/biosimage@latest
```

## Usage

```
biosimage info    FIRMWARE                       show where and how the logo is stored
biosimage extract [-o FILE] FIRMWARE             save the current logo image
biosimage replace [-o FILE] [-no-compress] [-force] FIRMWARE IMAGE
```

- `replace` writes `FIRMWARE-logo.<ext>` unless `-o` is given; it never overwrites the input.
- `-no-compress` stores the image uncompressed even if the original was LZMA-compressed.
- `-force` inserts the image despite warnings (see below).
- `-guid` targets a file other than the standard logo file.

## Preparing the image

AMI's setup module (AMITSE) contains decoders for BMP, JPEG, PNG and GIF and picks one by
looking at the file's first bytes. For the most predictable result:

- Use a **24-bit uncompressed BMP** on a black background, about the size of the original
  logo (`biosimage info` shows it). Stay at or below 800x600.
- PNG: 8- or 16-bit, **no alpha channel**, not interlaced.
- JPEG: **baseline** (not progressive), 1:1 pixel density.
- GIF: animated GIFs are played by AMITSE (min. 100 ms per frame, no disposal methods,
  endless loop), but a vendor's boot path may show only the first frame.

`biosimage replace` checks for these and refuses images it expects to fail unless you pass
`-force`.

With ImageMagick, a transparent logo can be centred on a black 672x378 canvas like this:

```sh
magick logo.png -background black -alpha remove -alpha off -resize 300x300 \
       -gravity center -extent 672x378 -type TrueColor BMP3:mylogo.bmp
```

Size limit: the logo lives in a small firmware volume (256 KiB on the ASUS board above) and the
stored, compressed file must fit. Flat-colour logos compress to a few dozen KiB; photos may not
fit.

## Flashing

Vendor BIOS files are usually **signed capsules**. Changing the logo invalidates the
signature, so the BIOS's own updater (ASUS EZ Flash, MSI M-Flash, ...) will reject the file.
You need a flashing method that does not verify the signature:

- **ASUS:** USB BIOS FlashBack. Rename the file with the `BIOSRenamer.exe` that comes with the
  BIOS download (do this on a USB stick that contains only the modified `.CAP`, so the
  renamer cannot pick up the original), plug the stick into the FlashBack port and press the
  button.
- Other vendors: check whether a "flashback"-style recovery or an SPI programmer is available.

FlashBack writes the whole chip, so expect your BIOS settings to be reset.

## How it works

1. Find all top-level firmware volumes (`_FVH` headers with a valid checksum).
2. Find the FFS file `7BB28B99-61BB-11D5-9A5D-0090273FC14D` (`gEfiDefaultBmpLogoGuid`), which
   EDK2 and AMI use for the boot logo, and decode it. Supported layouts: a raw file, an
   uncompressed `EFI_SECTION_RAW`, or a raw section inside an LZMA GUID-defined section
   (`EE4E5898-3914-4259-9D6E-DC7BD79403CF`). Other encapsulations (Tiano compression, Brotli)
   are replaced with an uncompressed section.
3. Rebuild the file with the new image, keeping the other sections (e.g. the name) and the
   original LZMA properties. The LZMA stream is written in the same form as EDK2's
   `LzmaCompress`: explicit size, no end-of-stream marker.
4. Put it back into the volume: in place if it is the last file or still fits its slot
   (remainder filled with a pad file), otherwise the old slot becomes a pad file and the new
   file is appended to the free space.
5. Re-parse the volume and verify all file checksums, that all other files are byte-identical,
   that nothing outside the volume changed and that the logo decodes to the new image.

Not supported: logos inside compressed volumes (common on some Intel boards and other
vendors), Intel Boot Guard-protected regions, and more than one logo file per volume.

## Tested with

| Board | BIOS | Result |
|---|---|---|
| ASUS TUF GAMING B850I WIFI NEO | 1804 (`A5766.CAP`) | works, flashed via USB BIOS FlashBack |

Reports for other boards are welcome; please include the output of `biosimage info`.

## Development

```sh
go test ./...
BIOSIMAGE_FIRMWARE=/path/to/bios.cap go test ./...   # also run against a real image
```

The test suite builds synthetic firmware volumes, so no vendor binaries are needed or
included.

### Web app

The web app in [`web/`](web) is plain HTML/CSS/JavaScript; the firmware code is the same Go
package compiled to WebAssembly ([`cmd/wasm`](cmd/wasm)). To run it locally:

```sh
go run ./cmd/buildweb -serve    # builds web/biosimage.wasm and serves http://localhost:8080
```

### Releases and deployment

Pushing a `v*` tag runs GoReleaser, which publishes the CLI binaries plus `biosimage.wasm`
and `wasm_exec.js` as release assets. The Pages workflow then deploys `web/` together with
the WebAssembly module of the latest release (it also runs when `web/` changes on `main`).
Enable it once under *Settings → Pages → Source: GitHub Actions*.

## License

MIT, see [LICENSE](LICENSE).
