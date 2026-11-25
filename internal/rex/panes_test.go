package rex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/osnotify"
	"obor/internal/testkit"
)

// readJSON decodes testdata/name into v.
func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// A terminal block is a pane: its id the pane's id and terminal, its session's label the group, its label the label,
// its own program's pid the run (login under a default block), the foreground's cwd the cwd; at its shell no agent.
func TestPaneOfBlock(t *testing.T) {
	const block = "block:01a21238-4d58-7b65-b12f-fb4043ae59fa"
	for _, c := range []struct{ file, run string }{
		{"process-login.json", "17242"}, // rex new: login runs the shell
		{"process-shell.json", "18580"}, // rex run -- /bin/zsh -l: the shell is the block's program
	} {
		var proc Processes
		readJSON(t, c.file, &proc)
		if proc.Child == nil || proc.Foreground == nil {
			t.Fatalf("precondition: %s has a child and a foreground", c.file)
		}
		got := term{Session: "session:x", Group: "capproj", Block: Block{ID: block, Label: "w1", Creator: Terminal}, Proc: proc}.pane(testkit.Kinds())
		want := port.Pane{ID: block, Terminal: block, Group: "capproj", Label: "w1", Run: c.run, Cwd: "/private/tmp/claude-501/rxl02/work/a"}
		if got != want {
			t.Errorf("%s: pane = %+v, want %+v", c.file, got, want)
		}
	}
}

// The block list's terminal blocks are panes; its other blocks are not read.
func TestListBlocks(t *testing.T) {
	var r struct {
		Blocks []Block `json:"blocks"`
	}
	readJSON(t, "list-blocks.json", &r)
	if len(r.Blocks) != 1 || r.Blocks[0].Creator != Terminal || r.Blocks[0].Label != "shell" {
		t.Fatalf("blocks = %+v, want the one terminal block labelled shell", r.Blocks)
	}
}

// The root record is the one without an id; a child record (OpenCode's per session) is never the status.
func TestRootRecord(t *testing.T) {
	recs := []Record{{ID: "ses_dedd9752bffeg5xJAUEuu4nEXZ", State: "blocked"}, {State: "working", App: "opencode"}}
	if r := root(recs); r == nil || r.State != "working" {
		t.Errorf("root = %+v, want the record without an id", r)
	}
	if r := root(recs[:1]); r != nil {
		t.Errorf("root of child records only = %+v, want none", r)
	}
}

// The record counts only while its owner holds the foreground: kept with a matching pid; dropped with a dead owner
// and the shell in the foreground (no agent: login's shell, or the block's own shell); dropped with another foreground
// (unknown), as is any program at the shell with no record.
func TestOwnerRule(t *testing.T) {
	login := Process{PID: 17242, Name: "login", Path: "/usr/bin/login"}
	zsh := Process{PID: 40382, Name: "zsh", Path: "/bin/zsh", Cwd: "/w"}
	claude := Process{PID: 42621, Name: "2.1.332", Path: "/Users/u/.local/share/claude/versions/2.1.332", Cwd: "/w"}
	sleep := Process{PID: 50425, Name: "sleep", Path: "/bin/sleep", Cwd: "/w"}
	rec := func(state string, owner int) *Record {
		r := &Record{State: state, App: "claude-code", Msg: "listed 2 files"}
		r.Owner = &struct {
			PID int `json:"pid"`
		}{owner}
		return r
	}
	for _, c := range []struct {
		name        string
		proc        Processes
		status      *Record
		word, agent string
		note        string
	}{
		{"record kept: its owner is the foreground", Processes{Child: &zsh, Foreground: &claude, Ancestors: []Process{zsh}},
			rec("done", 42621), "done", "claude", "listed 2 files"},
		{"dead owner, the block's shell in the foreground: no agent", Processes{Child: &zsh, Foreground: &zsh},
			rec("done", 42621), "", "", ""},
		{"dead owner, login's shell in the foreground: no agent", Processes{Child: &login, Foreground: &zsh, Ancestors: []Process{login}},
			rec("idle", 42621), "", "", ""},
		{"dead owner, another program in the foreground: unknown", Processes{Child: &zsh, Foreground: &sleep, Ancestors: []Process{zsh}},
			rec("done", 42621), "unknown", "", ""},
		{"another program under login's shell, no record: unknown", Processes{Child: &login, Foreground: &sleep, Ancestors: []Process{login, zsh}},
			nil, "unknown", "", ""},
		{"a record without an owner does not count", Processes{Child: &zsh, Foreground: &claude, Ancestors: []Process{zsh}},
			&Record{State: "working", App: "claude-code"}, "unknown", "", ""},
		{"login itself in the foreground (the shell not up): no agent", Processes{Child: &login, Foreground: &login},
			nil, "", "", ""},
		{"no foreground: no agent", Processes{Child: &zsh}, nil, "", "", ""},
	} {
		p := term{Block: Block{ID: "b"}, Proc: c.proc, Status: c.status}.pane(testkit.Kinds())
		if p.Status != c.word || p.Agent != c.agent || p.Note != c.note {
			t.Errorf("%s: status %q agent %q note %q, want %q %q %q", c.name, p.Status, p.Agent, p.Note, c.word, c.agent, c.note)
		}
	}
}

