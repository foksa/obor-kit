package herdr_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

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

func TestPanesSubscribe(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Group("w1", "pj")
	srv := herdrtest.Serve(t, f)
	st, err := herdr.Panes{S: srv.Client()}.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The core sees workspace w1 by its label, as the pane's group.
	want := port.Pane{ID: "w1:p1", Group: "pj", Cwd: "/proj-wt/L-01", Agent: "claude", Name: "pj-l-01", Status: "idle",
		Label: "pj-l-01", Session: "5e1d0c2b-aaaa", Launched: true}
	if len(st.Snapshot.Panes) != 3 || st.Snapshot.Panes[0] != want {
		t.Fatalf("snapshot = %+v\nwant first pane %+v", st.Snapshot, want)
	}
	srv.WaitSubscribed(t, herdr.EventStatus, "w1:p1")
	f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
	srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p1", "agent_status": "working", "agent": "claude"})
	srv.Push("pane_exited", map[string]any{"pane_id": "w1:p2"})
	// Status and lifecycle events come on separate subscriptions: their order is not defined.
	working := want
	working.Status = "working" // a status event carries the pane's label too
	want2 := map[port.Event]bool{
		{Kind: port.EventStatus, Pane: "w1:p1", Status: "working", Agent: "claude", New: working}: true,
		{Kind: port.EventExited, Pane: "w1:p2"}:                                                   true,
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
		"label": "pj-renamed", "cwd": "/proj-wt/L-01"}})
	relabelled := working
	relabelled.Label = "pj-renamed"
	if ev := nextApp(t, st); ev.Kind != port.EventStatus || ev.New != relabelled {
		t.Errorf("relabel event = %+v, want a status event for %+v", ev, relabelled)
	}
	// a new pane carries its label (herdr may drop an agent's name; the label is obor's agent name too)
	f.Shell("w1:p4", "pj-sol", "/tmp")
	srv.Push("pane_created", map[string]any{"type": "pane_created", "pane": map[string]any{"pane_id": "w1:p4",
		"label": "pj-sol", "cwd": "/tmp"}})
	if ev := nextApp(t, st); ev.Kind != port.EventCreated || ev.New.ID != "w1:p4" || ev.New.Label != "pj-sol" {
		t.Errorf("created event = %+v, want w1:p4 labelled pj-sol", ev)
	}
	st.Close()
	for range st.C { // C closes once the stream ends
	}
	if st.Err() == nil {
		t.Error("Err after Close is nil")
	}
}

func nextApp(t *testing.T, st *port.Stream) port.Event {
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
	return port.Event{}
}

// Type sends agent.prompt to the pane with the text as given (a job's wake with its tag); herdr's refusal is the error.
func TestPanesPromptSendsJobID(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": map[string]any{"name": "pj-l-01",
		"pane_id": "w1:p1", "agent_status": "working"}})
	p := herdr.Panes{S: srv.Client()}
	if _, err := p.Type(ctx, "pj-l-01", "[obor job T-7/2/review] review the candidate", false); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "agent.prompt"); got != `{"target":"pj-l-01","text":"[obor job T-7/2/review] review the candidate"}` {
		t.Errorf("agent.prompt params = %s", got)
	}
	srv.Fail("agent.prompt", "agent_not_found", "no agent pj-l-09")
	if typed, err := p.Type(ctx, "pj-l-09", "[obor job T-7/2/review] x", false); err == nil || typed {
		t.Errorf("a refused prompt: typed %v, error %v; want nothing typed, an error", typed, err)
	}
}

// Type types a line as it is (no job tag, no clear key without clear); a refused one typed nothing.
func TestPanesLineTypesTheLineAsItIs(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": map[string]any{"name": "pj-l-01",
		"pane_id": "w1:p1", "agent_status": "working"}})
	p := herdr.Panes{S: srv.Client()}
	if typed, err := p.Type(ctx, "pj-l-01", "[obor pj-lead] verify", false); err != nil || !typed {
		t.Fatalf("Line: typed %v, %v", typed, err)
	}
	if got := lastParams(t, srv, "agent.prompt"); got != `{"target":"pj-l-01","text":"[obor pj-lead] verify"}` {
		t.Errorf("agent.prompt params = %s", got)
	}
	for _, req := range srv.Requests() {
		if req.Method == "agent.send_keys" {
			t.Errorf("a key sent with the line: %s", req.Params)
		}
	}
	srv.Fail("agent.prompt", "agent_not_found", "no agent pj-l-09")
	if typed, err := p.Type(ctx, "pj-l-09", "x", false); err == nil || typed {
		t.Errorf("a refused line: typed %v, error %v; want nothing typed, an error", typed, err)
	}
}

