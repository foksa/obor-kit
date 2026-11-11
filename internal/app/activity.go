// activity.go: what an agent itself records of its work, the watchdogs' evidence: its transcript (the last write,
// the last turn) and the child processes under its pane's agent. It makes no terminal call but the pane's process
// info, which the terminal answers (Options.Info); the transcript files and the process table are the machine's.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/background"
)

// ErrUnknown is Busy's error when obor cannot tell whether a child runs under the pane's agent: no process info from
// the terminal, its foreground group is 0, the agent's pid is not in ps, or ps fails. It is never idle: the caller
// must treat it as unknown activity.
var ErrUnknown = errors.New("obor: unknown activity")

// AgentActivity is what an agent itself records of its work: its transcript's last write and last turn, and whether a
// child runs under its pane's agent. agentFiles implements it from the agent's files and the machine's process table;
// a test scripts it (Options.Activity).
type AgentActivity interface {
	// Transcript is pane's transcript file: the path the terminal reports, else the file the pane's session id names
	// under the agent's home ("" when none is found).
	Transcript(pane port.Pane) string
	// Activity is when pane's agent last wrote its transcript; known is false when obor cannot tell (no session, a
	// kind whose transcripts obor cannot find, the file not there: unknown, not inactive).
	Activity(pane port.Pane) (last time.Time, known bool)
	// Turn is the last turn pane's agent's transcript records, for a kind whose turns are read (agents.TurnsRead):
	// the file transcript names (its agent's recorded session's), else the one the pane's session names; ok false
	// when obor cannot tell.
	Turn(pane port.Pane, transcript string) (agents.Turn, bool)
	// Busy: the agent in pane still runs a child besides the agent CLI (a command, a background job). An error means
	// unknown, not idle.
	Busy(ctx context.Context, pane port.Pane) (bool, error)
}

// agentFiles is AgentActivity from the agents' transcript files under home and the machine's process table, with each
// pane's process info from the terminal.
type agentFiles struct {
	home  string // the agents' transcripts: .claude/projects, .codex/sessions ("" : the user's home)
	info  func(ctx context.Context, pane string) (port.ProcessInfo, error)
	procs func(ctx context.Context) ([]background.Proc, error) // nil: background.ReadProcs
}

var _ AgentActivity = (*agentFiles)(nil)

// Activity is when pane's agent last wrote its transcript.
func (a *agentFiles) Activity(pane port.Pane) (time.Time, bool) {
	path := a.Transcript(pane)
	if path == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// turnChunk is how much of a transcript Turn reads at a time, from its end back: a turn's last events first, then
// earlier chunks while none is found (a turn's tool outputs can be megabytes).
const turnChunk = 256 << 10

// Turn is the last turn pane's agent's transcript records: the file transcript names (its recorded session's), else
// the one the pane's session names; ok false when obor cannot tell: no session, a kind whose turns are not read, no
// file, no turn event in it.
func (a *agentFiles) Turn(pane port.Pane, transcript string) (agents.Turn, bool) {
	k, ok := agents.Lookup(pane.Agent)
	if !ok || !agents.TurnsRead(k) {
		return agents.Turn{}, false
	}
	path := transcript
	if path == "" {
		path = a.Transcript(pane)
	}
	if path == "" {
		return agents.Turn{}, false
	}
	return lastTurn(k, path)
}

// lastTurn is k.LastTurn over the file read back from its end a turnChunk at a time, whole lines only, until a chunk
// holds a turn event or the file starts. A line longer than a chunk is read whole: the cut start of a chunk joins the
// chunk before it.
func lastTurn(k agents.Kind, path string) (agents.Turn, bool) {
	f, err := os.Open(path)
	if err != nil {
		return agents.Turn{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return agents.Turn{}, false
	}
	var cut []byte // the start of the chunk read last, up to its first newline: the end of a line begun before it
	for off := fi.Size(); off > 0; {
		start := max(off-turnChunk, 0)
		b := make([]byte, off-start, off-start+int64(len(cut)))
		if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
			return agents.Turn{}, false
		}
		b, off = append(b, cut...), start
		cut = nil
		if start > 0 { // the first bytes may be the end of a line
			i := bytes.IndexByte(b, '\n')
			if i < 0 { // all of it inside one line
				cut = b
				continue
			}
			cut, b = b[:i+1], b[i+1:]
		}
		if t, ok := k.LastTurn(b); ok {
			return t, true
		}
	}
	return agents.Turn{}, false
}

// Transcript is pane's transcript file: the path the terminal reports, else the file its session id names under home
// (agents.Kind.Transcript: .claude/projects/*/<id>.jsonl, .codex/sessions/YYYY/MM/DD/rollout-*-<id>.jsonl; the newest
// if several); "" when none is found.
func (a *agentFiles) Transcript(pane port.Pane) string {
	if pane.Transcript != "" {
		return pane.Transcript
	}
	k, ok := agents.Lookup(pane.Agent)
	if pane.Session == "" || strings.ContainsAny(pane.Session, "/*?[\\") || !ok || !k.Caps().Transcript {
		return ""
	}
	home := a.home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	env := func(name string) string { // the home alone: the kinds' own dir variables are the agents' environment
		if name == "HOME" {
			return home
		}
		return ""
	}
	matches, _ := filepath.Glob(k.Transcript(env, pane.Session))
	path, newest := "", time.Time{}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && (path == "" || fi.ModTime().After(newest)) {
			path, newest = m, fi.ModTime()
		}
	}
	return path
}

// Busy: a process under the pane's agent runs in a process group of its own. The agent leads the pane's foreground
// group, with its long-lived helpers (MCP servers, caffeinate); the commands it runs, a background job too, get
// their own group (Claude's Bash tool), so the terminal's foreground list alone does not show them.
// (false, nil) only when the check could see the agent: an ErrUnknown error means the activity is not known, which
// is not the same as quiet.
func (a *agentFiles) Busy(ctx context.Context, pane port.Pane) (bool, error) {
	if a.info == nil {
		return false, fmt.Errorf("%w: the terminal gives no process info", ErrUnknown)
	}
	info, err := a.info(ctx, pane.ID)
	if err != nil {
		return false, err
	}
	if info.FgPGID == 0 {
		return false, fmt.Errorf("%w: the pane's foreground process group is 0", ErrUnknown)
	}
	if info.AtShell() {
		return false, nil
	}
	procs := a.procs
	if procs == nil {
		procs = background.ReadProcs
	}
	table, err := procs(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: ps: %w", ErrUnknown, err)
	}
	busy, known := background.NewProcIndex(table).ChildOutsideGroup(info.FgPGID)
	if !known {
		return false, fmt.Errorf("%w: pid %d is not in ps", ErrUnknown, info.FgPGID)
	}
	return busy, nil
}
