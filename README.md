# Remote Hands

Let an AI coding assistant (Claude Code, Codex, ...) on **one** computer control
**another** computer over your own [Tailscale](https://tailscale.com) network —
using nothing more exotic than SSH and a key.

There is no custom server and nothing new to keep running: Windows' own built-in
SSH service is the "daemon". One script, run once on the computer you want
controlled, sets everything up and prints the single line to give your AI.

## How it works

```
 AI computer                                 remote computer
┌────────────────────────┐    SSH over     ┌──────────────────────────────┐
│ Claude Code / Codex     │    Tailscale    │ OpenSSH server (built in)    │
│  runs  ssh -i key ...  ─┼────────────────►│  runs commands as you        │
└────────────────────────┘   (WireGuard)    └──────────────────────────────┘
```

The AI already knows how to run a shell command, so it just runs `ssh` and works
on the other computer. Tailscale carries the connection (encrypted, no ports open
to the internet); the SSH key is the secret that grants access.

## Set up the computer to be controlled (Windows)

1. Install [Tailscale](https://tailscale.com/download) and log in, so this
   computer is on your tailnet.
2. Download `setup-ai-ssh.ps1` from this repository.
3. Right-click **Windows PowerShell → Run as administrator**, then run it:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\setup-ai-ssh.ps1
   ```

It asks once whether you really want this (nothing changes unless you agree), then:

- installs and starts the built-in **OpenSSH server** (from now on it starts by
  itself — that is what keeps the computer reachable, no window to leave open);
- makes a **dedicated key** for this access and authorizes it on this computer;
- limits SSH in the firewall to **Tailscale addresses only** (`100.64.0.0/10`);
- prints the line to paste into your AI, and saves the **private key file** to
  carry to the other computer.

## Use it from the other computer

Save the private key file the script gave you, then tell your AI assistant, for
example:

> Control my computer `office-pc` over SSH. The key is at `C:\keys\ai-remote`.
> Connect with `ssh -i C:\keys\ai-remote -p 22 me@100.86.1.2` and then ...

Claude Code or Codex runs that `ssh` command with its normal shell tool — there is
no MCP server to add and no extra login. Test the path yourself first with:

```sh
ssh -i <key> -p 22 <user>@<tailscale-ip> whoami
```

### Prefer your key never to leave your computer?

Generate the key on the **AI** computer and let the script authorize only the
public half:

```powershell
# on the AI computer:
ssh-keygen -t ed25519 -f C:\keys\ai-remote
# then, on the computer to be controlled:
powershell -ExecutionPolicy Bypass -File .\setup-ai-ssh.ps1 -PublicKey "ssh-ed25519 AAAA... me@laptop"
```

## Security

- **Who can connect:** only devices in your own tailnet, and only with the key.
  The firewall rule allows the SSH port from `100.64.0.0/10` and nothing else, so
  it is not reachable from the local network or the internet. Tighten further with
  Tailscale ACLs if you like.
- **What the AI can do:** everything the logged-in user can do on that computer.
  SSH does not elevate on its own.
- **Revoke access:** delete the key's line from the `authorized_keys` file the
  script names, or turn SSH off entirely:

  ```powershell
  Stop-Service sshd; Set-Service sshd -StartupType Manual
  ```
- Nothing is sent anywhere: the key and the SSH server are standard, local, and
  under your control. There is no telemetry and no connection back to anyone.

## Options

```
setup-ai-ssh.ps1
  -User NAME       the account the AI logs in as (default: the current user)
  -Port N          SSH port (default 22)
  -KeyName NAME    file name for the generated key (default: ai-remote)
  -PublicKey "..." authorize this public key instead of generating a pair
  -AllowLan        do NOT restrict SSH to Tailscale addresses
  -IAgree          skip the confirmation popup (for an unattended install)
```

## Requirements and limits

- The controlled computer runs **Windows 10 or 11** (the script uses the built-in
  OpenSSH server and `New-NetFirewallRule`). On macOS or Linux, enable SSH and add
  the key the usual way — the connection line is the same.
- The AI computer needs `ssh` (built into Windows, macOS and Linux) and an AI that
  can run shell commands.
- Both computers must be on the same Tailscale network.
