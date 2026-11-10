package app

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/version"
)

// A Claude start records one session fact with its id and transcript; a second start of the agent a second
// fact, the newest its current one; a repeat of a session records none; a replay folds the same; the store's readers
// (launch, the hooks) read the same facts.
func TestSessionFacts(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.connect()
	if got := h.sessions("pj-l-01"); len(got) != 0 {
		t.Fatalf("precondition: no session recorded, got %q", got)
	}
	now := time.Now().Unix()
	eqs(t, h.session("pj-l-01", "s1", "/t/s1.jsonl", now-60), "SESSION pj-l-01 s1", "reply")
	eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "s1 /t/s1.jsonl", "one fact, id and transcript")
	eqs(t, h.current("pj-l-01"), "s1", "current")

	h.session("pj-l-01", "s2", "/t/s2.jsonl", now)
	eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "s1 /t/s1.jsonl,s2 /t/s2.jsonl", "a second start: a second fact")
	eqs(t, h.current("pj-l-01"), "s2", "current = newest")

	eqs(t, h.session("pj-l-01", "s1", "/t/s1.jsonl", now+60), "SESSION pj-l-01 s1", "a repeat's reply")
	eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "s1 /t/s1.jsonl,s2 /t/s2.jsonl", "a repeat (a resume): no fact")
	eqs(t, h.current("pj-l-01"), "s2", "current unchanged by the repeat")

	ss, err := store.ReadSessionsAt(h.root)
	if err != nil || len(ss) != 2 {
		t.Fatalf("store: %d sessions (%v), want 2", len(ss), err)
	}
	if s, ok := store.FoldSessions(ss).Current("pj-l-01"); !ok || s.ID != "s2" || s.Transcript != "/t/s2.jsonl" || s.Lane != "L-01" ||
		s.Task != "T-1" || s.Pane != "w1:p1" || s.Kind != "claude" || s.Started != now {
		t.Errorf("store's current %+v, want s2 with its fields", s)
	}
	eqs(t, store.TranscriptAt(h.root, "s1"), "/t/s1.jsonl", "transcript by id")

	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	h.connect()
	eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "s1 /t/s1.jsonl,s2 /t/s2.jsonl", "replay: the same facts")
	eqs(t, h.current("pj-l-01"), "s2", "replay: current")
}

// The identity reads the recorded session first: a pane whose herdr session is the record is identified by
// session; one whose herdr session differs is still identified by its label, or its start, as before, and the
// difference is logged once (not adopted).
func TestSessionIdentityCrossCheck(t *testing.T) {
	t.Parallel()
	t.Run("label", func(t *testing.T) {
		t.Parallel()
		h, out := newIdentHarness(t)
		h.connect()
		h.session("pj-l-01", "s1", "", time.Now().Unix())
		h.connect()
		eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "herdr's session is the record")

		h.status("w1:p1", func(p *port.Pane) { p.Session = "s-herdr" })
		eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "a differing herdr session: by label")
		h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
		const line = "herdr reports session s-herdr for pj-l-01 at pane w1:p1; its recorded session is s1 (kept)"
		if n := strings.Count(out.String(), line); n != 1 {
			t.Errorf("logged %d times, want once:\n%s", n, out)
		}
		eqs(t, h.current("pj-l-01"), "s1", "not adopted")
	})
	t.Run("start", func(t *testing.T) {
		t.Parallel()
		h, out := newIdentHarness(t)
		h.pane("w1:p1").Label, h.pane("w1:p1").Session = "", "s-herdr"
		h.started("pj-l-01", "w1:p1", "t1", "")
		h.connect()
		h.session("pj-l-01", "s1", "", time.Now().Unix())
		h.connect()
		eqs(t, h.where("pj-l-01"), "identified w1:p1 start", "a differing herdr session: by start")
		if !strings.Contains(out.String(), "herdr reports session s-herdr for pj-l-01 at pane w1:p1; its recorded session is s1 (kept)") {
			t.Errorf("not logged:\n%s", out)
		}
	})
}

