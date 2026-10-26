# Remote Hands – technical notes

The [README](../README.md) is the step-by-step guide for everyone. This page is for people who want to know how
Remote Hands works, configure it by hand, or build it.

## How it works

```
 AI computer                                    remote computer
┌────────────────────────┐   WireGuard via   ┌─────────────────────────────────────┐
│ Claude Code / Codex    │     Tailscale     │ remote-hands                        │
│  MCP client ───────────┼──────────────────►│  http://100.x.y.z:47863/mcp/<key>   │
│  (streamable HTTP)     │                   │  runs commands, files, screenshots  │
└────────────────────────┘                   └─────────────────────────────────────┘
```

1. `remote-hands` asks the Tailscale client for this computer's Tailscale IPv4 address (`tailscale status --json`)
   and falls back to looking for an interface address in `100.64.0.0/10`. Until Tailscale is installed, running and
   logged in, it waits and tells the user what is missing.
2. It listens **only on that address** (TCP port 47863, or the next free port), so nothing is reachable from the local
   network or the internet – only from devices in the same tailnet. Tailscale encrypts the traffic (WireGuard), so
   plain HTTP is fine.
3. It serves the [Model Context Protocol](https://modelcontextprotocol.io) over *streamable HTTP* at
   `/mcp/<key>`. Every request is a POST that gets a plain `application/json` answer; there are no sessions and no
   server-sent events (GET answers `405`, as the specification allows).
4. It prints two lines for the user to paste into the AI. They contain the URL and the commands that register it:

   ```
   Remote Hands: please connect to my computer office-pc. It is an MCP server (streamable HTTP; the key is inside the URL, no other login): http://100.86.1.2:47863/mcp/0f3c…
   How: Claude Code -> claude mcp add -s user -t http office-pc <URL> ; Codex -> codex mcp add office-pc --url <URL> ; then tell me to restart you, and use the office-pc tools (start with system_info).
   ```

   They are plain ASCII without quotes, so e-mail and chat apps do not "smarten" them on the way.

The key, the port and an optional name are saved, so the lines stay the same across restarts:

| System  | Settings file                                              |
|---------|------------------------------------------------------------|
| Windows | `%APPDATA%\remote-hands\config.json`                      |
| macOS   | `~/Library/Application Support/remote-hands/config.json`   |
| Linux   | `~/.config/remote-hands/config.json`                       |

## Tools offered to the AI

| Tool              | What it does |
|-------------------|--------------|
| `system_info`     | OS, user, home and real Desktop folder (OneDrive-aware on Windows), shell, local time. |
| `run_command`     | Runs a command in a fresh shell. If it is not done after `wait_seconds` (default 45), it keeps running in the background and the AI gets a `job_id`. Hard limit `timeout_seconds` (default 1800). |
| `check_command`   | Waits for a background command, returns its new output and finally the exit code; `stop=true` kills it with its child processes; without `job_id` it lists recent commands. |
| `read_file`       | Text with line numbers, `offset`/`limit` paging. Understands UTF-8, UTF-8 with BOM, UTF-16 and (read-only) the Windows ANSI code page, e.g. GBK or windows-1250. |
| `write_file`      | Writes UTF-8 text, creates folders, can append. |
| `edit_file`       | Exact search-and-replace; keeps the encoding and Windows line endings; refuses ambiguous matches. |
| `list_directory`  | Folders first, then files with size and date. |
| `take_screenshot` | Every screen as a JPEG, at most 1568 px on the long side. |

Background commands exist because MCP clients give up on slow tool calls (Codex after 60 s by default). Output is
capped per read (first 8 KB and last 24 KB, with a note about what was skipped) and terminal colour codes are
removed.

When the AI connects, the server's `instructions` tell it which computer it controls, which shell it gets, that
there is no keyboard input, and that the owner may not be technical (explain, prefer reversible steps, ask before
deleting or installing).

### Commands

* **Windows:** PowerShell 7 (`pwsh.exe`) if it is installed in Program Files, otherwise Windows PowerShell 5.1. The
  command is saved as a temporary `.ps1` file with a UTF-8 byte order mark and run with `-NoProfile
  -NonInteractive -ExecutionPolicy Bypass -File`, which avoids all command-line quoting problems. A prelude switches
  output to UTF-8 and turns off progress bars and colours; an epilogue turns PowerShell's success flag into an exit
  code (`$LASTEXITCODE` for programs, 1 for failed cmdlets). Commands get their own hidden console
  (`CREATE_NO_WINDOW`), so they cannot write into the Remote Hands window or wait for keys typed there.
  Stopping uses `taskkill /T /F`, which also ends child processes.
* **macOS / Linux:** the user's shell if it is bash, zsh, dash, ksh or sh, otherwise `/bin/bash` or `/bin/sh`, run
  as `<shell> -c <command>` in a new session (`setsid`). Without a controlling terminal, `sudo`, `ssh` and `git`
  fail at once instead of waiting for a password. Stopping kills the whole process group.
* Everywhere: stdin is empty, `GIT_TERMINAL_PROMPT=0` and `NO_COLOR=1` are set, at most 16 commands run at once.

### Other details

* While running, Remote Hands keeps the computer from falling asleep (`SetThreadExecutionState` on Windows,
  `caffeinate -i -w <pid>` on macOS; nothing on Linux). Use `--allow-sleep` to turn this off.
* The connection lines are put on the clipboard (`clip.exe`, `pbcopy`, `wl-copy`, `xclip` or `xsel`).
* Starting Remote Hands while it already runs shows the running copy's lines (it checks `GET /health` on the saved
  port) instead of starting a second server.
