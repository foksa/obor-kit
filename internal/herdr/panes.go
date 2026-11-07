package herdr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
)

// ErrUnknown is Busy's error when herdr cannot tell whether a child runs under the pane's agent: the pane's
// foreground group is 0, the agent's pid is not in ps, or ps fails. It is never idle: the caller must treat it as
// unknown activity (the app does: any Busy error is unknown).
var ErrUnknown = errors.New("herdr: unknown activity")

// Panes is port.Panes over the herdr socket.
type Panes struct {
	S *Socket
	// PS is the machine's process table, lines "pid ppid pgid" (nil: `ps -A -o pid=,ppid=,pgid=`).
	PS func(ctx context.Context) ([]byte, error)
	// Home holds the agents' transcripts: .claude/projects and .codex/sessions ("": the user's home).
	Home string
}

var _ port.Panes = Panes{}

// Subscribe is a Stream as the core sees it: lifecycle and status events.
// It pings first: a herdr on another protocol is refused with a clear error. An EventStatus carries the pane as read
// after the event in New (name, workspace, cwd and transcript too), like an EventCreated.
func (p Panes) Subscribe(ctx context.Context) (*port.Stream, error) {
	s, err := Dial(ctx, p.S.Path)
	if err != nil {
		return nil, err
	}
	s.Timeout = p.S.Timeout
	st, err := s.Subscribe(ctx)
	if err != nil {
		return nil, err
	}
	c, done := make(chan port.Event), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(c)
		for ev := range st.C {
			select {
			case c <- p.appEvent(ev):
			case <-done:
				return
			}
		}
	}()
	return &port.Stream{Snapshot: p.appSnapshot(st.Snapshot), C: c, Err: st.Err,
		Close: func() { once.Do(func() { close(done); st.Close() }) }}, nil
}

func (p Panes) appEvent(ev Event) port.Event {
	switch ev.Kind {
	case EventStatus:
		return port.Event{Kind: port.EventStatus, Pane: ev.Pane, Status: ev.Status, Agent: ev.Agent, New: p.appPane(ev.Now)}
	case EventCreated:
		pane := p.appPane(ev.Now)
		pane.Label, pane.Terminal = ev.New.Label, ev.New.Terminal
		return port.Event{Kind: port.EventCreated, Pane: ev.Pane, New: pane}
	case EventClosed:
		return port.Event{Kind: port.EventClosed, Pane: ev.Pane}
	}
	return port.Event{Kind: port.EventExited, Pane: ev.Pane}
}

// appPane is a pane as herdr reports it (its agent entry, or the pane's fields when no agent runs), with obor's
// reading of its session as the transcript. An agent entry has no label of its own: Label is the pane's, set by a
// Stream's reread and the snapshot.
func (p Panes) appPane(a port.Pane) port.Pane {
	a.Transcript = p.transcript(a)
	return a
}

func (p Panes) appSnapshot(s Snapshot) port.Snapshot {
	out := port.Snapshot{Workspaces: map[string]string{}, Version: s.Version, Protocol: s.Protocol, Speaks: Protocol}
	for _, w := range s.Workspaces {
		out.Workspaces[w.ID] = w.Label
	}
	seen := observed(s)
	for _, pane := range s.Panes {
		ap := p.appPane(seen[pane.ID])
		ap.Label, ap.Terminal = pane.Label, pane.Terminal
		out.Panes = append(out.Panes, ap)
	}
	return out
}

// Snapshot is herdr's session now, as the subscription's Snapshot: the same source (session.snapshot), for a pane whose
// cwd changed with no event (a cd in it: T-745).
func (p Panes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	s, err := p.S.Snapshot(ctx)
	if err != nil {
		return port.Snapshot{}, err
	}
	return p.appSnapshot(s), nil
}

