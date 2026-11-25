package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// An accept is of the spec rev its candidate was gated with. A green gate of another rev ends it
// (the candidate is reviewed again), even when the patch is the same; the same rev and patch keep it (row 20).

// acceptAt gates T-1 green (kind, spec rev, candidate sha), sends its review job and accepts it.
func acceptAt(t *testing.T, h *harness, f *sched, gate, kind string, rev int, sha string) {
	t.Helper()
	ctx := context.Background()
	addRecord(t, h, "T-1", gate, event.KindIntent, event.GateIn{Kind: kind, Input: "s1"})
	addRecord(t, h, "T-1", gate, event.KindResult, event.GateOut{Outcome: outGreen, SpecRev: rev,
		Candidate: port.Candidate{Task: "T-1", SHA: sha, Target: "base1", Patch: "same-diff"}})
	p := h.a.p
	mt := *p.folded.loop["T-1"]
	if !h.a.job(ctx, p, withAppCfg(h.a, &Loop{Agents: f, Prompter: f}), mt, nth(event.StepReview, mt.reviews+1)) {
		t.Fatal("precondition: the review job was not sent")
	}
	if _, err := h.a.verdict(ctx, wire.VerdictReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-int",
		Role: "integrator", CallID: "accept" + sha}, Verb: wire.MethodAccept, Task: "T-1",
		Job: JobID(p.folded.loop["T-1"].jobKey()), Patch: "same-diff"}); err != nil {
		t.Fatal(err)
	}
	if mt := p.folded.loop["T-1"]; mt.state != stAccepted || mt.accepted != "same-diff" || mt.gateRev != rev {
		t.Fatalf("precondition: T-1 %s accepted %q at rev %d; want accepted same-diff at rev %d", mt.state,
			mt.accepted, mt.gateRev, rev)
	}
}

// countStep is the number of T-1's records of step in the log.
func countStep(t *testing.T, h *harness, step string) int {
	t.Helper()
	recs, err := event.ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Task == "T-1" && r.Step == step {
			n++
		}
	}
	return n
}

// Serve moves the reviewer's and the deep dive's role worktree to the head it hands the role; the reviewer or
// the deep dive working holds no merge. A role's start is not admitted (it
// reads the head once); the publish admission is config_set's only.

// harnessModels are agents members for a config a harness test writes by hand: the models (testkit.Config's) and the
// levels the dispatch tests pick from.
const harnessModels = `"models":{"opus":{"kind":"claude","id":"opus"},"sonnet":{"kind":"claude","id":"sonnet"},` +
	`"haiku":{"kind":"claude","id":"haiku"},"deepseek":{"kind":"opencode","id":"opencode-go/deepseek-v4.1-flash"},` +
	`"terra":{"kind":"codex","id":"gpt-6.1-terra"},"luna":{"kind":"codex","id":"gpt-6-luna"}},` +
	`"difficulty":{"hard":["opus:high"],"medium":["sonnet","deepseek"],"simple":["deepseek","sonnet","haiku"]}`

// admitCfg is seedRoles' config with reviews.dir.
const admitCfg = `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":"","deep_dive":""}},"reviews":{"dir":"reviews"}}`

// seedRoles is seedSched's project with a reviewer (pane w1:rev) and a deep dive (pane w1:dd), both idle in their
// role worktrees, and T-1 accepted.
func seedRoles(t *testing.T) (*harness, *sched) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":"","deep_dive":""}}}`)
	h.snap.Panes = append(h.snap.Panes,
		port.Pane{ID: "w1:rev", Group: "pj", Cwd: filepath.Join(h.wt, "reviewer"), Agent: "claude", Label: "pj-rev", Status: "idle"},
		port.Pane{ID: "w1:dd", Group: "pj", Cwd: filepath.Join(h.wt, "deep_dive"), Agent: "claude", Label: "pj-deep-dive", Status: "idle"})
	f := newSched(h)
	h.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f}), nil
	}
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, event.SubmitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, event.GateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, event.GateOut{Outcome: outConflict})
	acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 1, "candidate1")
	return h, f
}

// work sets pane id working (or idle) as a herdr event reports it.
func (h *harness) work(id string, working bool) {
	h.status(id, func(p *port.Pane) {
		p.Status = map[bool]string{true: "working", false: "idle"}[working]
	})
}

// state is T-1's merge-loop state.
func (h *harness) state(task string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if mt := h.a.p.folded.loop[task]; mt != nil {
		return mt.state
	}
	return ""
}

