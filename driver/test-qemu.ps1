param([string]$Toolchain = 'C:\msys64\clang64\bin')
& python "$PSScriptRoot/test-qemu.py" --toolchain $Toolchain
if ($LASTEXITCODE) { throw 'Driver/QEMU tests failed' }
