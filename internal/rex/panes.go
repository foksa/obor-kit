package rex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/osnotify"
	"obor/internal/progstatus"
)

// Terminal is the creator of Rex's terminal blocks (creator_name): the only blocks that are panes.
const Terminal = "com.superlogical.terminal"

// APIRevision is the API revision the client's calls are checked against (testdata's describe copy; contract.go).
const APIRevision = 19

// Session is one entry of session.list.
type Session struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

// Block is one entry of session.list_blocks.
type Block struct {
	ID      string `json:"block_id"`
	Label   string `json:"label"`
	Creator string `json:"creator_name"`
}

// Process is one process of a terminal block (the process method's child, foreground and ancestors).
type Process struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"` // the process's name, which is not its command: Claude's is its version
	Path  string `json:"path"`
	Argv0 string `json:"argv0,omitempty"` // as the process shows it now (claude, -zsh)
	Cwd   string `json:"cwd,omitempty"`   // the foreground's only
}

// program is p's name for a person: its argv0 without its path, else its name.
func (p Process) program() string {
	return port.Process{Argv0: p.Argv0, Name: p.Name}.Program()
}

// Processes is the process method's answer: the block's own program (child: nil once it exited), what holds the
// terminal now (foreground), the processes between the two, the child first (none when the foreground is the
// child), and how the program exited (nil while it runs).
type Processes struct {
	Child      *Process  `json:"child"`
	Foreground *Process  `json:"foreground"`
	Ancestors  []Process `json:"ancestors"`
	LastExit   *struct {
		ExitCode int `json:"exit_code"`
	} `json:"last_exit"`
}

// Record is one program_status record: an OSC 7501 report as Rex relays it. ID is "" for the root record (left out
// of the method's list, "" in the stream's events); owner is the terminal's foreground process when the report came.
type Record struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Kind  string `json:"kind"`
	Msg   string `json:"msg"`
	App   string `json:"app"`
	Owner *struct {
		PID int `json:"pid"`
	} `json:"owner"`
}

// root is the root record of recs (nil: none): the program's own status; a child record (OpenCode's per session,
// Claude's per background shell) is not read.
func root(recs []Record) *Record {
	for i := range recs {
		if recs[i].ID == "" {
			return &recs[i]
		}
	}
	return nil
}

// term is one terminal block as the adapter knows it: its session's label, the block, its processes and its root
// record.
type term struct {
	Session, Group string
	Block          Block
	Proc           Processes
	Status         *Record
}

// pane is t as the port sees it: ID and Terminal the block id, Group its session's label, Label the block's (label),
// Run the block's own program's pid (child), Cwd the foreground's. The status is the root record's (progstatus.Report: its
// word, its note, its app's kind by agents.ByApp of kinds) only while the record's owner is the foreground (ownerRuns):
// an agent that exited or was killed leaves its last record behind. Without a record that counts, a foreground other
// than the block's shell is unknown; the shell itself is no agent.
func (t term) pane(kinds agents.Kinds) port.Pane {
	p := port.Pane{ID: t.Block.ID, Terminal: t.Block.ID, Group: t.Group, Label: label(t.Block)}
	if c := t.Proc.Child; c != nil {
		p.Run = strconv.Itoa(c.PID)
	}
	if f := t.Proc.Foreground; f != nil {
		p.Cwd = f.Cwd
	}
	switch {
	case t.ownerRuns():
		rep := progstatus.Report{State: t.Status.State, Kind: t.Status.Kind, Msg: t.Status.Msg, App: t.Status.App}
		p.Agent = agents.ByApp(kinds, rep.App)
		p.Status, p.Note = rep.Word(), rep.Note()
	case t.programRuns():
		p.Status = "unknown"
	}
	return p
}

// ShellFlavor is the flavor of the terminal blocks obor makes (the creator's one flavor); its short name is the
// label a new block gets.
const ShellFlavor = Terminal + ".shell"

