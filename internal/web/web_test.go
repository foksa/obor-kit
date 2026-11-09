package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
	"obor/internal/view"
)

// TestMain isolates the tests and removes the project template New copies (T-1009).
func TestMain(m *testing.M) { testkit.Main(m) }

// fixture is one project with a store and serve's answer, the view built from both (as obor status builds it) and a
// server over that one project. The store holds: T-01's dispatch, submission (a report), spec and a green gate; a job's
// prompt text; an agent's session (with a transcript file), a brief and a line; T-02 waits (a dispatch waiting after
// T-01). Serve's answer says T-01 works on L-01 with its job fetched, and holds one needs-you.
func fixture(t *testing.T) (*testkit.Project, view.Project, *Server) {
	t.Helper()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Spec("T-01", "# T-01 — a task title\n\nDifficulty: simple\n")
	p.Dispatched("L-01")
	p.Submit("L-01", "Summary: built it.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none.")
	addEvent(t, p.Root, "T-01", event.StepGate, event.KindResult, event.ClassGit, map[string]any{
		"outcome": "green", "line": "GREEN T-01 (ok 212 tests, 71s)", "spec_rev": 1,
		"candidate": map[string]any{"sha": "cafe1234", "checks": "212 tests"}})
	transcript := p.Write("transcript.jsonl", `{"type":"user"}`+"\n")
	now := time.Now()
	addEvent(t, p.Root, store.SessionTask, store.StepSession, event.KindFact, event.ClassFact, store.Session{
		Agent: "pj-l1", Kind: "claude", ID: "sid-1", Transcript: transcript, Pane: "w1:p1", Lane: "L-01", Task: "T-01",
		Started: now.Add(-time.Hour).Unix(), Source: "hook"})
	addEvent(t, p.Root, event.MsgTask, event.StepMessage, event.KindFact, event.ClassFact, event.Message{
		To: "pj-l1", From: "pj-lead", Line: "[obor pj-lead] work on T-01"})
	writePrompt(t, p.Root, store.PromptRow{Agent: "pj-l1", Kind: store.PromptJob, Job: "T-01/1/work", Task: "T-01",
		TS: now.UTC().Format(time.RFC3339), Text: "the whole job prompt"})
	writePrompt(t, p.Root, store.PromptRow{Agent: "pj-l1", Kind: store.PromptBrief, TS: now.UTC().Format(time.RFC3339),
		Text: "the brief's line"})
	ss := wire.Status{Since: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true,
			Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l1", Agent: "claude", Lane: "L-01", Task: "T-01",
				Status: "working", Session: "sid-1"}},
			Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "working", Job: "T-01/1/work",
				Delivered: true}, {Task: "T-02", Attempt: 1, State: "waiting", Waiting: true, Rank: 1,
				After: []string{"T-01"}, Why: "dispatch T-02: no free lane"}},
			Facts:   []event.Record{fact(t, 1, "T-01", event.FactData{Lane: "L-01", Reason: "asks: may I edit x.go?"})},
			Answers: map[int64]string{1: `obor resume T-01 "<answer>"`}}}
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: now, Events: 20})
	if !want.Serve.Up || len(want.NeedsYou) != 1 || len(want.Lanes) != 1 {
		t.Fatalf("precondition: serve up, one needs-you, one lane: %+v", want)
	}
	// T-803: the idle state is part of the view the page draws.
	want.Idle = &view.Idle{Since: now.Unix(), Last: "T-1 merged abc1234", Acked: true, Why: "nothing to dispatch"}
	s := &Server{
		Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: p.Root}} },
		Build:    func(context.Context, string) (view.Project, error) { return want, nil },
		Task: func(context.Context, string) (view.Detail, error) {
			return view.Detail{Root: p.Root, Project: want}, nil
		},
		Agent: func(context.Context, string) (view.Detail, error) {
			return view.Detail{Root: p.Root, Project: want}, nil
		},
		Events: 20,
	}
	return p, want, s
}

// fact is a needs-you record of task, as serve's status lists it.
func fact(t *testing.T, seq int64, task string, d event.FactData) event.Record {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Seq: seq, Task: task, Step: event.StepNeedsYou, Kind: event.KindFact, Data: b}
}

// addEvent appends one record to the project's store, as serve does (the next seq, now).
func addEvent(t *testing.T, root, task, step, kind, class string, data any) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339), task, step, kind, class, string(b)); err != nil {
		t.Fatal(err)
	}
}

