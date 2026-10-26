//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadFileRefusesPipes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	start := time.Now()
	out, isErr := callTool(t, newTestApp(t), "read_file", map[string]any{"path": p})
	if !isErr || !strings.Contains(out, "not a normal file") || time.Since(start) > 5*time.Second {
		t.Errorf("FIFO: %v %s", isErr, out)
	}
}

func TestIsTerminalDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("/dev/null counted as a terminal")
	}
}