// label is b's label as obor's: "" for a label Rex made up, the flavor's short name a new block starts with ("shell")
// or the default an empty label restores ("Block " and the id's last 8 characters).
func label(b Block) string {
	if b.Label == ShellFlavor[strings.LastIndex(ShellFlavor, ".")+1:] || len(b.ID) >= 8 && b.Label == "Block "+b.ID[len(b.ID)-8:] {
		return ""
	}
	return b.Label
}

// ownerRuns: the root record's owner holds the foreground now.
func (t term) ownerRuns() bool {
	f := t.Proc.Foreground
	return t.Status != nil && t.Status.Owner != nil && f != nil && t.Status.Owner.PID == f.PID
}

// programRuns: a program other than the block's shell holds the foreground. The block's shell is its own program
// (child), or under Rex's default `login` wrapper the process login runs: the foreground is the shell when no process
// but login wrappers lies between it and the child.
func (t term) programRuns() bool {
	f := t.Proc.Foreground
	if f == nil {
		return false
	}
	chain := append(append([]Process{}, t.Proc.Ancestors...), *f)
	for len(chain) > 0 && isLogin(chain[0]) {
		chain = chain[1:]
	}
	return len(chain) > 1
}

// isLogin: p is login(1), the wrapper Rex starts a default block's shell under.
func isLogin(p Process) bool { return p.Path == "/usr/bin/login" || p.Path == "" && p.Name == "login" }

// Panes is port.Panes over a Rex server: every terminal block of every session is a pane, its Group its session's
// label.
type Panes struct {
	C *Client
	// Notifier shows Notify's notification: Rex has none of its own (osnotify: on macOS, osascript).
	Notifier osnotify.Notifier
}

var _ port.Panes = Panes{}

// serverStatus is server.status as far as obor reads it: the run's id (a restart changes it).
type serverStatus struct {
	InstanceID string `json:"instance_id"`
}

// serverVersion is server.version as far as obor reads it.
type serverVersion struct {
	Version     string `json:"rex_version"`
	APIRevision int    `json:"api_revision"`
}

// snapshot is the port's snapshot of terms with the server's version, its instance_id as the instance and the
// revision note (revisionNote). Each pane's Run is its block's program's pid.
func snapshot(kinds agents.Kinds, v serverVersion, st serverStatus, terms []term) port.Snapshot {
	out := port.Snapshot{Version: v.Version, Instance: st.InstanceID, PaneRuns: true, Note: revisionNote(v)}
	for _, t := range terms {
		out.Panes = append(out.Panes, t.pane(kinds))
	}
	return out
}

// revisionNote is the note on a server whose API revision is not the one obor's calls are checked against ("" when it
// is): "Rex <version> speaks API revision <n>; obor's calls are checked against revision <APIRevision>".
func revisionNote(v serverVersion) string {
	if v.APIRevision == APIRevision {
		return ""
	}
	return fmt.Sprintf("Rex %s speaks API revision %d; obor's calls are checked against revision %d", v.Version,
		v.APIRevision, APIRevision)
}

// Note is the terminal's note on the API revision its server speaks when that is not the one obor's calls are checked
// against ("" when it is): obor setup show and obor init's summary print it. It reads server.version.
func (p Panes) Note(ctx context.Context) (string, error) {
	var v serverVersion
	if err := p.C.call(ctx, &v, "server.version", nil); err != nil {
		return "", err
	}
	return revisionNote(v), nil
}

// Snapshot is the server now: server.version, server.status, then every session's terminal blocks (reads.all).
func (p Panes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	var v serverVersion
	if err := p.C.call(ctx, &v, "server.version", nil); err != nil {
		return port.Snapshot{}, err
	}
	var st serverStatus
	if err := p.C.call(ctx, &st, "server.status", nil); err != nil {
		return port.Snapshot{}, err
	}
	_, terms, err := p.C.reads().all(ctx)
	if err != nil {
		return port.Snapshot{}, err
	}
	return snapshot(p.C.kinds(), v, st, terms), nil
}

