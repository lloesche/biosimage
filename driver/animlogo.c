// Experimental animation driver. Host-side compatibility gates control injection.
#include "efi.h"
#include "frames.h"

_Static_assert(__builtin_offsetof(EFI_BOOT_SERVICES, LocateProtocol) == 0x140, "BS ABI");
_Static_assert(__builtin_offsetof(EFI_SYSTEM_TABLE, BootServices) == 0x60, "ST ABI");
_Static_assert(__builtin_offsetof(EFI_LOADED_IMAGE_PROTOCOL, Unload) == 0x58, "image ABI");

static EFI_GUID gop_guid = {0x9042a9de,0x23dc,0x4a38,{0x96,0xfb,0x7a,0xde,0xd0,0x80,0x51,0x6a}};
static EFI_GUID image_guid = {0x5b1b31a1,0x9562,0x11d2,{0x8e,0x3f,0,0xa0,0xc9,0x69,0x72,0x3b}};
static EFI_GUID post_guid = {0x8a91b1e1,0x56c7,0x4adc,{0xab,0xeb,0x1c,0x2c,0xa1,0x72,0x9e,0xff}};
// AMI-specific ABI: must be validated by the host's disassembly gate before injection.
typedef struct { void *unused[10]; UINT32(EFIAPI *State)(void); } POST_MANAGER;
static EFI_BOOT_SERVICES *bs;
static EFI_EVENT timer, exit_event, deadline_event;
static BOOLEAN stopped;
static UINTN missing_ticks;
#define TIMER_PERIOD 100000
#define MISSING_LIMIT 500
static EFI_GUID fv_guid = {0x220e73b6,0x6bdb,0x4413,{0x84,0x05,0xb9,0x74,0xb1,0x08,0x61,0x9a}};
static EFI_GUID data_guid = {0x6edb8a32,0x70a0,0x4b5d,{0xb1,0x31,0x02,0x77,0xf6,0x02,0xe5,0x29}};
static UINT8 *data;
static ANIMATION animation;
static FRAME *frames;
static EFI_GRAPHICS_OUTPUT_BLT_PIXEL *canvas;
static UINTN current, remaining, plays;
static BOOLEAN displayed, held;
static BOOLEAN dirty=TRUE;
static EFI_GRAPHICS_OUTPUT_PROTOCOL *last_gop;
static UINTN last_mode, last_width, last_height;

static void apply_frame(UINTN index) {
  const FRAME *f=&frames[index];
  for (UINTN y=0;y<f->Height;y++) for (UINTN x=0;x<f->Width;x++) {
    const UINT8 *c=animation.Palette+4*f->Indices[y*f->Width+x];
    EFI_GRAPHICS_OUTPUT_BLT_PIXEL *p=&canvas[(f->Y+y)*animation.Width+f->X+x];
    p->Blue=c[0]; p->Green=c[1]; p->Red=c[2]; p->Reserved=0;
  }
}

static EFI_STATUS read_animation(EFI_LOADED_IMAGE_PROTOCOL *loaded) {
  EFI_FIRMWARE_VOLUME2_PROTOCOL *fv=NULL;
  // Data must be beside this driver, not an unrelated volume's matching GUID.
  if (!loaded->DeviceHandle) return EFI_NOT_FOUND;
  EFI_STATUS s=bs->HandleProtocol(loaded->DeviceHandle,&fv_guid,(void **)&fv);
  if (EFI_ERROR(s) || !fv || !fv->ReadSection) return EFI_NOT_FOUND;
  UINTN size=0; UINT32 authentication=0;
  s=fv->ReadSection(fv,&data_guid,EFI_SECTION_RAW,0,(void **)&data,&size,&authentication);
  if (EFI_ERROR(s)) return s;
  // PI authentication status TEST_FAILED (bit 3) must not be ignored.
  if ((authentication&8) || !parse_animation(data,size,&animation,NULL)) return EFI_LOAD_ERROR;
  s=bs->AllocatePool(EfiBootServicesData,animation.Count*sizeof(FRAME),(void **)&frames);
  if (EFI_ERROR(s)) return s;
  if (!parse_animation(data,size,&animation,frames)) return EFI_LOAD_ERROR;
  s=bs->AllocatePool(EfiBootServicesData,animation.Width*animation.Height*sizeof(*canvas),(void **)&canvas);
  if (EFI_ERROR(s)) return s;
  apply_frame(0); remaining=frames[0].Delay;
  return EFI_SUCCESS;
}

static void trace(const char *s) {
#ifdef ANIMLOGO_QEMU_TEST
  while (*s) { __asm__ volatile("outb %0, %1" :: "a"((UINT8)*s++), "Nd"((UINT16)0x402)); }
#else
  (void)s;
#endif
}

// ExitBootServices callback only changes our state. It makes no boot-service calls.
static void EFIAPI on_exit(EFI_EVENT e, void *ctx) {
  (void)e; (void)ctx;
  stopped = TRUE;
  trace("animlogo: exit-stop\n");
}

static void stop(const char *why) {
  stopped = TRUE;
  bs->SetTimer(timer, TimerCancel, 0);
  trace(why);
}