// phase is obor status' phase of task ("": not listed).
func (h *harness) phase(task string) string {
	for _, ts := range h.a.Status().Project.Tasks {
		if ts.Task == task {
			return ts.Phase
		}
	}
	return ""
}

// set is the owner's config_set of key to val (JSON) from the web.
func (h *harness) set(callID, key, val string) (string, error) {
	return h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: callID}, Changes: changes(key,
		val), Source: "web"})
}

// changes is key, value pairs (the value JSON) as a config_set call's changes.
func changes(kv ...string) []wire.ConfigChange {
	var out []wire.ConfigChange
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, wire.ConfigChange{Key: kv[i], Value: json.RawMessage(kv[i+1])})
	}
	return out
}

// leadLines is the lines to the lead in the log.
func leadLines(t *testing.T, h *harness) []string {
	t.Helper()
	var out []string
	for _, r := range readLog(t, h.root) {
		var m event.Message
		if r.Step == event.StepMessage && json.Unmarshal(r.Data, &m) == nil && m.To == "pj-lead" {
			out = append(out, m.Line)
		}
	}
	return out
}

// withLead gives pj a lead role (pj-lead), an integrator (pj-int) and an advisor (pj-advisor, no lane): every name a
// start row or a recorded session may prove a pane with, lane L-01's worker pj-l-01 included.
func (h *harness) withLead() {
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},`+
		`"lanes":{"count":1},"agents":{"roles":{"integrator":"","lead":"","advisor":""}}}`)
}

// sids makes agent → session each agent's current recorded session in the store (once the project is loaded): put in
// the fold directly, so two agents may hold one id (the tie cases; sessionCall refuses that). A tie is reached the way
// a log can: the owner moves on, the other agent takes the id, and the owner's confirmation of its newer record hands
// its current session back to the id.
func (h *harness) sids(m map[string]string) {
	h.sidMap = m
	h.applySids()
}

func (h *harness) applySids() {
	if h.a.p == nil {
		return
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	f := &h.a.p.folded.sessions
	byID := map[string][]string{}
	for agent, id := range h.sidMap {
		byID[id] = append(byID[id], agent)
	}
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		agents := byID[id]
		slices.Sort(agents)
		for _, agent := range agents {
			if cur, ok := f.Current(agent); ok && cur.ID == id {
				continue
			}
			h.sidSeq++
			rec := store.Session{Agent: agent, Kind: "claude", ID: id, Started: h.sidSeq}
			if f.Add(rec) {
				continue
			}
			owner := agents[0]
			moved := id + "-moved-" + owner
			h.sidSeq++
			f.Add(store.Session{Agent: owner, Kind: "claude", ID: moved, Started: h.sidSeq})
			h.sidSeq++
			f.Add(rec)
			h.sidSeq++
			f.Add(store.Session{Agent: owner, Kind: "claude", ID: moved, Transcript: "/t/moved", Started: 0})
		}
	}
}

// started writes serve's start of agent in pane (terminal term, session sid; "": none) to the log: agent.start's
// intent and result.
func (h *harness) started(agent, pane, term, sid string) {
	in, _ := json.Marshal(event.Start{Lane: "L-01", Agent: agent, Pane: pane})
	res, _ := json.Marshal(event.StartOut{Line: "STARTED " + agent, Agent: agent, Pane: pane, Terminal: term, Kind: "claude",
		Session: sid})
	h.log(event.Record{Task: "T-1", Attempt: 1, Step: event.StepStartWork, Kind: event.KindIntent, Class: event.ClassStart, Data: in},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepStartWork, Kind: event.KindResult, Data: res})
}

// where is whereIs(agent) in pj: "identified <pane> <source>", "ambiguous <panes>", "unavailable <why>".
func (h *harness) where(agent string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	w := h.a.whereIs(h.proj(), agent)
	switch w.State {
	case Identified:
		return "identified " + w.Pane.ID + " " + w.Source
	case Ambiguous:
		return "ambiguous " + strings.Join(w.Panes, " ")
	}
	return "unavailable " + w.Why
}

// who is whoIs of pane id in pj ("": nobody).
func (h *harness) who(id string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	o := h.a.obs[id]
	if o == nil {
		return ""
	}
	agent, _ := h.proj().reg.WhoIs(o.pane)
	return agent
}

// table is the identity table as its lookups give it: whoIs of each pane of h.snap (by id) and whereIs of each agent.
func (h *harness) table(agents ...string) string {
	ids := make([]string, 0, len(h.snap.Panes))
	for _, pane := range h.snap.Panes {
		ids = append(ids, pane.ID)
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s=%s; ", id, h.who(id))
	}
	for _, agent := range agents {
		fmt.Fprintf(&b, "%s: %s; ", agent, h.where(agent))
	}
	return b.String()
}

// move replaces pane id in h.snap by pane to (its terminal kept, as herdr's pane.move) and connects again (herdr's
// pane.move is a refresh: a new snapshot).
func (h *harness) move(id, to string) {
	h.pane(id).ID = to
	h.connect()
}

// add puts pane into h.snap and sends its created event.
func (h *harness) add(pane port.Pane) {
	h.snap.Panes = append(h.snap.Panes, pane)
	eventNow(h.a, port.Event{Kind: port.EventCreated, Pane: pane.ID, New: pane})
}

// twin is a second pane labelled pj-l-01 (w1:p3, terminal t3), no session.
func (h *harness) twin() port.Pane {
	return port.Pane{ID: "w1:p3", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-l-01",
		Terminal: "t3"}
}

func eqs(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// wake sends pj-l-01's work job T-1/1/work (send) through fx; true: sent.
func (h *harness) wake(fx *Loop) bool {
	return h.a.send(context.Background(), h.proj(), fx, mtask{task: "T-1", attempt: 1, lane: "L-01", agent: "pj-l-01"},
		event.Key{Task: "T-1", Attempt: 1, Step: event.StepWork}, "T-1/1/work", promptData{Agent: "pj-l-01"}, wakeText, "work job")
}

// brief records a brief to agent (line "": its start carried it).
func (h *harness) brief(agent, line string) {
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact},
		event.Message{To: agent, From: "pj-lead", Line: line}) {
		h.t.Fatal("brief not recorded")
	}
}

// worked: pj's open brief of agent saw its work.
func (h *harness) worked(agent string) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	b := h.proj().out.Brief(agent)
	return b != nil && b.Worked()
}

// lines is the lines of pj not delivered yet, as "<to>: <line>".
func (h *harness) lines() []string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	var out []string
	for _, q := range h.proj().out.Queued() {
		out = append(out, q.Msg().To+": "+q.Msg().Line)
	}
	return out
}

// kindSpy is a Poster recording each post's kind, agent and line.
type kindSpy struct{ calls []string }

func (s *kindSpy) Post(_ context.Context, kind, agent, line string) (bool, error) {
	s.calls = append(s.calls, kind+" "+agent+": "+line)
	return true, nil
}

// stopWith runs the after-merge stop of pj-l-01 (stopWorker) through ag while lane L-01 holds T-1.
func (h *harness) stopWith(ag port.Launcher) (string, error) {
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	return h.a.stopWorker(context.Background(), p, withAppCfg(h.a, &Loop{Agents: ag}), mtask{task: "T-1", attempt: 1, lane: "L-01",
		agent: "pj-l-01"})
}

// deliverIntents is the number of delivery intents in pj's log.
func (h *harness) deliverIntents() int {
	n := 0
	for _, r := range h.records() {
		if r.Kind == event.KindIntent && strings.HasPrefix(r.Step, outbox.StepDeliver) {
			n++
		}
	}
	return n
}

// queuedLeadThenBrief queues a lead line, then a brief to pj-l-01 (a Codex pane, typed).
func (h *harness) queuedLeadThenBrief() {
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
		event.Message{To: "pj-lead", Line: "[obor] lead line"})
	h.brief("pj-l-01", "[obor] verify")
}

// blockLog builds a case's records: each step appends records of blockTask in the event log's format.
type blockLog struct{ recs []event.Record }

func (b *blockLog) add(attempt int, step, kind string, data any) *blockLog {
	r := event.Record{Task: blockTask, Attempt: attempt, Step: step, Kind: kind}
	if data != nil {
		r.Data, _ = json.Marshal(data)
	}
	b.recs = append(b.recs, r)
	return b
}

func (b *blockLog) fact(attempt int, step string, data any) *blockLog {
	return b.add(attempt, step, event.KindFact, data)
}

// result appends step's intent and its result.
func (b *blockLog) result(attempt int, step string, in, out any) *blockLog {
	return b.add(attempt, step, event.KindIntent, in).add(attempt, step, event.KindResult, out)
}

func (b *blockLog) needs(attempt int, cause, reason string) *blockLog {
	return b.fact(attempt, event.StepNeedsYou, event.FactData{Lane: "L-01", Machine: Machine, Reason: reason, Merge: true,
		Cause: cause})
}

func (b *blockLog) failed(attempt int, step, why string, n int) *blockLog {
	for range n {
		b.fact(attempt, event.StepFailed, event.FactData{Lane: "L-01", Machine: Machine, Reason: step + ": " + why,
			Merge: true, Step: step})
	}
	return b
}

func (b *blockLog) copy() *blockLog { return &blockLog{recs: slices.Clone(b.recs)} }

// dispatched: attempt 1 of blockTask dispatched to L-01, prepared and started, its work job delivered (working).
func dispatched() *blockLog {
	b := &blockLog{}
	b.fact(1, event.StepDispatched, event.Dispatch{Lane: "L-01", Agent: "pj-w1", Reply: "DISPATCHED"})
	b.result(1, event.StepPrepare, nil, event.PrepareOut{Base: "base"})
	b.result(1, event.StepStartWork, nil, event.StartOut{Agent: "pj-w1"})
	b.add(1, event.StepWork, event.KindIntent, nil)
	b.fact(1, event.StepDelivered, event.FactData{Job: JobID(event.Key{Task: blockTask, Attempt: 1, Step: event.StepWork})})
	return b
}

// submitted: attempt n submitted by its worker and pinned; its gate's intent written.
func (b *blockLog) submitted(n int) *blockLog {
	b.fact(n, event.StepSubmitted, event.Submit{Lane: "L-01", Agent: "pj-w1", Report: "Summary: x"})
	b.result(n, event.StepSubmit, nil, event.SubmitOut{SHA: fmt.Sprintf("sha%d", n)})
	return b
}

func (b *blockLog) gate(n int, outcome string) *blockLog {
	out := event.GateOut{Outcome: outcome, Line: "gate " + outcome}
	if outcome == outGreen {
		out.Candidate = port.Candidate{SHA: "cand", Patch: "patch"}
	}
	return b.result(n, event.StepGate, event.GateIn{Kind: port.InputLane, Lane: "L-01"}, out)
}

// review: attempt n green and its review job out.
func (b *blockLog) review(n int) *blockLog {
	return b.gate(n, outGreen).add(n, event.StepReview, event.KindIntent, event.Job{Patch: "patch"})
}

func (b *blockLog) verdict(n int, method, job string, v event.VerdictResult) *blockLog {
	v.Job = JobID(event.Key{Task: blockTask, Attempt: n, Step: job})
	return b.result(n, verdictStep(method, "c"+method), nil, v)
}

func (b *blockLog) accepted(n int) *blockLog {
	return b.review(n).verdict(n, wire.MethodAccept, event.StepReview, event.VerdictResult{Patch: "patch", Reply: "ACCEPTED"})
}

// blockProj replays recs as a log serve opens: written to a new log, then read back and folded.
func blockProj(t *testing.T, recs []event.Record) (*App, *proj) {
	t.Helper()
	root := logRoot(t)
	appendRecs(t, root, recs)
	return blockProjAt(t, root)
}

// appendRecs appends recs to root's log.
func appendRecs(t *testing.T, root string, recs []event.Record) {
	t.Helper()
	l, _ := openLog(t, root)
	for _, r := range recs {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
}

// blockProjAt is serve's project of root's log, replayed.
func blockProjAt(t *testing.T, root string) (*App, *proj) {
	t.Helper()
	l, recs := openLog(t, root)
	a := &App{opt: Options{Now: func() time.Time { return logNow }, Out: io.Discard}}
	p := &proj{root: root, name: "pj", log: l, tasks: map[string]*watch{},
		integrator: "pj-integrator", out: outbox.New("pj-lead", ""), merging: true, timers: map[string]*jobTimer{},
		replies: map[string]string{}, nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		unwritten: map[event.Key]json.RawMessage{}, exec: map[event.Key]time.Time{}, scratch: make(chan struct{}, 1),
		held: map[string]string{}, ended: map[string]bool{}, roleFail: map[string]time.Time{}, reg: identity.New(),
		folded: newFolded()}
	for _, r := range recs {
		p.foldRecord(r)
	}
	a.p = p
	return a, p
}

// placeLane is serve's lane placement on the harness (Loop.PlaceLane): the lane it was asked about, and a new pane's id
// in the lane that the test brings into the snapshot (a created event) as herdr would, or not.
func (h *harness) placeLane(_ context.Context, lane string) (string, error) {
	h.placed = append(h.placed, lane)
	return "w1:p9", nil
}

// proj is pj now (a reload replaces it).
func (h *harness) proj() *proj { return h.a.p }

// step runs T-1's next dispatch step (false: none, or it waits).
func (h *harness) step(t *testing.T, fx *Loop) bool {
	t.Helper()
	p := h.proj()
	h.a.mu.Lock()
	s := h.a.nextDispatch(p, *p.folded.loop["T-1"], loadCfg(t, p.root))
	h.a.mu.Unlock()
	return s != nil && s(context.Background(), fx)
}

// count is how many records of pj's log have kind and step (step "": any step with kind).
func (h *harness) count(kind, step string) int {
	n := 0
	for _, r := range h.records() {
		if r.Kind == kind && (step == "" || r.Step == step) {
			n++
		}
	}
	return n
}

// result is key's result data in pj's log ("": none).
func (h *harness) result(step string) string {
	for _, r := range h.records() {
		if r.Kind == event.KindResult && r.Task == "T-1" && r.Step == step {
			return string(r.Data)
		}
	}
	return ""
}

// refuseOnce makes pj's log refuse the first append of a result of step.
func refuseOnce(p *proj, step string) {
	first := true
	p.log.Refuse = func(r event.Record) error {
		if first && r.Kind == event.KindResult && r.Step == step {
			first = false
			return errors.New("result append failed")
		}
		return nil
	}
}

// mustJSON is v as JSON.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// has: a fact of pj's log contains s.
func (h *harness) has(s string) bool {
	for _, f := range h.facts(0) {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// A green task whose worker's report leaves open questions gets its review job, the questions
// in it: the integrator decides the ones inside the spec's goal and holds the rest for the lead.

// seedQuestions is project pj with an integrator, lane L-01's T-1 submitted with report and its lane gate green.
func seedQuestions(t *testing.T, report string) (*harness, *sched) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},`+
		`"lanes":{"count":1},"agents":{"roles":{"integrator":""}}}`) // L-01: a sendback's lane.sync finds its worktree
	f := newSched(h)
	h.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Kit: "../.."}), nil
	}
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1",
		Report: report})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, event.SubmitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, event.GateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, event.GateOut{Outcome: outGreen, SpecRev: 1,
		Candidate: port.Candidate{Task: "T-1", Lane: "L-01", SHA: "c1", Target: "base1", Patch: "patch1"}})
	if mt := h.a.p.folded.loop["T-1"]; mt.state != stGreen || mt.report != report {
		t.Fatalf("precondition: T-1 %s with report %q; want green with the submitted report", mt.state, mt.report)
	}
	return h, f
}

