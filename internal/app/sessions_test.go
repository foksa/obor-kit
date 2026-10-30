package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/project"
	"obor/internal/store"
)

// session reports agent's session id (transcript tr, started at) to h's serve as the SessionStart hook does.
func (h *harness) session(agent, id, tr string, at int64) string {
	h.t.Helper()
	reply, err := h.a.sessionCall(Call{Root: h.root, Agent: agent, Kind: "claude", Session: id, Transcript: tr,
		Pane: "w1:p1", Lane: "O-01", Task: "T-1", Started: at, Source: "SessionStart hook"})
	if err != nil {
		h.t.Fatal(err)
	}
	return reply
}

// sessions is pj's recorded sessions of agent, as "<id> <transcript>".
func (h *harness) sessions(agent string) []string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	var out []string
	for _, s := range h.proj().sessions {
		if s.Agent == agent {
			out = append(out, s.ID+" "+s.Transcript)
		}
	}
	return out
}

// current is pj's current session of agent ("": none).
func (h *harness) current(agent string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	s, _ := h.proj().session(agent)
	return s.ID
}

// T-577: a Claude start records one session fact with its id and transcript; a second start of the agent a second
// fact, the newest its current one; a repeat of a session records none; a replay folds the same; the store's readers
// (launch, the hooks) read the same facts.
func TestSessionFacts(t *testing.T) {
	h, _ := newIdentHarness(t)
	h.connect()
	if got := h.sessions("pj-o-01"); len(got) != 0 {
		t.Fatalf("precondition: no session recorded, got %q", got)
	}
	now := time.Now().Unix()
	eqs(t, h.session("pj-o-01", "s1", "/t/s1.jsonl", now-60), "SESSION pj-o-01 s1", "reply")
	eqs(t, strings.Join(h.sessions("pj-o-01"), ","), "s1 /t/s1.jsonl", "one fact, id and transcript")
	eqs(t, h.current("pj-o-01"), "s1", "current")

	h.session("pj-o-01", "s2", "/t/s2.jsonl", now)
	eqs(t, strings.Join(h.sessions("pj-o-01"), ","), "s1 /t/s1.jsonl,s2 /t/s2.jsonl", "a second start: a second fact")
	eqs(t, h.current("pj-o-01"), "s2", "current = newest")

	eqs(t, h.session("pj-o-01", "s1", "/t/s1.jsonl", now+60), "SESSION pj-o-01 s1", "a repeat's reply")
	eqs(t, strings.Join(h.sessions("pj-o-01"), ","), "s1 /t/s1.jsonl,s2 /t/s2.jsonl", "a repeat (a resume): no fact")
	eqs(t, h.current("pj-o-01"), "s2", "current unchanged by the repeat")

	ss, err := store.ReadSessionsAt(h.root)
	if err != nil || len(ss) != 2 {
		t.Fatalf("store: %d sessions (%v), want 2", len(ss), err)
	}
	if s, ok := store.Current(ss, "pj-o-01"); !ok || s.ID != "s2" || s.Transcript != "/t/s2.jsonl" || s.Lane != "O-01" ||
		s.Task != "T-1" || s.Pane != "w1:p1" || s.Kind != "claude" || s.Started != now {
		t.Errorf("store's current %+v, want s2 with its fields", s)
	}
	eqs(t, store.TranscriptAt(h.root, "s1"), "/t/s1.jsonl", "transcript by id")

	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	h.connect()
	eqs(t, strings.Join(h.sessions("pj-o-01"), ","), "s1 /t/s1.jsonl,s2 /t/s2.jsonl", "replay: the same facts")
	eqs(t, h.current("pj-o-01"), "s2", "replay: current")
}

// T-577: serve down at SessionStart: the hook's .sid (Options.Recorded) of an agent the config knows is imported
// when serve loads the project, once; a .sid of a session already recorded is not imported again.
func TestSessionSidImported(t *testing.T) {
	h, out := newIdentHarness(t)
	h.withLead()
	at := time.Now().Add(-time.Minute).Unix()
	recorded := func(agent string) (store.Session, bool) {
		if agent != "pj-lead" {
			return store.Session{}, false
		}
		return store.Session{ID: "s-file", Transcript: "/t/s-file.jsonl", Started: at, Source: "sid file"}, true
	}
	h.a.opt.Recorded = recorded
	h.connect()
	eqs(t, strings.Join(h.sessions("pj-lead"), ","), "s-file /t/s-file.jsonl", "imported")
	eqs(t, h.current("pj-lead"), "s-file", "current")
	if !strings.Contains(out.String(), "pj-lead's session s-file imported from its .sid file") {
		t.Errorf("import not logged:\n%s", out)
	}

	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out, Recorded: recorded}, h.panes, h.lanes)
	h.connect()
	eqs(t, strings.Join(h.sessions("pj-lead"), ","), "s-file /t/s-file.jsonl", "a second load: not imported again")
}

