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

// Progress, if set, is called while large data (1 MiB or more) is LZMA-compressed or
// decompressed, with the operation ("Compressing" or "Decompressing") and the uncompressed
// bytes done so far out of total. Those are the slow steps, e.g. recompressing a main volume.
var Progress func(op string, done, total int)

const (
	progressMin   = 1 << 20
	progressChunk = 256 << 10
)

func report(op string, done, total int) {
	if Progress != nil && total >= progressMin {
		Progress(op, done, total)
	}
}

type lzmaParams struct {
	props byte
	dict  uint32
}

// progressBuffer collects compressed output and reports compression progress by the output
// written so far against the expected size: the encoder buffers a dictionary's worth of input
// (often all of it) before it compresses, so the input consumed says little.
type progressBuffer struct {
	bytes.Buffer
	total, expect, next int
}

func (b *progressBuffer) WriteByte(c byte) error {
	b.tick(1)
	return b.Buffer.WriteByte(c)
}

func (b *progressBuffer) Write(p []byte) (int, error) {
	b.tick(len(p))
	return b.Buffer.Write(p)
}

func (b *progressBuffer) tick(n int) {
	if out := b.Len() + n; out >= b.next {
		b.next = out + progressChunk/8
		report("Compressing", min(int(int64(b.total)*int64(out)/int64(b.expect)), b.total*99/100), b.total)
	}
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
	var out []byte
	chunk := make([]byte, progressChunk)
	for {
		n, err := r.Read(chunk)
		out = append(out, chunk[:n]...)
		report("Decompressing", len(out), int(size))
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if uint64(len(out)) != size {
		return nil, fmt.Errorf("LZMA stream decoded to %d bytes, header says %d", len(out), size)
	}
	return out, nil
}

// lzmaCompress compresses data. expect is the likely compressed size (e.g. that of the stream
// being replaced) for progress reports, or 0 if unknown.
func lzmaCompress(data []byte, p lzmaParams, expect int) ([]byte, error) {
	props, err := lzma.PropertiesForCode(p.props)
	if err != nil {
		return nil, err
	}
	if expect <= 0 {
		expect = len(data)/4 + 1
	}
	buf := &progressBuffer{total: len(data), expect: expect}
	cfg := lzma.WriterConfig{
		Properties:   &props,
		DictCap:      int(p.dict),
		Matcher:      lzma.HashTable4, // BinaryTree is pathologically slow on flat-colour bitmaps
		SizeInHeader: true,
		Size:         int64(len(data)),
		EOSMarker:    false,
	}
	w, err := cfg.NewWriter(buf)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	report("Compressing", len(data), len(data))
	out := buf.Bytes()
	if len(out) < lzmaHeaderSize || out[0] != p.props || binary.LittleEndian.Uint32(out[1:]) != p.dict ||
		binary.LittleEndian.Uint64(out[5:]) != uint64(len(data)) {
		return nil, errors.New("LZMA encoder produced an unexpected header")
	}
	return out, nil
}