// pickGreenStep runs the step pickGreen selects for T-1 (live, or after a replay of the log).
func pickGreenStep(t *testing.T, h *harness, f *sched, replay bool) *mtask {
	t.Helper()
	p := h.a.p
	if replay {
		p = reloadProj(t, h)
	}
	s := h.a.pickGreen(p, *p.folded.loop["T-1"])
	if s == nil || !s(context.Background(), withAppCfg(h.a, &Loop{Agents: f, Prompter: f})) {
		t.Fatal("pickGreen: no step, or it failed")
	}
	return p.folded.loop["T-1"]
}

func nextJob(t *testing.T, h *harness, c wire.JobNextReq) wire.JobReply {
	t.Helper()
	raw, err := h.a.jobNext(c)
	if err != nil {
		t.Fatal(err)
	}
	var jr wire.JobReply
	if err := json.Unmarshal([]byte(raw), &jr); err != nil {
		t.Fatal(err)
	}
	return jr
}

// known is pane id as the snapshot has it now (zero: not known).
func known(h *harness, id string) port.Pane {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if o := h.a.obs[id]; o != nil {
		return o.pane
	}
	return port.Pane{}
}

// since is when the no-pane clock of T-1 started (zero: not set).
func since(h *harness) time.Time {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.p.nopane["T-1"]
}

