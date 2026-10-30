"""Shared freestanding driver build for Linux, macOS and Windows (Python stdlib only)."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parent
BUILD = ROOT / "build"
FLAGS = ["-target", "x86_64-unknown-windows", "-ffreestanding", "-fno-builtin",
         "-fno-stack-protector", "-mno-red-zone", "-Os", "-Wall", "-Wextra", "-Werror"]


def tool(name, toolchain=None):
    suffix = ".exe" if os.name == "nt" else ""
    candidates = []
    if toolchain:
        candidates.append(Path(toolchain) / (name + suffix))
    else:
        override = os.environ.get(name.upper().replace("-", "_"))
        if override:
            candidates.append(Path(override))
        if os.name == "nt":
            candidates.append(Path("C:/msys64/clang64/bin") / (name + suffix))
        if sys.platform == "darwin":
            for prefix in ("/opt/homebrew/opt/llvm/bin", "/usr/local/opt/llvm/bin"):
                candidates.append(Path(prefix) / name)
        found = shutil.which(name)
        if found:
            candidates.append(Path(found))
    for candidate in candidates:
        if candidate.is_file():
            # Keep lld-link's invocation name: resolving its symlink to 'lld'
            # would select the wrong linker frontend on Linux/macOS.
            return os.path.abspath(candidate)
    raise RuntimeError(f"Cannot find {name}; install LLVM or pass --toolchain /path/to/bin")


def run(args):
    subprocess.run([str(a) for a in args], cwd=ROOT, check=True)


def build(toolchain=None, test=False):
    destination = BUILD / ("test" if test else "production")
    destination.mkdir(parents=True, exist_ok=True)
    obj, image = destination / "animlogo.obj", destination / "animlogo.efi"
    defines = ["-DANIMLOGO_QEMU_TEST"] if test else []
    run([tool("clang", toolchain), *FLAGS, *defines, "-c", "animlogo.c", "-o", obj])
    run([tool("lld-link", toolchain), "/subsystem:efi_boot_service_driver", "/entry:efi_main",
         "/nodefaultlib", "/machine:x64", "/dynamicbase", "/timestamp:0",
         f"/out:{image}", obj])
    if not test:
        shutil.copyfile(image, ROOT.parent / "uefi/animlogo.efi")
    return image


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--toolchain")
    parser.add_argument("--test", action="store_true")
    args = parser.parse_args()
    build(args.toolchain, args.test)