static void EFIAPI deadline(EFI_EVENT e,void *ctx) {
  (void)e;(void)ctx;
  if (stopped) return;
  POST_MANAGER *post=NULL;
  if (EFI_ERROR(bs->LocateProtocol(&post_guid,NULL,(void **)&post)) || !post || !post->State)
    stop("animlogo: timeout-stop\n");
}

static void EFIAPI tick(EFI_EVENT e, void *ctx) {
  (void)e; (void)ctx;
  if (stopped) return;
  POST_MANAGER *post = NULL;
  // Retry every tick: a DXE driver can run before AMITSE installs its protocol.
  if (!EFI_ERROR(bs->LocateProtocol(&post_guid, NULL, (void **)&post)) && post && post->State) {
    if (post->State() > 3) { stop("animlogo: state-stop\n"); return; }
  } else if (++missing_ticks >= MISSING_LIMIT) {
    stop("animlogo: timeout-stop\n"); return;
  }
  EFI_GRAPHICS_OUTPUT_PROTOCOL *gop = NULL;
  if (EFI_ERROR(bs->LocateProtocol(&gop_guid, NULL, (void **)&gop)) || !gop || !gop->Mode || !gop->Mode->Info) return;
  UINTN sw = gop->Mode->Info->HorizontalResolution, sh = gop->Mode->Info->VerticalResolution;
  const UINTN w=animation.Width,h=animation.Height;
  if (w > sw || h > sh) return;
  UINTN x = (sw-w)/2, y = (sh-h)/2;
  if (w*5 <= sw*2 && h*5 <= sh*2) {
    UINTN mid = sh*382/1000;
    if (mid < h/2) return;
    y = mid-h/2;
  }
  // A complete composited canvas is blitted each tick, so mode changes never expose
  // uninitialised portions of a changed-rectangle frame. Paused GOP means paused timing.
  BOOLEAN mode_changed=gop!=last_gop || gop->Mode->Mode!=last_mode || sw!=last_width || sh!=last_height;
  if (displayed && !dirty && (remaining>0 || held) && !mode_changed) { if (remaining) remaining--; return; }
  if (displayed && remaining==0 && !held) {
    // After the last frame of the last play, keep it on screen (redrawn on mode changes).
    if (current+1==animation.Count && animation.Plays && ++plays>=animation.Plays) {
      held=TRUE;
      trace("animlogo: hold\n");
      return;
    }
    current=(current+1)%animation.Count;
    apply_frame(current); remaining=frames[current].Delay; dirty=TRUE;
  }
  EFI_STATUS s=gop->Blt(gop,canvas,EfiBltBufferToVideo,0,0,x,y,w,h,w*sizeof(*canvas));
  if (!EFI_ERROR(s)) {
    if (!displayed || remaining==frames[current].Delay) trace("animlogo: frame\n");
    displayed=TRUE;
    dirty=FALSE;
    if (remaining) remaining--;
    last_gop=gop; last_mode=gop->Mode->Mode; last_width=sw; last_height=sh;
    trace("animlogo: draw\n");
  }
}

static EFI_STATUS EFIAPI unload(EFI_HANDLE image) {
  (void)image;
  stopped = TRUE;
  if (timer) { bs->SetTimer(timer, TimerCancel, 0); bs->CloseEvent(timer); timer = NULL; }
  if (exit_event) { bs->CloseEvent(exit_event); exit_event = NULL; }
  if (deadline_event) { bs->SetTimer(deadline_event,TimerCancel,0);bs->CloseEvent(deadline_event);deadline_event=NULL; }
  if (canvas) { bs->FreePool(canvas); canvas=NULL; }
  if (frames) { bs->FreePool(frames); frames=NULL; }
  if (data) { bs->FreePool(data); data=NULL; }
  trace("animlogo: unload\n");
  return EFI_SUCCESS;
}

EFI_STATUS EFIAPI efi_main(EFI_HANDLE image, EFI_SYSTEM_TABLE *st) {
  bs = st->BootServices;
  EFI_LOADED_IMAGE_PROTOCOL *loaded = NULL;
  EFI_STATUS s = bs->HandleProtocol(image, &image_guid, (void **)&loaded);
  if (EFI_ERROR(s)) return s;
  loaded->Unload = unload;
  s=read_animation(loaded);
  if (EFI_ERROR(s)) { unload(image); return s; }
  s = bs->CreateEvent(EVT_SIGNAL_EXIT_BOOT_SERVICES, TPL_NOTIFY, on_exit, NULL, &exit_event);
  if (EFI_ERROR(s)) { unload(image); return s; }
  s = bs->CreateEvent(EVT_TIMER|EVT_NOTIFY_SIGNAL, TPL_CALLBACK, tick, NULL, &timer);
  if (!EFI_ERROR(s)) s = bs->SetTimer(timer, TimerPeriodic, TIMER_PERIOD);
  if (!EFI_ERROR(s)) s=bs->CreateEvent(EVT_TIMER|EVT_NOTIFY_SIGNAL,TPL_CALLBACK,deadline,NULL,&deadline_event);
  if (!EFI_ERROR(s)) s=bs->SetTimer(deadline_event,TimerRelative,50000000);
  if (EFI_ERROR(s)) { unload(image); return s; }
  trace("animlogo: started\n");
  return EFI_SUCCESS;
}