// The fold of a resolution's green gate (live and replayed: a serve restart replays
// these records). A first-gate conflict has no accept, so its resolved candidate goes to its lane test and then to a
// review job (row 19). A moved conflict's earlier accept is kept, unless the spec rev changed.

// resolvedGate folds a resolved gate's intent and green result under gate: the task's own patch, green at rev.
func (b *blockLog) resolvedGate(gate string, rev int) *blockLog {
	return b.add(1, gate, event.KindIntent, event.GateIn{Kind: port.InputResolved, Lane: "L-01"}).
		result(1, gate, nil, event.GateOut{Outcome: outGreen, SpecRev: rev,
			Candidate: port.Candidate{Task: blockTask, SHA: "candidate2", Target: "base2", Base: "base", Patch: "diff"}})
}

// pass is merge loop passes until no more scratch effects start (each started one has folded): what the loop
// goroutine, woken by each effect's end, does.
func (h *harness) pass(ctx context.Context) {
	for range 16 {
		h.a.mu.Lock()
		n := h.a.scratched
		h.a.mu.Unlock()
		h.a.advance(ctx)
		h.a.effects.Wait()
		h.a.mu.Lock()
		more := h.a.scratched != n
		h.a.mu.Unlock()
		if !more {
			return
		}
	}
	h.t.Fatal("merge loop passes never settled")
}