// obor status shows an identified agent's current session: its id, transcript and start.
func TestSessionInStatus(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.connect()
	now := time.Now().Unix()
	h.session("pj-l-01", "s1", "/t/s1.jsonl", now)
	h.connect()
	st := h.a.Status()
	var got *wire.AgentStatus
	for i, a := range st.Project.Agents {
		if a.Pane == "w1:p1" {
			got = &st.Project.Agents[i]
		}
	}
	if got == nil || got.Name != "pj-l-01" {
		t.Fatalf("pj-l-01 not in the status: %+v", st.Project.Agents)
	}
	if got.Session != "s1" || got.Transcript != "/t/s1.jsonl" || got.Started != now {
		t.Errorf("status session %q %q %d, want s1 /t/s1.jsonl %d", got.Session, got.Transcript, got.Started, now)
	}
}

// A session obor named before a start (no transcript yet) is confirmed by its SessionStart hook. The same id
// fills the transcript in, one record for the agent, nothing logged; another id — Claude ran what it was not given —
// wins, one log line, and the pre-record stays in the history.
func TestSessionConfirmation(t *testing.T) {
	t.Parallel()
	// launch is serve's fact for the id obor chose (the launcher's session request before the start).
	launch := func(h *harness, id string, at int64) {
		h.t.Helper()
		if _, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: "pj-l-01", Kind: "claude", ID: id,
			Pane: "w1:p1", Lane: "L-01", Started: at, Source: "launch"})); err != nil {
			h.t.Fatal(err)
		}
	}
	t.Run("the same id: one record, the hook's transcript", func(t *testing.T) {
		t.Parallel()
		h, out := newIdentHarness(t)
		h.connect()
		now := time.Now().Unix()
		launch(h, "chosen", now)
		eqs(t, h.current("pj-l-01"), "chosen", "precondition: the launch's id is current")

		h.session("pj-l-01", "chosen", "/t/chosen.jsonl", now+1)
		eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "chosen /t/chosen.jsonl", "one record, its transcript filled in")
		eqs(t, h.current("pj-l-01"), "chosen", "the same session")
		if strings.Contains(out.String(), "chosen") {
			t.Errorf("logged a confirmation:\n%s", out)
		}
	})
	t.Run("another id: the hook's wins, one log line", func(t *testing.T) {
		t.Parallel()
		h, out := newIdentHarness(t)
		h.connect()
		now := time.Now().Unix()
		launch(h, "chosen", now)

		h.session("pj-l-01", "ran", "/t/ran.jsonl", now+1)
		eqs(t, h.current("pj-l-01"), "ran", "the session Claude ran")
		eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "chosen ,ran /t/ran.jsonl", "the pre-record kept, the session added")
		const line = "pj: pj-l-01 runs session ran, not chosen (the id obor chose): the reported session is recorded"
		if n := strings.Count(out.String(), line); n != 1 {
			t.Errorf("logged %d times, want once:\n%s", n, out)
		}
	})
	t.Run("replay: the confirmation in its record's place", func(t *testing.T) {
		t.Parallel()
		h, _ := newIdentHarness(t)
		h.connect()
		now := time.Now().Unix()
		launch(h, "chosen", now)
		h.session("pj-l-01", "chosen", "/t/chosen.jsonl", now+1)

		h.a.closeLog()
		h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
		h.connect()
		eqs(t, strings.Join(h.sessions("pj-l-01"), ","), "chosen /t/chosen.jsonl", "one record on replay too")
		if ss, err := store.ReadSessionsAt(h.root); err != nil || len(ss) != 1 || ss[0].Transcript != "/t/chosen.jsonl" {
			t.Errorf("store: %+v (%v), want the confirmation alone", ss, err)
		}
	})
}

