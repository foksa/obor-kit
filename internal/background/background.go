// Package background is the agents' live processes (T-714, T-1142): the machine's process table, the matching of a
// recorded background Bash call (project.BackgroundCall, obor's PreToolUse hook) to the process it started, and the
// calls of one agent that still run. Base, so the hook (guard), serve (app) and the view share one check.
package background

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/project"
)

// startWindow is how long before a process's start its background call may have been recorded (the hook records at
// PreToolUse, the shell starts the process a moment later); startAfter is the slack the other way (ps's etime counts
// whole seconds, the record's time too).
const (
	startWindow = 10 * time.Second
	startAfter  = 2 * time.Second
)

// commandMax is how many characters of a command a view shows.
const commandMax = 60

// Proc is one row of the process table: pid, parent, how long it has run and its full command.
type Proc struct {
	PID, PPID int
	Age       time.Duration
	Command   string
}

// psArgs is the one ps call (macOS and Linux alike): every process, full width, no header.
var psArgs = []string{"-A", "-ww", "-o", "pid=,ppid=,etime=,command="}

// ReadProcs is the machine's process table, by one ps under ctx.
func ReadProcs(ctx context.Context) ([]Proc, error) {
	out, err := exec.CommandContext(ctx, "ps", psArgs...).Output()
	if err != nil {
		return nil, err
	}
	return ParseProcs(string(out)), nil
}

// ParseProcs is ps's output (pid ppid etime command, one process per line) as rows; a line that does not parse is
// skipped.
func ParseProcs(out string) []Proc {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		age, ok := parseEtime(f[2])
		if err1 != nil || err2 != nil || !ok {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, Age: age, Command: strings.Join(f[3:], " ")})
	}
	return procs
}

// parseEtime is ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	secs := 0
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days*86400+secs) * time.Second, true
}

// Found is a long-running process and the commands of the shells between it and its agent.
type Found struct {
	Proc
	Chain []string
}

// Match is the newest call in calls that f started: recorded within startWindow before f's start (at, now less its age)
// and whose command f runs: the call's command (whitespace folded) is in f's command or in a shell's that wraps it, or
// f's command is in the call's (a call of several commands). ok is false when none matches: f is no recorded background
// call.
func Match(f Found, calls []project.BackgroundCall, at time.Time) (call project.BackgroundCall, ok bool) {
	start := at.Add(-f.Age)
	for i := len(calls) - 1; i >= 0; i-- {
		if rec := time.Unix(calls[i].At, 0); rec.Before(start.Add(-startWindow)) || rec.After(start.Add(startAfter)) {
			continue
		}
		want := Fold(calls[i].Command)
		if want == "" {
			continue
		}
		if runs(f.Command, want) {
			return calls[i], true
		}
		for _, sh := range f.Chain {
			if runs(sh, want) {
				return calls[i], true
			}
		}
	}
	return project.BackgroundCall{}, false
}

// runs: command runs the call's (folded) command want: want is in it, or it is in want (a call of several commands; the
// fragment is too short to tell anything below 8 characters), the shell's escaping undone (Claude wraps a call in a
// shell that quotes it).
func runs(command, want string) bool {
	for _, c := range []string{command, strings.ReplaceAll(command, `'\''`, `'`)} {
		cmd := Fold(c)
		if strings.Contains(cmd, want) || len(cmd) >= 8 && strings.Contains(want, cmd) {
			return true
		}
	}
	return false
}

// Fold is s with its whitespace runs as one space.
func Fold(s string) string { return strings.Join(strings.Fields(s), " ") }

// Live is the newest call in calls still running in procs at at: a process runs its command and started just after the
// call was recorded (Match, with no ancestry: the hook and serve know no agent pid). ok is false when every call's
// process has ended: the call's turn is over.
func Live(calls []project.BackgroundCall, procs []Proc, at time.Time) (call project.BackgroundCall, ok bool) {
	for i := len(calls) - 1; i >= 0; i-- {
		for _, p := range procs {
			if c, ok := Match(Found{Proc: p}, calls[i:i+1], at); ok {
				return c, true
			}
		}
	}
	return project.BackgroundCall{}, false
}

// Mine is the calls that belong to an agent, in log order: those recorded for session (when it is known) that name the
// agent, or name none and were recorded in its pane (the view's rule, RV-85 #3). An agent with no recorded session is
// known by its name alone (T-1142: an opencode agent that records none still has its calls).
func Mine(calls []project.BackgroundCall, agent, session, pane string) []project.BackgroundCall {
	var out []project.BackgroundCall
	for _, c := range calls {
		if session != "" && c.Session != session {
			continue
		}
		if c.Agent != agent && !(c.Agent == "" && c.Pane != "" && c.Pane == pane) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Text is what a line shows of c: its description, else its command (shortened).
func Text(c project.BackgroundCall) string {
	if c.Description != "" {
		return c.Description
	}
	return Shorten(c.Command)
}

// Shorten is a command as the views show it: argv0 without its path, whitespace folded, cut at commandMax characters.
func Shorten(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	f[0] = filepath.Base(f[0])
	s := strings.Join(f, " ")
	if r := []rune(s); len(r) > commandMax {
		s = string(r[:commandMax-1]) + "…"
	}
	return s
}