// reloadProj replays the log, as a serve restart does.
func reloadProj(t *testing.T, h *harness) *proj {
	t.Helper()
	p, err := h.a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	h.a.p = p
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p) // as loadProject does
	return p
}

// session reports agent's session id (transcript tr, started at) to h's serve as the SessionStart hook does.
func (h *harness) session(agent, id, tr string, at int64) string {
	h.t.Helper()
	reply, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: agent, Kind: "claude", ID: id, Transcript: tr,
		Pane: "w1:p1", Lane: "L-01", Task: "T-1", Started: at, Source: "SessionStart hook"}))
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
	for _, s := range h.proj().folded.sessions.All() {
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
	_, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: agent, Kind: "codex", ID: id, Started: at,
		Source: "codex transcript"}))
	if err == nil {
		return ""
	}
	if !strings.Contains(err.Error(), refusedMark) {
		h.t.Fatalf("an error that is not a refusal: %v", err)
	}
	return err.Error()
}

// waitCall is the lead's dispatch of T-1 that must wait.
func (h *harness) waitCall(c wire.DispatchReq) {
	h.t.Helper()
	c.Root, c.Task, c.Role = h.root, "T-1", "lead"
	if reply, err := h.a.dispatchCall(context.Background(), c); err != nil || !strings.HasPrefix(reply, "WAITING") {
		h.t.Fatalf("dispatch %s: %q, %v; want WAITING", c.CallID, reply, err)
	}
}

