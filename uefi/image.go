package uefi

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ImageInfo describes a logo image and how well it matches what AMI firmware decodes.
type ImageInfo struct {
	Format   string // "BMP", "PNG", "JPEG", "GIF"
	Ext      string
	Width    int
	Height   int
	Detail   string
	Frames   int // more than 1 for an animated GIF
	Warnings []string
}

// Recommended maximum logo size; larger logos are not shown by some AMI builds.
const maxLogoWidth, maxLogoHeight = 800, 600

// InspectImage identifies a BMP/PNG/JPEG/GIF image and flags properties that AMI's
// built-in decoders are known not to handle.
func InspectImage(b []byte) (*ImageInfo, error) {
	var (
		info *ImageInfo
		err  error
	)
	switch {
	case len(b) >= 2 && string(b[:2]) == "BM":
		info, err = inspectBMP(b)
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		info, err = inspectPNG(b)
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		info, err = inspectJPEG(b)
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		info, err = inspectGIF(b)
	default:
		return nil, errors.New("unknown image format (expected BMP, PNG, JPEG or GIF)")
	}
	if err != nil {
		return nil, err
	}
	info.Frames = max(info.Frames, 1)
	if info.Width > maxLogoWidth || info.Height > maxLogoHeight {
		info.Warnings = append(info.Warnings, fmt.Sprintf("larger than %dx%d; some firmware refuses to show such logos", maxLogoWidth, maxLogoHeight))
	}
	return info, nil
}

func inspectBMP(b []byte) (*ImageInfo, error) {
	if len(b) < 34 {
		return nil, errors.New("truncated BMP header")
	}
	w := int(int32(binary.LittleEndian.Uint32(b[18:])))
	h := int(int32(binary.LittleEndian.Uint32(b[22:])))
	bpp := binary.LittleEndian.Uint16(b[28:])
	comp := binary.LittleEndian.Uint32(b[30:])
	info := &ImageInfo{Format: "BMP", Ext: ".bmp", Width: w, Height: abs(h), Detail: fmt.Sprintf("%d bpp", bpp)}
	if bpp != 1 && bpp != 4 && bpp != 8 && bpp != 24 {
		info.Warnings = append(info.Warnings, fmt.Sprintf("%d bpp BMP; use 24-bit (or 4/8-bit palette)", bpp))
	}
	if comp != 0 {
		info.Warnings = append(info.Warnings, "compressed BMP; use uncompressed (BI_RGB)")
	}
	if h < 0 {
		info.Warnings = append(info.Warnings, "top-down BMP (negative height); use bottom-up")
	}
	return info, nil
}

func inspectPNG(b []byte) (*ImageInfo, error) {
	if len(b) < 29 || string(b[12:16]) != "IHDR" {
		return nil, errors.New("truncated PNG header")
	}
	depth, ctype, interlace := b[24], b[25], b[28]
	info := &ImageInfo{
		Format: "PNG", Ext: ".png",
		Width: int(binary.BigEndian.Uint32(b[16:])), Height: int(binary.BigEndian.Uint32(b[20:])),
		Detail: fmt.Sprintf("%d-bit, colour type %d", depth, ctype),
	}
	if ctype == 4 || ctype == 6 {
		info.Warnings = append(info.Warnings, "has an alpha channel; flatten it onto a background colour")
	}
	if interlace != 0 {
		info.Warnings = append(info.Warnings, "interlaced; save without interlacing")
	}
	return info, nil
}

func inspectJPEG(b []byte) (*ImageInfo, error) {
	info := &ImageInfo{Format: "JPEG", Ext: ".jpg"}
	for p := 2; p+4 <= len(b); {
		if b[p] != 0xFF {
			return nil, errors.New("corrupt JPEG marker stream")
		}
		m := b[p+1]
		if m == 0xFF {
			p++
			continue
		}
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			p += 2
			continue
		}
		n := int(binary.BigEndian.Uint16(b[p+2:]))
		if n < 2 || p+2+n > len(b) {
			return nil, errors.New("truncated JPEG segment")
		}
		seg := b[p+4 : p+2+n]
		switch {
		case m == 0xE0 && len(seg) >= 12 && string(seg[:5]) == "JFIF\x00":
			if xd, yd := binary.BigEndian.Uint16(seg[8:]), binary.BigEndian.Uint16(seg[10:]); xd != yd {
				info.Warnings = append(info.Warnings, "non-square pixel density; use 1:1")
			}
		case m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC:
			if len(seg) < 6 {
				return nil, errors.New("truncated JPEG frame header")
			}
			info.Height, info.Width = int(binary.BigEndian.Uint16(seg[1:])), int(binary.BigEndian.Uint16(seg[3:]))
			switch m {
			case 0xC0, 0xC1:
				info.Detail = "baseline"
			case 0xC2:
				info.Detail = "progressive"
				info.Warnings = append(info.Warnings, "progressive JPEG is not supported; save as baseline")
			default:
				info.Detail = fmt.Sprintf("SOF%d", m-0xC0)
				info.Warnings = append(info.Warnings, "lossless/arithmetic-coded JPEG is not supported; save as baseline")
			}
			return info, nil
		case m == 0xDA:
			return nil, errors.New("JPEG has no frame header")
		}
		p += 2 + n
	}
	return nil, errors.New("JPEG has no frame header")
}

func inspectGIF(b []byte) (*ImageInfo, error) {
	if len(b) < 13 {
		return nil, errors.New("truncated GIF header")
	}
	info := &ImageInfo{Format: "GIF", Ext: ".gif",
		Width: int(binary.LittleEndian.Uint16(b[6:])), Height: int(binary.LittleEndian.Uint16(b[8:]))}
	p := 13
	if b[10]&0x80 != 0 {
		p += 3 << (b[10]&7 + 1)
	}
	frames, fastFrames := 0, 0
	skipBlocks := func() error {
		for {
			if p >= len(b) {
				return errors.New("truncated GIF data")
			}
			n := int(b[p])
			p += 1 + n
			if n == 0 {
				return nil
			}
		}
	}
	for p < len(b) {
		switch b[p] {
		case 0x3B:
			p = len(b)
		case 0x21:
			if p+2 > len(b) {
				return nil, errors.New("truncated GIF extension")
			}
			if b[p+1] == 0xF9 && p+8 <= len(b) {
				if d := binary.LittleEndian.Uint16(b[p+4:]); d > 0 && d < MinFrameDelay {
					fastFrames++
				}
			}
			p += 2
			if err := skipBlocks(); err != nil {
				return nil, err
			}
		case 0x2C:
			if p+10 > len(b) {
				return nil, errors.New("truncated GIF image descriptor")
			}
			flags := b[p+9]
			p += 10
			if flags&0x80 != 0 {
				p += 3 << (flags&7 + 1)
			}
			p++ // LZW minimum code size
			if err := skipBlocks(); err != nil {
				return nil, err
			}
			frames++
		default:
			return nil, fmt.Errorf("unexpected GIF block %#x", b[p])
		}
	}
	info.Detail, info.Frames = fmt.Sprintf("%d frame(s)", frames), frames
	if frames > 1 {
		info.Warnings = append(info.Warnings,
			"animated: AMI firmware shows at most 10 frames/s, ignores disposal methods and loops forever; some boards show only the first frame")
		if fastFrames > 0 {
			info.Warnings = append(info.Warnings, "frame delays below 100 ms will be stretched to 100 ms")
		}
	}
	return info, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
