param([string]$Toolchain = 'C:\msys64\clang64\bin', [switch]$Test)
$arguments = @("$PSScriptRoot/build.py", '--toolchain', $Toolchain)
if ($Test) { $arguments += '--test' }
& python @arguments
if ($LASTEXITCODE) { throw 'Driver build failed' }
