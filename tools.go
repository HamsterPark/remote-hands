package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxFileSize = 20 << 20 // read_file / edit_file limit
	// Claude Code moves tool results over 50,000 characters into a file, so
	// stay well below that.
	maxReadOutput  = 40 << 10
	maxWaitSeconds = 55 // clients stop waiting for an HTTP answer after 60 s
	maxLineLength  = 2000
	defaultLines   = 2000
	maxListEntries = 1000
)

type toolDef struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`

	run func(ctx context.Context, args json.RawMessage, who string) toolResult
}

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

var (
	readOnly    = map[string]any{"readOnlyHint": true, "openWorldHint": false}
	destructive = map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false}
)

func (a *app) buildTools() []*toolDef {
	shell := shellName()
	return []*toolDef{
		{
			Name:  "system_info",
			Title: "System information",
			Description: fmt.Sprintf("Facts about the remote computer %q: operating system, user, home and Desktop folders, "+
				"the shell used by run_command, and local time. Call this first.", a.name),
			InputSchema: object(nil, map[string]any{}),
			Annotations: readOnly,
			run:         a.toolSystemInfo,
		},
		{
			Name:  "run_command",
			Title: "Run a command",
			Description: fmt.Sprintf("Run a shell command on the remote computer %q and return its output (stdout and stderr together) and exit code. "+
				"Shell: %s. Every call starts a fresh shell in cwd (default: the home folder); nothing carries over between calls. "+
				"If the command is still running after wait_seconds, you get the output so far and a job_id, and it keeps running in the background: "+
				"use check_command to follow it. There is no keyboard input, so avoid commands that prompt. "+
				"Start GUI programs or servers that should stay open without waiting for them, and send their output elsewhere "+
				"(Start-Process on Windows, open -a on macOS, nohup program >/dev/null 2>&1 & on macOS and Linux).",
				a.name, shell),
			InputSchema: object([]string{"command"}, map[string]any{
				"command":         prop("string", "The command or script to run. Several lines are fine."),
				"cwd":             prop("string", "Folder to run in: absolute, or relative to the home folder. Default: the home folder."),
				"wait_seconds":    prop("integer", "How long to wait for the command to finish before returning (1-55, default 45). Longer commands simply continue in the background."),
				"timeout_seconds": prop("integer", "Kill the command if it still runs after this many seconds (default 1800, max 86400)."),
			}),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true},
			run:         a.toolRunCommand,
		},
		{
			Name:  "check_command",
			Title: "Check a running command",
			Description: "Follow a command that run_command left running in the background: waits up to wait_seconds for it to finish and returns the output produced " +
				"since the last check, plus the exit code once it has finished. stop=true kills it together with the processes it started. Without job_id, lists recent commands.",
			InputSchema: object(nil, map[string]any{
				"job_id":       prop("integer", "The job_id that run_command returned."),
				"wait_seconds": prop("integer", "How long to wait for the command to finish (0-55, default 30)."),
				"stop":         prop("boolean", "Kill the command instead of waiting."),
			}),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
			run:         a.toolCheckCommand,
		},
		{
			Name:  "read_file",
			Title: "Read a text file",
			Description: "Read a text file on the remote computer. Lines are returned as \"line number<TAB>text\" (the numbers are not part of the file). " +
				"Use offset and limit for long files. Relative paths start in the home folder.",
			InputSchema: object([]string{"path"}, map[string]any{
				"path":   prop("string", "File path: absolute, or relative to the home folder."),
				"offset": prop("integer", "First line to return, counting from 1."),
				"limit":  prop("integer", fmt.Sprintf("Maximum number of lines to return (default %d).", defaultLines)),
			}),
			Annotations: readOnly,
			run:         a.toolReadFile,
		},
		{
			Name:  "write_file",
			Title: "Write a text file",
			Description: "Create or overwrite a text file on the remote computer (written as UTF-8). Missing folders are created. " +
				"append=true adds to the end of the file instead of replacing it.",
			InputSchema: object([]string{"path", "content"}, map[string]any{
				"path":    prop("string", "File path: absolute, or relative to the home folder."),
				"content": prop("string", "The complete text to write."),
				"append":  prop("boolean", "Add to the end of the file instead of replacing it."),
			}),
			Annotations: destructive,
			run:         a.toolWriteFile,
		},
		{
			Name:  "edit_file",
			Title: "Edit a text file",
			Description: "Replace exact text in a text file on the remote computer. old_string must match the file exactly (without the line numbers that read_file shows) " +
				"and must occur exactly once, unless replace_all=true. The file keeps its encoding and line endings.",
			InputSchema: object([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":        prop("string", "File path: absolute, or relative to the home folder."),
				"old_string":  prop("string", "The exact text to replace."),
				"new_string":  prop("string", "The text to put in its place."),
				"replace_all": prop("boolean", "Replace every occurrence instead of exactly one."),
			}),
			Annotations: destructive,
			run:         a.toolEditFile,
		},
		{
			Name:        "list_directory",
			Title:       "List a folder",
			Description: "List a folder on the remote computer (default: the home folder): sub-folders first, then files with size and last-modified time.",
			InputSchema: object(nil, map[string]any{
				"path": prop("string", "Folder path: absolute, or relative to the home folder."),
			}),
			Annotations: readOnly,
			run:         a.toolListDirectory,
		},
		{
			Name:  "take_screenshot",
			Title: "Take a screenshot",
			Description: "Take a screenshot of every screen of the remote computer, scaled down to at most 1568 pixels. Use it to see what the person there sees, " +
				"for example an error message. Fails while the screen is locked or the computer sleeps. On macOS the Terminal app needs the Screen Recording permission, " +
				"otherwise the picture shows only the desktop background.",
			InputSchema: object(nil, map[string]any{}),
			Annotations: readOnly,
			run:         a.toolScreenshot,
		},
	}
}

// instructions are sent to the AI when it connects.
func (a *app) instructions() string {
	si := getSysInfo()
	var b strings.Builder
	fmt.Fprintf(&b, "These tools control the computer %q (%s, user %s, home folder %s) through Remote Hands over Tailscale. "+
		"Everything they do happens on THAT computer, not on the machine you are running on.\n", a.name, si.os, si.user, si.home)
	b.WriteString("- Call system_info first.\n")
	fmt.Fprintf(&b, "- run_command uses %s. Every call starts a fresh shell: the current folder and variables do not carry over, so use the cwd argument. "+
		"There is no keyboard, so commands that ask for input, a password or a confirmation fail or hang.\n", shellName())
	b.WriteString("- A command that runs longer than wait_seconds keeps running in the background; follow it with check_command.\n")
	if runtime.GOOS == "windows" {
		b.WriteString("- On Windows the Desktop and Documents folders are often inside OneDrive; system_info shows the real Desktop path.\n")
	}
	b.WriteString("- The owner of that computer may not be technical. Explain in plain words what you are going to do, prefer safe and reversible steps, " +
		"and ask before deleting files, installing software or changing settings. Anything that needs administrator rights (a UAC prompt on Windows, " +
		"sudo or a password on macOS and Linux) must be approved by someone sitting at that computer.\n")
	return b.String()
}

// flexInt and flexBool accept numbers and booleans also when an AI sends
// them as strings.
type flexInt struct {
	v   int64
	set bool
}

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("%s is not a number", b)
	}
	f.v, f.set = int64(n), true
	return nil
}

type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(string(b)), `"`)) {
	case "true", "1", "yes":
		*f = true
	case "false", "0", "no", "", "null":
		*f = false
	default:
		return fmt.Errorf("%s is not true or false", b)
	}
	return nil
}