* The activity log in the window names the connecting device by its Tailscale machine name.

## Security model

* **Who can connect:** only devices that are in the same tailnet *and* know the 128-bit key in the URL. Tailscale
  ACLs can narrow this further (for example, allow port 47863 only from your own laptop).
* **What a connected AI can do:** everything the logged-in user can do on that computer. Remote Hands does not
  elevate; a UAC prompt or `sudo` password has to be answered by a person at the computer.
* **Browsers:** requests that carry an `Origin` header are refused, so a web page cannot talk to the server even if
  it learned the address.
* **Wrong key:** `403 Forbidden`, not `401`, because a 401 makes MCP clients start an OAuth login. Failed attempts
  appear in the activity log (at most once a minute).
* **Revoking access:** type `new` in the window (or start with `--new-key`). Every old URL stops working at once.
* **The key is stored in plain text** in the settings file above and, on the AI computers, in the Claude Code /
  Codex configuration – just like the API keys those tools already store. Anyone who can read those files can
  already act as you.
* **No telemetry**, no outgoing connections except to the local Tailscale client.

## Command-line options

```
remote-hands [options]
  -lang en|zh|cs   language of the window (default: the system language, or REMOTE_HANDS_LANG)
  -name NAME       name the AI should use for this computer (default: its Tailscale machine name); remembered
  -port N          TCP port (default 47863, or the next free one); remembered
  -new-key         make a new key, so the old connection lines stop working
  -allow-sleep     do not keep the computer awake
  -listen H:P      for testing only: listen on host:port instead of the Tailscale address
  -version         print the version
```

Environment variables: `REMOTE_HANDS_LANG`, `REMOTE_HANDS_CONFIG` (path of the settings file), `NO_COLOR`.

The installers also understand `REMOTE_HANDS_DOWNLOAD` (base URL of a mirror that holds the release files),
`REMOTE_HANDS_NO_START=1` (install only) and `REMOTE_HANDS_NO_SHORTCUTS=1` (Windows: no Desktop/Start-menu
shortcuts).

## Adding the server to an AI by hand

Claude Code (`-s user` makes it available in every folder):

```sh
claude mcp add -s user -t http office-pc http://100.86.1.2:47863/mcp/<key>
claude mcp list        # shows "✓ Connected" when it works
```

Codex:

```sh
codex mcp add office-pc --url http://100.86.1.2:47863/mcp/<key>
```

or in `~/.codex/config.toml`:

```toml
[mcp_servers.office-pc]
url = "http://100.86.1.2:47863/mcp/<key>"
```

Any other MCP client: a streamable-HTTP server at that URL; alternatively use `http://100.86.1.2:47863/mcp` with
the header `Authorization: Bearer <key>`.

Check the network path without an AI: `curl http://100.86.1.2:47863/health` answers `remote-hands ok <version>`.

## Building and testing

Requires Go 1.23 or newer. There are no third-party dependencies.

```sh
go test ./...                 # unit and integration tests (runs real shell commands)
go build .                    # build for this computer
VERSION=v1.2.3 ./scripts/build.sh   # all release binaries + SHA256SUMS in dist/
```

Pushing a tag `v*` runs `.github/workflows/release.yml`: it tests, builds Windows (amd64, arm64), macOS (amd64,
arm64) and Linux (amd64, arm64, armv7) binaries, publishes a GitHub release, and then installs that release with
`install.ps1` / `install.sh` on Windows, macOS and Linux runners.

| File                    | Purpose |
|-------------------------|---------|
| `main.go`               | flags, start-up, connection lines, the keyboard commands in the window |
| `tailscale.go`          | finding the Tailscale address and machine names |
| `mcp.go`                | HTTP handler, authentication, JSON-RPC and MCP methods |
| `tools.go`              | tool definitions and the file tools |
| `jobs.go`               | running commands, background jobs, output capping |
| `textcodec.go`          | text file encodings |
| `sysinfo.go`            | system facts and screenshots |
| `platform_windows.go`, `platform_other.go` | everything system-specific |
| `ui.go`, `i18n.go`      | the window: layout and English / Chinese / Czech texts |
| `install.ps1`, `install.sh` | one-line installers |
