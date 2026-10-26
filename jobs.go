package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	outputHead = 8 << 10  // bytes kept from the start of unread output
	outputTail = 24 << 10 // bytes kept from the end of unread output
	maxRunning = 16
	maxJobs    = 64
)

// A job is one command started by run_command. Commands that take longer
// than the caller wants to wait keep running and can be followed with
// check_command, so slow installs or builds never hit a client timeout.
type job struct {
	id      int
	command string
	started time.Time
	cmd     *exec.Cmd
	out     *cappedBuffer
	done    chan struct{}

	// Written before done is closed.
	exitCode int
	runErr   error
	ended    time.Time
	lingered bool // child processes kept the output open after the command ended

	mu       sync.Mutex
	timedOut bool
	stopped  bool
}

func (j *job) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// wait waits up to d for the job to end and reports whether it has.
func (j *job) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return j.finished()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-j.done:
		return true
	case <-t.C:
		return j.finished()
	case <-ctx.Done():
		return j.finished()
	}
}

// report describes the job and returns the output produced since the last
// report.
func (j *job) report(finished bool) string {
	var b strings.Builder
	if finished {
		j.mu.Lock()
		timedOut, stopped := j.timedOut, j.stopped
		j.mu.Unlock()
		took := fmtDuration(j.ended.Sub(j.started))
		switch {
		case j.runErr != nil:
			fmt.Fprintf(&b, "The command failed to run: %v\n", j.runErr)
		case stopped:
			fmt.Fprintf(&b, "Stopped on request after %s (job_id %d).\n", took, j.id)
		case timedOut:
			fmt.Fprintf(&b, "Killed because it reached its timeout after %s (job_id %d).\n", took, j.id)
		default:
			fmt.Fprintf(&b, "Exit code: %d (took %s)\n", j.exitCode, took)
		}
		if j.lingered {
			b.WriteString("Note: processes started by the command were still holding its output open; output they write later is not captured.\n")
		}
	} else {
		fmt.Fprintf(&b, "Still running after %s (job_id %d). It keeps running in the background: call check_command with job_id %d to wait for more output and the exit code, or with stop=true to kill it.\n",
			fmtDuration(time.Since(j.started)), j.id, j.id)
	}
	out := j.out.take()
	switch {
	case out != "":
		b.WriteString("Output:\n")
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteString("\n")
		}
	case finished:
		b.WriteString("(no output)\n")
	default:
		b.WriteString("(no new output yet)\n")
	}
	return b.String()
}

type jobManager struct {
	mu   sync.Mutex
	next int
	jobs map[int]*job
}

func newJobManager() *jobManager {
	return &jobManager{jobs: map[int]*job{}}
}

func (m *jobManager) start(command, dir string, timeout time.Duration) (*job, error) {
	m.mu.Lock()
	running := 0
	for _, j := range m.jobs {
		if !j.finished() {
			running++
		}
	}
	if running >= maxRunning {
		m.mu.Unlock()
		return nil, fmt.Errorf("%d commands are still running; wait for them or stop some with check_command", running)
	}
	m.next++
	id := m.next
	m.mu.Unlock()

	cmd, cleanup, err := shellCommand(command)
	if err != nil {
		return nil, err
	}
	j := &job{id: id, command: command, started: time.Now(), cmd: cmd, out: &cappedBuffer{}, done: make(chan struct{})}
	cmd.Dir = dir
	cmd.Stdout = j.out
	cmd.Stderr = j.out
	// No password prompts from git, and no colour codes (they only waste
	// the AI's attention).
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")
	if runtime.GOOS != "windows" {
		cmd.Env = append(cmd.Env, "PWD="+dir)
	}
	// When a command leaves a background process behind that still holds
	// the output pipe, stop waiting for it shortly after the command ends.
	cmd.WaitDelay = 3 * time.Second
	prepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, err
	}

	m.mu.Lock()
	m.jobs[id] = j
	m.pruneLocked()
	m.mu.Unlock()

	go func() {
		timer := time.AfterFunc(timeout, func() {
			j.mu.Lock()
			j.timedOut = true
			j.mu.Unlock()
			killTree(cmd)
		})
		err := cmd.Wait()
		timer.Stop()
		cleanup()
		j.exitCode = -1
		if cmd.ProcessState != nil {
			j.exitCode = cmd.ProcessState.ExitCode()
		}
		var exitErr *exec.ExitError
		switch {
		case err == nil, errors.As(err, &exitErr):
		case errors.Is(err, exec.ErrWaitDelay):
			j.lingered = true
		default:
			j.runErr = err
		}
		j.ended = time.Now()
		close(j.done)
	}()
	return j, nil
}

// pruneLocked forgets the oldest finished jobs when there are too many.
func (m *jobManager) pruneLocked() {
	if len(m.jobs) <= maxJobs {
		return
	}
	var ids []int
	for id, j := range m.jobs {
		if j.finished() {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		if len(m.jobs) <= maxJobs {
			break
		}
		delete(m.jobs, id)
	}
}

func (m *jobManager) get(id int) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *jobManager) stop(ctx context.Context, j *job) bool {
	// Never kill by process id after the job ended: the id may already
	// belong to an unrelated process.
	if j.finished() {
		return true
	}
	j.mu.Lock()
	j.stopped = true
	j.mu.Unlock()
	killTree(j.cmd)
	return j.wait(ctx, 10*time.Second)
}

func (m *jobManager) killAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if !j.finished() {
			killTree(j.cmd)
		}
	}
}

// list describes all remembered jobs, newest first.
func (m *jobManager) list() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) == 0 {
		return "No commands have been started yet.\n"
	}
	ids := make([]int, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	var b strings.Builder
	for _, id := range ids {
		j := m.jobs[id]
		state := "running for " + fmtDuration(time.Since(j.started))
		if j.finished() {
			state = fmt.Sprintf("finished, exit code %d", j.exitCode)
		}
		fmt.Fprintf(&b, "job_id %d: %s: %s\n", id, state, oneLine(j.command, 100))
	}
	return b.String()
}

// ansiEscape matches terminal colour and cursor codes, which some programs
// print even when their output is not a terminal.
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-_])`)

// cappedBuffer collects command output. Between two reads it keeps the
// first outputHead and the last outputTail bytes and counts what it had to
// drop in between, so a chatty command cannot eat memory or flood the AI.
type cappedBuffer struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	dropped int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if room := outputHead - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if len(p) >= outputTail {
		b.dropped += int64(len(b.tail) + len(p) - outputTail)
		b.tail = append(b.tail[:0], p[len(p)-outputTail:]...)
		return n, nil
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - outputTail; over > 0 {
		b.dropped += int64(over)
		b.tail = b.tail[:copy(b.tail, b.tail[over:])]
	}
	return n, nil
}

// take returns the unread output and starts over.
func (b *cappedBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var s string
	if b.dropped > 0 {
		s = string(b.head) + fmt.Sprintf("\n[... %d bytes of output skipped ...]\n", b.dropped) + string(b.tail)
	} else {
		s = string(b.head) + string(b.tail)
	}
	b.head, b.tail, b.dropped = b.head[:0], b.tail[:0], 0
	s = strings.ReplaceAll(ansiEscape.ReplaceAllString(s, ""), "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff")
	return strings.ToValidUTF8(s, "\uFFFD")
}