// writePrompt stores one prompts row, as serve does when it sends a job, a brief or a start.
func writePrompt(t *testing.T, root string, r store.PromptRow) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var seq int64
	if err := db.QueryRow("SELECT COALESCE(MAX(seq), 0) + 1 FROM prompts").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	r.Seq = seq
	if err := store.WritePrompt(db, r); err != nil {
		t.Fatal(err)
	}
}

// newestSeq is the store's newest record's seq.
func newestSeq(t *testing.T, root string) int64 {
	t.Helper()
	db, err := store.OpenRO(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recs, err := event.RecentEvents(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("precondition: the store has no records")
	}
	return recs[0].Seq
}

// get runs one GET on the handler.
func get(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	return w
}

// decode reads r's body into v.
func decode(t *testing.T, r *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", r.Body.String(), err)
	}
}

// TestViewAPIIsTheViewsJSON: /api/view answers with the project's view, field for field the struct view.Build built
// (what `obor status --json` prints), and /api/projects sums it (the needs-you count, the queue's length).
func TestViewAPIIsTheViewsJSON(t *testing.T) {
	_, want, s := fixture(t)
	h := s.Handler()

	r := get(t, h, "/api/view?project=pj")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/view: %d %s", r.Code, r.Body.String())
	}
	var got, expect any
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatalf("view JSON: %v\n%s", err, r.Body.String())
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &expect); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expect) {
		t.Errorf("/api/view is not the view:\n got %s\nwant %s", r.Body.String(), b)
	}
	var idle view.Project
	decode(t, r, &idle)
	if idle.Idle == nil || !idle.Idle.Acked || idle.Idle.Why != "nothing to dispatch" || idle.Idle.Last != "T-1 merged abc1234" {
		t.Errorf("/api/view idle %+v, want the acknowledged state", idle.Idle)
	}

	r = get(t, h, "/api/projects")
	var entries []ProjectsEntry
	decode(t, r, &entries)
	if len(entries) != 1 {
		t.Fatalf("/api/projects: %+v, want one project", entries)
	}
	if e := entries[0]; e.Name != "pj" || e.NeedsYou != len(want.NeedsYou) ||
		e.Queue != len(want.Queue.Merge)+len(want.Queue.Waiting) {
		t.Errorf("/api/projects: %+v, want pj with %d needs-you and %d queued", e, len(want.NeedsYou),
			len(want.Queue.Merge)+len(want.Queue.Waiting))
	}
	// T-675: the landing's row says whether serve is up and how many lanes are busy (L-01 works on T-01) and free.
	if e := entries[0]; !e.Up || e.Busy != 1 || e.Free != 0 || e.Running == nil {
		t.Errorf("/api/projects: up %v, busy %d, free %d, running %v; want up, 1 busy, 0 free, a running list", e.Up,
			e.Busy, e.Free, e.Running)
	}
}

// TestSetupAPI (T-750, T-765): /api/setup answers with the project's setup read model — the three difficulty levels,
// the model refs each names with what runs it, the lane with its state, the roles the config names, the models — and
// counts the fixture's bare dispatches (records from before T-721) as unknown (every lane is pooled).
func TestSetupAPI(t *testing.T) {
	_, _, s := fixture(t)
	r := get(t, s.Handler(), "/api/setup?project=pj")
	if r.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", r.Code, r.Body.String())
	}
	var setup view.Setup
	decode(t, r, &setup)
	if len(setup.Difficulty) != 3 || setup.Difficulty[0].Level != "hard" || setup.Difficulty[2].Level != "simple" {
		t.Fatalf("difficulty %+v, want hard, medium and simple", setup.Difficulty)
	}
	if len(setup.Lanes) != 1 || setup.Lanes[0].Lane != "L-01" || setup.Lanes[0].Agent != "pj-l-01" ||
		setup.Lanes[0].State != "working" {
		t.Errorf("lanes %+v, want L-01, working, pj-l-01", setup.Lanes)
	}
	roles := map[string]view.SetupRole{}
	for _, role := range setup.Roles {
		roles[role.Role] = role
	}
	if _, ok := roles["lead"]; !ok || roles["reviewer"].Agent != "pj-rev" || roles["integrator"].Agent != "pj-int" {
		t.Errorf("roles %+v, want the lead's, the integrator's and the reviewer's rows", setup.Roles)
	}
	if want := []view.DispatchCount{{Level: "", Type: "", Recent: 2, All: 2}}; !slices.Equal(setup.Distribution, want) {
		t.Errorf("distribution %+v, want the fixture's two dispatches, unknown", setup.Distribution)
	}
	// the built-in levels' refs, in level order, each once
	var refs []string
	for _, ty := range setup.Types {
		refs = append(refs, ty.Ref)
	}
	if want := []string{"opus:high", "sonnet", "deepseek", "haiku"}; !slices.Equal(refs, want) {
		t.Errorf("types %+v, want %q", setup.Types, want)
	}
	if len(setup.Models) != 8 || setup.Models[0].Name != "opus" || !setup.Models[0].Builtin {
		t.Errorf("models %+v, want the eight built-in ones", setup.Models)
	}
}

