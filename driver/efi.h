// Minimal UEFI definitions for a freestanding x64 DXE driver: only what animlogo.c uses, laid
// out as in the UEFI Specification 2.10 and the PI Specification 1.8.
#ifndef EFI_H
#define EFI_H

typedef unsigned char UINT8;
typedef unsigned short UINT16;
typedef unsigned int UINT32;
typedef unsigned long long UINT64;
typedef long long INT64;
typedef UINT64 UINTN;
typedef INT64 INTN;
typedef UINT8 BOOLEAN;
typedef UINT16 CHAR16;
typedef UINTN EFI_STATUS;
typedef void *EFI_HANDLE;
typedef void *EFI_EVENT;
typedef UINTN EFI_TPL;

#if defined(__x86_64__) || defined(_M_X64)
#define EFIAPI __attribute__((ms_abi))
#else
// Native parser tests can run on Apple Silicon; firmware is always x64.
#define EFIAPI
#endif
#define TRUE 1
#define FALSE 0
#ifndef NULL
#define NULL ((void *)0)
#endif

#define EFI_SUCCESS 0
#define EFI_ERROR_BIT (1ULL << 63)
#define EFI_ERROR(s) (((s) & EFI_ERROR_BIT) != 0)
#define EFI_LOAD_ERROR (EFI_ERROR_BIT | 1)
#define EFI_UNSUPPORTED (EFI_ERROR_BIT | 3)
#define EFI_NOT_FOUND (EFI_ERROR_BIT | 14)

typedef struct {
  UINT32 Data1;
  UINT16 Data2;
  UINT16 Data3;
  UINT8 Data4[8];
} EFI_GUID;

typedef struct {
  UINT64 Signature;
  UINT32 Revision;
  UINT32 HeaderSize;
  UINT32 CRC32;
  UINT32 Reserved;
} EFI_TABLE_HEADER;

#define TPL_CALLBACK 8
#define TPL_NOTIFY 16

#define EVT_TIMER 0x80000000
#define EVT_NOTIFY_SIGNAL 0x00000200
#define EVT_SIGNAL_EXIT_BOOT_SERVICES 0x00000201

typedef enum { TimerCancel, TimerPeriodic, TimerRelative } EFI_TIMER_DELAY;

typedef enum {
  AllHandles,
  ByRegisterNotify,
  ByProtocol
} EFI_LOCATE_SEARCH_TYPE;

typedef enum {
  EfiReservedMemoryType,
  EfiLoaderCode,
  EfiLoaderData,
  EfiBootServicesCode,
  EfiBootServicesData,
} EFI_MEMORY_TYPE;

typedef void(EFIAPI *EFI_EVENT_NOTIFY)(EFI_EVENT Event, void *Context);

// Unused entries are void pointers; the offsets of the used ones are in the comments.
typedef struct {
  EFI_TABLE_HEADER Hdr;
  void *RaiseTPL;
  void *RestoreTPL;
  void *AllocatePages;
  void *FreePages;
  EFI_STATUS(EFIAPI *GetMemoryMap)(UINTN *, void *, UINTN *, UINTN *, UINT32 *);
  EFI_STATUS(EFIAPI *AllocatePool)(EFI_MEMORY_TYPE PoolType, UINTN Size, void **Buffer); // 0x40
  EFI_STATUS(EFIAPI *FreePool)(void *Buffer);                                            // 0x48
  EFI_STATUS(EFIAPI *CreateEvent)(UINT32 Type, EFI_TPL NotifyTpl, EFI_EVENT_NOTIFY NotifyFunction,
                                  void *NotifyContext, EFI_EVENT *Event); // 0x50
  EFI_STATUS(EFIAPI *SetTimer)(EFI_EVENT Event, EFI_TIMER_DELAY Type, UINT64 TriggerTime); // 0x58
  void *WaitForEvent;
  void *SignalEvent;
  EFI_STATUS(EFIAPI *CloseEvent)(EFI_EVENT Event); // 0x70
  void *CheckEvent;
  EFI_STATUS(EFIAPI *InstallProtocolInterface)(EFI_HANDLE *, EFI_GUID *, UINT32, void *);
  void *ReinstallProtocolInterface;
  void *UninstallProtocolInterface;
  EFI_STATUS(EFIAPI *HandleProtocol)(EFI_HANDLE Handle, EFI_GUID *Protocol, void **Interface); // 0x98
  void *Reserved;
  void *RegisterProtocolNotify;
  void *LocateHandle;
  void *LocateDevicePath;
  void *InstallConfigurationTable;
  EFI_STATUS(EFIAPI *LoadImage)(BOOLEAN, EFI_HANDLE, void *, void *, UINTN, EFI_HANDLE *);
  EFI_STATUS(EFIAPI *StartImage)(EFI_HANDLE, UINTN *, CHAR16 **);
  void *Exit;
  EFI_STATUS(EFIAPI *UnloadImage)(EFI_HANDLE);
  EFI_STATUS(EFIAPI *ExitBootServices)(EFI_HANDLE, UINTN);
  void *GetNextMonotonicCount;
  EFI_STATUS(EFIAPI *Stall)(UINTN);
  void *SetWatchdogTimer;
  void *ConnectController;
  void *DisconnectController;
  void *OpenProtocol;
  void *CloseProtocol;
  void *OpenProtocolInformation;
  void *ProtocolsPerHandle;
  EFI_STATUS(EFIAPI *LocateHandleBuffer)(EFI_LOCATE_SEARCH_TYPE SearchType, EFI_GUID *Protocol,
                                         void *SearchKey, UINTN *NoHandles, EFI_HANDLE **Buffer); // 0x138
  EFI_STATUS(EFIAPI *LocateProtocol)(EFI_GUID *Protocol, void *Registration, void **Interface); // 0x140
  void *InstallMultipleProtocolInterfaces;
  void *UninstallMultipleProtocolInterfaces;
  void *CalculateCrc32;
  void *CopyMem;
  void *SetMem;
  EFI_STATUS(EFIAPI *CreateEventEx)(UINT32 Type, EFI_TPL NotifyTpl, EFI_EVENT_NOTIFY NotifyFunction,
                                    const void *NotifyContext, const EFI_GUID *EventGroup,
                                    EFI_EVENT *Event); // 0x170
} EFI_BOOT_SERVICES;