// probeTerm is the block of a probe's replay (testdata/probe-<kind>.jsonl, cut from the probe's stream) as the
// snapshot would hold it before the first line: `rex run -- /bin/zsh -l`, the shell its program.
func probeTerm(t *testing.T, kind string) (term, [][]byte) {
	t.Helper()
	f, err := os.Open("testdata/probe-" + kind + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	var first struct {
		Payload struct {
			SessionID string `json:"session_id"`
			Event     struct {
				Payload blockEvent `json:"payload"`
			} `json:"event"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil || len(lines) < 20 {
		t.Fatalf("precondition: probe-%s.jsonl has its block's lines (%d, %v)", kind, len(lines), err)
	}
	var shell *Process
	for _, l := range lines { // the shell: the first foreground, before the agent starts
		var d struct {
			Payload struct {
				Event struct {
					Payload struct {
						Payload struct {
							Foreground *Process `json:"foreground"`
						} `json:"payload"`
					} `json:"payload"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal(l, &d) == nil && d.Payload.Event.Payload.Payload.Foreground != nil {
			shell = d.Payload.Event.Payload.Payload.Foreground
			break
		}
	}
	if shell == nil || shell.Name != "zsh" {
		t.Fatalf("precondition: probe-%s.jsonl's first foreground is zsh, got %+v", kind, shell)
	}
	sid, bid := first.Payload.SessionID, first.Payload.Event.Payload.BlockID
	return term{Session: sid, Group: "probe", Block: Block{ID: bid, Label: kind, Creator: Terminal}, Proc: Processes{Child: shell}}, lines
}

// step is one line of a replay and what the stream sent for it.
type step struct {
	line   []byte
	events []port.Event
}

// childID is the record id of a program_status_changed line ("" for the root's and for any other line).
func childID(l []byte) string {
	var d struct {
		Payload struct {
			Event struct {
				Payload struct {
					Name    string `json:"name"`
					Payload struct {
						ID string `json:"id"`
					} `json:"payload"`
				} `json:"payload"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(l, &d) != nil || d.Payload.Event.Payload.Name != EventStatusChanged {
		return ""
	}
	return d.Payload.Event.Payload.Payload.ID
}

// replay applies a probe's lines to a stream holding only its block: each line's events, and the block at the end. A
// child record's line (OpenCode's per session, Claude's per background shell) sends nothing.
func replay(t *testing.T, kind string) ([]step, term) {
	t.Helper()
	tm, lines := probeTerm(t, kind)
	st := &stream{ctx: context.Background(), kinds: testkit.Kinds, labels: map[string]string{tm.Session: "probe"},
		terms: map[string]term{tm.Block.ID: tm},
		reads: reads{blocks: func(context.Context, string) ([]Block, error) {
			t.Fatal("a probe's lines are all of a known block: no read")
			return nil, nil
		}}}
	var steps []step
	for _, l := range lines {
		evs, err := st.apply(l)
		if err != nil {
			t.Fatalf("apply %s: %v", l, err)
		}
		if id := childID(l); id != "" && len(evs) != 0 {
			t.Errorf("%s: a child record's line (%s) sent %+v", kind, id, evs)
		}
		steps = append(steps, step{l, evs})
	}
	return steps, st.terms[tm.Block.ID]
}

// statuses are the replay's EventStatus panes, in order.
func statuses(steps []step) []port.Pane {
	var out []port.Pane
	for _, s := range steps {
		for _, e := range s.events {
			if e.Kind == port.EventStatus {
				out = append(out, e.New)
			}
		}
	}
	return out
}

// has: one of ps has the status, agent and note.
func has(ps []port.Pane, status, agent, note string) bool {
	for _, p := range ps {
		if p.Status == status && p.Agent == agent && p.Note == note {
			return true
		}
	}
	return false
}

// Each kind's records as the probe recorded them in a Rex block: Claude's blocked dialog with its msg; Codex's with
// none; OpenCode's child records ignored; agy's app antigravity as the agy kind. A record left after its agent exited
// or was killed is no agent once the shell is back in the foreground.
func TestKindRecords(t *testing.T) {
	if agents.ByApp(testkit.Kinds(), "antigravity") != "agy" || agents.ByApp(testkit.Kinds(), "opencode") != "opencode" {
		t.Fatal("precondition: the test kinds hold agy and opencode")
	}
	t.Run("claude: blocked with its msg; a killed Claude's done record is no agent", func(t *testing.T) {
		steps, end := replay(t, "claude")
		if !slices.ContainsFunc(steps, func(s step) bool { return childID(s.line) == "b2b2uaf3g" }) {
			t.Fatal("precondition: the replay has the background shell's child record")
		}
		ps := statuses(steps)
		if !has(ps, "blocked", "claude", "permission: approve Write: /private/tmp/claude-501/rx/work/claude/hello.txt") {
			t.Errorf("no blocked pane with Claude's msg in %+v", ps)
		}
		if !has(ps, "working", "claude", "Listing files in current directory") || !has(ps, "done", "claude", "listed 2 files here: a.txt and hello.txt") {
			t.Errorf("no working and done panes with their msgs in %+v", ps)
		}
		if end.Status == nil || end.Status.State != "done" || end.Proc.Foreground.Name != "zsh" {
			t.Fatalf("precondition: the killed Claude's done record stays with zsh in the foreground, got %+v", end)
		}
		if p := end.pane(testkit.Kinds()); p.Status != "" || p.Agent != "" {
			t.Errorf("after the kill: status %q agent %q, want no agent", p.Status, p.Agent)
		}
	})
	t.Run("codex: blocked with no msg is only its kind", func(t *testing.T) {
		ps := statuses(func() []step { s, _ := replay(t, "codex"); return s }())
		if !has(ps, "blocked", "codex", "permission") || !has(ps, "done", "codex", "") {
			t.Errorf("no blocked (permission) and done codex panes in %+v", ps)
		}
	})
	t.Run("opencode: child records ignored; a stale idle record counts neither for the shell nor the next opencode", func(t *testing.T) {
		steps, end := replay(t, "opencode")
		children, unknownAfterRestart := 0, false
		for _, s := range steps {
			if childID(s.line) != "" {
				children++
			}
			// The second opencode (67294) up while the first's idle record (60857) is still there: unknown.
			var d struct {
				Payload struct {
					Event struct {
						Payload struct {
							Payload struct {
								Foreground *Process `json:"foreground"`
							} `json:"payload"`
						} `json:"payload"`
					} `json:"event"`
				} `json:"payload"`
			}
			if json.Unmarshal(s.line, &d) == nil && d.Payload.Event.Payload.Payload.Foreground != nil && d.Payload.Event.Payload.Payload.Foreground.PID == 67294 {
				unknownAfterRestart = len(s.events) == 1 && s.events[0].Status == "unknown"
			}
		}
		if children < 5 {
			t.Fatalf("precondition: the replay has OpenCode's child records, got %d", children)
		}
		if !unknownAfterRestart {
			t.Error("the next opencode in the foreground with the last one's idle record: want unknown")
		}
		ps := statuses(steps)
		if !has(ps, "blocked", "opencode", "permission: Permission needs input") || !has(ps, "idle", "opencode", "") {
			t.Errorf("no blocked and idle opencode panes in %+v", ps)
		}
		if p := end.pane(testkit.Kinds()); end.Status != nil || p.Status != "" {
			t.Errorf("after Rex removed the root record (foreground): %+v, status %q, want none", end.Status, p.Status)
		}
	})
	t.Run("agy: antigravity is agy; its idle record after exit is no agent", func(t *testing.T) {
		steps, end := replay(t, "agy")
		ps := statuses(steps)
		if !has(ps, "blocked", "agy", "auth: Sign in required") || !has(ps, "blocked", "agy", "permission: Approve Bash: touch hello2.txt") {
			t.Errorf("no blocked agy panes (auth, permission) in %+v", ps)
		}
		if end.Status == nil || end.Status.State != "idle" || end.Proc.Foreground.Name != "zsh" {
			t.Fatalf("precondition: agy's idle record stays with zsh in the foreground, got %+v", end)
		}
		if p := end.pane(testkit.Kinds()); p.Status != "" || p.Agent != "" {
			t.Errorf("after agy exited: status %q agent %q, want no agent", p.Status, p.Agent)
		}
	})
}

// A notification that does not show (osnotify's reason) is a NotShown naming rex and the reason.
func TestNotify(t *testing.T) {
	fail := func(context.Context, string, string) error { return errors.New("execution error: not allowed") }
	err := (Panes{C: &Client{}, Notifier: osnotify.Notifier{GOOS: "darwin", Post: fail}}).Notify(context.Background(), "t", "b")
	var ns *port.NotShown
	if !errors.As(err, &ns) || ns.Reason != "rex: osascript: execution error: not allowed" {
		t.Errorf("Notify = %v, want a NotShown with rex and osascript's error", err)
	}
}