// Activity is when pane's agent last wrote its transcript; known is false when herdr reports no session for it, or
// for a kind whose transcripts obor cannot find, or when the file is not there (unknown, not inactive). pane is the one
// the caller observed (T-951): its Session and Transcript, no herdr call.
func (p Panes) Activity(_ context.Context, pane port.Pane) (last time.Time, known bool) {
	path := p.transcript(pane)
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
// earlier chunks while none is found (a turn's tool outputs can be megabytes, RV-59 #2).
const turnChunk = 256 << 10

// Turn is the last turn pane's agent's transcript records (lastTurn): the file transcript names (its recorded
// session's, T-577), else the one herdr reports; ok false when obor cannot tell: no session, a kind whose turns are
// not read, no file, no turn event in it. pane is the one the caller observed (T-951): its Agent, Session and
// Transcript, no herdr call.
func (p Panes) Turn(_ context.Context, pane port.Pane, transcript string) (agents.Turn, bool) {
	k, ok := agents.Lookup(pane.Agent)
	if !ok || !k.Caps().Turns {
		return agents.Turn{}, false
	}
	path := transcript
	if path == "" {
		path = p.transcript(pane)
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

// transcript is a's transcript file: the path herdr reports, else the file its session id names under the home
// (agents.Kind.Transcript: .claude/projects/*/<id>.jsonl, .codex/sessions/YYYY/MM/DD/rollout-*-<id>.jsonl; the newest
// if several); "" when none is found.
func (p Panes) transcript(a port.Pane) string {
	if a.Transcript != "" {
		return a.Transcript
	}
	k, ok := agents.Lookup(a.Agent)
	if a.Session == "" || strings.ContainsAny(a.Session, "/*?[\\") || !ok || !k.Caps().Transcript {
		return ""
	}
	home := p.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	env := func(name string) string { // the home alone: the kinds' own dir variables are the agents' environment
		if name == "HOME" {
			return home
		}
		return ""
	}
	matches, _ := filepath.Glob(k.Transcript(env, a.Session))
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
// their own group (Claude's Bash tool), so herdr's foreground list alone does not show them.
// (false, nil) only when the check could see the agent: an ErrUnknown error means the activity is not known, which
// is not the same as quiet.
func (p Panes) Busy(ctx context.Context, pane string) (bool, error) {
	info, err := p.S.ProcessInfo(ctx, pane)
	if err != nil {
		return false, err
	}
	if info.FgPGID == 0 {
		return false, fmt.Errorf("%w: the pane's foreground process group is 0", ErrUnknown)
	}
	if info.AtShell() {
		return false, nil
	}
	ps := p.PS
	if ps == nil {
		ps = func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,pgid=").Output()
		}
	}
	table, err := ps(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: ps: %w", ErrUnknown, err)
	}
	return childOutsideGroup(string(table), info.FgPGID)
}

// childOutsideGroup: in the ps table (lines "pid ppid pgid"), a descendant of process agent is in another group.
// Its error is ErrUnknown when the agent is not in the table: no child seen there is no evidence of quiet.
func childOutsideGroup(table string, agent int) (bool, error) {
	type proc struct{ ppid, pgid int }
	procs := map[int]proc{}
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		if e1 == nil && e2 == nil && e3 == nil {
			procs[pid] = proc{ppid, pgid}
		}
	}
	a, ok := procs[agent]
	if !ok {
		return false, fmt.Errorf("%w: pid %d is not in ps", ErrUnknown, agent)
	}
	for _, pr := range procs {
		if pr.pgid == a.pgid {
			continue
		}
		for q, n := pr.ppid, 0; q > 1 && n < 64; q, n = procs[q].ppid, n+1 {
			if q == agent {
				return true, nil
			}
		}
	}
	return false, nil
}

// Notify is notification.show.
func (p Panes) Notify(ctx context.Context, title, body string) error {
	return p.S.Notify(ctx, title, body)
}

var _ port.Typer = Panes{}

// Type types text into pane (agent.prompt, no wait): a job's wake with its [obor job <id>] tag, or serve's line as it
// is. The agent's own proof of delivery (a job's job_next) counts, not herdr's answer. clear: the input line is
// cleared first (ctrl+u). No key follows the text: a bare Enter would act on a selection dialog herdr reports as idle,
// and an undelivered wake is the app's delivery timers' to send again. began is false only when nothing of the text
// was typed: the clear failed, or agent.prompt was refused by herdr or never sent (Reached); after that, any error
// (a lost answer) comes with began true.
func (p Panes) Type(ctx context.Context, pane, text string, clear bool) (bool, error) {
	if clear {
		if err := p.S.AgentKeys(ctx, pane, "ctrl+u"); err != nil {
			return false, err
		}
	}
	if _, err := p.S.Prompt(ctx, pane, text); err != nil {
		return Reached(err), err
	}
	return true, nil
}
