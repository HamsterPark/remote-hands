package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func callTool(t *testing.T, a *app, name string, args map[string]any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range a.tools {
		if tl.Name == name {
			r := tl.run(context.Background(), raw, "test")
			var b strings.Builder
			for _, c := range r.Content {
				b.WriteString(c.Text)
			}
			return b.String(), r.IsError
		}
	}
	t.Fatalf("no tool %s", name)
	return "", true
}

func mustCall(t *testing.T, a *app, name string, args map[string]any) string {
	t.Helper()
	out, isErr := callTool(t, a, name, args)
	if isErr {
		t.Fatalf("%s(%v) failed: %s", name, args, out)
	}
	return out
}

func TestWriteReadEditList(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "notes.txt")

	mustCall(t, a, "write_file", map[string]any{"path": p, "content": "alpha\nbeta\ngamma\nbeta\n"})
	out := mustCall(t, a, "read_file", map[string]any{"path": p})
	if !strings.Contains(out, "lines 1-4 of 4") || !strings.Contains(out, "     2\tbeta\n") {
		t.Errorf("read_file:\n%s", out)
	}
	out = mustCall(t, a, "read_file", map[string]any{"path": p, "offset": 2, "limit": "2"})
	if !strings.Contains(out, "lines 2-3 of 4") || strings.Contains(out, "alpha") || !strings.Contains(out, "use offset 4") {
		t.Errorf("read_file offset/limit:\n%s", out)
	}

	if out, isErr := callTool(t, a, "edit_file", map[string]any{"path": p, "old_string": "beta", "new_string": "BETA"}); !isErr || !strings.Contains(out, "2 times") {
		t.Errorf("ambiguous edit was not refused: %s", out)
	}
	mustCall(t, a, "edit_file", map[string]any{"path": p, "old_string": "beta\ngamma", "new_string": "beta\nGAMMA"})
	mustCall(t, a, "edit_file", map[string]any{"path": p, "old_string": "beta", "new_string": "BETA", "replace_all": "true"})
	if b, _ := os.ReadFile(p); string(b) != "alpha\nBETA\nGAMMA\nBETA\n" {
		t.Errorf("after edits: %q", b)
	}
	if _, isErr := callTool(t, a, "edit_file", map[string]any{"path": p, "old_string": "nothere", "new_string": "x"}); !isErr {
		t.Error("edit with missing text did not fail")
	}

	mustCall(t, a, "write_file", map[string]any{"path": p, "content": "delta\n", "append": true})
	if b, _ := os.ReadFile(p); !strings.HasSuffix(string(b), "BETA\ndelta\n") {
		t.Errorf("append: %q", b)
	}

	out = mustCall(t, a, "list_directory", map[string]any{"path": dir})
	if !strings.Contains(out, "[folder]") || !strings.Contains(out, "sub") {
		t.Errorf("list_directory:\n%s", out)
	}
	out = mustCall(t, a, "list_directory", map[string]any{"path": filepath.Join(dir, "sub")})
	if !strings.Contains(out, "notes.txt") || !strings.Contains(out, "(1 items)") {
		t.Errorf("list_directory:\n%s", out)
	}
	if _, isErr := callTool(t, a, "read_file", map[string]any{"path": dir}); !isErr {
		t.Error("read_file on a folder did not fail")
	}
}

func TestEditKeepsCRLFAndUTF16(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()

	crlf := filepath.Join(dir, "crlf.txt")
	os.WriteFile(crlf, []byte("one\r\ntwo\r\nthree\r\n"), 0o644)
	mustCall(t, a, "edit_file", map[string]any{"path": crlf, "old_string": "one\ntwo", "new_string": "one\n2"})
	if b, _ := os.ReadFile(crlf); string(b) != "one\r\n2\r\nthree\r\n" {
		t.Errorf("CRLF edit: %q", b)
	}

	u16 := filepath.Join(dir, "utf16.txt")
	os.WriteFile(u16, encodeText("Grüße 你好\r\n", textEncoding{utf16: binary.LittleEndian}), 0o644)
	out := mustCall(t, a, "read_file", map[string]any{"path": u16})
	if !strings.Contains(out, "Grüße 你好") || !strings.Contains(out, "utf-16le") {
		t.Errorf("read UTF-16:\n%s", out)
	}
	mustCall(t, a, "edit_file", map[string]any{"path": u16, "old_string": "你好", "new_string": "Ahoj"})
	b, _ := os.ReadFile(u16)
	if text, enc, _ := decodeText(b); text != "Grüße Ahoj\r\n" || enc.name != "utf-16le" {
		t.Errorf("UTF-16 edit: %q %s", text, enc.name)
	}

	// A one-line old_string with a multi-line new_string must not bring
	// bare "\n" into a Windows-style file.
	mustCall(t, a, "edit_file", map[string]any{"path": crlf, "old_string": "2", "new_string": "two\nand a half"})
	if b, _ := os.ReadFile(crlf); string(b) != "one\r\ntwo\r\nand a half\r\nthree\r\n" {
		t.Errorf("CRLF edit with new lines: %q", b)
	}

	// UTF-16 that would not survive decoding and encoding again is left alone.
	odd := filepath.Join(dir, "odd16.txt")
	os.WriteFile(odd, []byte{0xFF, 0xFE, 'a', 0, 0x00, 0xD8, 'b', 0}, 0o644) // lone surrogate
	if out, isErr := callTool(t, a, "edit_file", map[string]any{"path": odd, "old_string": "a", "new_string": "c"}); !isErr {
		t.Errorf("unsafe UTF-16 edit was not refused: %s", out)
	}

	bin := filepath.Join(dir, "data.bin")
	os.WriteFile(bin, []byte{1, 2, 0, 3}, 0o644)
	if _, isErr := callTool(t, a, "read_file", map[string]any{"path": bin}); !isErr {
		t.Error("binary file was read as text")
	}

	bad := filepath.Join(dir, "legacy.txt")
	os.WriteFile(bad, []byte("caf\xe9 \xe8\n"), 0o644) // not UTF-8
	if out, isErr := callTool(t, a, "edit_file", map[string]any{"path": bad, "old_string": "caf", "new_string": "tea"}); !isErr {
		t.Errorf("edit of a non-UTF-8 file was not refused: %s", out)
	}
}