// TestTaskPage: a task's page carries serve's state, its spec rev and title, the report of its last submission, the
// gate's line (no log path: the RED line names the real one, T-1095), its needs-you with
// the answer commands, its records as a timeline and its jobs' text.
func TestTaskPage(t *testing.T) {
	_, _, s := fixture(t)
	r := get(t, s.Handler(), "/api/task?project=pj&task=T-01")
	if r.Code != http.StatusOK {
		t.Fatalf("task: %d %s", r.Code, r.Body.String())
	}
	var d view.TaskDetail
	decode(t, r, &d)
	if d.TaskState != "working" || d.Attempt != 1 || d.Lane != "L-01" || d.Agent != "pj-l1" {
		t.Errorf("task %+v: want working, attempt 1, lane L-01, agent pj-l1", d)
	}
	if d.SpecRev != 1 || d.Title != "a task title" {
		t.Errorf("spec: rev %d, title %q, want rev 1 and \"a task title\"", d.SpecRev, d.Title)
	}
	if !strings.Contains(d.Report, "Summary: built it.") {
		t.Errorf("report %q, want the submission's", d.Report)
	}
	if d.GateLine != "GREEN T-01 (ok 212 tests, 71s)" || d.GatedRev != 1 {
		t.Errorf("gate: line %q, rev %d, want the green gate's", d.GateLine, d.GatedRev)
	}
	if len(d.NeedsYou) != 1 || d.NeedsYou[0].Answer != `obor resume T-01 "<answer>"` {
		t.Errorf("needs-you %+v, want serve's answer commands", d.NeedsYou)
	}
	if len(d.Records) == 0 || d.Records[0].Kind == "" {
		t.Fatalf("records %+v, want the task's timeline", d.Records)
	}
	if d.Records[0].Seq < d.Records[len(d.Records)-1].Seq {
		t.Errorf("records are not newest first: %+v", d.Records)
	}
	if len(d.Jobs) != 1 || d.Jobs[0].Text != "the whole job prompt" || d.Jobs[0].Job != "T-01/1/work" {
		t.Errorf("jobs %+v, want the job's prompt text", d.Jobs)
	}
}

// TestJobTextNeedsNoView (T-687): /api/job answers with the prompts row (agent, task, time and the whole text) from the
// project's store alone: a view that cannot be built is no error (the job's page needs neither the lane table, git nor
// serve's ask).
func TestJobTextNeedsNoView(t *testing.T) {
	_, _, s := fixture(t)
	built := false
	s.Build = func(context.Context, string) (view.Project, error) {
		built = true
		return view.Project{}, errors.New("no view")
	}
	r := get(t, s.Handler(), "/api/job?project=pj&job=T-01/1/work")
	if r.Code != http.StatusOK {
		t.Fatalf("job: %d %s", r.Code, r.Body.String())
	}
	var p view.Prompt
	decode(t, r, &p)
	if p.Text != "the whole job prompt" || p.Agent != "pj-l1" || p.Task != "T-01" || p.Kind != store.PromptJob {
		t.Errorf("job %+v, want the prompts row's text, agent, task and kind", p)
	}
	if built {
		t.Errorf("/api/job built the project's view")
	}
}