// selected is T-1's next step now (App.next, with its begin/release wrapper).
func (h *harness) selected() step {
	h.t.Helper()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.next(h.a.p, h.a.p.folded.loop["T-1"], cfgAt(h.a.p.root))
}

func (h *harness) freeLanes() {
	for i := range h.lanes.lanes {
		h.lanes.lanes[i].State, h.lanes.lanes[i].Task = "free", ""
	}
}

// fact records a fact of task (folded).
func (h *harness) fact(task, step string, data any) {
	h.t.Helper()
	if !h.a.record(h.a.p, event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindFact}, data) {
		h.t.Fatalf("fact %s of %s refused", step, task)
	}
}

func (h *harness) t1() mtask {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return *h.a.p.folded.loop["T-1"]
}

// newTestApp is New with the test's own scripted activity and screen: a package test's app reads Busy, Activity and
// Turn from its fake panes (activity.go) and a blocked pane's question from their Screen, as the test scripts them.
func newTestApp(opt Options, panes *fakePanes, lanes *fakeLanes) *App {
	if opt.Activity == nil {
		opt.Activity = panes
	}
	if opt.Screen == nil {
		opt.Screen = panes.Screen
	}
	return New(opt, panes, lanes)
}

// The watchdogs' case table: each row is (prior state, event or tick, evidence) → the facts and notifications that
// follow. The core runs without Serve: the row calls connect, event and check itself on a clock it moves, over an
// in-memory herdr (fakePanes) and lane table. setup builds the prior state; the event is the first action after it.

type fakePanes struct {
	busy      bool
	busyErr   error
	last      time.Time // the transcript's last write (zero: unknown)
	screen    string    // what Screen shows
	screenErr error
	screens   []string // the panes Screen was asked about
	notifyFn  func() error
	notes     []string
	asked     []string // the panes Busy was asked about
	turn      agents.Turn
	turned    bool // Turn knows turn
	// Snapshot's answer: snap when an error does not come first (nil snap, nil err: "not in the table", as for the
	// table's rows); after runs once the answer is read, before it returns (a stream event landing in between);
	// snaps counts the calls.
	snap    *port.Snapshot
	snapErr error
	after   func()
	snaps   int
}