func shellSleep(sec int) string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds " + itoa(sec)
	}
	return "sleep " + itoa(sec)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRunCommand(t *testing.T) {
	a := newTestApp(t)
	out := mustCall(t, a, "run_command", map[string]any{"command": "echo hello-remote-hands", "wait_seconds": 60})
	if !strings.Contains(out, "Exit code: 0") || !strings.Contains(out, "hello-remote-hands") {
		t.Errorf("echo:\n%s", out)
	}
	out = mustCall(t, a, "run_command", map[string]any{"command": "exit 3", "wait_seconds": 60})
	if !strings.Contains(out, "Exit code: 3") {
		t.Errorf("exit 3:\n%s", out)
	}
	dir := t.TempDir()
	pwd := "pwd"
	if runtime.GOOS == "windows" {
		pwd = "(Get-Location).Path"
	}
	out = mustCall(t, a, "run_command", map[string]any{"command": pwd, "cwd": dir, "wait_seconds": 60})
	if !strings.Contains(strings.ToLower(out), strings.ToLower(filepath.Base(dir))) {
		t.Errorf("cwd not used:\n%s", out)
	}
	if _, isErr := callTool(t, a, "run_command", map[string]any{"command": "echo x", "cwd": filepath.Join(dir, "missing")}); !isErr {
		t.Error("missing cwd accepted")
	}
}

func TestRunCommandUnicodeAndFailure(t *testing.T) {
	a := newTestApp(t)
	out := mustCall(t, a, "run_command", map[string]any{"command": "echo 'Příliš žluťoučký 你好'", "wait_seconds": 60})
	if !strings.Contains(out, "Příliš žluťoučký 你好") {
		t.Errorf("unicode output garbled:\n%s", out)
	}
	fail := "false"
	if runtime.GOOS == "windows" {
		fail = "Get-Item -LiteralPath 'C:\\definitely\\not\\here'"
	}
	out = mustCall(t, a, "run_command", map[string]any{"command": fail, "wait_seconds": 60})
	if !strings.Contains(out, "Exit code: 1") {
		t.Errorf("failing command:\n%s", out)
	}
}

func TestBackgroundJobs(t *testing.T) {
	a := newTestApp(t)
	out := mustCall(t, a, "run_command", map[string]any{"command": "echo started; " + shellSleep(3) + "; echo finished", "wait_seconds": 1})
	if !strings.Contains(out, "Still running") || !strings.Contains(out, "job_id 1") {
		t.Fatalf("expected a background job:\n%s", out)
	}
	out = mustCall(t, a, "check_command", map[string]any{"job_id": 1, "wait_seconds": 60})
	if !strings.Contains(out, "Exit code: 0") || !strings.Contains(out, "finished") {
		t.Errorf("check_command:\n%s", out)
	}
	if list := mustCall(t, a, "check_command", map[string]any{}); !strings.Contains(list, "job_id 1: finished") {
		t.Errorf("job list:\n%s", list)
	}

	start := time.Now()
	out = mustCall(t, a, "run_command", map[string]any{"command": shellSleep(60), "wait_seconds": 1})
	if !strings.Contains(out, "job_id 2") {
		t.Fatalf("expected job 2:\n%s", out)
	}
	out = mustCall(t, a, "check_command", map[string]any{"job_id": "2", "stop": true})
	if !strings.Contains(out, "Stopped on request") || time.Since(start) > 30*time.Second {
		t.Errorf("stop:\n%s", out)
	}

	out = mustCall(t, a, "run_command", map[string]any{"command": shellSleep(60), "timeout_seconds": 2, "wait_seconds": 60})
	if !strings.Contains(out, "reached its timeout") {
		t.Errorf("timeout:\n%s", out)
	}
	if _, isErr := callTool(t, a, "check_command", map[string]any{"job_id": 99}); !isErr {
		t.Error("unknown job accepted")
	}
}

