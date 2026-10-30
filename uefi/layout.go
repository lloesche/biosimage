package uefi

import (
	"bytes"
	"errors"
	"fmt"
)

// Placement says how a rebuilt file was put back into its volume.
type Placement int

const (
	PlacedLast      Placement = iota // last file, grown or shrunk in place
	PlacedInPlace                    // same slot, exact fit
	PlacedPadded                     // same slot, remainder filled with a pad file
	PlacedMoved                      // moved after the last file, old slot turned into a pad file
	PlacedShifted                    // grown in place, the following files moved towards the end
	PlacedFreeSpace                  // added after the last file of the main volume, old file padded
)

func (p Placement) String() string {
	return [...]string{
		"rewritten in place (last file in volume)",
		"rewritten in place",
		"rewritten in place, remainder padded",
		"moved to end of volume, old slot padded",
		"rewritten in place, following files moved",
		"moved to the free space of the main volume, old logo file padded",
	}[p]
}

// span is a byte range [pos, pos+size).
type span struct{ pos, size int }

// move relocates a file's bytes.
type move struct{ from, to, size int }

// layout is a plan for putting a new file of a given size into a volume. Planning only looks
// at positions and free space; apply does the writing.
type layout struct {
	placement Placement
	pos       int  // where the new file goes
	clear     span // erased before anything is written
	pads      []span
	moves     []move
}

// errTooBig reports that a rebuilt file does not fit into its volume.
var errTooBig = errors.New("not enough space in the volume")

// volumeSlot describes the file being replaced and its surroundings.
type volumeSlot struct {
	buf   []byte
	v     *Volume
	files []File
	idx   int // index of the file being replaced
	used  int // end of the last file
}

func newVolumeSlot(buf []byte, v *Volume, old File) (*volumeSlot, error) {
	files, used, err := v.Files(buf)
	if err != nil {
		return nil, err
	}
	for i, f := range files {
		if f.Offset == old.Offset {
			return &volumeSlot{buf: buf, v: v, files: files, idx: i, used: used}, nil
		}
	}
	return nil, errors.New("file to replace not found in its volume")
}

func (s *volumeSlot) old() File    { return s.files[s.idx] }
func (s *volumeSlot) slotEnd() int { return s.v.alignUp(s.old().End()) }
func (s *volumeSlot) last() bool   { return s.idx == len(s.files)-1 }
func (s *volumeSlot) erased(from, to int) bool {
	return isErased(s.buf[from:max(from, min(to, s.v.End()))], s.v.EraseByte())
}

// plan returns the cheapest layout for a new file of the given size: keep the file where it is
// if possible, else move it to the free space, else move the files after it.
func (s *volumeSlot) plan(size int) (layout, error) {
	for _, try := range []func(int) (layout, bool){s.planLast, s.planInPlace, s.planMove, s.planShift} {
		if l, ok := try(size); ok {
			return l, nil
		}
	}
	c := s.capacity()
	if size < c {
		return layout{}, fmt.Errorf("%w: a new logo file of %d bytes does not fit the alignment of the following files (one of %d bytes would); use a slightly different image",
			errTooBig, size, c)
	}
	return layout{}, fmt.Errorf("%w: new logo file needs %d bytes but only %d are available; use a smaller or simpler image",
		errTooBig, size, c)
}

func (s *volumeSlot) planLast(size int) (layout, bool) {
	old := s.old()
	end := old.Offset + size
	if !s.last() || end > s.v.End() || !s.erased(s.slotEnd(), s.v.alignUp(end)) {
		return layout{}, false
	}
	return layout{placement: PlacedLast, pos: old.Offset, clear: span{old.Offset, max(end, s.slotEnd()) - old.Offset}}, true
}

func (s *volumeSlot) planInPlace(size int) (layout, bool) {
	old := s.old()
	end := s.v.alignUp(old.Offset + size)
	gap := s.slotEnd() - end
	if s.last() || (gap != 0 && gap < ffsHeaderSize) {
		return layout{}, false
	}
	l := layout{placement: PlacedInPlace, pos: old.Offset, clear: span{old.Offset, s.slotEnd() - old.Offset}}
	if gap > 0 {
		l.placement, l.pads = PlacedPadded, []span{{end, gap}}
	}
	return l, true
}