// T-680 (RV-76 #2): a role on no lane has an agent page too: the detail joins the matching role row, so a stopped role
// is found with its status and its history, and a role with no history is not a 404; an active agent's lane, task and
// status keep precedence.
func TestAgentPageOfRoles(t *testing.T) {
	p, _, s := fixture(t)
	p.Config(testkit.Set("agents.roles.reviewer", "opus"),
		testkit.Set("agents.roles.advisor", "opus"))
	// the reviewer was sent a line (its history); the advisor has none.
	addEvent(t, p.Root, event.MsgTask, event.StepMessage, event.KindFact, event.ClassFact, event.Message{
		To: "pj-rev", From: "pj-lead", Line: "[obor pj-lead] review T-01"})
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
			Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l1", Agent: "claude", Lane: "L-01", Task: "T-01",
				Status: "working"}},
			Stopped: []string{"pj-rev", "pj-advisor"}}}
	v := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: now, Events: 20})
	roles := map[string]view.Role{}
	for _, r := range v.Roles {
		roles[r.Agent] = r
	}
	if r := roles["pj-rev"]; r.Status != view.RoleStopped {
		t.Fatalf("precondition: the reviewer's role row says %q, want stopped: %+v", r.Status, v.Roles)
	}
	if r, ok := roles["pj-advisor"]; !ok || r.Status != view.RoleStopped {
		t.Fatalf("precondition: the advisor's role row says stopped: %+v, %v", r, ok)
	}
	s.Agent = func(context.Context, string) (view.Detail, error) { return view.Detail{Root: p.Root, Project: v}, nil }
	h := s.Handler()
	for _, c := range []struct {
		agent string
		lines int
	}{{"pj-rev", 1}, {"pj-advisor", 0}} {
		r := get(t, h, "/api/agent?project=pj&agent="+c.agent)
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.agent, r.Code, r.Body.String())
		}
		var a view.AgentDetail
		decode(t, r, &a)
		if a.Status != view.RoleStopped {
			t.Errorf("%s: status %q, want %q (its role row's)", c.agent, a.Status, view.RoleStopped)
		}
		if len(a.Lines) != c.lines {
			t.Errorf("%s: %d lines, want %d (its history)", c.agent, len(a.Lines), c.lines)
		}
	}
	// The active agent's page keeps its own lane, task and status, and lists its sessions with the transcript path and
	// when it was last written (never the contents), its briefs, the lines it was sent and its jobs with their text.
	var a view.AgentDetail
	decode(t, get(t, h, "/api/agent?project=pj&agent=pj-l1"), &a)
	if a.Status != "working" || a.Lane != "L-01" || a.Task != "T-01" {
		t.Errorf("active agent: %+v, want working on L-01/T-01", a)
	}
	if len(a.Sessions) != 1 {
		t.Fatalf("sessions %+v, want one", a.Sessions)
	}
	if sess := a.Sessions[0]; sess.ID != "sid-1" || sess.Transcript == "" || sess.LastWrite == 0 || sess.Task != "T-01" ||
		strings.Contains(sess.Transcript, "\n") {
		t.Errorf("session %+v, want sid-1 with its transcript path and its last write", sess)
	}
	if len(a.Briefs) != 1 || a.Briefs[0].Text != "the brief's line" {
		t.Errorf("briefs %+v, want the brief's line", a.Briefs)
	}
	if len(a.Lines) != 1 || a.Lines[0].Line != "[obor pj-lead] work on T-01" || a.Lines[0].From != "pj-lead" {
		t.Errorf("lines %+v, want the line posted to the agent", a.Lines)
	}
	if len(a.Jobs) != 1 || a.Jobs[0].Text != "the whole job prompt" {
		t.Errorf("jobs %+v, want the job's prompt text", a.Jobs)
	}
}

