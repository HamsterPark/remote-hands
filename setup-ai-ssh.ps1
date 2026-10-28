<#
.SYNOPSIS
  Turnkey: let an AI (Claude Code / Codex) on another computer control THIS
  Windows PC over your Tailscale network, using the built-in OpenSSH + a key.
  No extra program to keep running: Windows' own SSH service is the "daemon".

.DESCRIPTION
  Run once, from an elevated PowerShell, on the computer you want controlled.
  It:
    1. shows a popup explaining what this turns on, and stops unless you agree;
    2. installs and starts the built-in OpenSSH server (if not already there);
    3. makes a dedicated key for this access and authorizes it on this PC
       (handling the Windows administrators_authorized_keys rule correctly);
    4. limits SSH to your Tailscale network (100.64.0.0/10) unless -AllowLan;
    5. prints the one line to paste into Claude Code / Codex on the other
       computer, and saves the private key file for you to carry over.

  Undo: delete the line it added to the authorized_keys file it names (that
  revokes this one key), or  Stop-Service sshd; Set-Service sshd -StartupType Manual
  to turn SSH off entirely.

  ASCII only on purpose: Chinese Windows reads non-ASCII .ps1 files as GBK.

  NOTE (verify on a real machine; this was written but not executed): the
  OpenSSH capability name, the administrators_authorized_keys ACL, the
  firewall rule name OpenSSH-Server-In-TCP, and ssh-keygen's empty-passphrase
  quoting can differ between Windows builds and PowerShell 5.1 vs 7.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\setup-ai-ssh.ps1
#>
[CmdletBinding()]
param(
    [string]$User = $env:USERNAME,
    [int]$Port = 22,
    [string]$KeyName = 'ai-remote',
    [string]$PublicKey,   # authorize THIS public key (operator-generated) instead of making one
    [switch]$AllowLan,    # do NOT restrict SSH to Tailscale addresses
    [switch]$IAgree       # skip the popup (for an unattended install)
)

$ErrorActionPreference = 'Stop'

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}
if (-not (Test-Admin)) {
    throw 'Run this from an elevated PowerShell (right-click -> Run as administrator).'
}

# 1. Consent. A popup if a desktop is available, otherwise a console question.
if (-not $IAgree) {
    $msg = @"
Let an AI on your OTHER computer control THIS computer?

It will be reachable only inside your own Tailscale network, and only to whoever
holds the key this script makes. The AI will be able to run commands, read and
write files, and do anything the user '$User' can do here.

This turns on the built-in Windows SSH server. You can undo it anytime (the
script prints how). Continue?
"@
    $agreed = $false
    try {
        Add-Type -AssemblyName System.Windows.Forms
        $r = [System.Windows.Forms.MessageBox]::Show($msg, 'Set up AI remote access',
            [System.Windows.Forms.MessageBoxButtons]::YesNo,
            [System.Windows.Forms.MessageBoxIcon]::Warning)
        $agreed = ($r -eq [System.Windows.Forms.DialogResult]::Yes)
    } catch {
        Write-Host $msg
        $agreed = ((Read-Host 'Type  yes  to continue').Trim().ToLower() -eq 'yes')
    }
    if (-not $agreed) { Write-Host 'Cancelled. Nothing was changed.'; return }
}

# 2. OpenSSH server: install the optional feature, then start it for good.
$cap = Get-WindowsCapability -Online | Where-Object { $_.Name -like 'OpenSSH.Server*' } | Select-Object -First 1
if (-not $cap) { throw 'OpenSSH.Server capability not found on this Windows edition.' }
if ($cap.State -ne 'Installed') {
    Write-Host 'Installing the OpenSSH server (one-time)...'
    Add-WindowsCapability -Online -Name $cap.Name | Out-Null
}
Set-Service -Name sshd -StartupType Automatic
Start-Service -Name sshd
# ssh-agent is handy but optional; do not fail if it is absent.
try { Set-Service -Name ssh-agent -StartupType Automatic; Start-Service ssh-agent } catch {}
Write-Host 'OpenSSH server is installed and running (starts automatically from now on).'