func (s *volumeSlot) planMove(size int) (layout, bool) {
	old := s.old()
	pos := alignedFilePos(s.v, s.used, ffsHeaderSize, max(dataAlignment(old.Attributes), 8))
	if pos+size > s.v.End() || !s.erased(s.used, s.v.alignUp(pos+size)) {
		return layout{}, false
	}
	l := layout{placement: PlacedMoved, pos: pos, pads: []span{{old.Offset, s.slotEnd() - old.Offset}}}
	if pos > s.used {
		l.pads = append(l.pads, span{s.used, pos - s.used})
	}
	return l, true
}

// planShift grows the file in place and moves the following files (never ones marked as
// fixed) towards the end, keeping their data alignment and dropping old pad files.
func (s *volumeSlot) planShift(size int) (layout, bool) {
	old := s.old()
	if old.Offset+size > s.v.End() || !s.erased(s.used, s.v.End()) {
		return layout{}, false
	}
	l := layout{placement: PlacedShifted, pos: old.Offset, clear: span{old.Offset, s.used - old.Offset}}
	pos := s.v.alignUp(old.Offset + size)
	for _, f := range s.files[s.idx+1:] {
		if f.Type == FileTypePad {
			continue
		}
		if f.Attributes&ffsAttribFixed != 0 {
			return layout{}, false
		}
		at := alignedFilePos(s.v, pos, f.HeaderSize, max(dataAlignment(f.Attributes), 8))
		if at > pos {
			l.pads = append(l.pads, span{pos, at - pos})
		}
		l.moves = append(l.moves, move{f.Offset, at, f.Size})
		pos = s.v.alignUp(at + f.Size)
	}
	return l, pos <= s.v.End()
}

// capacity is the largest new file (header included) that some layout accepts.
func (s *volumeSlot) capacity() int {
	best := 0
	for _, try := range []func(int) (layout, bool){s.planLast, s.planMove} {
		// These accept every size up to their limit, so search for the limit.
		best = max(best, largest(ffsHeaderSize, s.v.End()-s.old().Offset, try))
	}
	if _, ok := s.planInPlace(s.slotEnd() - s.old().Offset); ok {
		best = max(best, s.slotEnd()-s.old().Offset)
	}
	return max(best, s.shiftCapacity())
}

// shiftCapacity returns the largest size planShift accepts. Shifting is not monotonic in the
// size: a following file's data alignment can need a pad file in front of it, so a slightly
// larger file may fit where a smaller one does not. Each following file needs at most its
// alignment plus a pad header of extra space, so the largest size is within that much of the
// bound left by the following files themselves.
func (s *volumeSlot) shiftCapacity() int {
	off, top, slack := s.old().Offset, s.v.End(), 0
	for _, f := range s.files[s.idx+1:] {
		if f.Type != FileTypePad {
			top -= alignUp(f.Size, 8)
			slack += max(dataAlignment(f.Attributes), 8) + ffsHeaderSize
		}
	}
	for end := top; end >= max(off+ffsHeaderSize, top-slack); end -= 8 {
		if _, ok := s.planShift(end - off); ok {
			return end - off
		}
	}
	return 0
}

// largest returns the largest size in [lo, hi] that fits, assuming fitting is monotonic.
func largest(lo, hi int, fits func(int) (layout, bool)) int {
	if _, ok := fits(lo); !ok {
		return 0
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, ok := fits(mid); ok {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// apply writes nf according to the layout.
func (l layout) apply(buf []byte, erase byte, nf []byte) {
	srcs := make([][]byte, len(l.moves))
	for i, m := range l.moves {
		srcs[i] = bytes.Clone(buf[m.from : m.from+m.size])
	}
	for i := l.clear.pos; i < l.clear.pos+l.clear.size; i++ {
		buf[i] = erase
	}
	for _, p := range l.pads {
		copy(buf[p.pos:], buildPadFile(p.size, erase))
	}
	for i, m := range l.moves {
		copy(buf[m.to:], srcs[i])
	}
	copy(buf[l.pos:], nf)
}

// alignedFilePos returns the first position at or after pos where a file with the given
// header size has its data aligned, leaving either no gap or one large enough for a pad file.
func alignedFilePos(v *Volume, pos, headerSize, align int) int {
	at := pos
	for (at+headerSize-v.Offset)%align != 0 || (at != pos && at-pos < ffsHeaderSize) {
		at += 8
	}
	return at
}

func isErased(b []byte, erase byte) bool {
	for _, c := range b {
		if c != erase {
			return false
		}
	}
	return true
}