func (f *fakePanes) Subscribe(context.Context) (*port.Stream, error) {
	return nil, errors.New("not in the table")
}

// Snapshot is the read a lane's start makes again (rereadPanes): snap's panes, unless the context or snapErr says
// otherwise; a row that never set snap gets the error Subscribe gives.
func (f *fakePanes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	f.snaps++
	if err := ctx.Err(); err != nil {
		return port.Snapshot{}, err
	}
	if f.snapErr != nil {
		return port.Snapshot{}, f.snapErr
	}
	if f.snap == nil {
		return port.Snapshot{}, errors.New("not in the table")
	}
	snap := *f.snap
	snap.Panes = append([]port.Pane(nil), f.snap.Panes...)
	if f.after != nil {
		f.after()
	}
	return snap, nil
}

func (f *fakePanes) Busy(_ context.Context, pane port.Pane) (bool, error) {
	f.asked = append(f.asked, pane.ID)
	return f.busy, f.busyErr
}

func (f *fakePanes) Activity(agents.Kind, port.Pane) (time.Time, bool) {
	return f.last, !f.last.IsZero()
}

// Transcript is the path the test's pane reports, if any (the fake panes have no session id: the file's lookup is
// agentFiles').
func (f *fakePanes) Transcript(_ agents.Kind, pane port.Pane) string { return pane.Transcript }

// Screen is what the pane shows, its last lines raw (lines is honoured, as herdr's pane.read does): the question a
// blocked pane shows sits above the TUI's blank rows and footer.
func (f *fakePanes) Screen(_ context.Context, pane string, lines int) (string, error) {
	f.screens = append(f.screens, pane)
	text := f.screen
	if lines > 0 {
		raw := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if len(raw) > lines {
			raw = raw[len(raw)-lines:]
		}
		text = strings.Join(raw, "\n")
	}
	return text, f.screenErr
}

func (f *fakePanes) Turn(agents.Kind, port.Pane, string) (agents.Turn, bool) {
	return f.turn, f.turned
}

func (f *fakePanes) Notify(_ context.Context, title, body string) error {
	if f.notifyFn != nil {
		if err := f.notifyFn(); err != nil {
			return err
		}
	}
	f.notes = append(f.notes, title+": "+body)
	return nil
}

// fakeLanes is the lane table; err, when set, fails the whole read instead. It counts its scans (each one is git per
// lane in serve); when gate is set a scan signals entered and waits for gate to close.
type fakeLanes struct {
	mu      sync.Mutex
	lanes   []port.Lane
	err     error
	scanned int
	gate    chan struct{}
	entered chan struct{}
}

func (l *fakeLanes) Lanes(_ context.Context, _ string) ([]port.Lane, error) {
	l.mu.Lock()
	l.scanned++
	gate, entered := l.gate, l.entered
	l.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	if l.err != nil {
		return nil, l.err
	}
	return append([]port.Lane(nil), l.lanes...), nil
}

// scans is how many times the table was read.
func (l *fakeLanes) scans() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scanned
}

// harness is project pj (workspace w1) with lane L-01 holding T-1, its pane w1:p1 an idle claude; not connected.
type harness struct {
	t        *testing.T
	a        *App
	panes    *fakePanes
	lanes    *fakeLanes
	now      time.Time
	root, wt string
	snap     port.Snapshot     // herdr's session now
	sidMap   map[string]string // sids: agent → session id, put in the store (applySids)
	sidSeq   int64
	placed   []string // the lanes serve placed a pane for (Loop.PlaceLane, placeLane)
}

