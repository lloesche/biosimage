#ifndef ANIMLOGO_SAMPLE_H
#define ANIMLOGO_SAMPLE_H
// Test fixture: full grey first frame, yellow changed rectangle second frame.
static UINT8 sample[FRAME_HEADER+12+160*80+12+16*48];
static void put16(UINT8 *p,UINT16 v) { p[0]=(UINT8)v;p[1]=(UINT8)(v>>8); }
static void make_sample(void) {
  for (UINTN i=0;i<sizeof(sample);i++) sample[i]=0;
  sample[0]='B';sample[1]='I';sample[2]='A';sample[3]='1';
  put16(sample+4,160);put16(sample+6,80);sample[8]=2;
  sample[12]=32;sample[13]=32;sample[14]=32;
  sample[16]=0x20;sample[17]=0xc0;sample[18]=0xff;
  UINT8 *f=sample+FRAME_HEADER;
  put16(f,10);put16(f+6,160);put16(f+8,80);
  f+=12+160*80;
  put16(f,10);put16(f+2,8);put16(f+4,16);put16(f+6,16);put16(f+8,48);
  for (UINTN i=0;i<16*48;i++) f[12+i]=1;
}
#endif
