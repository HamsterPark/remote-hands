package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const rule = "=================================================================="

// ui writes to the console window of the controlled computer. The person
// sitting there may not be technical, so everything is short and in their
// language.
type ui struct {
	tr    translator
	color bool
	mu    sync.Mutex
	out   io.Writer
	// Activity lines are queued and written by one goroutine, so that a
	// stuck console (for example while the user selects text in an old
	// Windows console) never blocks a request.
	logs    chan string
	logOnce sync.Once
}

func newUI(out io.Writer, tr translator, color bool) *ui {
	return &ui{tr: tr, color: color && os.Getenv("NO_COLOR") == "", out: out, logs: make(chan string, 256)}
}

func (u *ui) t(key string, args ...any) string { return u.tr.T(key, args...) }

func (u *ui) write(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, _ = io.WriteString(u.out, s)
}

func (u *ui) paint(code, s string) string {
	if !u.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *ui) say(msg string)  { u.write(msg + "\n") }
func (u *ui) ok(msg string)   { u.write(u.paint("1;32", "[OK]") + " " + msg + "\n") }
func (u *ui) warn(msg string) { u.write(u.paint("1;33", "[!]") + " " + msg + "\n") }

func (u *ui) banner(ver string) {
	u.write(rule + "\n  " + u.paint("1", "Remote Hands "+ver) + "\n  " +
		strings.ReplaceAll(u.t("tagline"), "\n", "\n  ") + "\n" + rule + "\n\n")
}

func (u *ui) showLines(lines [2]string, copied bool) {
	hint := u.t("lines.nocopy")
	if copied {
		hint = u.t("lines.copied")
	}
	var b strings.Builder
	b.WriteString("\n" + u.paint("1;33", u.t("lines.title")) + "\n")
	b.WriteString(rule + "\n")
	// The lines start at the very left so that no spaces get copied.
	b.WriteString(u.paint("1;36", lines[0]) + "\n")
	b.WriteString(u.paint("1;36", lines[1]) + "\n")
	b.WriteString(rule + "\n")
	b.WriteString(hint + "\n\n")
	u.write(b.String())
}

func (u *ui) tips(awake, interactive bool) {
	var b strings.Builder
	b.WriteString(" * " + u.t("tips.open") + "\n")
	if awake {
		b.WriteString(" * " + u.t("tips.awake") + "\n")
	}
	b.WriteString(" * " + u.paint("1", u.t("tips.secret")) + "\n")
	if interactive {
		b.WriteString(" * " + u.t("tips.newkey") + "\n")
	}
	b.WriteString("\n" + u.paint("1", u.t("activity")) + "\n")
	u.write(b.String())
}

func (u *ui) startActivityLog() {
	u.logOnce.Do(func() {
		go func() {
			for line := range u.logs {
				u.write(line)
			}
		}()
	})
}

// activity records something a remote AI did. It never blocks.
func (u *ui) activity(who, what string) {
	line := time.Now().Format("15:04:05") + "  " + u.paint("36", who) + "  " + what + "\n"
	select {
	case u.logs <- line:
	default:
	}
}

// fatal shows an error, waits for Enter when a person is watching (so the
// window does not vanish before they could read it) and returns exit code 1.
func (u *ui) fatal(ctx context.Context, msg string) int {
	u.write(u.paint("1;31", "[X]") + " " + msg + "\n")
	u.pause(ctx)
	return 1
}

func (u *ui) pause(ctx context.Context) {
	if !isTerminal(os.Stdin) {
		return
	}
	u.say("\n" + u.t("press.enter"))
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// oneLine shortens text for the activity log.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

// fmtDuration formats a duration for people and AIs alike: "0.4 s", "12 s",
// "3 min 5 s".
func fmtDuration(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1f s", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}
