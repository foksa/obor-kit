package herdr_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/app"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

func TestSocketSnapshotWorkspacesAndTranscript(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "pj"}}
	p := f.Pane("w1:p1")
	p.Workspace, p.Transcript = "w1", "/t/s.jsonl"
	snap, err := herdrtest.Serve(t, f).Client().Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Workspaces) != 1 || snap.Workspaces[0] != (herdr.Workspace{ID: "w1", Label: "pj"}) {
		t.Errorf("workspaces = %+v", snap.Workspaces)
	}
	a := snap.Agents[0]
	if a.Workspace != "w1" || a.Transcript != "/t/s.jsonl" || snap.Panes[0].Transcript != "/t/s.jsonl" {
		t.Errorf("agent = %+v, pane = %+v: want workspace w1 and the transcript path", a, snap.Panes[0])
	}
	if snap.Agents[1].Transcript != "" {
		t.Errorf("an agent without a session has transcript %q", snap.Agents[1].Transcript)
	}

	// A session reported by id is no transcript.
	g := fixture()
	snap, err = herdrtest.Serve(t, g).Client().Snapshot(ctx)
	if err != nil || snap.Agents[0].Session != "5e1d0c2b-aaaa" || snap.Agents[0].Transcript != "" {
		t.Errorf("session by id: %+v, %v", snap.Agents[0], err)
	}
}

func TestSocketProcessInfo(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Pane("w1:p1").Procs = []herdr.Process{{PID: 2, Name: "claude", Argv0: "claude"}}
	srv := herdrtest.Serve(t, f)
	info, err := srv.Client().ProcessInfo(ctx, "w1:p1")
	if err != nil || info.ShellPID != 1 || info.FgPGID != 2 || info.Foreground() != "claude" {
		t.Errorf("ProcessInfo = %+v, %v", info, err)
	}
	if got := lastParams(t, srv, "pane.process_info"); got != `{"pane_id":"w1:p1"}` {
		t.Errorf("params = %s", got)
	}
	if _, err := srv.Client().ProcessInfo(ctx, "w9:p9"); err == nil {
		t.Error("ProcessInfo of a missing pane: no error")
	}
}