# 3. The key. Either authorize a public key you pass in, or make a new pair here.
$sshDir = Join-Path $env:USERPROFILE '.ssh'
New-Item -ItemType Directory -Force -Path $sshDir | Out-Null
$privPath = Join-Path $sshDir $KeyName
$pubLine = $null
$handoverNote = $null
if ($PublicKey) {
    $pubLine = $PublicKey.Trim()
    if ($pubLine -notmatch '^(ssh-ed25519|ssh-rsa|ecdsa-)') { throw 'That does not look like an SSH public key.' }
    $handoverNote = 'You passed a public key, so the private key stays on the other computer (nothing to hand over).'
} else {
    if (-not (Test-Path -LiteralPath $privPath)) {
        Write-Host 'Making a new SSH key for this access...'
        # -N '""' is the empty passphrase; verify on your PowerShell version.
        & ssh-keygen -q -t ed25519 -f $privPath -N '""' -C "$KeyName@$env:COMPUTERNAME"
    }
    $pubLine = (Get-Content -Raw -LiteralPath "$privPath.pub").Trim()
    $handoverNote = "Carry this PRIVATE key file to the other computer: $privPath"
}

# 4. Authorize the public key. Admins use a special, strictly-permissioned file.
$adminMember = $false
try {
    $members = Get-LocalGroupMember -Group 'Administrators' -ErrorAction Stop | ForEach-Object { $_.Name }
    $adminMember = ($members -contains "$env:COMPUTERNAME\$User") -or ($members -contains "AzureAD\$User")
} catch { $adminMember = $false }

if ($adminMember) {
    $authFile = Join-Path $env:ProgramData 'ssh\administrators_authorized_keys'
    if (-not (Test-Path -LiteralPath $authFile)) { New-Item -ItemType File -Force -Path $authFile | Out-Null }
    $existing = @(Get-Content -LiteralPath $authFile -ErrorAction SilentlyContinue)
    if ($existing -notcontains $pubLine) { Add-Content -LiteralPath $authFile -Value $pubLine }
    # Required ACL: only SYSTEM and Administrators, nothing inherited.
    & icacls.exe $authFile /inheritance:r /grant 'SYSTEM:F' /grant 'BUILTIN\Administrators:F' | Out-Null
    Write-Host "Authorized the key in $authFile (admin user)."
} else {
    $authFile = Join-Path $sshDir 'authorized_keys'
    if (-not (Test-Path -LiteralPath $authFile)) { New-Item -ItemType File -Force -Path $authFile | Out-Null }
    $existing = @(Get-Content -LiteralPath $authFile -ErrorAction SilentlyContinue)
    if ($existing -notcontains $pubLine) { Add-Content -LiteralPath $authFile -Value $pubLine }
    Write-Host "Authorized the key in $authFile (user $User)."
}

# 5. Firewall: by default allow the SSH port ONLY from Tailscale addresses.
$ruleName = 'OpenSSH-Server-In-TCP'
if (-not $AllowLan) {
    if (Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue) {
        Set-NetFirewallRule -Name $ruleName -RemoteAddress '100.64.0.0/10'
    } else {
        New-NetFirewallRule -Name $ruleName -DisplayName 'OpenSSH SSH Server (sshd)' -Enabled True `
            -Direction Inbound -Protocol TCP -Action Allow -LocalPort $Port -RemoteAddress '100.64.0.0/10' | Out-Null
    }
    Write-Host "Firewall: SSH ($Port) allowed from Tailscale (100.64.0.0/10) only."
} else {
    Write-Host "Firewall: left SSH ($Port) open to all networks (-AllowLan)."
}

# 6. Find this computer's Tailscale address for the connection line.
$tsExe = (Get-Command tailscale -ErrorAction SilentlyContinue).Source
if (-not $tsExe) { foreach ($p in "$env:ProgramFiles\Tailscale\tailscale.exe", "$env:ProgramFiles(x86)\Tailscale\tailscale.exe") { if (Test-Path $p) { $tsExe = $p; break } } }
$tsip = $null
if ($tsExe) {
    try { $tsip = (& $tsExe ip -4 2>$null | Select-Object -First 1).Trim() } catch {}
}
$host4 = if ($tsip) { $tsip } else { '<this computer''s Tailscale IP>' }

# 7. Print what to take to the other computer.
Write-Host ''
Write-Host '==================================================================' -ForegroundColor Cyan
Write-Host ' Done. On your OTHER computer, give Claude Code / Codex this:' -ForegroundColor Cyan
Write-Host '==================================================================' -ForegroundColor Cyan
Write-Host ("Control my computer {0} over SSH:  ssh -i {1} -p {2} {3}@{4}" -f $env:COMPUTERNAME, "<the private key file>", $Port, $User, $host4)
Write-Host $handoverNote
if (-not $tsip) { Write-Host 'Tailscale address not found yet: install/log in to Tailscale, then run  tailscale ip -4  for the address.' }
Write-Host ''
Write-Host 'To revoke later: delete that key line from the authorized_keys file above,'
Write-Host 'or  Stop-Service sshd; Set-Service sshd -StartupType Manual  to turn SSH off.'
