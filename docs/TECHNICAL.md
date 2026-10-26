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
   How: Claude Code -> claude mcp add -s user -t http office-pc <URL> (if office-pc already exists, first: claude mcp remove -s user office-pc) ; Codex -> codex mcp add office-pc --url <URL> ; then tell me to restart you, and use the office-pc tools (start with system_info).
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
| `run_command`     | Runs a command in a fresh shell. If it is not done after `wait_seconds` (default 45, at most 55), it keeps running in the background and the AI gets a `job_id`. Hard limit `timeout_seconds` (default 1800). |
| `check_command`   | Waits for a background command, returns its new output and finally the exit code; `stop=true` kills it with its child processes; without `job_id` it lists recent commands. |
| `read_file`       | Text with line numbers, `offset`/`limit` paging; normal files up to 20 MB. Understands UTF-8, UTF-8 with BOM, UTF-16 and (read-only) legacy Windows code pages such as GBK or windows-1250. |
| `write_file`      | Writes UTF-8 text, creates folders, can append. |
| `edit_file`       | Exact search-and-replace; keeps the encoding and Windows line endings; refuses ambiguous matches and files it could not write back unchanged. |
| `list_directory`  | Folders first, then files with size and date. |
| `take_screenshot` | Every screen as a JPEG, at most 1568 px on the long side. |

Background commands exist because MCP clients stop waiting for a plain JSON answer: Claude Code's HTTP request
timer starts at 60 s, and Codex has its own per-tool timeout. No call waits longer than 55 s; slow work continues
in the background and the AI polls it. Output is capped per read (first 8 KB and last 24 KB, with a note about what
was skipped), terminal colour codes are removed, and `read_file` returns at most 40 KB per call (Claude Code moves
tool results over 50,000 characters into a file).

When the AI connects, the server's `instructions` tell it which computer it controls, which shell it gets, that
there is no keyboard input, and that the owner may not be technical (explain, prefer reversible steps, ask before
deleting or installing).

### Client compatibility

Tested with Claude Code 2.1.310 (tools, background jobs and screenshots) and Codex 0.144.4 (connection). Notes for
anyone changing the protocol code:

* Newer Claude Code versions first send a `server/discover` probe (2026-07-28 protocol). The server answers it like
  any unknown method, with a JSON-RPC `-32601` error, and Claude Code falls back to `initialize`.
* `initialize` echoes the client's protocol version when it is one of 2025-11-25, 2025-06-18, 2025-03-26 or
  2024-11-05 (Claude Code sends 2025-11-25, Codex 2025-06-18).
* Codex probes the endpoint and `/.well-known/oauth-*` with GET when a server is added and at start-up; the `405`
  and `404` answers tell it that no login is needed.
* Results use `content` only, never `structuredContent` (Codex would show only the latter and drop images).
* Claude Code and Codex read new servers only when they start, so the AI has to be restarted once after adding one.

### Commands

* **Windows:** PowerShell 7 (`pwsh.exe`) if it is installed in Program Files, otherwise Windows PowerShell 5.1
  (`REMOTE_HANDS_POWERSHELL` can name another one). The command is saved as a temporary `.ps1` file with a UTF-8
  byte order mark and run with `-NoProfile -NonInteractive -ExecutionPolicy Bypass -File`, which avoids all
  command-line quoting problems. A prelude switches output to UTF-8, widens the console buffer to 300 columns (so
  tables are not cut) and turns off progress bars and colours. The command runs inside a script block piped to
  `Out-Default`, so PowerShell finishes printing tables before the script exits; afterwards PowerShell's success
  flag becomes the exit code (`$LASTEXITCODE` for programs, 1 for failed cmdlets; stderr output of a program that
  returned 0 does not count as failure, unlike in plain Windows PowerShell 5.1). Commands get their own hidden
  console (`CREATE_NO_WINDOW`), so they cannot write into the Remote Hands window or wait for keys typed there.
  Stopping uses `taskkill /T /F`, which also ends child processes.
* **macOS / Linux:** the user's shell if it is bash, zsh, dash, ksh or sh, otherwise `/bin/bash` or `/bin/sh`, run
  as `<shell> -c <command>` (or from a temporary file for commands over 64 KB) in a new session (`setsid`).
  Without a controlling terminal, `sudo`, `ssh` and `git` fail at once instead of waiting for a password. Stopping
  kills the whole process group.
* Everywhere: stdin is empty, `GIT_TERMINAL_PROMPT=0` and `NO_COLOR=1` are set, at most 16 commands run at once.
  Stop and timeout go through `exec.CommandContext`, which only kills a process that has not been reaped yet, so a
  reused process id is never hit. A background process that a command leaves behind should redirect its output
  (`nohup program >/dev/null 2>&1 &`): Remote Hands stops reading the output 3 s after the command itself ended.

### Other details

* While running, Remote Hands keeps the computer from falling asleep (`SetThreadExecutionState` on Windows,
  `caffeinate -i -w <pid>` on macOS; nothing on Linux). Use `--allow-sleep` to turn this off.
* The connection lines are put on the clipboard (`clip.exe`, `pbcopy`, `wl-copy`, `xclip` or `xsel`).
* Starting Remote Hands while it already runs shows the running copy's lines (it checks `GET /health` on the saved
  port) instead of starting a second server.
* The activity log in the window names the connecting device by its Tailscale machine name.

## Security model

* **Who can connect:** only devices that are in the same tailnet *and* know the 128-bit key in the URL. Requests
  whose sender address is not a Tailscale address (`100.64.0.0/10`) or this computer are refused, even if they
  reach the port some other way. Tailscale ACLs can narrow this further (for example, allow port 47863 only from
  your own laptop).
* **What a connected AI can do:** everything the logged-in user can do on that computer. Remote Hands does not
  elevate; a UAC prompt or `sudo` password has to be answered by a person at the computer.
* **Browsers:** requests that carry an `Origin` header are refused, so a web page cannot talk to the server even if
  it learned the address.
* **Wrong key:** `404 Not Found`, not `401` or `403`, because MCP clients answer those with an OAuth login. Failed
  attempts appear in the activity log (at most once a minute).
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

Environment variables: `REMOTE_HANDS_LANG`, `REMOTE_HANDS_CONFIG` (path of the settings file),
`REMOTE_HANDS_POWERSHELL` (Windows: the PowerShell to run commands with), `NO_COLOR`.

If Remote Hands is already running, starting it again only shows that copy's lines; `-new-key`, `-name` and `-port`
are refused then, so a key cannot look revoked while the running copy still accepts it.

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
arm64) and Linux (amd64, arm64, armv6+) binaries, publishes a GitHub release, and then installs that release with
`install.ps1` / `install.sh` on Windows, macOS and Linux runners. Tags with a dash (`v0.2.0-rc1`) become
pre-releases, which the installers do not pick up.

| File                    | Purpose |
|-------------------------|---------|
| `main.go`               | flags, start-up, connection lines, the keyboard commands in the window |
| `tailscale.go`          | finding the Tailscale address and machine names |
| `mcp.go`                | HTTP handler, authentication, JSON-RPC and MCP methods |
| `tools.go`              | tool definitions and the file tools |
| `jobs.go`               | running commands, background jobs, output capping |
| `textcodec.go`          | text file encodings |
| `sysinfo.go`            | system facts and screenshots |
| `platform_windows.go`, `platform_other.go`, `term_*.go` | everything system-specific |
| `ui.go`, `i18n.go`      | the window: layout and English / Chinese / Czech texts |
| `install.ps1`, `install.sh` | one-line installers |
