package uefi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
)

// jfifHeader is a JFIF APP0 segment (version 1.1, 1:1 pixel aspect), which Go's JPEG encoder
// does not write but which the logo JPEGs of all tested vendors have.
var jfifHeader = []byte{0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}

// EncodeImage encodes an opaque image in a form that AMI's decoders handle: a 24-bit bottom-up
// BMP, a baseline JPEG of the given quality (1–100), an 8-bit RGB PNG, or a GIF (see EncodeGIF).
func EncodeImage(img image.Image, format string, quality int) ([]byte, error) {
	var buf bytes.Buffer
	switch format {
	case "BMP":
		return encodeBMP(img), nil
	case "JPEG":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		b := buf.Bytes()
		return append(append(append([]byte{}, b[:2]...), jfifHeader...), b[2:]...), nil
	case "PNG":
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, opaque(img)); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "GIF":
		b, _, err := EncodeGIF([]image.Image{img}, nil, 0, true)
		return b, err
	}
	return nil, fmt.Errorf("cannot encode %s", format)
}

// opaque returns img with alpha forced to 255, so that the PNG encoder writes RGB.
func opaque(img image.Image) image.Image {
	r := img.Bounds()
	out := image.NewRGBA(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			i := out.PixOffset(x, y)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = byte(cr>>8), byte(cg>>8), byte(cb>>8), 0xFF
		}
	}
	return out
}

func encodeBMP(img image.Image) []byte {
	r := img.Bounds()
	w, h := r.Dx(), r.Dy()
	stride := (w*3 + 3) &^ 3
	b := make([]byte, 54+stride*h)
	copy(b, "BM")
	binary.LittleEndian.PutUint32(b[2:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[10:], 54)
	binary.LittleEndian.PutUint32(b[14:], 40)
	binary.LittleEndian.PutUint32(b[18:], uint32(w))
	binary.LittleEndian.PutUint32(b[22:], uint32(h))
	binary.LittleEndian.PutUint16(b[26:], 1)
	binary.LittleEndian.PutUint16(b[28:], 24)
	binary.LittleEndian.PutUint32(b[34:], uint32(stride*h))
	binary.LittleEndian.PutUint32(b[38:], 2835) // 72 dpi
	binary.LittleEndian.PutUint32(b[42:], 2835)
	for y := 0; y < h; y++ {
		row := 54 + (h-1-y)*stride
		for x := 0; x < w; x++ {
			cr, cg, cb, _ := img.At(r.Min.X+x, r.Min.Y+y).RGBA()
			b[row+3*x], b[row+3*x+1], b[row+3*x+2] = byte(cb>>8), byte(cg>>8), byte(cr>>8)
		}
	}
	return b
}
