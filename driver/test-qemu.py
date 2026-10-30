"""Run the same native parser and headless OVMF scenarios on all supported hosts."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import time

from build import BUILD, FLAGS, ROOT, build, run, tool


def firmware_pair(qemu, code=None, variables=None):
    code = code or os.environ.get("OVMF_CODE")
    variables = variables or os.environ.get("OVMF_VARS")
    if code or variables:
        if not code or not variables:
            raise RuntimeError("Specify both --ovmf-code and --ovmf-vars (matching firmware pair)")
        pair = Path(code), Path(variables)
        if not all(p.is_file() for p in pair):
            raise RuntimeError(f"Missing OVMF image: {pair}")
        return pair
    # QEMU on MSYS2/Homebrew, Ubuntu/Debian's ovmf package, then Fedora's edk2-ovmf.
    share = Path(qemu).parent.parent / "share/qemu"
    candidates = [(share / "edk2-x86_64-code.fd", share / "edk2-i386-vars.fd")]
    for folder in (Path("/usr/share/OVMF"), Path("/usr/share/edk2/ovmf")):
        for code_name, vars_name in (("OVMF_CODE_4M.fd", "OVMF_VARS_4M.fd"), ("OVMF_CODE.fd", "OVMF_VARS.fd")):
            candidates.append((folder / code_name, folder / vars_name))
    for pair in candidates:
        if all(p.is_file() for p in pair):
            return pair
    raise RuntimeError("Cannot find OVMF; install ovmf/edk2-ovmf or provide --ovmf-code and --ovmf-vars")


def embed_driver(image):
    data = image.read_bytes()
    body = ",".join(f"0x{b:02x}" for b in data)
    (BUILD / "driver_bytes.h").write_text(f"static unsigned char driver_bytes[] = {{{body}}};\n")


def capture_animation(proc, log, port, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise RuntimeError("QEMU exited before rendering the animation")
        if "animlogo: draw" in log.read_text(errors="replace"):
            break
        time.sleep(0.1)
    else:
        raise RuntimeError("Timed out waiting for the animation")
    with socket.create_connection(("127.0.0.1", port), timeout=5) as monitor:
        with monitor.makefile("rwb") as stream:
            json.loads(stream.readline())  # QMP greeting
            for command in ({"execute": "qmp_capabilities"},
                            {"execute": "screendump", "arguments": {"filename": str(BUILD / "animation.ppm")}}):
                stream.write(json.dumps(command).encode() + b"\n")
                stream.flush()
                while True:
                    response = json.loads(stream.readline())
                    if "error" in response:
                        raise RuntimeError(f"QEMU screenshot failed: {response['error']}")
                    if "return" in response:
                        break


def check_placement():
    magic, dimensions, maximum, pixels = (BUILD / "animation.ppm").read_bytes().split(b"\n", 3)
    if magic != b"P6" or maximum != b"255":
        raise RuntimeError("Unexpected screenshot format")
    sw, sh = map(int, dimensions.split())
    x, y = (sw - 160) // 2, (sh - 80) // 2
    if 160 * 5 <= sw * 2 and 80 * 5 <= sh * 2:
        y = sh * 382 // 1000 - 40
    at = ((y + 79) * sw + x + 159) * 3
    if pixels[at:at + 3] != bytes([32, 32, 32]):
        raise RuntimeError("Animation not at the expected screen position")


def check_log(case, log):
    text = "\n".join(line for line in log.read_text(errors="replace").splitlines()
                     if line.startswith(("animlogo:", "bootcheck:", "PASS ", "FAIL ")))
    if case in (6, 7, 9):
        if "PASS rejected" not in text or re.search(r"animlogo: (started|draw)|FAIL", text):
            raise RuntimeError(f"Input rejection case {case} failed:\n{text}")
        return
    stop = ("timeout-stop", "state-stop", "exit-stop", "state-stop", "unload",
            "timeout-stop", "", "", "unload", "", "timeout-stop")[case]
    if "FAIL" in text or "PASS" not in text or "animlogo: draw" not in text or f"animlogo: {stop}" not in text:
        raise RuntimeError(f"Case {case} failed:\n{text}")
    if re.search(r"animlogo: (?:timeout|state|exit)-stop\n(?:(?!animlogo: started)[\s\S])*?animlogo: draw", text):
        raise RuntimeError(f"Drawing continued after stop in case {case}")
    if case == 1 and (text.count("state-stop") != 4 or len(re.findall(r"bootcheck: state-three\n(?:animlogo: frame\n)?animlogo: draw", text)) != 4):
        raise RuntimeError("States 0/3 did not play or states 4–7 did not stop")
    if case == 5 and (text.count("animlogo: frame") < 3 or "bootcheck: animation-pixels-loop" not in text):
        raise RuntimeError("Real frame pixels/loop reset not verified")
    if case in (4, 8) and not re.search(r"bootcheck: mode-change\n(?:animlogo: frame\n)?animlogo: draw", text):
        raise RuntimeError("No animation redraw after GOP mode change")
    if case == 10 and "bootcheck: blt-retry" not in text:
        raise RuntimeError("Failed GOP frame not retried")
    if case == 0:
        check_placement()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--toolchain")
    parser.add_argument("--qemu")
    parser.add_argument("--ovmf-code")
    parser.add_argument("--ovmf-vars")
    parser.add_argument("--timeout", type=int, default=60, help="maximum seconds per OVMF boot")
    args = parser.parse_args()
    if args.toolchain:
        os.environ["PATH"] = str(Path(args.toolchain)) + os.pathsep + os.environ.get("PATH", "")
    qemu = args.qemu
    if not qemu:
        try:
            qemu = tool("qemu-system-x86_64", args.toolchain)
        except RuntimeError:
            # A custom LLVM bin directory need not also contain QEMU.
            if not args.toolchain:
                raise
            qemu = tool("qemu-system-x86_64")
    code, variables = firmware_pair(qemu, args.ovmf_code, args.ovmf_vars)
    print(f"OVMF pair: {code}, {variables}", flush=True)
    asset = ROOT.parent / "uefi/animlogo.efi"
    before = hashlib.sha256(asset.read_bytes()).hexdigest() if asset.exists() else None
    production = BUILD / "production/animlogo.efi"
    production_before = hashlib.sha256(production.read_bytes()).hexdigest() if production.exists() else None
    test_image = build(args.toolchain, test=True)
    native_test = BUILD / ("frames_test.exe" if os.name == "nt" else "frames_test")
    run([tool("clang", args.toolchain), "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "frames_test.c", "-o", native_test])
    run([native_test])
    embed_driver(test_image)
    fat = BUILD / "fat"
    (fat / "EFI/BOOT").mkdir(parents=True, exist_ok=True)
    for case in range(11):
        run([tool("clang", args.toolchain), *FLAGS, f"-DTEST_CASE={case}", "-c", "bootcheck.c", "-o", "build/bootcheck.obj"])
        run([tool("lld-link", args.toolchain), "/subsystem:efi_application", "/entry:efi_main", "/nodefaultlib",
             "/machine:x64", "/out:build/fat/EFI/BOOT/BOOTX64.EFI", "build/bootcheck.obj"])
        shutil.copyfile(variables, BUILD / "vars.fd")
        log = BUILD / f"case-{case}.log"
        log.write_text("")
        with socket.socket() as reserve:
            reserve.bind(("127.0.0.1", 0))
            port = reserve.getsockname()[1]
        command = [qemu, "-machine", "q35", "-accel", "tcg", "-m", "256", "-net", "none",
                   "-display", "none", "-serial", "none", "-qmp", f"tcp:127.0.0.1:{port},server=on,wait=off", "-no-reboot",
                   "-drive", f"if=pflash,format=raw,readonly=on,file={code}",
                   "-drive", f"if=pflash,format=raw,file={BUILD / 'vars.fd'}",
                   "-drive", f"format=raw,file=fat:rw:{fat}", "-debugcon", f"file:{log}",
                   "-global", "isa-debugcon.iobase=0x402", "-device", "isa-debug-exit,iobase=0xf4,iosize=0x04"]
        with (BUILD / f"qemu-{case}.err").open("wb") as stderr:
            proc = subprocess.Popen(command, cwd=ROOT, stderr=stderr, stdout=subprocess.DEVNULL,
                                    creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
            try:
                if case == 0:
                    capture_animation(proc, log, port, args.timeout)
                status = proc.wait(timeout=args.timeout)
                if status != 1:
                    raise RuntimeError(f"QEMU case {case} exited {status}; see {stderr.name}")
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()
        check_log(case, log)
        print(f"QEMU case {case} PASS", flush=True)
    after = hashlib.sha256(asset.read_bytes()).hexdigest() if asset.exists() else None
    production_after = hashlib.sha256(production.read_bytes()).hexdigest() if production.exists() else None
    if before != after or production_before != production_after:
        raise RuntimeError("Test builds replaced the production asset")
    print("All 11 OVMF scenarios passed; production driver unchanged", flush=True)


if __name__ == "__main__":
    main()