// One session id belongs to one agent: an id that is another agent's current session is refused (nothing
// recorded, both agents in serve's log, the first agent unchanged); a repeat by its owner is one fact; a log holding
// such a duplicate replays with one owner; the registry-failure shape (two starts, one rollout) records the id for
// the agent that reported it first only.
func TestSessionOneOwner(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.connect()
	now := time.Now().Unix()
	h.session("pj-a", "x", "/t/x.jsonl", now-60)
	eqs(t, h.current("pj-a"), "x", "precondition: A holds x")

	err := h.refused("pj-b", "x", now)
	has(t, err, "pj-b", "the refusal names the reporter")
	has(t, err, "pj-a", "the refusal names the holder")
	eqs(t, h.current("pj-b"), "", "B has no current session")
	eqs(t, strings.Join(h.sessions("pj-b"), ","), "", "B has no session fact")
	eqs(t, strings.Join(h.sessions("pj-a"), ","), "x /t/x.jsonl", "A unchanged")
	has(t, out.String(), "pj-b's session x refused: it is pj-a's current session", "serve's log names both")

	h.session("pj-a", "x", "/t/x.jsonl", now+60)
	eqs(t, strings.Join(h.sessions("pj-a"), ","), "x /t/x.jsonl", "A reports x twice: one fact")

	t.Run("an id A has moved on from is free", func(t *testing.T) {
		h.session("pj-a", "y", "/t/y.jsonl", now+120)
		eqs(t, h.current("pj-a"), "y", "precondition: A moved on to y")
		eqs(t, h.refused("pj-b", "x", now+180), "", "x is no one's current session: B records it")
		eqs(t, h.current("pj-b"), "x", "B holds x")
	})

	t.Run("replay of a log with a duplicate", func(t *testing.T) {
		t.Parallel()
		h2, _ := newIdentHarness(t)
		h2.connect()
		dir := t.TempDir()
		own, dup := filepath.Join(dir, "own-zzzz0001.jsonl"), filepath.Join(dir, "dup-zzzz0001.jsonl")
		for _, f := range []string{own, dup} {
			if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		h2.session("pj-a", "zzzz0001", own, now)
		// the duplicate, as an older log may hold it: written to the log directly, after the owner's (the readers'
		// newest fact of the id)
		p := h2.proj()
		if !h2.a.record(p, event.Record{Task: store.SessionTask, Step: store.StepSession, Kind: event.KindFact},
			store.Session{Agent: "pj-b", ID: "zzzz0001", Transcript: dup, Started: now + 60}) {
			t.Fatal("precondition: the duplicate is in the log")
		}
		if got := h2.current("pj-b"); got != "" {
			t.Errorf("B owns zzzz0001 live: %q", got)
		}
		h2.a.closeLog()
		h2.a = New(Options{Root: h2.root, Now: h2.a.opt.Now, Out: h2.a.opt.Out}, h2.panes, h2.lanes)
		h2.connect()
		eqs(t, h2.current("pj-a"), "zzzz0001", "replay: A owns zzzz0001")
		eqs(t, h2.current("pj-b"), "", "replay: B does not")

		// The store's readers apply the same rule (the session fold): the owner's only.
		if s, ok := store.CurrentAt(h2.root, "pj-a"); !ok || s.ID != "zzzz0001" || s.Transcript != own {
			t.Errorf("CurrentAt A: %+v %v, want zzzz0001 at %s", s, ok, own)
		}
		if s, ok := store.CurrentAt(h2.root, "pj-b"); ok {
			t.Errorf("CurrentAt B: %+v, want none", s)
		}
		eqs(t, store.TranscriptAt(h2.root, "zzzz0001"), own, "TranscriptAt: the owner's")
		eqs(t, project.TranscriptOf(h2.root, "zzzz0001", func(string) (string, bool) { return "", false }), own, "TranscriptOf: the owner's")
	})

	t.Run("registry failure: two starts, one rollout", func(t *testing.T) {
		t.Parallel()
		h3, _ := newIdentHarness(t)
		h3.connect()
		// both searches found the first start's rollout: the true owner reports it first, the other after
		eqs(t, h3.refused("pj-c", "r1", now), "", "the first report is recorded")
		has(t, h3.refused("pj-d", "r1", now+1), "pj-c", "the second start's report of r1 is refused")
		eqs(t, h3.current("pj-c"), "r1", "the true owner records it")
		eqs(t, h3.current("pj-d"), "", "the other does not")
	})
}

// sessionFactGolden is a session fact's data as an older serve recorded it (the flat session call): the stored
// shape the one session type keeps, key for key.
const sessionFactGolden = `{"agent":"pj-l-01","kind":"claude","id":"sid-1","transcript":"/t/sid-1.jsonl","pane":"w1:p1",` +
	`"lane":"L-01","task":"T-1","started":1700000000,"source":"SessionStart hook"}`

// An older stored session fact folds the same, and a session recorded now is stored in that shape.
func TestSessionFactGoldenFolds(t *testing.T) {
	t.Parallel()
	want := store.Session{Agent: "pj-l-01", Kind: "claude", ID: "sid-1", Transcript: "/t/sid-1.jsonl", Pane: "w1:p1",
		Lane: "L-01", Task: "T-1", Started: 1700000000, Source: "SessionStart hook"}
	p := &proj{}
	p.foldSession(event.Record{Task: store.SessionTask, Step: store.StepSession, Kind: event.KindFact,
		Data: []byte(sessionFactGolden)})
	if all := p.folded.sessions.All(); len(all) != 1 || all[0] != want {
		t.Errorf("the golden fact folds to %+v, want [%+v]", all, want)
	}
	h, _ := newIdentHarness(t)
	h.connect()
	h.session("pj-l-01", "sid-1", "/t/sid-1.jsonl", 1700000000)
	var data string
	for _, rec := range h.records() {
		if rec.Task == store.SessionTask && rec.Step == store.StepSession && rec.Kind == event.KindFact {
			data = string(rec.Data)
		}
	}
	if data != sessionFactGolden {
		t.Errorf("a session recorded now is stored as %s, want %s", data, sessionFactGolden)
	}
}

// The session call's shape changed with its key (session_fact, not the flat session id): a hook older than
// serve sends the flat fields, and serve refuses it naming both versions, recording nothing; a serve older than its
// hook reads the new call as one without a session id (its refusal names both versions, as above), never as an
// unreadable request.
func TestSessionCallShapeSkewRefusedByVersion(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.connect()
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go h.a.Listen(ln)
	client, server := net.Pipe()
	ln.conns <- server
	old := `{"method":"session","call":{"root":` + strconv.Quote(h.root) + `,"agent":"pj-l-01","role":"","call_id":"",` +
		`"task":"T-1","version":"v0.1.0","session":"sid-old","kind":"claude","transcript":"/t/old.jsonl","started":5}}`
	go func() { _, _ = client.Write([]byte(old + "\n")) }()
	ans := read(t, client)
	client.Close()
	want := "session: the agent and the session id are required (caller pj-l-01 runs obor v0.1.0, serve " +
		version.Version + ")"
	if ans.Error != want {
		t.Errorf("an old hook's session call: error %q, want %q", ans.Error, want)
	}
	if got := h.sessions("pj-l-01"); len(got) != 0 {
		t.Errorf("an old hook's refused call recorded %v", got)
	}

	call, err := json.Marshal(wire.SessionRequest(h.root, store.Session{Agent: "pj-l-01", ID: "sid-new", Kind: "claude"}))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(wire.Request{Method: wire.MethodSession, Call: call})
	var older struct { // the call as an older obor decodes it
		Call struct {
			Agent   string `json:"agent"`
			Session string `json:"session"`
		} `json:"call"`
	}
	if err := json.Unmarshal(b, &older); err != nil {
		t.Fatalf("an older serve cannot read the new session call %s: %v", b, err)
	}
	if older.Call.Agent != "pj-l-01" || older.Call.Session != "" {
		t.Errorf("an older serve reads agent %q, session %q; want pj-l-01 and no session id (its refusal)",
			older.Call.Agent, older.Call.Session)
	}
}