func TestLongCommand(t *testing.T) {
	// Longer than one command-line argument may be on Linux (128 KiB).
	filler := strings.Repeat("# filler\n", 20000)
	out := mustCall(t, newTestApp(t), "run_command", map[string]any{"command": "echo begin\n" + filler + "echo end", "wait_seconds": 55})
	if !strings.Contains(out, "begin") || !strings.Contains(out, "end") || !strings.Contains(out, "Exit code: 0") {
		t.Errorf("long command:\n%s", out)
	}
}

func TestCancelledRequestKeepsOutput(t *testing.T) {
	a := newTestApp(t)
	j, err := a.jobs.start("echo kept-for-later", os.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !j.wait(context.Background(), 30*time.Second) {
		t.Fatal("command did not finish")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := a.jobReport(ctx, j, true); !r.IsError {
		t.Errorf("cancelled request got a normal answer: %+v", r)
	}
	if out := j.report(true); !strings.Contains(out, "kept-for-later") {
		t.Errorf("output was lost:\n%s", out)
	}
}

func TestNoCommandsAfterShutdown(t *testing.T) {
	a := newTestApp(t)
	a.jobs.killAll()
	if _, isErr := callTool(t, a, "run_command", map[string]any{"command": "echo late"}); !isErr {
		t.Error("a command started after shutdown")
	}
}

func TestCappedBuffer(t *testing.T) {
	var b cappedBuffer
	b.Write([]byte(strings.Repeat("a", outputHead)))
	b.Write([]byte(strings.Repeat("b", 100000)))
	b.Write([]byte("END"))
	s := b.take()
	if !strings.HasPrefix(s, strings.Repeat("a", outputHead)) || !strings.HasSuffix(s, "bEND") || !strings.Contains(s, "bytes of output skipped") {
		t.Errorf("unexpected capped output (len %d)", len(s))
	}
	if len(s) > outputHead+outputTail+100 {
		t.Errorf("output too long: %d", len(s))
	}
	if b.take() != "" {
		t.Error("take did not reset the buffer")
	}
	b.Write([]byte("small"))
	if got := b.take(); got != "small" {
		t.Errorf("small output: %q", got)
	}
	b.Write([]byte("\x1b[32;1mName\x1b[0m plain \x1b]0;title\x07done"))
	if got := b.take(); got != "Name plain done" {
		t.Errorf("colour codes not removed: %q", got)
	}
}

func TestResolvePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := map[string]string{
		"~":                      home,
		"Desktop":                filepath.Join(home, "Desktop"),
		"~/Documents/a.txt":      filepath.Join(home, "Documents", "a.txt"),
		`"` + home + `"`:         home,
		filepath.Join(home, "x"): filepath.Join(home, "x"),
	}
	if runtime.GOOS == "windows" {
		cases["/c/Users"] = `C:\Users`
		cases["/d"] = `D:\`
		cases[`%USERPROFILE%\Desktop`] = filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
		cases[`$env:USERPROFILE\Desktop`] = filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	} else {
		cases["$HOME/x"] = filepath.Join(home, "x")
	}
	for in, want := range cases {
		if got, err := resolvePath(in); err != nil || got != want {
			t.Errorf("resolvePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := resolvePath("  "); err == nil {
		t.Error("empty path accepted")
	}
}

func TestFlexibleArguments(t *testing.T) {
	var in struct {
		N flexInt  `json:"n"`
		M flexInt  `json:"m"`
		B flexBool `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"n":"12","m":3.0,"b":"true"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.N.v != 12 || !in.N.set || in.M.v != 3 || !bool(in.B) {
		t.Errorf("got %+v", in)
	}
	if seconds(flexInt{}, 45, 1, maxWaitSeconds) != 45*time.Second || seconds(flexInt{v: 999, set: true}, 45, 1, maxWaitSeconds) != maxWaitSeconds*time.Second {
		t.Error("seconds() does not apply default and limits")
	}
}

func TestDownscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	if b := downscale(img, maxShotSide).Bounds(); b.Dx() != maxShotSide || b.Dy() != 1045 {
		t.Errorf("downscaled to %v", b)
	}
	small := image.NewRGBA(image.Rect(0, 0, 800, 600))
	if downscale(small, maxShotSide) != image.Image(small) {
		t.Error("small image was resized")
	}
}
