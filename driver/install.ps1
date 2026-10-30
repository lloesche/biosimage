param([string]$Source = $env:ANIMLOGO_DRIVER)
$taskPreviousSource = $env:ANIMLOGO_DRIVER
Push-Location (Join-Path $PSScriptRoot '..')
try {
  if ($Source) { $env:ANIMLOGO_DRIVER = $Source }
  go run ./driver/install.go
  if ($LASTEXITCODE) { throw 'Driver install failed' }
} finally {
  $env:ANIMLOGO_DRIVER = $taskPreviousSource
  Pop-Location
}