// TestEventsSince: /api/events answers with the records newer than since only (the store's seq is the cursor), and
// with its Seq as the next since.
func TestEventsSince(t *testing.T) {
	p, _, s := fixture(t)
	s.Wait, s.Poll = 50*time.Millisecond, 10*time.Millisecond // a held poll ends quickly here; its wait is tested below
	h := s.Handler()
	newest := newestSeq(t, p.Root)
	if newest < 3 {
		t.Fatalf("precondition: %d records, want a store with a history", newest)
	}
	r := get(t, h, "/api/events?project=pj&since="+strconv.FormatInt(newest, 10))
	if r.Code != http.StatusOK {
		t.Fatalf("events: %d %s", r.Code, r.Body.String())
	}
	var page view.EventsPage
	decode(t, r, &page)
	if page.Seq != newest || len(page.Events) != 0 {
		t.Errorf("since the newest record: seq %d, %d events; want %d and none", page.Seq, len(page.Events), newest)
	}
	r = get(t, h, "/api/events?project=pj&since=1")
	if r.Code != http.StatusOK {
		t.Fatalf("events: %d %s", r.Code, r.Body.String())
	}
	decode(t, r, &page)
	if page.Seq != newest || len(page.Events) < 2 {
		t.Fatalf("since 1: seq %d, %d events; want the history after the first record", page.Seq, len(page.Events))
	}
	for _, e := range page.Events {
		if e.Seq <= 1 {
			t.Errorf("event %+v: not newer than since 1", e)
		}
	}
	if last := page.Events[len(page.Events)-1]; !strings.Contains(last.Line, "line to pj-l1") {
		t.Errorf("last event %+v, want the newest line (oldest first)", last)
	}

	// A poll held at the newest seq answers as soon as a record lands (no reload).
	s.Wait, s.Poll = 5*time.Second, 10*time.Millisecond
	since := newestSeq(t, p.Root)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	done := make(chan view.EventsPage, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/api/events?project=pj&since=" + strconv.FormatInt(since, 10))
		if err != nil {
			done <- view.EventsPage{}
			return
		}
		defer resp.Body.Close()
		var page view.EventsPage
		_ = json.NewDecoder(resp.Body).Decode(&page)
		done <- page
	}()
	time.Sleep(100 * time.Millisecond) // the poll is held: the record below is what ends it
	addEvent(t, p.Root, "T-01", event.StepSendback, event.KindFact, event.ClassFact, map[string]any{"note": "fix x"})
	select {
	case page := <-done:
		if page.Seq <= since || len(page.Events) == 0 || page.Events[len(page.Events)-1].Seq != since+1 {
			t.Errorf("poll: %+v, want the new record %d", page, since+1)
		}
		if !strings.Contains(page.Events[len(page.Events)-1].Line, "sendback T-01") {
			t.Errorf("poll line %q, want the worded sendback", page.Events[len(page.Events)-1].Line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the poll did not answer a new record")
	}
}

// T-880: the task, agent and setup pages never build the overview: their narrow reads answer, and a Build that counts
// is not asked. /api/view still builds it.
func TestDetailPagesDoNotBuildTheView(t *testing.T) {
	_, _, s := fixture(t)
	built := 0
	s.Build = func(context.Context, string) (view.Project, error) {
		built++
		return view.Project{}, nil
	}
	h := s.Handler()
	for _, url := range []string{"/api/task?project=pj&task=T-01", "/api/agent?project=pj&agent=pj-l1",
		"/api/setup?project=pj"} {
		if r := get(t, h, url); r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", url, r.Code, r.Body.String())
		}
	}
	if built != 0 {
		t.Errorf("the detail and setup pages built the overview %d times; want none", built)
	}
	if r := get(t, h, "/api/view?project=pj"); r.Code != http.StatusOK || built != 1 {
		t.Errorf("/api/view: %d, %d overview builds; want 200 and one", r.Code, built)
	}
}

// TestUnknownProjectAndTaskAre404: a project or a task the machine does not know is a 404; a missing parameter is a 400.
func TestUnknownProjectAndTaskAre404(t *testing.T) {
	_, _, s := fixture(t)
	h := s.Handler()
	for _, c := range []struct {
		url  string
		code int
	}{
		{"/api/view?project=nope", http.StatusNotFound},
		{"/api/view", http.StatusBadRequest},
		{"/api/setup?project=nope", http.StatusNotFound},
		{"/api/setup", http.StatusBadRequest},
		{"/api/task?project=pj&task=T-999", http.StatusNotFound},
		{"/api/task?project=nope&task=T-01", http.StatusNotFound},
		{"/api/task?project=pj", http.StatusBadRequest},
		{"/api/agent?project=pj&agent=nobody", http.StatusNotFound},
		{"/api/job?project=pj&job=T-999/1/work", http.StatusNotFound},
		{"/api/job?project=nope&job=T-01/1/work", http.StatusNotFound},
		{"/api/job?project=pj", http.StatusBadRequest},
		{"/api/nothing?project=pj", http.StatusNotFound},
	} {
		if r := get(t, h, c.url); r.Code != c.code {
			t.Errorf("%s: %d, want %d (%s)", c.url, r.Code, c.code, r.Body.String())
		}
	}
}

// TestReadOnlyAndThePage: every route but GET/HEAD is refused (no verbs) and "/" serves the embedded page.
func TestReadOnlyAndThePage(t *testing.T) {
	_, _, s := fixture(t)
	h := s.Handler()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(m, "/api/view?project=pj", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/view: %d, want 405", m, w.Code)
		}
	}
	r := get(t, h, "/")
	if r.Code != http.StatusOK || !strings.Contains(strings.ToLower(r.Body.String()), "<!doctype html>") {
		t.Errorf("page: %d, %q", r.Code, r.Body.String()[:min(80, len(r.Body.String()))])
	}
	if ct := r.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("page content type %q, want text/html", ct)
	}
	// T-803: the page renders the idle state (idle, not acknowledged since <time>; idle: <why> once acknowledged).
	for _, want := range []string{"not acknowledged since", "obor idle", "last: "} {
		if !strings.Contains(bundle(t), want) {
			t.Errorf("the page's bundle has no %q: the idle state is not rendered", want)
		}
	}
}