func TestPanesBusy(t *testing.T) {
	t.Parallel()
	f := fixture() // w1:p1's agent is pid 2 (the fake's foreground group); w1:p2 is a shell at its prompt
	srv := herdrtest.Serve(t, f)
	for _, tc := range []struct {
		name, pane, ps string
		want           bool
	}{
		{"nothing under the agent", "w1:p1", "1 0 1\n2 1 2\n", false},
		{"helpers in the agent's group (MCP server, caffeinate)", "w1:p1", "2 1 2\n3 2 2\n4 3 2\n5 2 2\n", false},
		{"a command in a group of its own", "w1:p1", "2 1 2\n3 2 2\n50 2 50\n", true},
		{"a grandchild in another group", "w1:p1", "2 1 2\n3 2 2\n60 3 60\n", true},
		{"another group not under the agent", "w1:p1", "2 1 2\n70 1 70\n71 70 71\n", false},
		{"the shell has the foreground", "w1:p2", "1 0 1\n80 1 80\n", false},
	} {
		ps := tc.ps
		p := herdr.Panes{S: srv.Client(), PS: func(context.Context) ([]byte, error) { return []byte(ps), nil }}
		if got, err := p.Busy(ctx, tc.pane); err != nil || got != tc.want {
			t.Errorf("%s: Busy = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

// T-331: the cases herdr cannot tell are errors (unknown), never (false, nil) (unknown is never idle, RV-36).
func TestPanesBusyUnknown(t *testing.T) {
	t.Parallel()
	psOf := func(table string) func(context.Context) ([]byte, error) {
		return func(context.Context) ([]byte, error) { return []byte(table), nil }
	}
	zero := fixture() // w1:p1's agent is pid 2; herdr's answer is scripted below
	zeroSrv := herdrtest.Serve(t, zero)
	zeroSrv.Reply("pane.process_info", map[string]any{"type": "pane_process_info",
		"process_info": map[string]any{"pane_id": "w1:p1", "shell_pid": 1, "foreground_process_group_id": 0}})
	for _, tc := range []struct {
		name  string
		panes herdr.Panes
		want  string // what the error says
	}{
		{"the foreground group is 0", herdr.Panes{S: zeroSrv.Client(), PS: psOf("1 0 1\n2 1 2\n")},
			"foreground process group is 0"},
		{"the agent's pid is not in ps", herdr.Panes{S: herdrtest.Serve(t, fixture()).Client(), PS: psOf("1 0 1\n70 1 70\n71 70 71\n")},
			"pid 2 is not in ps"},
		{"ps itself fails", herdr.Panes{S: herdrtest.Serve(t, fixture()).Client(),
			PS: func(context.Context) ([]byte, error) { return nil, errors.New("ps: permission denied") }},
			"ps: permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			busy, err := tc.panes.Busy(ctx, "w1:p1")
			if busy || !errors.Is(err, herdr.ErrUnknown) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Busy = %v, %v; want false, unknown (%q)", busy, err, tc.want)
			}
		})
	}
}

func TestPanesSubscribe(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "pj"}}
	f.Pane("w1:p1").Workspace = "w1"
	srv := herdrtest.Serve(t, f)
	st, err := herdr.Panes{S: srv.Client(), Home: t.TempDir()}.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := app.Pane{ID: "w1:p1", Workspace: "w1", Cwd: "/proj-wt/O-01", Agent: "claude", Status: "idle",
		Label: "pj-o-01", Session: "5e1d0c2b-aaaa"}
	if st.Snapshot.Workspaces["w1"] != "pj" || len(st.Snapshot.Panes) != 3 || st.Snapshot.Panes[0] != want {
		t.Fatalf("snapshot = %+v\nwant first pane %+v", st.Snapshot, want)
	}
	srv.WaitSubscribed(t, herdr.EventStatus, "w1:p1")
	f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
	srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p1", "agent_status": "working", "agent": "claude"})
	srv.Push("pane_exited", map[string]any{"pane_id": "w1:p2"})
	// Status and lifecycle events come on separate subscriptions: their order is not defined.
	working := want
	working.Status = "working" // a status event carries the pane's label too
	want2 := map[app.Event]bool{
		{Kind: app.EventStatus, Pane: "w1:p1", Status: "working", Agent: "claude", New: working}: true,
		{Kind: app.EventExited, Pane: "w1:p2"}:                                                   true,
	}
	for range 2 {
		if ev := nextApp(t, st); !want2[ev] {
			t.Errorf("event = %+v, want one of %+v", ev, want2)
		} else {
			delete(want2, ev)
		}
	}
	// pane.rename emits no event of its own: the next update of the pane rereads it, and the relabel is delivered
	f.Set("w1:p1", func(p *herdrtest.Pane) { p.Label = "pj-renamed" })
	srv.Push("pane_updated", map[string]any{"type": "pane_updated", "pane": map[string]any{"pane_id": "w1:p1",
		"label": "pj-renamed", "cwd": "/proj-wt/O-01"}})
	relabelled := working
	relabelled.Label = "pj-renamed"
	if ev := nextApp(t, st); ev.Kind != app.EventStatus || ev.New != relabelled {
		t.Errorf("relabel event = %+v, want a status event for %+v", ev, relabelled)
	}
	// a new pane carries its label (herdr may drop an agent's name; the label is obor's agent name too)
	f.Shell("w1:p4", "pj-sol", "/tmp")
	srv.Push("pane_created", map[string]any{"type": "pane_created", "pane": map[string]any{"pane_id": "w1:p4",
		"label": "pj-sol", "cwd": "/tmp"}})
	if ev := nextApp(t, st); ev.Kind != app.EventCreated || ev.New.ID != "w1:p4" || ev.New.Label != "pj-sol" {
		t.Errorf("created event = %+v, want w1:p4 labelled pj-sol", ev)
	}
	st.Close()
	for range st.C { // C closes once the stream ends
	}
	if st.Err() == nil {
		t.Error("Err after Close is nil")
	}
}

