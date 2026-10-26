# Remote Hands installer for Windows 10 and 11.
#
# Paste this into PowerShell (it downloads the program, checks it, puts a
# "Remote Hands" shortcut on the Desktop and in the Start menu, and starts it):
#
#   irm https://raw.githubusercontent.com/HamsterPark/remote-hands/main/install.ps1 | iex
#
# Window language: put  $env:REMOTE_HANDS_LANG='zh';  (or 'cs', 'en') in front.
# Running it again installs the newest version.

& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch { }

    $repo = 'HamsterPark/remote-hands'
    $base = "https://github.com/$repo/releases/latest/download"
    if ($env:REMOTE_HANDS_DOWNLOAD) { $base = $env:REMOTE_HANDS_DOWNLOAD.TrimEnd('/') }
    $arch = 'amd64'
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $arch = 'arm64' }
    $asset = "remote-hands-windows-$arch.exe"
    $dir = Join-Path $env:LOCALAPPDATA 'Programs\RemoteHands'
    $exe = Join-Path $dir 'remote-hands.exe'
    $lang = "$env:REMOTE_HANDS_LANG".Trim().ToLower()

    Write-Host ''
    Write-Host 'Remote Hands installer' -ForegroundColor Cyan
    Write-Host "Downloading $asset ..."
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $tmp = "$exe.download"
    $sums = "$exe.sha256sums"
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $tmp
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sums
    } catch {
        throw "Download failed: $($_.Exception.Message) - check the internet connection and try again."
    }
    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($asset) + '$'
    $expected = Get-Content $sums | ForEach-Object { if ($_ -match $pattern) { $Matches[1].ToLower() } } | Select-Object -First 1
    Remove-Item $sums -Force
    $actual = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash.ToLower()
    if (-not $expected -or $expected -ne $actual) {
        Remove-Item $tmp -Force
        throw 'The download is damaged (checksum mismatch). Please try again.'
    }

    # An older copy that is still running has to stop before it can be replaced.
    Get-Process -Name 'remote-hands' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $exe } | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500
    Move-Item -Force $tmp $exe
    Unblock-File -Path $exe -ErrorAction SilentlyContinue

    # Shortcuts, so that next time a double-click is enough.
    $arguments = ''
    if ($lang -in @('en', 'zh', 'cs')) { $arguments = "--lang $lang" }
    $shell = New-Object -ComObject WScript.Shell
    $places = @([Environment]::GetFolderPath('Desktop'), (Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs'))
    if ($env:REMOTE_HANDS_NO_SHORTCUTS) { $places = @() }
    foreach ($place in $places) {
        try {
            $link = $shell.CreateShortcut((Join-Path $place 'Remote Hands.lnk'))
            $link.TargetPath = $exe
            $link.Arguments = $arguments
            $link.WorkingDirectory = $dir
            $link.Description = 'Let Claude Code or Codex on your other computer control this computer through Tailscale'
            $link.Save()
        } catch { }
    }
    Write-Host "Installed: $exe"
    Write-Host 'Next time, just double-click "Remote Hands" on your Desktop or in the Start menu.'
    Write-Host ''

    if ($env:REMOTE_HANDS_NO_START) { return }
    if ($arguments) { & $exe --lang $lang } else { & $exe }
}
