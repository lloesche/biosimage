# Experimental DXE animation driver

`animlogo.c` is a freestanding x64 UEFI boot-service driver. biosimage injects it only when
its compatibility and firmware-protection checks allow the experimental animation feature.
The animation data is a separate FFS file beside the driver; the normal logo remains a BMP
of the animation's first frame as a fallback. The driver does not patch existing executable
code or bypass image authentication. Hardware boot and recovery remain unverified.

## Build and test on Linux and macOS

The shared build and test programs use Python 3's standard library, LLVM clang/lld-link,
QEMU and an OVMF firmware pair. They always produce an **x64 UEFI** driver, including when
the host is an Apple Silicon Mac.

Ubuntu/Debian:

```sh
sudo apt-get install clang lld qemu-system-x86 ovmf python3
```

Fedora:

```sh
sudo dnf install clang lld qemu-system-x86-core edk2-ovmf python3
```

macOS (Homebrew, with Xcode Command Line Tools installed):

```sh
brew install llvm qemu python
```

Then, on either Linux or macOS:

```sh
sh driver/build.sh
sh driver/test-qemu.sh
```

Homebrew's keg-only LLVM is discovered under `/opt/homebrew` or `/usr/local`. Custom tool
locations can be passed with `--toolchain /path/to/bin`, or `CLANG` and `LLD_LINK`. The test
runner accepts `--qemu`, `--ovmf-code` and `--ovmf-vars` (or `OVMF_CODE`/`OVMF_VARS`) for a
custom matched firmware pair. Otherwise it finds QEMU's bundled images or Linux's OVMF
package. Firmware templates are copied before each boot, never modified in place.

## Build and test on Windows

Requires Python 3 and MSYS2 clang64 clang, lld-link, QEMU and its bundled OVMF images.

```powershell
./driver/build.ps1                 # normal build; updates uefi/animlogo.efi for Go embedding
./driver/test-qemu.ps1             # C parser tests and eleven OVMF scenarios
```

The `.sh` and `.ps1` entry points call the same `build.py` and `test-qemu.py`; flags and
test assertions do not diverge between hosts.

All intermediate products, virtual FAT disks, variable stores, screenshots and logs are
ignored under `driver/build/`. The production PE has EFI_BOOT_SERVICE_DRIVER subsystem 11,
no CRT imports, and deterministic link timestamp zero. `-Test` builds add QEMU debug-port
logging. Production builds live in `driver/build/production/`; instrumented builds live
in `driver/build/test/` and never overwrite production outputs or the Go-embedded asset.
The ordinary build performs no port I/O. `bootcheck.c` and its port-0xf4 QEMU exit mechanism are test-only: never flash or run
the bootcheck application on real hardware.

## Runtime

At entry the driver uses LoadedImage.DeviceHandle's Firmware Volume 2 protocol to read the
RAW section of data file `6EDB8A32-70A0-4B5D-B131-0277F602E529`. Data cannot silently come from
another volume. Firmware does section decompression; ReadSection authentication failure,
missing data, malformed data and allocation failures prevent the timer from starting.
The complete BIA1 stream is validated before frame/canvas allocation and playback.

All allocations happen at entry. A composited full canvas is retained; changed rectangles
update it, and frame zero is full so every loop restores the initial image. A 10 ms timer
tracks GIF delays (at least 100 ms per frame), looking up the current GOP and its mode each
tick. The driver advances only when a usable GOP exists. A changed mode triggers a full
canvas redraw, avoiding stale partial frames. It never changes the screen mode itself.

Placement matches the gated AMITSE rule: centred horizontally, with the image's middle at
38.2% of screen height when both dimensions are at most 40% of the screen; otherwise centred.
Frames larger than the current screen are not drawn.

The driver retries AMI post-manager protocol discovery on every tick. The no-argument
UINT32-returning getter at table offset 0x50 ends playback permanently when its result is
greater than 3. An independent five-second relative deadline ends fallback playback when
the protocol is absent. ExitBootServices always sets the stopped flag; that callback makes
no boot-service calls. Unload cancels and closes all events and frees all allocated buffers.

## BIA1 data format

All integers are little endian. No native C structs are serialized.