// sessions is session.list.
func (cl *Client) sessions(ctx context.Context) ([]Session, error) {
	var r struct {
		Sessions []Session `json:"sessions"`
	}
	if err := cl.call(ctx, &r, "session.list", nil); err != nil {
		return nil, err
	}
	return r.Sessions, nil
}

// blocks is session.list_blocks of session sid.
func (cl *Client) blocks(ctx context.Context, sid string) ([]Block, error) {
	var r struct {
		Blocks []Block `json:"blocks"`
	}
	if err := cl.call(ctx, &r, "session.list_blocks", map[string]any{"session_id": sid}); err != nil {
		return nil, err
	}
	return r.Blocks, nil
}

// reads are the calls that read the server's sessions and blocks: the client's (Client.reads), or a test's recorded
// answers.
type reads struct {
	sessions func(ctx context.Context) ([]Session, error)
	blocks   func(ctx context.Context, sid string) ([]Block, error)
	term     func(ctx context.Context, t *term) error
}

func (cl *Client) reads() reads {
	return reads{sessions: cl.sessions, blocks: cl.blocks, term: cl.term}
}

// all is every session and the terminal blocks in each (session): the snapshot. A session gone by the time its
// blocks are listed has none.
func (r reads) all(ctx context.Context) ([]Session, []term, error) {
	ss, err := r.sessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []term
	for _, s := range ss {
		ts, err := r.session(ctx, s, nil)
		if err != nil && !port.NotFound(err) {
			return nil, nil, err
		}
		out = append(out, ts...)
	}
	return ss, out, nil
}

// session is session s's terminal blocks (blocks), each with its processes and its root record. A block in known is
// taken from it with its label from the list (no calls); any other is read (term) and left out when its program has
// exited (a kept block: no program will run in it again) or it is gone by the time it is read.
func (r reads) session(ctx context.Context, s Session, known map[string]term) ([]term, error) {
	blocks, err := r.blocks(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	out := make([]term, 0, len(blocks))
	var read []int
	for _, b := range blocks {
		if b.Creator != Terminal {
			continue
		}
		t, ok := known[b.ID]
		t.Session, t.Group, t.Block = s.ID, s.Label, b
		out = append(out, t)
		if !ok {
			read = append(read, len(out)-1)
		}
	}
	// The block methods run concurrently, a few at a time: each is one CLI process (~30 ms).
	errs := make([]error, len(out))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, i := range read {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = r.term(ctx, &out[i])
		}()
	}
	wg.Wait()
	kept := out[:0]
	for i, t := range out {
		switch err := errs[i]; {
		case err == nil && t.Proc.Child != nil:
			kept = append(kept, t)
		case err == nil, port.NotFound(err):
		default:
			return nil, err
		}
	}
	return kept, nil
}

// term reads t's block: its processes and its root record.
func (cl *Client) term(ctx context.Context, t *term) error {
	if err := cl.call(ctx, &t.Proc, Terminal+".process", blockParams(t.Session, t.Block.ID, nil)); err != nil {
		return err
	}
	var ps struct {
		Records []Record `json:"records"`
	}
	if err := cl.call(ctx, &ps, Terminal+".program_status", blockParams(t.Session, t.Block.ID, nil)); err != nil {
		return err
	}
	t.Status = root(ps.Records)
	return nil
}

// Notify: Rex has no notifications of its own, so the toast is the OS's notification with the same title and body
// (osnotify), bounded by ctx; when none shows, it is a port.NotShown with the reason.
func (p Panes) Notify(ctx context.Context, title, body string) error {
	if err := p.Notifier.Show(ctx, title, body); err != nil {
		return &port.NotShown{Reason: "rex: " + err.Error()}
	}
	return nil
}