// T-577: the identity reads the recorded session first: a pane whose herdr session is the record is identified by
// session; one whose herdr session differs is still identified by its label, or its start, as before, and the
// difference is logged once (not adopted).
func TestSessionIdentityCrossCheck(t *testing.T) {
	t.Run("label", func(t *testing.T) {
		h, out := newIdentHarness(t)
		h.sids(map[string]string{"pj-o-01": "s-file"}) // the .sid: the fallback, not read while a session is recorded
		h.connect()
		h.session("pj-o-01", "s1", "", time.Now().Unix())
		h.connect()
		eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "herdr's session is the record")

		h.status("w1:p1", func(p *Pane) { p.Session = "s-herdr" })
		eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "a differing herdr session: by label")
		h.status("w1:p1", func(p *Pane) { p.Status = "working" })
		const line = "herdr reports session s-herdr for pj-o-01 at pane w1:p1; its recorded session is s1 (kept)"
		if n := strings.Count(out.String(), line); n != 1 {
			t.Errorf("logged %d times, want once:\n%s", n, out)
		}
		eqs(t, h.current("pj-o-01"), "s1", "not adopted")
	})
	t.Run("start", func(t *testing.T) {
		h, out := newIdentHarness(t)
		h.pane("w1:p1").Label, h.pane("w1:p1").Session = "", "s-herdr"
		h.started("pj-o-01", "w1:p1", "t1", "")
		h.connect()
		h.session("pj-o-01", "s1", "", time.Now().Unix())
		h.connect()
		eqs(t, h.where("pj-o-01"), "identified w1:p1 start", "a differing herdr session: by start")
		if !strings.Contains(out.String(), "herdr reports session s-herdr for pj-o-01 at pane w1:p1; its recorded session is s1 (kept)") {
			t.Errorf("not logged:\n%s", out)
		}
	})
}

// T-577: obor status shows an identified agent's current session: its id, transcript and start.
func TestSessionInStatus(t *testing.T) {
	h, _ := newIdentHarness(t)
	h.connect()
	now := time.Now().Unix()
	h.session("pj-o-01", "s1", "/t/s1.jsonl", now)
	h.connect()
	st := h.a.Status()
	var got *AgentStatus
	for i, a := range st.Project.Agents {
		if a.Pane == "w1:p1" {
			got = &st.Project.Agents[i]
		}
	}
	if got == nil || got.Name != "pj-o-01" {
		t.Fatalf("pj-o-01 not in the status: %+v", st.Project.Agents)
	}
	if got.Session != "s1" || got.Transcript != "/t/s1.jsonl" || got.Started != now {
		t.Errorf("status session %q %q %d, want s1 /t/s1.jsonl %d", got.Session, got.Transcript, got.Started, now)
	}
}

// has fails when s lacks sub.
func has(t *testing.T, s, sub, what string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: %q lacks %q", what, s, sub)
	}
}

// refused reports agent's session id to h's serve: the error ("" none).
func (h *harness) refused(agent, id string, at int64) string {
	h.t.Helper()
	_, err := h.a.sessionCall(Call{Root: h.root, Agent: agent, Kind: "codex", Session: id, Started: at, Source: "codex transcript"})
	if err == nil {
		return ""
	}
	if !SessionRefused(err) {
		h.t.Fatalf("an error that is not a refusal: %v", err)
	}
	return err.Error()
}

// T-586: one session id belongs to one agent: an id that is another agent's current session is refused (nothing
// recorded, both agents in serve's log, the first agent unchanged); a repeat by its owner is one fact; a log holding
// such a duplicate replays with one owner; the registry-failure shape (two starts, one rollout) records the id for
// the agent that reported it first only.
func TestSessionOneOwner(t *testing.T) {
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
		if !h2.a.record(p, Record{Task: store.SessionTask, Step: store.StepSession, Kind: KindFact},
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

		// T-588: the store's readers apply the same rule (store.Admits): the owner's only.
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
		h3, _ := newIdentHarness(t)
		h3.connect()
		// both searches found the first start's rollout: the true owner reports it first, the other after
		eqs(t, h3.refused("pj-c", "r1", now), "", "the first report is recorded")
		has(t, h3.refused("pj-d", "r1", now+1), "pj-c", "the second start's report of r1 is refused")
		eqs(t, h3.current("pj-c"), "r1", "the true owner records it")
		eqs(t, h3.current("pj-d"), "", "the other does not")
	})
}
