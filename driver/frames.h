// BIA2: little-endian header with a play count, BGRA palette, bounded changed rectangles.
#ifndef ANIMLOGO_FRAMES_H
#define ANIMLOGO_FRAMES_H
#include "efi.h"
#define FRAME_HEADER 1036
#define MAX_ANIMATION_BYTES (16U << 20)
typedef struct {
  UINT16 Delay, X, Y, Width, Height;
  const UINT8 *Indices;
} FRAME;
typedef struct {
  UINTN Width, Height, Count;
  UINTN Plays; // 0 loops forever, n plays n times and then holds the last frame
  const UINT8 *Palette;
} ANIMATION;
static UINT16 le16(const UINT8 *p) { return (UINT16)(p[0] | ((UINT16)p[1] << 8)); }
static UINTN parse_frame(const UINT8 *p, FRAME *f) {
  f->Delay=le16(p); f->X=le16(p+2); f->Y=le16(p+4);
  f->Width=le16(p+6); f->Height=le16(p+8); f->Indices=p+12;
  return 12+(UINTN)f->Width*f->Height;
}
// Validate the entire stream before exposing any frame pointer or allocating the canvas.
// Frames may be NULL for a validation-only pass, then populated after allocation.
static BOOLEAN parse_animation(const UINT8 *b, UINTN size, ANIMATION *a, FRAME *frames) {
  if (!b || size<FRAME_HEADER || size>MAX_ANIMATION_BYTES || b[0]!='B' || b[1]!='I' || b[2]!='A' || b[3]!='2') return FALSE;
  a->Width=le16(b+4); a->Height=le16(b+6); a->Count=le16(b+8); a->Plays=le16(b+10); a->Palette=b+12;
  if (!a->Width || !a->Height || a->Width>1920 || a->Height>1080 || !a->Count || a->Count>3000) return FALSE;
  UINTN off=FRAME_HEADER;
  for (UINTN i=0;i<a->Count;i++) {
    if (size-off<12) return FALSE;
    FRAME f; UINTN len=parse_frame(b+off,&f);
    if (f.Delay<10 || f.Delay>6000 || le16(b+off+10) || !f.Width || !f.Height ||
        (UINTN)f.X+f.Width>a->Width || (UINTN)f.Y+f.Height>a->Height || len>size-off) return FALSE;
    if (i==0 && (f.X || f.Y || f.Width!=a->Width || f.Height!=a->Height)) return FALSE;
    if (frames) frames[i]=f;
    off+=len;
  }
  return off==size;
}
#endif