typedef struct {
  EFI_TABLE_HEADER Hdr;
  CHAR16 *FirmwareVendor;
  UINT32 FirmwareRevision;
  EFI_HANDLE ConsoleInHandle;
  void *ConIn;
  EFI_HANDLE ConsoleOutHandle;
  void *ConOut;
  EFI_HANDLE StandardErrorHandle;
  void *StdErr;
  void *RuntimeServices;
  EFI_BOOT_SERVICES *BootServices;
  UINTN NumberOfTableEntries;
  void *ConfigurationTable;
} EFI_SYSTEM_TABLE;

typedef struct {
  UINT32 Revision;
  EFI_HANDLE ParentHandle;
  EFI_SYSTEM_TABLE *SystemTable;
  EFI_HANDLE DeviceHandle;
  void *FilePath;
  void *Reserved;
  UINT32 LoadOptionsSize;
  void *LoadOptions;
  void *ImageBase;
  UINT64 ImageSize;
  EFI_MEMORY_TYPE ImageCodeType;
  EFI_MEMORY_TYPE ImageDataType;
  EFI_STATUS(EFIAPI *Unload)(EFI_HANDLE);
} EFI_LOADED_IMAGE_PROTOCOL;

// Graphics Output Protocol

typedef struct {
  UINT8 Blue;
  UINT8 Green;
  UINT8 Red;
  UINT8 Reserved;
} EFI_GRAPHICS_OUTPUT_BLT_PIXEL;

typedef enum {
  EfiBltVideoFill,
  EfiBltVideoToBltBuffer,
  EfiBltBufferToVideo,
  EfiBltVideoToVideo,
} EFI_GRAPHICS_OUTPUT_BLT_OPERATION;

typedef struct {
  UINT32 Version;
  UINT32 HorizontalResolution;
  UINT32 VerticalResolution;
  UINT32 PixelFormat;
  UINT32 PixelInformation[4];
  UINT32 PixelsPerScanLine;
} EFI_GRAPHICS_OUTPUT_MODE_INFORMATION;

typedef struct {
  UINT32 MaxMode;
  UINT32 Mode;
  EFI_GRAPHICS_OUTPUT_MODE_INFORMATION *Info;
  UINTN SizeOfInfo;
  UINT64 FrameBufferBase;
  UINTN FrameBufferSize;
} EFI_GRAPHICS_OUTPUT_PROTOCOL_MODE;

typedef struct EFI_GRAPHICS_OUTPUT_PROTOCOL EFI_GRAPHICS_OUTPUT_PROTOCOL;
struct EFI_GRAPHICS_OUTPUT_PROTOCOL {
  EFI_STATUS(EFIAPI *QueryMode)(EFI_GRAPHICS_OUTPUT_PROTOCOL *, UINT32, UINTN *, EFI_GRAPHICS_OUTPUT_MODE_INFORMATION **);
  EFI_STATUS(EFIAPI *SetMode)(EFI_GRAPHICS_OUTPUT_PROTOCOL *, UINT32);
  EFI_STATUS(EFIAPI *Blt)(EFI_GRAPHICS_OUTPUT_PROTOCOL *This, EFI_GRAPHICS_OUTPUT_BLT_PIXEL *BltBuffer,
                          EFI_GRAPHICS_OUTPUT_BLT_OPERATION BltOperation, UINTN SourceX, UINTN SourceY,
                          UINTN DestinationX, UINTN DestinationY, UINTN Width, UINTN Height,
                          UINTN Delta);
  EFI_GRAPHICS_OUTPUT_PROTOCOL_MODE *Mode;
};

// Firmware Volume 2 Protocol (PI Specification, Volume 3)

#define EFI_SECTION_RAW 0x19

typedef struct EFI_FIRMWARE_VOLUME2_PROTOCOL EFI_FIRMWARE_VOLUME2_PROTOCOL;
struct EFI_FIRMWARE_VOLUME2_PROTOCOL {
  void *GetVolumeAttributes;
  void *SetVolumeAttributes;
  void *ReadFile;
  EFI_STATUS(EFIAPI *ReadSection)(EFI_FIRMWARE_VOLUME2_PROTOCOL *This, const EFI_GUID *NameGuid,
                                  UINT8 SectionType, UINTN SectionInstance, void **Buffer,
                                  UINTN *BufferSize, UINT32 *AuthenticationStatus); // 0x18
};

#endif
