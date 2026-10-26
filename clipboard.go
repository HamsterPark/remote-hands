package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// copyToClipboard puts text on this computer's clipboard if it can. The
// text is plain ASCII, which every clipboard tool handles.
func copyToClipboard(text string) bool {
	var tools [][]string
	switch runtime.GOOS {
	case "windows":
		tools = [][]string{{"clip.exe"}}
	case "darwin":
		tools = [][]string{{"pbcopy"}}
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			tools = append(tools, []string{"wl-copy"})
		}
		if os.Getenv("DISPLAY") != "" {
			tools = append(tools, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
		}
	}
	for _, t := range tools {
		path, err := exec.LookPath(t[0])
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, path, t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		hideWindow(cmd)
		err = cmd.Run()
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}

// trimOutput shortens helper program output for error messages.
func trimOutput(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}
