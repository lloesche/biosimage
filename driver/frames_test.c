#include <stdio.h>
#include "frames.h"
#include "sample.h"
static int failed;
static void expect(BOOLEAN valid,const char *name,UINTN size) {
  ANIMATION a;
  BOOLEAN result=parse_animation(sample,size,&a,NULL);
  if (result!=valid) { fprintf(stderr,"FAIL %s\n",name);failed++; }
}
int main(void) {
  make_sample();expect(TRUE,"sample",sizeof(sample));
  ANIMATION a;FRAME frames[2];
  if (!parse_animation(sample,sizeof(sample),&a,frames) || frames[1].Indices[0]!=1 || frames[1].X!=8) return 1;
  for (UINTN i=0;i<sizeof(sample);i++) expect(FALSE,"truncation",i);
  make_sample();sample[0]='X';expect(FALSE,"magic",sizeof(sample));
  make_sample();put16(sample+4,0);expect(FALSE,"zero width",sizeof(sample));
  make_sample();put16(sample+6,1081);expect(FALSE,"height cap",sizeof(sample));
  make_sample();sample[8]=0;expect(FALSE,"zero count",sizeof(sample));
  make_sample();sample[9]=255;expect(FALSE,"frame cap",sizeof(sample));
  make_sample();put16(sample+10,1);expect(TRUE,"play once",sizeof(sample));
  if (!parse_animation(sample,sizeof(sample),&a,NULL) || a.Plays!=1 || a.Count!=2) return 1;
  make_sample();sample[3]='1';expect(FALSE,"old version",sizeof(sample));
  make_sample();put16(sample+FRAME_HEADER,9);expect(FALSE,"short delay",sizeof(sample));
  make_sample();put16(sample+FRAME_HEADER,6001);expect(FALSE,"long delay",sizeof(sample));
  make_sample();put16(sample+FRAME_HEADER+2,1);expect(FALSE,"first must be full",sizeof(sample));
  make_sample();put16(sample+FRAME_HEADER+10,1);expect(FALSE,"reserved",sizeof(sample));
  make_sample();put16(sample+FRAME_HEADER+12+160*80+2,159);expect(FALSE,"outside rect",sizeof(sample));
  make_sample();sample[8]=1;expect(FALSE,"trailing frame",sizeof(sample));
  if (!failed) puts("BIA2 C parser tests PASS");
  return failed?1:0;
}