// TestListenLocalhostOnly: --listen takes a loopback host and a port ("" is the fixed default); any other host is
// refused, and so is no host at all (every interface).
func TestListenLocalhostOnly(t *testing.T) {
	for _, c := range []struct{ listen, want string }{
		{"", DefaultListen},
		{"127.0.0.1:0", "127.0.0.1:0"},
		{"127.0.0.1:8787", "127.0.0.1:8787"},
		{"localhost:9000", "localhost:9000"},
		{"127.0.0.2:9000", "127.0.0.2:9000"},
		{"[::1]:9000", "[::1]:9000"},
	} {
		got, err := ListenAddr(c.listen)
		if err != nil || got != c.want {
			t.Errorf("ListenAddr(%q) = %q, %v; want %q", c.listen, got, err, c.want)
		}
	}
	for _, bad := range []string{"0.0.0.0:8787", ":8787", "example.com:80", "192.168.1.4:8787", "127.0.0.1", "127.0.0.1:x"} {
		if got, err := ListenAddr(bad); err == nil {
			t.Errorf("ListenAddr(%q) = %q, want a refusal (localhost only)", bad, got)
		}
	}
}

// T-709: a project whose config has a key this binary does not know (a long-running obor web older than the config:
// "unknown field roles.researcher") is served with the warning among the view's notes instead of failing the page,
// and a stopped project says so; the landing's row carries no error.
func TestViewOfANewerConfigWarns(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config(testkit.Set("zz_future", "")) // T-801: agents.roles.<name> is a custom role now, no unknown key
	p.Spec("T-01", "# T-01 — stored, never dispatched\n\nDifficulty: simple\n")
	if _, unknown, err := config.LoadForValidate(p.Root); err != nil || len(unknown) != 1 {
		t.Fatalf("precondition: the strict validate load refuses the unknown key: %q, %v", unknown, err)
	}
	if err := project.WriteStopMarker(p.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Projects: func() []project.Project { return []project.Project{{Name: "proj", Root: p.Root}} }}).Handler()
	r := get(t, h, "/api/view?project=proj")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/view: %d %s", r.Code, r.Body.String())
	}
	var v view.Project
	decode(t, r, &v)
	if len(v.Notes) != 2 || !strings.HasPrefix(v.Notes[0], "WARN ") || !strings.Contains(v.Notes[0], `"zz_future"`) ||
		v.Notes[1] != project.StoppedLine {
		t.Errorf("notes %q, want the unknown key's warning and the stopped line", v.Notes)
	}
	// T-1028: the page's view is view.Build's (view.Load), and serve is down: T-01's stored spec is not listed as never
	// dispatched.
	if len(v.Queue.Waiting) != 0 {
		t.Errorf("queue %+v, want none with serve down: a stored-only spec is not listed", v.Queue.Waiting)
	}
	var entries []ProjectsEntry
	decode(t, get(t, h, "/api/projects"), &entries)
	if len(entries) != 1 || entries[0].Error != "" {
		t.Errorf("/api/projects: %+v, want the project without an error", entries)
	}
}