func nextApp(t *testing.T, st *app.Stream) app.Event {
	t.Helper()
	select {
	case ev, ok := <-st.C:
		if !ok {
			t.Fatalf("stream ended: %v", st.Err())
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	return app.Event{}
}

// T-563: Turn reads the last turn of a Codex agent's rollout (found from its session id, or the path herdr reports)
// from its tail, a long rollout's too (its first, cut line skipped); a kind whose turns are not read (Claude), no file,
// no session or no agent: unknown. RV-59 #2: a turn whose start is more than a tail back (one 300 KiB output after it,
// or thirty 10 KiB ones) is still found, by either path; an oversized task_complete is read whole; a rollout of many
// chunks with no turn event, its last line cut: unknown.
func TestPanesTurn(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	write := func(rel, body string) string {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const (
		started = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n"
		limited = `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"try again at 6:30 PM","codex_error_info":"usage_limit_exceeded"}}}` + "\n"
	)
	write(".claude/projects/-proj-wt-O-01/5e1d0c2b-aaaa.jsonl", started+limited)
	write(".codex/sessions/2026/10/24/rollout-2026-10-24T15-05-17-cdx1.jsonl", started+limited)
	// over 256 KiB: one huge item line, cut at the tail's start, then the turn's end
	long := write("long.jsonl", started+`{"type":"response_item","payload":{"output":"`+strings.Repeat("x", 300<<10)+`"}}`+"\n"+limited)
	output := func(n int) string {
		return `{"type":"response_item","payload":{"output":"` + strings.Repeat("x", n) + `"}}` + "\n"
	}
	const newTurn = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}` + "\n"
	oneBig := started + limited + newTurn + output(300<<10)
	many := started + limited + newTurn + strings.Repeat(output(10<<10), 30)
	if len(oneBig)-len(started+limited+newTurn) <= 256<<10 || len(many)-len(started+limited+newTurn) <= 256<<10 {
		t.Fatal("precondition: the new turn's start is more than 256 KiB from the end")
	}
	bigPath := write("big.jsonl", oneBig)
	manyPath := write("many.jsonl", many)
	write(".codex/sessions/2026/10/30/rollout-2026-10-30T09-00-00-cdxbig.jsonl", oneBig)
	write(".codex/sessions/2026/10/30/rollout-2026-10-30T09-00-00-cdxmany.jsonl", many)
	huge := "try again at 6:30 PM " + strings.Repeat("y", 300<<10)
	hugeEnd := write("huge.jsonl", started+output(10)+`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"`+huge+`","codex_error_info":"usage_limit_exceeded"}}}`+"\n")
	noTurn := write("noturn.jsonl", strings.Repeat(output(10<<10), 60)+`{"type":"event_msg","payload":{"type":"task_comp`)

	f := fixture() // w1:p1: claude, session 5e1d0c2b-aaaa
	f.Agent("w1:p4", "pj-cdx", "idle", "/proj-wt/O-04").Kind = "codex"
	f.Pane("w1:p4").Session = "cdx1"
	f.Agent("w1:p5", "pj-cdx2", "idle", "/proj-wt/O-05").Kind = "codex"
	f.Pane("w1:p5").Transcript = long
	f.Agent("w1:p6", "pj-cdx3", "idle", "/proj-wt/O-06").Kind = "codex"
	f.Pane("w1:p6").Session = "no-such-file"
	for i, c := range []struct{ session, transcript string }{
		{"", bigPath}, {"", manyPath}, {"cdxbig", ""}, {"cdxmany", ""}, {"", hugeEnd}, {"", noTurn},
	} {
		pane := fmt.Sprintf("w1:p%d", 10+i)
		f.Agent(pane, fmt.Sprintf("pj-cdx%d", 10+i), "working", fmt.Sprintf("/proj-wt/O-%d", 10+i)).Kind = "codex"
		f.Pane(pane).Session, f.Pane(pane).Transcript = c.session, c.transcript
	}
	srv := herdrtest.Serve(t, f)
	p := herdr.Panes{S: srv.Client(), Home: home}
	hit := agents.Turn{ID: "t1", Error: "usage_limit_exceeded", Details: "try again at 6:30 PM", Ended: true}
	t2 := agents.Turn{ID: "t2"}
	for _, tc := range []struct {
		name, pane string
		want       agents.Turn
		ok         bool
	}{
		{"Codex session id: its rollout's last turn", "w1:p4", hit, true},
		{"a long rollout herdr names: its tail", "w1:p5", hit, true},
		{"herdr's path: one 300 KiB output after the new start", "w1:p10", t2, true},
		{"herdr's path: thirty 10 KiB outputs after the new start", "w1:p11", t2, true},
		{"session id: one 300 KiB output after the new start", "w1:p12", t2, true},
		{"session id: thirty 10 KiB outputs after the new start", "w1:p13", t2, true},
		{"an oversized task_complete with an error", "w1:p14", agents.Turn{ID: "t1", Error: "usage_limit_exceeded", Details: huge, Ended: true}, true},
		{"no turn event, the last line cut", "w1:p15", agents.Turn{}, false},
		{"Claude: turns not read", "w1:p1", agents.Turn{}, false},
		{"no file for the id", "w1:p6", agents.Turn{}, false},
		{"no session (w1:p3)", "w1:p3", agents.Turn{}, false},
		{"no agent", "w1:p2", agents.Turn{}, false},
		{"no pane", "w9:p9", agents.Turn{}, false},
	} {
		if got, ok := p.Turn(ctx, tc.pane, ""); ok != tc.ok || got != tc.want {
			t.Errorf("%s: Turn = %.200v, %v; want %.200v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// Fable A3: herdr reports every live session by id, so the transcript is found from it: Claude's under
// .claude/projects, Codex's rollout under .codex/sessions. Unknown stays unknown.
func TestPanesActivity(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	write := func(rel string, at time.Time) string {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	at := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	claude := write(".claude/projects/-proj-wt-O-01/5e1d0c2b-aaaa.jsonl", at)
	write(".claude/projects/-proj-wt-O-01/other.jsonl", at.Add(time.Hour))
	codex := write(".codex/sessions/2026/10/24/rollout-2026-10-24T15-05-17-01a1-cdx.jsonl", at.Add(time.Minute))

	f := fixture() // w1:p1: claude, session 5e1d0c2b-aaaa
	f.Agent("w1:p4", "pj-cdx", "idle", "/proj-wt/O-04").Kind = "codex"
	f.Pane("w1:p4").Session = "01a1-cdx"
	f.Agent("w1:p5", "pj-gem", "idle", "/proj-wt/O-05").Kind = "gemini"
	f.Pane("w1:p5").Session = "5e1d0c2b-aaaa" // a Claude file of this id exists, but the kind is not Claude
	f.Agent("w1:p6", "pj-new", "idle", "/proj-wt/O-06").Session = "no-such-file"
	srv := herdrtest.Serve(t, f)
	p := herdr.Panes{S: srv.Client(), Home: home}
	for _, tc := range []struct {
		name, pane string
		want       time.Time
		known      bool
	}{
		{"Claude session id: its transcript", "w1:p1", at, true},
		{"Codex session id: its rollout", "w1:p4", at.Add(time.Minute), true},
		{"unknown kind", "w1:p5", time.Time{}, false},
		{"no file for the id yet", "w1:p6", time.Time{}, false},
		{"no session (w1:p3)", "w1:p3", time.Time{}, false},
		{"no agent", "w1:p2", time.Time{}, false},
		{"no pane", "w9:p9", time.Time{}, false},
	} {
		if last, known := p.Activity(ctx, tc.pane); known != tc.known || !last.Equal(tc.want) {
			t.Errorf("%s: Activity = %v, %v; want %v, %v", tc.name, last, known, tc.want, tc.known)
		}
	}

	// The stream's panes carry the same path, for the core's transcript check.
	st, err := p.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got := map[string]string{}
	for _, pane := range st.Snapshot.Panes {
		got[pane.ID] = pane.Transcript
	}
	if got["w1:p1"] != claude || got["w1:p4"] != codex || got["w1:p5"] != "" || got["w1:p6"] != "" {
		t.Errorf("snapshot transcripts = %v, want w1:p1 %s, w1:p4 %s, none for the rest", got, claude, codex)
	}
}

// Type sends agent.prompt to the pane with the text as given (a job's wake with its tag); herdr's refusal is the error.
func TestPanesPromptSendsJobID(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": map[string]any{"name": "pj-o-01",
		"pane_id": "w1:p1", "agent_status": "working"}})
	p := herdr.Panes{S: srv.Client()}
	if _, err := p.Type(ctx, "pj-o-01", "[obor job T-7/2/review] review the candidate", false); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "agent.prompt"); got != `{"target":"pj-o-01","text":"[obor job T-7/2/review] review the candidate"}` {
		t.Errorf("agent.prompt params = %s", got)
	}
	srv.Fail("agent.prompt", "agent_not_found", "no agent pj-o-09")
	if typed, err := p.Type(ctx, "pj-o-09", "[obor job T-7/2/review] x", false); err == nil || typed {
		t.Errorf("a refused prompt: typed %v, error %v; want nothing typed, an error", typed, err)
	}
}

// T-499: Type types a line as it is (no job tag, no clear key without clear); a refused one typed nothing.
func TestPanesLineTypesTheLineAsItIs(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": map[string]any{"name": "pj-o-01",
		"pane_id": "w1:p1", "agent_status": "working"}})
	p := herdr.Panes{S: srv.Client()}
	if typed, err := p.Type(ctx, "pj-o-01", "[obor pj-lead] verify", false); err != nil || !typed {
		t.Fatalf("Line: typed %v, %v", typed, err)
	}
	if got := lastParams(t, srv, "agent.prompt"); got != `{"target":"pj-o-01","text":"[obor pj-lead] verify"}` {
		t.Errorf("agent.prompt params = %s", got)
	}
	for _, req := range srv.Requests() {
		if req.Method == "agent.send_keys" {
			t.Errorf("a key sent with the line: %s", req.Params)
		}
	}
	srv.Fail("agent.prompt", "agent_not_found", "no agent pj-o-09")
	if typed, err := p.Type(ctx, "pj-o-09", "x", false); err == nil || typed {
		t.Errorf("a refused line: typed %v, error %v; want nothing typed, an error", typed, err)
	}
}

