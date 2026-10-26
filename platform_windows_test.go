package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shells returns the PowerShell versions installed here: Windows
// PowerShell 5.1 always, PowerShell 7 when present.
func shells(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{"powershell5": filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")}
	if p := defaultPowerShell(); strings.EqualFold(filepath.Base(p), "pwsh.exe") {
		m["pwsh7"] = p
	}
	return m
}

func TestPowerShellOutputAndExitCodes(t *testing.T) {
	a := newTestApp(t)
	for name, exe := range shells(t) {
		t.Run(name, func(t *testing.T) {
			t.Setenv("REMOTE_HANDS_POWERSHELL", exe)
			cases := []struct{ command, want, code string }{
				// Objects without a formatting view used to vanish because the
				// script exited before PowerShell printed the table.
				{"[pscustomobject]@{Alpha = 1; Beta = 'x'}", "Alpha", "Exit code: 0"},
				{"Get-Process -Id $PID | Select-Object Name, Id", "Name", "Exit code: 0"},
				{"Write-Output before; [pscustomobject]@{Gamma = 3}; Write-Output after", "after", "Exit code: 0"},
				{"param([int]$x = 5)\n\"x=$x\"", "x=5", "Exit code: 0"},
				// Windows PowerShell 5.1 treats stderr of a program as an error.
				{`cmd /c "echo to-stderr 1>&2" 2>&1`, "to-stderr", "Exit code: 0"},
				{"cmd /c exit 4", "", "Exit code: 4"},
				{"Write-Output partial; exit 3", "partial", "Exit code: 3"},
				{"throw 'boom'", "boom", "Exit code: 1"},
			}
			for _, c := range cases {
				out := mustCall(t, a, "run_command", map[string]any{"command": c.command, "wait_seconds": 55})
				if !strings.Contains(out, c.code) || !strings.Contains(out, c.want) {
					t.Errorf("%q:\n%s", c.command, out)
				}
			}
		})
	}
}

func TestWideTables(t *testing.T) {
	a := newTestApp(t)
	long := strings.Repeat("w", 200)
	out := mustCall(t, a, "run_command", map[string]any{"command": "[pscustomobject]@{Short = 1; Long = '" + long + "'} | Format-Table", "wait_seconds": 55})
	if !strings.Contains(out, long) {
		t.Errorf("table was cut:\n%s", out)
	}
}

func TestDecodeLegacyCodePage(t *testing.T) {
	// "Příliš žluťoučký" in windows-1250 and "中文" in GBK: whichever of the
	// two matches this computer's language must decode exactly.
	cases := map[uintptr]struct {
		data []byte
		want string
	}{
		1250: {[]byte("P\xf8\xedli\x9a \x9elu\x9dou\xe8k\xfd"), "Příliš žluťoučký"},
		936:  {[]byte("\xd6\xd0\xce\xc4"), "中文"},
	}
	for cp, c := range cases {
		s, ok := decodeCodePage(cp, c.data)
		if !ok || s != c.want {
			t.Errorf("code page %d: %q %v", cp, s, ok)
		}
	}
	p := filepath.Join(t.TempDir(), "legacy.txt")
	if c, ok := cases[languageCodePage()]; ok {
		os.WriteFile(p, c.data, 0o644)
		out := mustCall(t, newTestApp(t), "read_file", map[string]any{"path": p})
		if !strings.Contains(out, c.want) {
			t.Errorf("read_file:\n%s", out)
		}
	}
}