// TestOverviewShowsWorkingRoles (T-713): a project whose only work is a working deep dive lists it in /api/projects
// (busy stays the lane count); a role with a brief waiting counts; a stopped role and an idle one are not listed; and
// the page script shows the roles on the card.
func TestOverviewShowsWorkingRoles(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config(testkit.Set("agents.roles.deep_dive", "opus"),
		testkit.Set("agents.roles.reviewer", "opus"),
		testkit.Set("agents.roles.advisor", "opus"))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	deep, rev, adv := cfg.AgentName("deep_dive", ""), cfg.AgentName("reviewer", ""), cfg.AgentName("advisor", "")
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
			Agents:  []wire.AgentStatus{{Pane: "w1:p1", Name: deep, Agent: "claude", Status: "working", Age: 540}},
			Stopped: []string{rev},
			Briefs:  []wire.BriefStatus{{To: adv}}}}
	v := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), Now: now, Events: 20})
	if len(v.Roles) < 3 {
		t.Fatalf("precondition: the view lists the three roles: %+v", v.Roles)
	}
	s := &Server{Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: p.Root}} },
		Build: func(context.Context, string) (view.Project, error) { return v, nil }}
	var entries []ProjectsEntry
	decode(t, get(t, s.Handler(), "/api/projects"), &entries)
	if len(entries) != 1 || entries[0].Busy != 0 {
		t.Fatalf("/api/projects: %+v, want one project with no busy lane", entries)
	}
	got := map[string]string{}
	for _, r := range entries[0].Roles {
		got[r.Role] = r.Status
	}
	if len(got) != 2 || got["deep_dive"] != "working" {
		t.Errorf("roles %v, want the working deep dive and the advisor with a brief only", got)
	}
	if _, ok := got["advisor"]; !ok {
		t.Errorf("roles %v: the advisor has a brief waiting and counts", got)
	}
	if _, ok := got["reviewer"]; ok {
		t.Errorf("roles %v: the stopped reviewer is listed", got)
	}
	if !strings.Contains(bundle(t), ".roles") {
		t.Error("the page does not read the overview's roles")
	}
}

// T-714, T-727: /api/projects counts the agents' recorded background processes for the overview card (a long
// foreground command is not counted), and an agent page shows the processes its read supplies (T-880: the narrow agent
// read scans none; the overview is where they are read); the page reads both.
func TestBackgroundOnOverviewAndAgentPage(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01") // its row holds T-01, not landed: busy (T-1012)
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	lead := cfg.AgentName("lead", "")
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Agents: []wire.AgentStatus{
		{Pane: "w1:p1", Name: "pj-l1", Lane: "L-01", Status: "working"}, {Pane: "w1:p2", Name: lead, Status: "idle"}}}}
	conv := view.Background{PID: 42, Command: "python convert.py", Background: true, Label: "convert maps", Age: 900}
	v := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), Now: time.Now(), Events: 20,
		Background: map[string][]view.Background{"pj-l1": {conv}, lead: {{PID: 7, Command: "sleep 600", Background: true, Age: 45},
			{PID: 8, Command: "go test ./...", Age: 300}}}})
	s := &Server{Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: p.Root}} },
		Build: func(context.Context, string) (view.Project, error) { return v, nil },
		Agent: func(context.Context, string) (view.Detail, error) { return view.Detail{Root: p.Root, Project: v}, nil }}
	var entries []ProjectsEntry
	decode(t, get(t, s.Handler(), "/api/projects"), &entries)
	if len(entries) != 1 || entries[0].Background != 2 {
		t.Errorf("/api/projects: %+v, want 2 background processes", entries)
	}
	// T-760: the fleet page's lanes: L-01 busy with T-01 and its agent
	if l := entries[0].Lanes; len(l) != 1 || l[0].Lane != "L-01" || l[0].Task.Task != "T-01" || l[0].Agent != "pj-l1" {
		t.Errorf("/api/projects lanes %+v, want L-01 on T-01 with pj-l1", l)
	}
	var a view.AgentDetail
	decode(t, get(t, s.Handler(), "/api/agent?project=pj&agent=pj-l1"), &a)
	if len(a.Background) != 1 || a.Background[0] != conv {
		t.Errorf("agent background %+v, want %+v", a.Background, conv)
	}
	if !strings.Contains(bundle(t), ".background") {
		t.Error("the page does not read the agents' background processes")
	}
}
