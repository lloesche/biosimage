package uefi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ulikunitz/xz/lzma"
)

// EDK2's LzmaCompress (and AMI's tooling) emit the classic .lzma layout: 1 byte of
// lc/lp/pb properties, a 4-byte dictionary size, an 8-byte uncompressed size and the
// raw stream WITHOUT an end-of-stream marker.
const (
	lzmaHeaderSize   = 13
	lzmaDefaultProps = 0x5D // lc=3 lp=0 pb=2
	lzmaDefaultDict  = 1 << 24
	lzmaMaxOutput    = 256 << 20
	// lzmaMaxDict bounds the dictionary a stream may ask for; the decoder allocates it
	// before reading any data (GHSA-jc7w-c686-c4v9). Firmware uses at most a few MiB.
	lzmaMaxDict = 64 << 20
)

type lzmaParams struct {
	props byte
	dict  uint32
}

func lzmaDecompress(stream []byte) ([]byte, error) {
	if len(stream) < lzmaHeaderSize {
		return nil, errors.New("truncated LZMA header")
	}
	size := binary.LittleEndian.Uint64(stream[5:])
	if size > lzmaMaxOutput {
		return nil, fmt.Errorf("implausible LZMA output size %d", size)
	}
	if dict := binary.LittleEndian.Uint32(stream[1:]); dict > lzmaMaxDict {
		return nil, fmt.Errorf("implausible LZMA dictionary size %d", dict)
	}
	r, err := lzma.ReaderConfig{DictCap: lzmaMaxDict}.NewReader(bytes.NewReader(stream))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if uint64(len(out)) != size {
		return nil, fmt.Errorf("LZMA stream decoded to %d bytes, header says %d", len(out), size)
	}
	return out, nil
}

func lzmaCompress(data []byte, p lzmaParams) ([]byte, error) {
	props, err := lzma.PropertiesForCode(p.props)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	cfg := lzma.WriterConfig{
		Properties:   &props,
		DictCap:      int(p.dict),
		Matcher:      lzma.HashTable4, // BinaryTree is pathologically slow on flat-colour bitmaps
		SizeInHeader: true,
		Size:         int64(len(data)),
		EOSMarker:    false,
	}
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(out) < lzmaHeaderSize || out[0] != p.props || binary.LittleEndian.Uint32(out[1:]) != p.dict ||
		binary.LittleEndian.Uint64(out[5:]) != uint64(len(data)) {
		return nil, errors.New("LZMA encoder produced an unexpected header")
	}
	return out, nil
}