func seconds(f flexInt, def, lo, hi int64) time.Duration {
	v := def
	if f.set {
		v = min(max(f.v, lo), hi)
	}
	return time.Duration(v) * time.Second
}

func parseArgs(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

var (
	gitBashDrive  = regexp.MustCompile(`^/([a-zA-Z])(/.*)?$`)
	driveRelative = regexp.MustCompile(`^([a-zA-Z]):([^\\/].*)?$`)
	winEnvVar     = regexp.MustCompile(`%([A-Za-z0-9_()]+)%`)
	psEnvVar      = regexp.MustCompile(`(?i)\$env:([A-Za-z0-9_]+)`)
	psHome        = regexp.MustCompile(`(?i)^\$(home|\{home\})([\\/].*)?$`)
	unixEnvVar    = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// resolvePath turns what an AI wrote into an absolute path. It understands
// "~", relative paths (from the home folder), %VAR%, $env:VAR and $HOME on
// Windows, $VAR and ${VAR} elsewhere, and on Windows also Git Bash style
// "/c/Users/...", "\Users" (on the home drive) and "C:Users".
func resolvePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if len(p) >= 2 && (p[0] == '"' && p[len(p)-1] == '"' || p[0] == '\'' && p[len(p)-1] == '\'') {
		p = p[1 : len(p)-1]
	}
	if p == "" {
		return "", errors.New("path is empty")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		p = winEnvVar.ReplaceAllStringFunc(p, func(m string) string {
			if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
				return v
			}
			return m
		})
		p = psEnvVar.ReplaceAllStringFunc(p, func(m string) string {
			if v, ok := os.LookupEnv(m[5:]); ok {
				return v
			}
			return m
		})
		if m := psHome.FindStringSubmatch(p); m != nil {
			p = home + m[2]
		}
		if m := gitBashDrive.FindStringSubmatch(p); m != nil {
			p = strings.ToUpper(m[1]) + ":" + m[2] + `\`
		} else if m := driveRelative.FindStringSubmatch(p); m != nil {
			p = m[1] + `:\` + m[2]
		} else if (p[0] == '\\' || p[0] == '/') && !strings.HasPrefix(p, `\\`) && !strings.HasPrefix(p, "//") {
			p = filepath.VolumeName(home) + p
		}
	} else {
		p = unixEnvVar.ReplaceAllStringFunc(p, func(m string) string {
			if v, ok := os.LookupEnv(strings.Trim(m, "${}")); ok {
				return v
			}
			return m
		})
	}
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"), strings.HasPrefix(p, `~\`):
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(home, p)
	}
	return filepath.Clean(p), nil
}

func (a *app) toolSystemInfo(_ context.Context, _ json.RawMessage, who string) toolResult {
	a.ui.activity(who, a.ui.t("act.info"))
	si := getSysInfo()
	var b strings.Builder
	fmt.Fprintf(&b, "Computer name: %s\n", a.name)
	fmt.Fprintf(&b, "Operating system: %s, %s\n", si.os, runtime.GOARCH)
	fmt.Fprintf(&b, "User: %s\n", si.user)
	fmt.Fprintf(&b, "Home folder: %s\n", si.home)
	if si.desktop != "" {
		fmt.Fprintf(&b, "Desktop folder: %s\n", si.desktop)
	}
	fmt.Fprintf(&b, "Shell used by run_command: %s\n", shellName())
	fmt.Fprintf(&b, "Tailscale address: %s\n", a.host)
	fmt.Fprintf(&b, "Local time: %s\n", time.Now().Format("Monday 2006-01-02 15:04:05 MST (-07:00)"))
	fmt.Fprintf(&b, "Remote Hands version: %s\n", appVersion())
	return textResult(b.String())
}

func (a *app) toolRunCommand(ctx context.Context, raw json.RawMessage, who string) toolResult {
	var in struct {
		Command        string  `json:"command"`
		Cwd            string  `json:"cwd"`
		WaitSeconds    flexInt `json:"wait_seconds"`
		TimeoutSeconds flexInt `json:"timeout_seconds"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return errorResult("%v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return errorResult("command is empty")
	}
	dir, _ := os.UserHomeDir()
	if strings.TrimSpace(in.Cwd) != "" {
		p, err := resolvePath(in.Cwd)
		if err != nil {
			return errorResult("cwd: %v", err)
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return errorResult("cwd %s is not an existing folder", p)
		}
		dir = p
	}
	a.ui.activity(who, a.ui.t("act.run", oneLine(in.Command, 120)))
	j, err := a.jobs.start(in.Command, dir, seconds(in.TimeoutSeconds, 1800, 1, 86400))
	if err != nil {
		return errorResult("Could not start the command: %v", err)
	}
	return a.jobReport(ctx, j, j.wait(ctx, seconds(in.WaitSeconds, 45, 1, maxWaitSeconds)))
}

// jobReport answers with the job's state and new output, unless the client
// has gone away: then the output stays for the next check_command.
func (a *app) jobReport(ctx context.Context, j *job, finished bool) toolResult {
	if ctx.Err() != nil {
		return errorResult("Request cancelled; the command (job_id %d) continues in the background.", j.id)
	}
	return textResult(j.report(finished))
}

func (a *app) toolCheckCommand(ctx context.Context, raw json.RawMessage, who string) toolResult {
	var in struct {
		JobID       flexInt  `json:"job_id"`
		WaitSeconds flexInt  `json:"wait_seconds"`
		Stop        flexBool `json:"stop"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return errorResult("%v", err)
	}
	if !in.JobID.set {
		return textResult(a.jobs.list())
	}
	j := a.jobs.get(int(in.JobID.v))
	if j == nil {
		return errorResult("There is no command with job_id %d (old finished commands are forgotten). Call check_command without job_id to list them.", in.JobID.v)
	}
	if in.Stop {
		a.ui.activity(who, a.ui.t("act.stop", j.id))
		return a.jobReport(ctx, j, a.jobs.stop(ctx, j))
	}
	a.ui.activity(who, a.ui.t("act.check", j.id))
	return a.jobReport(ctx, j, j.wait(ctx, seconds(in.WaitSeconds, 30, 0, maxWaitSeconds)))
}

func (a *app) toolReadFile(_ context.Context, raw json.RawMessage, who string) toolResult {
	var in struct {
		Path   string  `json:"path"`
		Offset flexInt `json:"offset"`
		Limit  flexInt `json:"limit"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return errorResult("%v", err)
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	a.ui.activity(who, a.ui.t("act.read", p))
	data, _, err := readRegularFile(p)
	if err != nil {
		return errorResult("%v", err)
	}
	text, enc, ok := decodeText(data)
	if !ok {
		return errorResult("%s does not look like a text file (%s). Use run_command to inspect it.", p, humanSize(int64(len(data))))
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first := 1
	if in.Offset.set && in.Offset.v > 1 {
		first = int(min(in.Offset.v, int64(len(lines)+1)))
	}
	limit := defaultLines
	if in.Limit.set && in.Limit.v > 0 {
		limit = int(min(in.Limit.v, 100000))
	}
	var body strings.Builder
	last := first - 1
	for i := first; i <= len(lines) && i < first+limit; i++ {
		line := strings.TrimSuffix(lines[i-1], "\r")
		if r := []rune(line); len(r) > maxLineLength {
			line = string(r[:maxLineLength]) + " [... line shortened ...]"
		}
		if body.Len()+len(line) > maxReadOutput && i > first {
			break
		}
		fmt.Fprintf(&body, "%6d\t%s\n", i, line)
		last = i
	}
	var b strings.Builder
	switch {
	case len(lines) == 0 || (len(lines) == 1 && lines[0] == ""):
		fmt.Fprintf(&b, "%s is empty.\n", p)
	case last < first:
		fmt.Fprintf(&b, "%s has only %d lines.\n", p, len(lines))
	default:
		fmt.Fprintf(&b, "%s, lines %d-%d of %d", p, first, last, len(lines))
		if enc.name != "utf-8" {
			fmt.Fprintf(&b, ", encoding %s", enc.name)
		}
		if last < len(lines) {
			fmt.Fprintf(&b, " (more: use offset %d)", last+1)
		}
		b.WriteString(":\n")
		b.WriteString(body.String())
	}
	return textResult(b.String())
}

func (a *app) toolWriteFile(_ context.Context, raw json.RawMessage, who string) toolResult {
	var in struct {
		Path    string   `json:"path"`
		Content *string  `json:"content"`
		Append  flexBool `json:"append"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return errorResult("%v", err)
	}
	if in.Content == nil {
		return errorResult("content is missing")
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	a.ui.activity(who, a.ui.t("act.write", p))
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return errorResult("%s is a folder", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return errorResult("%v", err)
	}
	if in.Append {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return errorResult("%v", err)
		}
		_, err = f.WriteString(*in.Content)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return errorResult("%v", err)
		}
		return textResult(fmt.Sprintf("Appended %d bytes to %s.", len(*in.Content), p))
	}
	if err := os.WriteFile(p, []byte(*in.Content), 0o644); err != nil {
		return errorResult("%v", err)
	}
	return textResult(fmt.Sprintf("Wrote %d bytes to %s.", len(*in.Content), p))
}

func (a *app) toolEditFile(_ context.Context, raw json.RawMessage, who string) toolResult {
	var in struct {
		Path       string   `json:"path"`
		OldString  string   `json:"old_string"`
		NewString  string   `json:"new_string"`
		ReplaceAll flexBool `json:"replace_all"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return errorResult("%v", err)
	}
	if in.OldString == "" {
		return errorResult("old_string is empty; use write_file to create or replace a whole file")
	}
	if in.OldString == in.NewString {
		return errorResult("old_string and new_string are the same")
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	a.ui.activity(who, a.ui.t("act.edit", p))
	data, st, err := readRegularFile(p)
	if err != nil {
		return errorResult("%v", err)
	}
	text, enc, ok := decodeText(data)
	// Only rewrite files that come back byte for byte when nothing changes.
	if !ok || !enc.writable || !bytes.Equal(encodeText(text, enc), data) {
		return errorResult("%s is not a UTF-8 or UTF-16 text file that edit_file can rewrite safely (%s). Use run_command instead.", p, enc.name)
	}
	oldS, newS := in.OldString, in.NewString
	// The AI usually writes "\n" even when the file uses Windows line endings.
	crlf := strings.Contains(text, "\r\n")
	if crlf {
		newS = toCRLF(newS)
	}
	count := strings.Count(text, oldS)
	if count == 0 && crlf {
		if o := toCRLF(oldS); o != oldS {
			if c := strings.Count(text, o); c > 0 {
				oldS, count = o, c
			}
		}
	}
	switch {
	case count == 0:
		return errorResult("old_string was not found in %s. Read the file again and copy the text exactly, without the line numbers.", p)
	case count > 1 && !bool(in.ReplaceAll):
		return errorResult("old_string occurs %d times in %s. Include more of the surrounding text so it is unique, or set replace_all=true.", count, p)
	}
	if in.ReplaceAll {
		text = strings.ReplaceAll(text, oldS, newS)
	} else {
		text = strings.Replace(text, oldS, newS, 1)
	}
	if err := os.WriteFile(p, encodeText(text, enc), st.Mode().Perm()); err != nil {
		return errorResult("%v", err)
	}
	return textResult(fmt.Sprintf("Edited %s: replaced %d occurrence(s).", p, count))
}

func (a *app) toolListDirectory(_ context.Context, raw json.RawMessage, who string) toolResult {
	var in struct {
		Path string `json:"path"`
	}
	if err := parseArgs(raw, &in); err != nil {
		return errorResult("%v", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		in.Path = "~"
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	a.ui.activity(who, a.ui.t("act.list", p))
	entries, err := os.ReadDir(p)
	if err != nil {
		return errorResult("%v", err)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d items):\n", p, len(entries))
	for i, e := range entries {
		if i == maxListEntries {
			fmt.Fprintf(&b, "... and %d more\n", len(entries)-i)
			break
		}
		info, err := e.Info()
		switch {
		case err != nil:
			fmt.Fprintf(&b, "%-28s %s\n", "?", e.Name())
		case e.IsDir():
			fmt.Fprintf(&b, "%-28s %s%c\n", "[folder]", e.Name(), filepath.Separator)
		default:
			fmt.Fprintf(&b, "%10s  %s  %s\n", humanSize(info.Size()), info.ModTime().Format("2006-01-02 15:04"), e.Name())
		}
	}
	return textResult(b.String())
}

func (a *app) toolScreenshot(ctx context.Context, _ json.RawMessage, who string) toolResult {
	a.ui.activity(who, a.ui.t("act.screenshot"))
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	images, err := takeScreenshots(ctx)
	if err != nil {
		return errorResult("Could not take a screenshot: %v", err)
	}
	note := content{Type: "text", Text: fmt.Sprintf("%d screen(s), taken %s.", len(images), time.Now().Format("15:04:05"))}
	return toolResult{Content: append([]content{note}, images...)}
}

func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// readRegularFile reads a normal file of at most maxFileSize bytes. Devices
// and pipes are refused: they could be endless or never answer.
func readRegularFile(p string) ([]byte, os.FileInfo, error) {
	st, err := os.Stat(p)
	switch {
	case err != nil:
		return nil, nil, err
	case st.IsDir():
		return nil, nil, fmt.Errorf("%s is a folder; use list_directory", p)
	case !st.Mode().IsRegular():
		return nil, nil, fmt.Errorf("%s is not a normal file; use run_command to inspect it", p)
	case st.Size() > maxFileSize:
		return nil, nil, fmt.Errorf("%s is too big (%s); use run_command to look at parts of it", p, humanSize(st.Size()))
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxFileSize {
		return nil, nil, fmt.Errorf("%s is too big; use run_command to look at parts of it", p)
	}
	return data, st, nil
}

func humanSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