| Offset | Field |
|---|---|
| 0 | Four bytes `BIA1` |
| 4 | Width, uint16 |
| 6 | Height, uint16 |
| 8 | Frame count, uint32 |
| 12 | 256 palette entries, four bytes each: blue, green, red, reserved |
| 1036 | First frame; subsequent frames follow immediately |

Each frame has six uint16 fields: **delay** (units of 10 ms), **x**, **y**, **width**,
**height**, and **reserved** (zero), followed by width×height one-byte palette indices in
row-major order. Frame zero must cover the complete canvas. Other frames overwrite their
rectangle without disposal. Palette reserved bytes are ignored and never copied to GOP.

Limits: 16 MiB total, canvas at most 1920×1080, 1–3000 frames, 10–6000 delay units per frame.
Empty dimensions, rectangles outside the canvas, nonzero frame reserved fields, truncation,
and trailing data are rejected. `frames.h` is the runtime parser shared with C parser tests.

## Verification

The OVMF application embeds the actual driver PE and calls `LoadImage` / `StartImage`, rather
than directly calling the driver's entry point. It does not bypass image authentication.
The eleven scenarios cover:

- All scenarios load the same real BIA1 playback driver through a mock FV2 ReadSection
  on the loaded device, including missing-protocol deadline, states 0/3 continuing and
  4–7 stopping, ExitBootServices while active, late protocol discovery, and GOP mode changes.
- Pixel readback sees the coloured changed rectangle and the restored first frame on looping; fallback timeout
  and mode-change redraw are checked.
- Bad data, missing FV2/data and authentication TEST_FAILED are refused before playback.
- A failed GOP blit is retried with the same frame, rather than silently skipping it.

QMP-confirmed screenshots also check the expected placement. The independent C tests exercise
every byte truncation of a valid sample, frame parsing, invalid sizes/counts, delay bounds, full-first-
frame requirement, reserved fields, rectangle bounds and trailing data. Tests filter the
driver/application log records so unrelated OVMF diagnostics do not affect assertions.

OVMF's AMI protocol and FV2 data provider in these tests are mocks. These tests establish
loadability, parsing, actual GOP pixel output, timing/lifecycle logic, and cleanup under OVMF.
They do not establish real AMITSE dispatch order, firmware-volume driver discovery, ASUS
physical boot behavior, Secure Boot compatibility, or successful FlashBack recovery.
GitHub Actions uses `.github/workflows/driver.yml` to install LLVM, QEMU and OVMF,
build the normal driver, and run these tests on an Ubuntu runner. Only after they pass is
the production `uefi/animlogo.efi` shared with the calling workflow. CI's Go matrix,
vendor firmware tests, releases and Pages all download that same-run artifact before
compiling Go or WebAssembly, replacing the checkout's embedded driver. A test build cannot
replace the production artifact: the job checks its SHA-256 before uploading it.

Driver source changes trigger Pages and vendor tests as well as normal CI. Release and
Pages publication wait for the driver tests; a missing artifact fails the build rather than
falling back to an older binary. Pages checks out the same triggering commit as its driver
job. Only synthetic QEMU logs/screenshots are retained on failure; vendor images are never
uploaded.

Releases first download the driver into ignored `driver/build/release/`. GoReleaser's global
before hook runs `go run ./driver/install.go` after its clean-checkout validation and before Go
compilation. This avoids a compiler-dependent generated binary making the checkout dirty
before validation, while keeping the normal release checks enabled. For a local GoReleaser
run, first run `driver/build.sh` or `driver/build.ps1`; the installer defaults to
`driver/build/production/animlogo.efi`, or uses
the file named by `ANIMLOGO_DRIVER`. It rejects missing or instrumented drivers.

## Definition sources

UEFI ABI layouts follow the [UEFI specification](https://uefi.org/specifications) and EDK II's
[GOP](https://github.com/tianocore/edk2/blob/master/MdePkg/Include/Protocol/GraphicsOutput.h),
[Loaded Image](https://github.com/tianocore/edk2/blob/master/MdePkg/Include/Protocol/LoadedImage.h),
and [boot services](https://github.com/tianocore/edk2/blob/master/MdePkg/Include/Uefi/UefiSpec.h)
definitions. AMI-specific behavior must be established by biosimage's AMITSE disassembly gate;
OVMF alone cannot verify proprietary AMI behavior.