// T-417: Prompt types the text once and sends no key after it, whatever the agent does (an idle agent that shows no
// change, a quick turn, a working one): a bare Enter would act on a dialog herdr reports as idle.
func TestPanesPromptSendsNoEnter(t *testing.T) {
	t.Parallel()
	const job, text = "T-7/1/work", "do T-7"
	const msg = "[obor job " + job + "] " + text
	for _, c := range []struct {
		name, status string
		prompts      []string
		submitted    []string
		draft        string
	}{
		{name: "idle, no change: text typed, no key", prompts: []string{"typed"}, draft: msg},
		{name: "taken at once: no key", submitted: []string{msg}},
		{name: "a quick turn: no key", prompts: []string{"quick"}, submitted: []string{msg}},
		{name: "working agent: queued, no key", status: "working", prompts: []string{"typed"}, draft: msg},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := fixture()
			srv := herdrtest.Serve(t, f)
			f.Set("w1:p1", func(p *herdrtest.Pane) {
				p.Prompts = c.prompts
				if c.status != "" {
					p.Status = c.status
				}
			})
			p := herdr.Panes{S: srv.Client()}
			if _, err := p.Type(ctx, "pj-o-01", "[obor job "+job+"] "+text, false); err != nil {
				t.Fatal(err)
			}
			a := f.Pane("w1:p1")
			f.Locked(func() {
				if len(a.Keys) != 0 || strings.Join(a.Submitted, "|") != strings.Join(c.submitted, "|") || a.Draft != c.draft {
					t.Errorf("keys %q, submitted %q, draft %q; want no keys, %q, %q", a.Keys, a.Submitted, a.Draft,
						c.submitted, c.draft)
				}
			})
			for _, r := range srv.Requests() {
				if r.Method == "agent.send_keys" {
					t.Errorf("agent.send_keys sent: %s", r.Params)
				}
			}
		})
	}
}