func newHarness(t *testing.T) *harness {
	base := t.TempDir()
	h := &harness{t: t, panes: &fakePanes{}, lanes: &fakeLanes{}, now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		root: filepath.Join(base, "pj"), wt: filepath.Join(base, "pj-wt")}
	if err := os.MkdirAll(filepath.Join(h.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	h.lanes.lanes = []port.Lane{{Name: "L-01", Dir: h.lane("L-01"), Task: "T-1"}}
	h.snap = port.Snapshot{Panes: []port.Pane{{ID: "w1:p1", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle"}}}
	h.a = newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	return h
}

func (h *harness) write(path, text string) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) lane(name string) string { return filepath.Join(h.wt, name) }

// pane is pane id of h.snap (to change it before a connect, or to build an event from).
func (h *harness) pane(id string) *port.Pane {
	for i := range h.snap.Panes {
		if h.snap.Panes[i].ID == id {
			return &h.snap.Panes[i]
		}
	}
	h.t.Fatalf("no pane %s", id)
	return nil
}

// setLane puts task on the lane; done: its task_done is in (fact submitted in the loaded project's log).
func (h *harness) setLane(task string, done bool) {
	h.lanes.lanes[0].Task = task
	if !done {
		return
	}
	if h.a.p != nil {
		if _, err := Append(h.a, h.root, event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted,
			Kind: event.KindFact, Data: []byte(`{"lane":"L-01"}`)}, h.a.opt.Now()); err != nil {
			h.t.Fatal(err)
		}
	}
}

// connect is a subscription: load the project, then work from h.snap.
func (h *harness) connect() {
	h.a.loadProject()
	h.applySids()
	snap := h.snap
	snap.Panes = append([]port.Pane(nil), h.snap.Panes...)
	h.a.connect(context.Background(), snap, false)
}

// tick moves the clock by d and runs one watchdog tick.
func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.a.loadProject()
	h.a.check(context.Background(), true)
}

// status changes pane id in h.snap and sends the status event that carries it.
func (h *harness) status(id string, fn func(p *port.Pane)) {
	p := h.pane(id)
	fn(p)
	eventNow(h.a, port.Event{Kind: port.EventStatus, Pane: id, Status: p.Status, Agent: p.Agent, New: *p})
}

// remove drops pane id from h.snap and sends kind (closed or exited).
func (h *harness) remove(id, kind string) {
	var keep []port.Pane
	for _, p := range h.snap.Panes {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	h.snap.Panes = keep
	eventNow(h.a, port.Event{Kind: kind, Pane: id})
}

// log writes prior records to pj's log (a crash left them).
func (h *harness) log(recs ...event.Record) {
	if err := WriteLog(h.root, h.now, recs...); err != nil {
		h.t.Fatal(err)
	}
}

// records is pj's log.
func (h *harness) records() []event.Record {
	h.t.Helper()
	recs, err := event.ReadLog(h.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return recs
}

// facts is pj's facts from seq on, as "task step{reason}".
func (h *harness) facts(from int64) []string {
	var out []string
	for _, r := range h.records() {
		if r.Kind == event.KindFact && r.Seq > from {
			out = append(out, r.Task+" "+r.String())
		}
	}
	return out
}

func (h *harness) lastSeq() int64 {
	recs := h.records()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

type row struct {
	name  string
	setup func(h *harness) // the prior state (nil: connect)
	event func(h *harness) // the event or tick the row is about
	facts []string         // the facts the event records
	then  func(h *harness) // later steps (nil: none)
	later []string         // the facts recorded from the event through then
	notes int              // the notifications shown from the event on
	check func(t *testing.T, h *harness)
}

func connect(h *harness) { h.connect() }

func tick(d time.Duration) func(h *harness) { return func(h *harness) { h.tick(d) } }

func steps(fs ...func(h *harness)) func(h *harness) {
	return func(h *harness) {
		for _, f := range fs {
			f(h)
		}
	}
}

const minute = time.Minute

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// The work job carries the worker's role texts, so the worker reads them once (in the job) instead of spending
// turns on the files, and a role text too big to carry is named for the worker to read.

// roleFixture is a project root whose config puts the agents docs in doc/roles, and the obor kit it is dispatched from:
// a worker prompt template and the kit's worker role text (kitRole).
func roleFixture(t *testing.T, kitRole, doc string) (*proj, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":2,"project":{"name":"pj"},"agents":{"docs_dir":"doc/roles"}}`
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc != "" {
		writeRoleFile(t, filepath.Join(root, "doc/roles/worker.md"), doc)
	}
	kit := t.TempDir()
	writeRoleFile(t, filepath.Join(kit, "templates", "worker-prompt.txt"), "Implement {{task}} on {{base}}.\n"+
		"Report: {{done_sections}}.\n")
	writeRoleFile(t, filepath.Join(kit, "templates", "agents", "worker.md"), kitRole)
	return &proj{root: root}, kit
}

// writeRoleFile writes text to path, creating its directories.
func writeRoleFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
