// OVMF-only test application: LoadImage/StartImage exercise the actual DXE image.
#include "efi.h"
#include "frames.h"
#include "sample.h"
#include "build/driver_bytes.h"
#ifndef TEST_CASE
#define TEST_CASE 0
#endif
static void log(const char *s) {
  while (*s) __asm__ volatile("outb %0, %1" :: "a"((UINT8)*s++), "Nd"((UINT16)0x402));
}
static void finish(UINT8 code) {
  __asm__ volatile("outb %0, %1" :: "a"(code), "Nd"((UINT16)0xf4));
  for (;;) __asm__ volatile("hlt");
}
static UINTN state;
static UINT32 EFIAPI get_state(void) { return (UINT32)state; }
static EFI_GUID post_guid = {0x8a91b1e1,0x56c7,0x4adc,{0xab,0xeb,0x1c,0x2c,0xa1,0x72,0x9e,0xff}};
static struct { void *unused[10]; UINT32(EFIAPI *State)(void); } mock_post = {{0},get_state};
static EFI_BOOT_SERVICES *test_bs;
static EFI_HANDLE mock_volume;
static EFI_GUID image_guid = {0x5b1b31a1,0x9562,0x11d2,{0x8e,0x3f,0,0xa0,0xc9,0x69,0x72,0x3b}};
static EFI_GUID fv_guid = {0x220e73b6,0x6bdb,0x4413,{0x84,0x05,0xb9,0x74,0xb1,0x08,0x61,0x9a}};
static EFI_STATUS EFIAPI read_section(EFI_FIRMWARE_VOLUME2_PROTOCOL *this, const EFI_GUID *name, UINT8 type, UINTN index, void **out, UINTN *size, UINT32 *auth) {
  (void)this;
  if (name->Data1!=0x6edb8a32 || type!=EFI_SECTION_RAW || index) return EFI_NOT_FOUND;
  *size=sizeof(sample);*auth=TEST_CASE==9?8:0;
  EFI_STATUS s=test_bs->AllocatePool(EfiBootServicesData,*size,out);
  if (EFI_ERROR(s)) return s;
  for (UINTN i=0;i<*size;i++) ((UINT8 *)*out)[i]=sample[i];
  return EFI_SUCCESS;
}
static EFI_FIRMWARE_VOLUME2_PROTOCOL mock_fv={NULL,NULL,NULL,read_section};
static EFI_STATUS(EFIAPI *real_blt)(EFI_GRAPHICS_OUTPUT_PROTOCOL *,EFI_GRAPHICS_OUTPUT_BLT_PIXEL *,EFI_GRAPHICS_OUTPUT_BLT_OPERATION,UINTN,UINTN,UINTN,UINTN,UINTN,UINTN,UINTN);
static UINTN injected_failure;
static EFI_STATUS EFIAPI flaky_blt(EFI_GRAPHICS_OUTPUT_PROTOCOL *g,EFI_GRAPHICS_OUTPUT_BLT_PIXEL *p,EFI_GRAPHICS_OUTPUT_BLT_OPERATION op,UINTN sx,UINTN sy,UINTN dx,UINTN dy,UINTN w,UINTN h,UINTN delta) {
  if (op==EfiBltBufferToVideo && w==160 && h==80) {
    BOOLEAN yellow=p[16*160+8].Green==0xc0;
    if (injected_failure==0 && yellow) { injected_failure=1;return EFI_ERROR_BIT|7; }
    if (injected_failure==1) {
      if (!yellow) { log("FAIL failed frame skipped\n");finish(1); }
      injected_failure=2;log("bootcheck: blt-retry\n");
    }
  }
  return real_blt(g,p,op,sx,sy,dx,dy,w,h,delta);
}
static void check_pixels(EFI_BOOT_SERVICES *bs) {
  EFI_GUID guid={0x9042a9de,0x23dc,0x4a38,{0x96,0xfb,0x7a,0xde,0xd0,0x80,0x51,0x6a}};
  EFI_GRAPHICS_OUTPUT_PROTOCOL *gop=NULL;
  if (EFI_ERROR(bs->LocateProtocol(&guid,NULL,(void **)&gop))) finish(1);
  UINTN sw=gop->Mode->Info->HorizontalResolution,sh=gop->Mode->Info->VerticalResolution;
  UINTN x=(sw-160)/2,y=(sh-80)/2;
  if (160*5<=sw*2 && 80*5<=sh*2) y=sh*382/1000-40;
  UINTN observed=0;
  for (UINTN i=0;i<50;i++) {
    bs->Stall(10000);
    EFI_GRAPHICS_OUTPUT_BLT_PIXEL p;
    if (EFI_ERROR(gop->Blt(gop,&p,EfiBltVideoToBltBuffer,x+8,y+16,0,0,1,1,0))) finish(1);
    if (p.Blue==0x20 && p.Green==0xc0 && p.Red==0xff) observed|=1;
    if ((observed&1) && p.Blue==32 && p.Green==32 && p.Red==32) observed|=2;
  }
  if (observed!=3) { log("FAIL animation pixels/loop\n");finish(1); }
  log("bootcheck: animation-pixels-loop\n");
}
// check_hold: played once, the animation must keep its last (yellow) frame on screen.
static void check_hold(EFI_BOOT_SERVICES *bs) {
  EFI_GUID guid={0x9042a9de,0x23dc,0x4a38,{0x96,0xfb,0x7a,0xde,0xd0,0x80,0x51,0x6a}};
  EFI_GRAPHICS_OUTPUT_PROTOCOL *gop=NULL;
  if (EFI_ERROR(bs->LocateProtocol(&guid,NULL,(void **)&gop))) finish(1);
  UINTN sw=gop->Mode->Info->HorizontalResolution,sh=gop->Mode->Info->VerticalResolution;
  UINTN x=(sw-160)/2,y=(sh-80)/2;
  if (160*5<=sw*2 && 80*5<=sh*2) y=sh*382/1000-40;
  BOOLEAN yellow=FALSE;
  for (UINTN i=0;i<100;i++) {
    bs->Stall(10000);
    EFI_GRAPHICS_OUTPUT_BLT_PIXEL p;
    if (EFI_ERROR(gop->Blt(gop,&p,EfiBltVideoToBltBuffer,x+8,y+16,0,0,1,1,0))) finish(1);
    if (p.Blue==0x20 && p.Green==0xc0 && p.Red==0xff) yellow=TRUE;
    else if (yellow) { log("FAIL animation looped after its last play\n");finish(1); }
  }
  if (!yellow) { log("FAIL last frame not shown\n");finish(1); }
  log("bootcheck: animation-hold\n");
}
static EFI_HANDLE load(EFI_BOOT_SERVICES *bs, EFI_HANDLE app) {
  EFI_HANDLE driver = NULL;
  if (EFI_ERROR(bs->LoadImage(FALSE, app, NULL, driver_bytes, sizeof(driver_bytes), &driver))) { log("FAIL load\n"); finish(1); }
  if (TEST_CASE!=7) {
    EFI_LOADED_IMAGE_PROTOCOL *loaded=NULL;
    if (EFI_ERROR(bs->HandleProtocol(driver,&image_guid,(void **)&loaded))) finish(1);
    loaded->DeviceHandle=mock_volume;
  }
  EFI_STATUS s=bs->StartImage(driver, NULL, NULL);
  if (TEST_CASE==6 || TEST_CASE==7 || TEST_CASE==9) {
    if (!EFI_ERROR(s)) { log("FAIL invalid input accepted\n"); finish(1); }
    log("PASS rejected\n");finish(0);
  }
  if (EFI_ERROR(s)) { log("FAIL start\n"); finish(1); }
  return driver;
}
EFI_STATUS EFIAPI efi_main(EFI_HANDLE app, EFI_SYSTEM_TABLE *st) {
  EFI_BOOT_SERVICES *bs = st->BootServices;
  test_bs=bs;
  if (TEST_CASE==10) {
    EFI_GUID guid={0x9042a9de,0x23dc,0x4a38,{0x96,0xfb,0x7a,0xde,0xd0,0x80,0x51,0x6a}};
    EFI_GRAPHICS_OUTPUT_PROTOCOL *gop=NULL;
    if (EFI_ERROR(bs->LocateProtocol(&guid,NULL,(void **)&gop))) finish(1);
    real_blt=gop->Blt;gop->Blt=flaky_blt;
  }
  // Also exercise the exact parser in the application, so it cannot be optimised away.
  make_sample();
  if (TEST_CASE==11) put16(sample+10,1); // play once
  ANIMATION checked;
  if (!parse_animation(sample,sizeof(sample),&checked,NULL)) finish(1);
  if (TEST_CASE==6) sample[0]='X';
  if (TEST_CASE!=7 && EFI_ERROR(bs->InstallProtocolInterface(&mock_volume,&fv_guid,0,&mock_fv))) finish(1);
  log("bootcheck: start\n");
  if (TEST_CASE == 0 || TEST_CASE==5 || TEST_CASE==10 || TEST_CASE==11) {
    EFI_HANDLE driver = load(bs, app);
    if (TEST_CASE==5 || TEST_CASE==10) check_pixels(bs);
    if (TEST_CASE==11) check_hold(bs);
    if (TEST_CASE==10 && injected_failure!=2) { log("FAIL no GOP retry\n");finish(1); }
    bs->Stall(10000000); // allow TCG timer delivery margin on a busy host
    if (EFI_ERROR(bs->UnloadImage(driver))) { log("FAIL unload\n"); finish(1); }
    log("PASS fallback\n");
  } else if (TEST_CASE == 1) {
    EFI_HANDLE handle = NULL;
    if (EFI_ERROR(bs->InstallProtocolInterface(&handle, &post_guid, 0, &mock_post))) { log("FAIL install\n"); finish(1); }
    for (UINTN end = 4; end <= 7; end++) {
      state = 0;
      EFI_HANDLE driver = load(bs, app);
      bs->Stall(300000);
      state = 3;
      log("bootcheck: state-three\n");
      bs->Stall(300000);
      state = end;
      bs->Stall(300000);
      if (EFI_ERROR(bs->UnloadImage(driver))) { log("FAIL unload\n"); finish(1); }
    }
    log("PASS states\n");
  } else if (TEST_CASE == 3) {
    EFI_HANDLE driver = load(bs, app), handle = NULL;
    bs->Stall(300000);
    state = 5;
    if (EFI_ERROR(bs->InstallProtocolInterface(&handle, &post_guid, 0, &mock_post))) finish(1);
    bs->Stall(300000);
    bs->UnloadImage(driver);
    log("PASS late-protocol\n");
  } else if (TEST_CASE == 4 || TEST_CASE==8) {
    EFI_HANDLE driver = load(bs, app);
    EFI_GUID gop_guid = {0x9042a9de,0x23dc,0x4a38,{0x96,0xfb,0x7a,0xde,0xd0,0x80,0x51,0x6a}};
    EFI_GRAPHICS_OUTPUT_PROTOCOL *gop = NULL;
    if (EFI_ERROR(bs->LocateProtocol(&gop_guid, NULL, (void **)&gop))) finish(1);
    bs->Stall(300000);
    UINT32 mode = (gop->Mode->Mode + 1) % gop->Mode->MaxMode;
    if (gop->Mode->MaxMode < 2 || EFI_ERROR(gop->SetMode(gop, mode))) finish(1);
    log("bootcheck: mode-change\n");
    bs->Stall(300000);
    bs->UnloadImage(driver);
    log("PASS mode-change\n");
  } else if (TEST_CASE==6 || TEST_CASE==7 || TEST_CASE==9) {
    load(bs,app);
    finish(1);
  } else {
    load(bs, app);
    // Reserve space before obtaining the memory map. No allocations after GetMemoryMap.
    void *map = NULL;
    UINTN size = 128*1024, key = 0, desc = 0;
    UINT32 version = 0;
    if (EFI_ERROR(bs->AllocatePool(EfiLoaderData, size, &map))) finish(1);
    bs->Stall(300000);
    EFI_STATUS s = bs->GetMemoryMap(&size, map, &key, &desc, &version);
    if (EFI_ERROR(s)) { log("FAIL map\n"); finish(1); }
    s = bs->ExitBootServices(app, key);
    if (EFI_ERROR(s)) { size = 128*1024; bs->GetMemoryMap(&size, map, &key, &desc, &version); s = bs->ExitBootServices(app, key); }
    if (EFI_ERROR(s)) { log("FAIL exit\n"); finish(1); }
    log("PASS exit\n");
  }
  finish(0);
  return EFI_SUCCESS;
}