// The retry of a prompt left in the box: ctrl+u clears the unsent text before the prompt is typed again, so the
// agent takes the text once (not twice, one on top of the other).
func TestPanesPromptAgainClearsTheLine(t *testing.T) {
	t.Parallel()
	const msg = "[obor job T-7/1/review] review T-7"
	f := fixture()
	srv := herdrtest.Serve(t, f)
	f.Set("w1:p1", func(p *herdrtest.Pane) { p.Prompts, p.OnEnter = []string{"typed"}, "lost" })
	p := herdr.Panes{S: srv.Client()}
	if _, err := p.Type(ctx, "pj-o-01", "[obor job T-7/1/review] review T-7", false); err != nil {
		t.Fatal(err)
	}
	a := f.Pane("w1:p1")
	f.Locked(func() {
		if a.Draft != msg || len(a.Submitted) != 0 {
			t.Fatalf("precondition: draft %q, submitted %q; want the text unsent in the box", a.Draft, a.Submitted)
		}
	})
	if _, err := p.Type(ctx, "pj-o-01", "[obor job T-7/1/review] review T-7", true); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range srv.Requests() {
		if r.Method == "agent.send_keys" || r.Method == "agent.prompt" {
			order = append(order, r.Method+" "+string(r.Params))
		}
	}
	want := []string{`agent.prompt {"target":"pj-o-01","text":"` + msg + `"}`,
		`agent.send_keys {"keys":["ctrl+u"],"target":"pj-o-01"}`,
		`agent.prompt {"target":"pj-o-01","text":"` + msg + `"}`}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	f.Locked(func() {
		if strings.Join(a.Submitted, "|") != msg || a.Draft != "" {
			t.Errorf("submitted %q, draft %q; want the text once, the box empty", a.Submitted, a.Draft)
		}
	})
}

