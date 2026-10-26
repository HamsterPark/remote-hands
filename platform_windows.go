//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode           = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode           = kernel32.NewProc("SetConsoleMode")
	procSetConsoleTitleW         = kernel32.NewProc("SetConsoleTitleW")
	procSetThreadExecutionState  = kernel32.NewProc("SetThreadExecutionState")
	procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")
	procMultiByteToWideChar      = kernel32.NewProc("MultiByteToWideChar")
	procGetACP                   = kernel32.NewProc("GetACP")
	procRtlGetVersion            = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion")
	procSHGetKnownFolderPath     = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree            = syscall.NewLazyDLL("ole32.dll").NewProc("CoTaskMemFree")
)

const createNoWindow = 0x08000000

// setupConsole turns on ANSI colours in the console and reports whether
// they work.
func setupConsole() bool {
	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	const enableVirtualTerminalProcessing = 0x0004
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}

// isTerminal reports whether f is a real console (the NUL device also
// looks like a character device).
func isTerminal(f *os.File) bool {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

func setConsoleTitle(title string) {
	if p, err := syscall.UTF16PtrFromString(title); err == nil {
		procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
	}
}

// keepAwake stops Windows from going to sleep while Remote Hands runs
// (the screen may still turn off).
func keepAwake() (release func(), ok bool) {
	const esSystemRequired = 0x00000001
	if r, _, _ := procSetThreadExecutionState.Call(esSystemRequired); r == 0 {
		return func() {}, false
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				procSetThreadExecutionState.Call(esSystemRequired)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, true
}

func systemLanguage() string {
	r, _, _ := procGetUserDefaultUILanguage.Call()
	switch r & 0x3ff { // primary language id
	case 0x04:
		return "zh"
	case 0x05:
		return "cs"
	}
	return ""
}

// hideWindow keeps helper programs from opening console windows.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}

// prepareProcess gives commands their own hidden console, so they cannot
// scribble over the Remote Hands window or wait for keys typed into it.
func prepareProcess(cmd *exec.Cmd) { hideWindow(cmd) }

// killTree kills a command together with every process it started.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command(filepath.Join(systemRoot(), "System32", "taskkill.exe"), "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	hideWindow(k)
	_ = k.Run()
	_ = cmd.Process.Kill()
}

func systemRoot() string {
	if r := os.Getenv("SystemRoot"); r != "" {
		return r
	}
	return `C:\Windows`
}

// powerShell prefers PowerShell 7 when it is installed normally (not the
// Microsoft Store build, which virtualises parts of the file system), and
// otherwise uses the Windows PowerShell that every Windows has.
var powerShell = sync.OnceValue(func() string {
	for _, env := range []string{"ProgramW6432", "ProgramFiles"} {
		if d := os.Getenv(env); d != "" {
			p := filepath.Join(d, "PowerShell", "7", "pwsh.exe")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
})

func shellName() string {
	if filepath.Base(powerShell()) == "pwsh.exe" {
		return "PowerShell 7 (pwsh.exe)"
	}
	return "Windows PowerShell 5.1 (powershell.exe)"
}

// The command is saved as a script file, which avoids every quoting problem
// of the Windows command line. The prelude makes output UTF-8 and hides
// progress bars; the epilogue turns PowerShell's success flag into an exit
// code (1 for failed cmdlets, the program's own code for native programs).
const (
	psPrelude = "$ProgressPreference = 'SilentlyContinue'\r\n" +
		"if ($PSStyle) { $PSStyle.OutputRendering = 'PlainText' }\r\n" +
		"try { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false) } catch {}\r\n" +
		"$OutputEncoding = [System.Text.UTF8Encoding]::new($false)\r\n"
	psEpilogue = "\r\nif ($?) { exit 0 } elseif ($LASTEXITCODE) { exit $LASTEXITCODE } else { exit 1 }\r\n"
)

func shellCommand(command string) (*exec.Cmd, func(), error) {
	script, err := writePSScript(psPrelude + command + psEpilogue)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(powerShell(), "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
	return cmd, func() { os.Remove(script) }, nil
}

// writePSScript saves a script as UTF-8 with BOM, the only Unicode form
// Windows PowerShell 5.1 reads correctly.
func writePSScript(script string) (string, error) {
	f, err := os.CreateTemp("", "remote-hands-*.ps1")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString("\ufeff" + script)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func osDescription() string {
	var v struct {
		size, major, minor, build, platformID uint32
		csdVersion                            [128]uint16
	}
	v.size = uint32(unsafe.Sizeof(v))
	if r, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&v))); r != 0 {
		return "Windows"
	}
	name := "Windows " + strconv.Itoa(int(v.major))
	if v.major == 10 && v.build >= 22000 {
		name = "Windows 11"
	}
	return fmt.Sprintf("%s (version %d.%d, build %d)", name, v.major, v.minor, v.build)
}

// desktopDir asks Windows where the Desktop really is; with OneDrive
// backup it is not in the home folder.
func desktopDir(home string) string {
	folderIDDesktop := syscall.GUID{Data1: 0xB4BFCC3A, Data2: 0xDB2C, Data3: 0x424C, Data4: [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	var p *uint16
	if r, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&folderIDDesktop)), 0, 0, uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return homeDesktop(home)
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), 2*n)) != 0 {
		n++
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}

// decodeANSI decodes text in the Windows "ANSI" code page.
func decodeANSI(data []byte) (string, string, bool) {
	if len(data) == 0 || len(data) > 1<<30 {
		return "", "", false
	}
	const mbErrInvalidChars = 0x8
	src, srcLen := uintptr(unsafe.Pointer(&data[0])), uintptr(len(data))
	n, _, _ := procMultiByteToWideChar.Call(0, mbErrInvalidChars, src, srcLen, 0, 0)
	if n == 0 {
		return "", "", false
	}
	buf := make([]uint16, n)
	n, _, _ = procMultiByteToWideChar.Call(0, mbErrInvalidChars, src, srcLen, uintptr(unsafe.Pointer(&buf[0])), n)
	if n == 0 {
		return "", "", false
	}
	acp, _, _ := procGetACP.Call()
	return string(utf16.Decode(buf[:n])), fmt.Sprintf("Windows code page %d (edit_file will not change it)", acp), true
}

// The screenshot script makes the process DPI aware first; otherwise
// Windows reports scaled screen sizes and only part of the screen is
// captured.
const screenshotScript = `param([string]$Dir)
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition 'using System.Runtime.InteropServices; public static class RemoteHandsDpi { [DllImport("user32.dll")] public static extern bool SetProcessDPIAware(); }'
[void][RemoteHandsDpi]::SetProcessDPIAware()
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$i = 0
foreach ($s in [System.Windows.Forms.Screen]::AllScreens) {
  $b = $s.Bounds
  $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($b.Left, $b.Top, 0, 0, $bmp.Size)
  $bmp.Save((Join-Path $Dir ("screen{0}.png" -f $i)), [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose(); $bmp.Dispose(); $i++
}
`

func captureScreens(ctx context.Context, dir string) ([]string, error) {
	script, err := writePSScript(screenshotScript)
	if err != nil {
		return nil, err
	}
	defer os.Remove(script)
	cmd := exec.CommandContext(ctx, powerShell(), "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Dir", dir)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v (is the screen locked or the computer asleep?) %s", err, trimOutput(out))
	}
	var files []string
	for i := 0; i < 16; i++ {
		files = append(files, filepath.Join(dir, fmt.Sprintf("screen%d.png", i)))
	}
	return existingFiles(files), nil
}
