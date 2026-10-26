//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

func setupConsole() bool { return isTerminal(os.Stdout) }

func setConsoleTitle(title string) {
	if isTerminal(os.Stdout) {
		fmt.Printf("\x1b]0;%s\x07", title)
	}
}

// keepAwake stops a Mac from going to sleep while Remote Hands runs.
// caffeinate exits by itself when this process ends.
func keepAwake() (release func(), ok bool) {
	if runtime.GOOS != "darwin" {
		return func() {}, false
	}
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return func() {}, false
	}
	go func() { _ = cmd.Wait() }()
	var once sync.Once
	return func() { once.Do(func() { _ = cmd.Process.Kill() }) }, true
}

func systemLanguage() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if v := os.Getenv(k); v != "" && v != "C" && !strings.HasPrefix(v, "C.") && v != "POSIX" {
			return v
		}
	}
	if runtime.GOOS == "darwin" {
		// For example: (\n    "zh-Hans-CN",\n    "en-GB"\n)
		if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			langs := strings.FieldsFunc(string(out), func(r rune) bool { return strings.ContainsRune("(),\"\n\t ", r) })
			if len(langs) > 0 {
				return langs[0]
			}
		}
	}
	return ""
}

func hideWindow(*exec.Cmd) {}

// prepareProcess starts commands in a new session without a controlling
// terminal: a command that wants a password (sudo, ssh, git) then fails at
// once instead of waiting for keys typed into the Remote Hands window, and
// the whole process group can be killed.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

var unixShell = sync.OnceValue(func() string {
	if s := os.Getenv("SHELL"); s != "" {
		switch filepath.Base(s) {
		case "bash", "zsh", "sh", "dash", "ksh":
			if isExecutable(s) {
				return s
			}
		}
	}
	for _, s := range []string{"/bin/bash", "/usr/bin/bash", "/bin/zsh", "/bin/sh"} {
		if isExecutable(s) {
			return s
		}
	}
	return "/bin/sh"
})

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

func shellName() string {
	return fmt.Sprintf("%s (%s -c)", filepath.Base(unixShell()), unixShell())
}

func shellCommand(ctx context.Context, command string) (*exec.Cmd, func(), error) {
	// One argument may not be longer than 128 KiB on Linux, so long scripts
	// go through a temporary file.
	if len(command) < 64<<10 {
		return exec.CommandContext(ctx, unixShell(), "-c", command), func() {}, nil
	}
	f, err := os.CreateTemp("", "remote-hands-*.sh")
	if err != nil {
		return nil, nil, err
	}
	_, err = f.WriteString(command)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return nil, nil, err
	}
	return exec.CommandContext(ctx, unixShell(), f.Name()), func() { os.Remove(f.Name()) }, nil
}

func osDescription() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
		return "macOS"
	case "linux":
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
					return strings.Trim(v, `"'`)
				}
			}
		}
		return "Linux"
	}
	return runtime.GOOS
}

func desktopDir(home string) string {
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("xdg-user-dir", "DESKTOP").Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" && d != home {
				return d
			}
		}
	}
	return homeDesktop(home)
}

func decodeANSI([]byte) (string, string, bool) { return "", "", false }

func captureScreens(ctx context.Context, dir string) ([]string, error) {
	var files []string
	for i := 0; i < 4; i++ {
		files = append(files, filepath.Join(dir, fmt.Sprintf("screen%d.png", i)))
	}
	if runtime.GOOS == "darwin" {
		// One file per display; screencapture ignores the extra names.
		out, err := exec.CommandContext(ctx, "screencapture", append([]string{"-x", "-t", "png"}, files...)...).CombinedOutput()
		if err != nil {
			if out2, err2 := exec.CommandContext(ctx, "screencapture", "-x", "-t", "png", files[0]).CombinedOutput(); err2 != nil {
				return nil, fmt.Errorf("screencapture: %v %s %s", err2, trimOutput(out), trimOutput(out2))
			}
		}
		return existingFiles(files), nil
	}
	// Linux: use whichever screenshot tool is installed.
	tools := [][]string{
		{"grim", files[0]},
		{"gnome-screenshot", "-f", files[0]},
		{"spectacle", "-b", "-n", "-f", "-o", files[0]},
		{"scrot", files[0]},
		{"import", "-window", "root", files[0]},
	}
	var tried []string
	for _, t := range tools {
		if _, err := exec.LookPath(t[0]); err != nil {
			continue
		}
		tried = append(tried, t[0])
		if err := exec.CommandContext(ctx, t[0], t[1:]...).Run(); err == nil {
			if got := existingFiles(files[:1]); len(got) > 0 {
				return got, nil
			}
		}
	}
	if len(tried) == 0 {
		return nil, fmt.Errorf("no screenshot tool found; install one of grim, gnome-screenshot, spectacle, scrot or ImageMagick")
	}
	return nil, fmt.Errorf("%s could not take a screenshot (is there a graphical session?)", strings.Join(tried, ", "))
}