// T-371 (docs/state-model.md "Prompts"): Prompt's typed says whether any of the text may be in the input box. Refused
// before typing (herdr's error answer, the clear failing, no socket): false, the app sends it again. Typing began,
// then an error (the connection dropped before the answer): true with the error, so
// the app never types it again.
func TestPanesPromptTyped(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, prompt, onEnter string
		clear, noSocket       bool
		typed                 bool
		draft                 string
	}{
		{name: "refused by herdr: not typed", prompt: "refused"},
		{name: "no socket: not typed", noSocket: true},
		{name: "the clear fails: not typed", clear: true, onEnter: "fail"},
		{name: "connection drops after typing: typed", prompt: "fail-typed", typed: true, draft: "[obor job T-7/1/work] do T-7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := fixture()
			srv := herdrtest.Serve(t, f)
			f.Set("w1:p1", func(p *herdrtest.Pane) { p.OnPrompt, p.OnEnter = c.prompt, c.onEnter })
			cl := srv.Client()
			if c.noSocket {
				cl.Path = filepath.Join(t.TempDir(), "none.sock")
			}
			if c.clear { // the clear's keys fail as an Enter would: ctrl+u is refused with them
				f.Set("w1:p1", func(p *herdrtest.Pane) { p.OnEnter = "" })
				srv.Fail("agent.send_keys", "keys_failed", "cannot send keys")
			}
			p := herdr.Panes{S: cl}
			typed, err := p.Type(ctx, "pj-o-01", "[obor job T-7/1/work] do T-7", c.clear)
			if err == nil || typed != c.typed {
				t.Fatalf("typed %v, error %v; want typed %v and an error", typed, err, c.typed)
			}
			a := f.Pane("w1:p1")
			f.Locked(func() {
				if a.Draft != c.draft || len(a.Submitted) != 0 {
					t.Errorf("draft %q, submitted %q; want draft %q, nothing submitted", a.Draft, a.Submitted, c.draft)
				}
			})
		})
	}
}

// RV-40 #2 (TestRV40SessionBeforeTranscript, fixed): two Claude sessions whose transcripts are not found yet are two
// sessions to the app: Pane.Session is herdr's id, whatever the transcript lookup finds.
func TestPanesSessionWithoutTranscript(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Pane("w1:p3").Session = "new-session"
	srv := herdrtest.Serve(t, f)
	st, err := herdr.Panes{S: srv.Client(), Home: t.TempDir()}.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got := map[string]app.Pane{}
	for _, p := range st.Snapshot.Panes {
		got[p.ID] = p
	}
	if got["w1:p1"].Transcript != "" || got["w1:p3"].Transcript != "" {
		t.Fatalf("precondition: no transcript found (%q, %q)", got["w1:p1"].Transcript, got["w1:p3"].Transcript)
	}
	if got["w1:p1"].Session != "5e1d0c2b-aaaa" || got["w1:p3"].Session != "new-session" {
		t.Errorf("sessions %q, %q; want herdr's ids", got["w1:p1"].Session, got["w1:p3"].Session)
	}
}