// Prompt types the text once and sends no key after it, whatever the agent does (an idle agent that shows no
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
			if _, err := p.Type(ctx, "pj-l-01", "[obor job "+job+"] "+text, false); err != nil {
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
	if _, err := p.Type(ctx, "pj-l-01", "[obor job T-7/1/review] review T-7", false); err != nil {
		t.Fatal(err)
	}
	a := f.Pane("w1:p1")
	f.Locked(func() {
		if a.Draft != msg || len(a.Submitted) != 0 {
			t.Fatalf("precondition: draft %q, submitted %q; want the text unsent in the box", a.Draft, a.Submitted)
		}
	})
	if _, err := p.Type(ctx, "pj-l-01", "[obor job T-7/1/review] review T-7", true); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range srv.Requests() {
		if r.Method == "agent.send_keys" || r.Method == "agent.prompt" {
			order = append(order, r.Method+" "+string(r.Params))
		}
	}
	want := []string{`agent.prompt {"target":"pj-l-01","text":"` + msg + `"}`,
		`agent.send_keys {"keys":["ctrl+u"],"target":"pj-l-01"}`,
		`agent.prompt {"target":"pj-l-01","text":"` + msg + `"}`}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	f.Locked(func() {
		if strings.Join(a.Submitted, "|") != msg || a.Draft != "" {
			t.Errorf("submitted %q, draft %q; want the text once, the box empty", a.Submitted, a.Draft)
		}
	})
}

// TestPanesPromptTyped (docs/state-model.md "Prompts"): Prompt's typed says whether any of the text may be in the input box. Refused
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
			typed, err := p.Type(ctx, "pj-l-01", "[obor job T-7/1/work] do T-7", c.clear)
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

// Two Claude sessions whose transcripts are not found yet are two
// sessions to the app: Pane.Session is herdr's id, whatever the transcript lookup finds.
// The terminal's session is carried as herdr reports it (an id), and the pane's transcript only when herdr reports the
// session as a path: the core finds the file a session id names (activity.go).
func TestPanesSessionAndTranscript(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Pane("w1:p3").Session = "new-session"
	f.Agent("w1:p4", "pj-cdx", "idle", "/proj-wt/L-04").Transcript = "/home/u/.codex/sessions/rollout-cdx.jsonl"
	srv := herdrtest.Serve(t, f)
	st, err := herdr.Panes{S: srv.Client()}.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got := map[string]port.Pane{}
	for _, p := range st.Snapshot.Panes {
		got[p.ID] = p
	}
	if got["w1:p1"].Transcript != "" || got["w1:p3"].Transcript != "" {
		t.Fatalf("precondition: no transcript reported by herdr (%q, %q)", got["w1:p1"].Transcript, got["w1:p3"].Transcript)
	}
	if got["w1:p4"].Transcript != "/home/u/.codex/sessions/rollout-cdx.jsonl" {
		t.Errorf("the reported path = %q, want it as herdr gave it", got["w1:p4"].Transcript)
	}
	if got["w1:p1"].Session != "5e1d0c2b-aaaa" || got["w1:p3"].Session != "new-session" {
		t.Errorf("sessions %q, %q; want herdr's ids", got["w1:p1"].Session, got["w1:p3"].Session)
	}
}

// The note is the adapter's own sentence when herdr speaks a newer protocol than obor knows; none at obor's own.
func TestPanesNoteOnANewerProtocol(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	p := herdr.Panes{S: srv.Client()}
	if snap, err := p.Snapshot(ctx); err != nil || snap.Note != "" {
		t.Errorf("at obor's protocol: note %q, %v; want none", snap.Note, err)
	}
	srv.Protocol(herdr.Protocol + 1)
	snap, err := p.Snapshot(ctx)
	want := "herdr fake, protocol " + strconv.Itoa(herdr.Protocol+1) + " (newer than obor's " + strconv.Itoa(herdr.Protocol) + ")"
	if err != nil || snap.Note != want {
		t.Errorf("on a newer protocol: note %q, %v; want %q", snap.Note, err, want)
	}
}

// the subscription's snapshot and Snapshot carry herdr's instance, its socket file's time in RFC 3339: a restarted
// herdr (a new socket file) gives another one, and two reads of the same server the same.
func TestPanesInstanceIsTheSocketFileTime(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	p := herdr.Panes{S: srv.Client()}
	at := time.Date(2026, 11, 9, 8, 0, 0, 123456789, time.UTC)
	srv.Restart(t, at)
	st, err := p.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := at.Format(time.RFC3339Nano); st.Snapshot.Instance != want {
		t.Errorf("stream snapshot Instance = %q, want the socket file's %q", st.Snapshot.Instance, want)
	}
	st.Close()
	later := at.Add(time.Hour)
	srv.Restart(t, later)
	snap, err := p.Snapshot(ctx)
	if want := later.Format(time.RFC3339Nano); err != nil || snap.Instance != want {
		t.Errorf("Snapshot after a restart: Instance %q, %v; want %q", snap.Instance, err, want)
	}
	again, err := p.Snapshot(ctx)
	if err != nil || again.Instance != snap.Instance {
		t.Errorf("two snapshots of one server: Instance %q, %q, %v; want them equal", snap.Instance, again.Instance, err)
	}
}
