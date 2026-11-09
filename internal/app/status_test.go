package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"obor/internal/version"
)

// T-896, T-921: serve ships whether a task ended: the state table's ended column, not a list of state names its
// readers keep. A dispatched task has not ended; a dropped one has, and stays in the list (obor status --json shows
// it).
func TestStatusEndedIsTheStateTablesEnded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`)
	h.connect()
	for _, task := range []string{"T-1", "T-2"} {
		addRecord(t, h, task, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
			Agent: "pj-l-01", Branch: "task/" + task, Target: "b1"})
	}
	addRecord(t, h, "T-2", event.StepDropped, event.KindFact, event.FactData{Reason: "why"})
	ended := map[string]bool{}
	for _, ts := range h.a.Status().Project.Tasks {
		ended[ts.Task] = ts.Ended
	}
	if got, ok := ended["T-1"]; !ok || got {
		t.Errorf("T-1: ended %v (listed %v), want not ended: dispatched", got, ok)
	}
	if got, ok := ended["T-2"]; !ok || !got {
		t.Errorf("T-2: ended %v (listed %v), want ended: dropped, and still in the list", got, ok)
	}
}

// T-1043: serve ships the queue's order and its waiting predicate, so the view's queue lists the dispatched waits in
// the order serve starts them instead of reconstructing both from the state string: a waiting dispatch is flagged
// Waiting and ranked by passOrder — T-20, dispatched first, ranks before T-10.
func TestStatusShipsTheWaitingRank(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a, p := lineApp(t, lineRoot(t), &now)
	for _, task := range []string{"T-20", "T-10"} { // the reversed dispatch order: T-20's waiting fact first
		if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepWaiting, Kind: event.KindFact},
			dispatchData{Level: "medium", Types: []string{"sonnet"}}) {
			t.Fatalf("the log refused %s's waiting fact", task)
		}
	}
	ps := a.Status().Project
	if ps == nil || len(ps.Tasks) != 2 {
		t.Fatalf("precondition: serve has the two waiting tasks: %+v", ps)
	}
	rank := map[string]int{}
	for _, ts := range ps.Tasks {
		if !ts.Waiting {
			t.Errorf("%s: Waiting false, want true: its dispatch waits for a lane", ts.Task)
		}
		rank[ts.Task] = ts.Rank
	}
	if rank["T-20"] == 0 || rank["T-10"] == 0 || rank["T-20"] >= rank["T-10"] {
		t.Errorf("ranks %v, want T-20 before T-10: its dispatch came first", rank)
	}
}

// T-896, T-921: serve ships the lanes its config-change rule would not refuse to drop (laneHolds), so obor web's
// setup page warns without a list of lane states: a lane holding a task is not free, a free one is, and an ended task
// frees its lane.
func TestStatusFreeLanesFollowLaneHolds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`)
	h.lanes.lanes = []port.Lane{{Name: "L-01", Dir: h.lane("L-01")}, {Name: "L-02", Dir: h.lane("L-02")}}
	h.connect()
	if ps := h.a.Status().Project; ps == nil {
		t.Fatal("precondition: serve has no project")
	}
	addRecord(t, h, "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: h.lane("L-02"),
		Agent: "pj-l-02", Branch: "task/T-2", Target: "b1"})
	h.a.mu.Lock()
	h.a.p.lanes = []port.Lane{{Name: "L-01", State: port.LaneFree}, {Name: "L-02", State: port.LaneWorking}}
	h.a.mu.Unlock()
	if free := h.a.Status().Project.Free; len(free) != 1 || free[0] != "L-01" {
		t.Errorf("free %v, want L-01 alone: L-02 holds T-2, L-01 holds nothing", free)
	}
	addRecord(t, h, "T-2", event.StepDropped, event.KindFact, event.FactData{Reason: "why"})
	if free := h.a.Status().Project.Free; len(free) != 2 || free[0] != "L-01" || free[1] != "L-02" {
		t.Errorf("free %v, want both: T-2 ended", free)
	}
}

// RV-49 #4: a waiting task has no lane, so the drop its needs-you advertises names the task (obor drop <T-xx>, T-462),
// whichever dependency ended the wait.
func TestAnswerPathWaitingTaskNamesTheTask(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{
		"after: T-8 waits for T-1, which is dropped",
		"after: T-8 waits for T-1, which is rejected",
	} {
		raw, _ := json.Marshal(event.FactData{Merge: true, Cause: causeAfter, Reason: reason})
		r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
		path := answerPath(nil, r, mtask{task: "T-8", state: stWaiting, needs: block{fact: r}})
		if strings.Contains(path, "<LANE>") {
			t.Errorf("%q: path %q names <LANE>, want the task", reason, path)
		}
		if !strings.Contains(path, `obor drop T-8 "<why>"`) {
			t.Errorf("%q: path %q, want obor drop T-8", reason, path)
		}
	}
}

// T-775: a stalled worker's needs-you names the task, never its lane: obor resume with the line, obor drop by task id.
func TestAnswerPathNamesTheTaskNotTheLane(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(event.FactData{Lane: "L-02", Reason: "stalled"})
	r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
	p := &proj{folded: folded{active: map[string][]event.Record{"T-8": {r}}}}
	path := answerPath(p, r, mtask{task: "T-8", state: stWorking, lane: "L-02", agent: "pj-l-02"})
	if want := `its pane, or obor resume T-8 "<text>" | obor drop T-8 "<why>"`; path != want {
		t.Errorf("path %q, want %q", path, want)
	}
}

// T-657/T-662: the running line's steps are the merge loop's command steps whose effect runs now (p.exec), in seq
// order — a numbered gate, a numbered publish, the nightly check — and a pending self-restart last, elapsed from the
// start of each execution. An open intent whose effect does not run now is pending, not running; an intent with a
// result, a prompt and a start are neither.
func TestRunningStepsAreTheCommandStepsWhoseEffectRuns(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	for _, r := range []event.Record{
		{Task: "T-1", Attempt: 1, Step: event.StepGate + ".2", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-4", Step: event.StepGate, Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-4", Step: event.StepGate, Kind: event.KindResult}, // its result: not an open intent
		{Task: "T-2", Step: event.StepAfterMerge, Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-3", Step: event.StepPublish + ".2", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: event.NightlyTask, Step: event.StepNightly + ".2026-11-01", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-5", Step: event.StepFix, Kind: event.KindIntent, Class: event.ClassPrompt}, // a prompt: no command
		{Task: "T-5", Step: event.StepStop, Kind: event.KindIntent, Class: event.ClassStart}, // a start: no command
	} {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	open := l.OpenIntents()
	if n := len(open); n != 6 {
		t.Fatalf("precondition: %d open intents, want 6: T-4's gate has its result", n)
	}
	// The gate, the publish and the nightly check run now, each from its own start (not from its intent's write time):
	// T-2's after_merge does not.
	gated, published := logNow.Add(30*time.Second), logNow.Add(2*time.Minute)
	night := logNow.Add(3 * time.Minute)
	p := &proj{exec: map[event.Key]time.Time{
		{Task: "T-1", Attempt: 1, Step: event.StepGate + ".2"}:                         gated,
		{Task: "T-3", Attempt: 0, Step: event.StepPublish + ".2"}:                      published,
		{Task: event.NightlyTask, Attempt: 0, Step: event.StepNightly + ".2026-11-01"}: night,
		{Task: "T-5", Attempt: 0, Step: event.StepStop}:                                published, // a start: no command the line shows
	}, folded: newFolded()}
	want := []wire.RunningStep{
		{Step: event.StepGate, Task: "T-1", Since: gated.Unix()},
		{Step: event.StepPublish, Task: "T-3", Since: published.Unix()},
		{Step: event.StepNightly, Since: night.Unix()},
	}
	got := p.runningSteps(open)
	if len(got) != len(want) {
		t.Fatalf("running steps %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("running step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if pending := p.pendingSteps(open); len(pending) != 1 || pending[0] != (wire.PendingStep{Step: event.StepAfterMerge,
		Task: "T-2", Since: logNow.Unix()}) {
		t.Errorf("pending steps %+v, want after_merge T-2 queued and nothing else", pending)
	}
	restart := logNow.Add(90 * time.Second)
	p.restarting = restart
	got = p.runningSteps(open)
	if n := len(got); n != len(want)+1 || got[n-1] != (wire.RunningStep{Step: event.StepServeRestart, Since: restart.Unix()}) {
		t.Errorf("with a pending self-restart: %+v, want the three steps then restarting serve", got)
	}
	// Its result in: the gate's step is gone from both lines.
	if _, err := l.Append(event.Record{Task: "T-1", Attempt: 1, Step: event.StepGate + ".2", Kind: event.KindResult}, logNow); err != nil {
		t.Fatal(err)
	}
	open = l.OpenIntents()
	if got, want := p.runningSteps(open), []wire.RunningStep{{Step: event.StepPublish, Task: "T-3", Since: published.Unix()},
		{Step: event.StepNightly, Since: night.Unix()}, {Step: event.StepServeRestart, Since: restart.Unix()}}; len(got) != len(want) {
		t.Errorf("after the gate's result: %+v, want its step gone: %+v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("after the gate's result: step %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	}
	if got := p.pendingSteps(open); len(got) != 1 || got[0].Step != event.StepAfterMerge {
		t.Errorf("after the gate's result: pending %+v, want only after_merge T-2", got)
	}
}

// T-662: a pending step names its last failure (the fold's reason without the step's prefix, and the fact's time) and
// reads queued when it never failed; an intent written before a restart is pending until its effect starts, then
// running, elapsed from that start — not from the intent's older time.
func TestPendingStepsWaitForTheirEffect(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	failedAt := logNow.Add(time.Minute)
	for _, r := range []event.Record{
		{Task: "T-2", Step: event.StepAfterMerge, Kind: event.KindIntent, Class: event.ClassGit}, // written before the restart
		{Task: event.NightlyTask, Step: event.StepNightly + ".2026-11-01", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-6", Step: event.StepGate, Kind: event.KindIntent, Class: event.ClassGit}, // replayed: no failure recorded for it
	} {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	p := &proj{exec: map[event.Key]time.Time{}, folded: newFolded()}
	p.folded.loop["T-2"] = &mtask{task: "T-2"}
	p.foldLoop(event.Record{Task: "T-2", Step: event.StepFailed, Kind: event.KindFact, TS: failedAt.Format(time.RFC3339),
		Data: mustJSON(event.FactData{Merge: true, Step: event.StepAfterMerge, Reason: "after_merge: git I/O: index.lock exists"})})
	p.foldNightly(event.Record{Task: event.NightlyTask, Step: event.StepFailed, Kind: event.KindFact, TS: failedAt.Format(time.RFC3339),
		Data: mustJSON(event.FactData{Machine: Machine, Step: event.StepNightly + ".2026-11-01",
			Reason: event.StepNightly + ".2026-11-01: worktree add: locked"})})
	open := l.OpenIntents()
	want := []wire.PendingStep{
		{Step: event.StepAfterMerge, Task: "T-2", Since: logNow.Unix(), Error: "git I/O: index.lock exists", FailedAt: failedAt.Unix()},
		{Step: event.StepNightly, Since: logNow.Unix(), Error: "worktree add: locked", FailedAt: failedAt.Unix()},
		{Step: event.StepGate, Task: "T-6", Since: logNow.Unix()},
	}
	got := p.pendingSteps(open)
	if len(got) != len(want) {
		t.Fatalf("pending steps %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pending step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := p.runningSteps(open); len(got) != 0 {
		t.Fatalf("running steps %+v, want none: no effect runs yet", got)
	}
	// T-2's after_merge runs now, three minutes after its intent: running from that start, out of the pending line.
	started := logNow.Add(3 * time.Minute)
	p.exec[event.Key{Task: "T-2", Attempt: 0, Step: event.StepAfterMerge}] = started
	if got, want := p.runningSteps(open), []wire.RunningStep{{Step: event.StepAfterMerge, Task: "T-2", Since: started.Unix()}}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("running steps %+v, want after_merge T-2 from %d (its intent's time is %d)", got, started.Unix(), logNow.Unix())
	}
	if got := p.pendingSteps(open); len(got) != len(want)-1 || got[0] != want[1] {
		t.Errorf("pending steps %+v, want after_merge gone and the rest", got)
	}
}

// T-1022: a task's status carries its spec's Ref: line as ref (obor status --json only); a task whose spec has none
// ships no ref key at all.
func TestStatusCarriesTheSpecsRef(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`)
	h.connect()
	for _, task := range []string{"T-1", "T-2"} {
		addRecord(t, h, task, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
			Agent: "pj-l-01", Branch: "task/" + task, Target: "b1"})
	}
	if _, _, err := store.SetSpec(h.a.p.log.DB(), "T-1", "# T-1 — A\n\nDifficulty: medium\nRef: JIRA-12\n", "", "lead",
		h.now); err != nil {
		t.Fatalf("precondition: set T-1's spec: %v", err)
	}
	seen := map[string]string{}
	for _, ts := range h.a.Status().Project.Tasks {
		raw, err := json.Marshal(ts)
		if err != nil {
			t.Fatalf("marshal %s: %v", ts.Task, err)
		}
		seen[ts.Task] = string(raw)
	}
	for _, c := range []struct {
		task string
		ref  bool
	}{
		{"T-1", true},
		{"T-2", false},
	} {
		raw, ok := seen[c.task]
		if !ok {
			t.Fatalf("precondition: %s is not in the status", c.task)
		}
		if got := strings.Contains(raw, `"ref":`); got != c.ref {
			t.Errorf("%s's status has a ref key %v, want %v: %s", c.task, got, c.ref, raw)
		}
	}
	if raw := seen["T-1"]; !strings.Contains(raw, `"ref":"JIRA-12"`) {
		t.Errorf("T-1's status %s, want ref JIRA-12", raw)
	}
}

// runAnswer runs cmd, one command of an answerPath, as the lead does: obor resume through its own parser (ResumeArgs)
// with a one-word text for its placeholder (RV-54 #4), obor drop as its CLI calls serve (by task id, T-775).
func runAnswer(h *harness, fact event.Record, cmd string) (string, error) {
	id := wire.Envelope{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "c1"}
	args := strings.Fields(regexp.MustCompile(`"<[a-z]+>"`).ReplaceAllString(cmd, "yes"))
	switch {
	case len(args) > 2 && args[1] == "resume":
		rc, ok := wire.ResumeArgs(args[2:])
		if !ok {
			return "", &usageError{cmd}
		}
		rc.Envelope = id
		return h.a.resume(rc)
	case len(args) == 4 && args[1] == "drop" && args[2] == fact.Task:
		return h.a.drop(wire.DropReq{Envelope: id, Task: args[2], Why: "not needed"})
	}
	return "", &usageError{cmd}
}

type usageError struct{ cmd string }

func (e *usageError) Error() string { return "not an obor command: " + e.cmd }

// RV-54 #2 (T-515), T-775: each cause's suggested commands are the ones its verbs take in the state the needs-you
// leaves (resumeFits, mtask.may): the one obor resume form (its text where it needs one; the owner's choice of --regate
// or --sendback where there is no default: red or sendbacks past K, a resolve rejected, a fix job not sent), no drop
// for a merged task (after-merge failures), only drop for a reject; every command names the task, never the lane.
// Each command, run on a fresh seed through obor resume's parser, is accepted.
func TestAnswerPathEveryCause(t *testing.T) {
	t.Parallel()
	const drop, resume = ` | obor drop T-1 "<why>"`, "obor resume T-1"
	const choice = resume + " --regate | " + resume + ` --sendback "<why>"`
	for _, row := range []struct {
		cause, state, job string
		watch             bool
		want              string
	}{
		{event.CauseHeld, stAwaiting, event.StepReview, false, `obor resume T-1 "<decision>"` + drop},
		{causeRed, stRed, "", false, choice + drop},
		{causeUndelivered, stAwaiting, event.StepReview, false, resume + drop},
		{causeStalled, stAwaiting, event.StepReview, false, resume + drop},
		{causeGate, stSubmitted, "", false, resume + drop},
		{causePublish, stAccepted, "", false, resume + drop},
		{causeSendbacks, stAwaiting, event.StepReview, false, choice + drop},
		{causeResolveRejected, stResolving, event.StepResolve, false, choice + drop},
		{causeFix, stWorking, "", false, choice + drop},
		{causeLane, stDispatched, "", false, resume + drop},
		{causeStart, stDispatched, "", false, resume + drop},
		{causeNoPane, stDispatched, "", false, resume + drop},
		{causeAfter, stWaiting, "", false, resume + drop}, // no lane: the drop just cancels it
		{"submit" + failedSuffix, stSubmitted, "", false, resume + drop},
		{"review" + failedSuffix, stAwaiting, event.StepReview, false, resume + drop},
		{event.StepAfterMerge + failedSuffix, stMerged, "", false, resume},
		{event.StepStop + failedSuffix, stMerged, "", false, resume},
		{causeRejected, stRejected, "", false, `obor drop T-1 "<why>"`},
		{"stalled", "", "", true, `its pane, or obor resume T-1 "<text>"` + drop}, // T-746
		{"blocked", "", "", true, `its pane, or obor drop T-1 "<why>"`},
		{"pane exited", "", "", true, `its pane, or obor drop T-1 "<why>"`},
	} {
		t.Run(row.cause, func(t *testing.T) {
			t.Parallel()
			h, p, fact := answerSeed(t, row.cause, row.state, row.job, row.watch)
			h.a.mu.Lock()
			path := p.answerPath(fact)
			h.a.mu.Unlock()
			if path != row.want {
				t.Fatalf("suggested %q, want %q", path, row.want)
			}
			if got := h.a.Status().Project.Answers[fact.Seq]; got != path { // obor status shows serve's
				t.Errorf("status answers %q, want %q", got, path)
			}
			for _, cmd := range strings.Split(strings.TrimPrefix(path, "its pane, or "), " | ") {
				if cmd == "its pane" {
					continue
				}
				h, _, fact := answerSeed(t, row.cause, row.state, row.job, row.watch)
				// a drop answers DROPPED, or DROPPING for a task holding a lane (T-852)
				if reply, err := runAnswer(h, fact, cmd); err != nil {
					t.Errorf("%s: refused: %v", cmd, err)
				} else if want := map[bool]string{true: "SENDBACK", false: map[string]string{"resume": "RESUMED",
					"drop": "DROPP"}[strings.Fields(cmd)[1]]}[strings.Contains(cmd, "--sendback")]; !strings.Contains(reply, want) {
					t.Errorf("%s: %q", cmd, reply)
				}
			}
		})
	}
	for _, task := range []string{event.LoopTask, event.NightlyTask, event.MsgTask} { // a project's fact: fix what it names
		if path := answerPath(nil, event.Record{Task: task, Step: event.StepNeedsYou, Kind: event.KindFact}, mtask{}); path != "" {
			t.Errorf("%s: suggests %q, want none", task, path)
		}
	}
}

// T-826: a call names its binary's version, and a serve that runs another one names both versions in its refusal; a
// call from a binary older than the field ("", no version) is "an older obor". A difference alone refuses nothing.

// askApp sends one call to a's socket (Listen on an in-memory listener) and gives its answer.
func askApp(t *testing.T, a *App) func(c wire.Req) wire.Answer {
	t.Helper()
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go a.Listen(ln)
	return func(c wire.Req) wire.Answer {
		client := ln.sendRaw(t, c)
		defer client.Close()
		return read(t, client)
	}
}

// callApp is a serving rootApp's app: its project loaded and its socket answering, its log in out.
func callApp(t *testing.T, out *bytes.Buffer) func(c wire.Req) wire.Answer {
	t.Helper()
	a := rootApp(projectRoot(t), out)
	a.loadProject()
	t.Cleanup(a.closeLog)
	if a.p == nil {
		t.Fatal("precondition: project pj loaded")
	}
	return askApp(t, a)
}

func TestRefusalNamesVersionSkew(t *testing.T) {
	t.Parallel()
	ask := callApp(t, &bytes.Buffer{})
	// job_next without a call id is refused before anything else: the ordinary refusal, unchanged at serve's version.
	same := ask(&wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01", Version: version.Version}})
	if want := "job_next: a call id is required"; same.Error != want {
		t.Fatalf("same version: error %q, want %q", same.Error, want)
	}
	older := ask(&wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01", Version: "v0.1.0"}})
	want := "job_next: a call id is required (caller pj-l-01 runs obor v0.1.0, serve " + version.Version + ")"
	if older.Error != want {
		t.Errorf("another version: error %q, want %q", older.Error, want)
	}
	none := ask(&wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01"}})
	want = "job_next: a call id is required (caller pj-l-01 runs an older obor, serve " + version.Version + ")"
	if none.Error != want {
		t.Errorf("no version: error %q, want %q", none.Error, want)
	}
}

// An accepted call is not refused for the mismatch: the caller wants its work done, not a version lecture.
func TestAcceptedCallIsNotRefusedForSkew(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	var out bytes.Buffer
	a := rootApp(root, &out)
	a.loadProject()
	t.Cleanup(a.closeLog)
	ask := askApp(t, a)
	ans := ask(&wire.JobNextReq{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-02", CallID: "c1",
		Version: "v0.1.0"}})
	if ans.Error != "" || ans.Reply != `{"state":"none"}` {
		t.Errorf("job_next at another version = %+v; want the ordinary none reply, refused nothing", ans)
	}
}

// The line is one per (agent, version), not per call: two calls from one agent at one version log once; another agent
// or another version logs its own.
func TestSkewLoggedOncePerAgentAndVersion(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	var out bytes.Buffer
	a := rootApp(root, &out)
	a.loadProject()
	t.Cleanup(a.closeLog)
	ask := askApp(t, a)
	if version.Version == "v0.1.0" || version.Version == "v0.2.0" {
		t.Fatalf("precondition: the test versions differ from serve's %q", version.Version)
	}
	calls := []wire.JobNextReq{
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c1", Version: "v0.1.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c2", Version: "v0.1.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-02", CallID: "c3", Version: "v0.1.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c4", Version: "v0.2.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c5"}},
	}
	for _, c := range calls {
		if ans := ask(&c); ans.Error != "" {
			t.Fatalf("job_next %s at %q: %q", c.Agent, c.Version, ans.Error)
		}
	}
	log := out.String()
	for _, c := range []struct {
		agent, skew string
		want        int
	}{
		{"pj-l-01", "obor v0.1.0", 1},
		{"pj-l-02", "obor v0.1.0", 1},
		{"pj-l-01", "obor v0.2.0", 1},
		{"pj-l-01", "an older obor", 1},
	} {
		line := "caller " + c.agent + " runs " + c.skew + ", serve " + version.Version
		if got := strings.Count(log, line); got != c.want {
			t.Errorf("log has %d %q lines, want %d:\n%s", got, line, c.want, log)
		}
	}
	if got := strings.Count(log, "caller "); got != 4 {
		t.Errorf("log has %d skew lines, want 4 (one per agent and version):\n%s", got, log)
	}
}

// T-1055: Listen reads a request's envelope, then its method's request type. A request with no call (none, or null)
// is refused as before the split, an unknown method is, and an old binary's line with keys of the union its method
// does not use (task, patch on a pause) reaches the method's handler, those keys ignored.
func TestListenDecodesTheMethodsRequest(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	a := rootApp(root, &bytes.Buffer{})
	a.loadProject()
	t.Cleanup(a.closeLog)
	if a.p == nil {
		t.Fatal("precondition: project pj loaded")
	}
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go a.Listen(ln)
	ask := func(line string) wire.Answer {
		client, server := net.Pipe()
		ln.conns <- server
		go func() { _, _ = client.Write([]byte(line + "\n")) }()
		defer client.Close()
		return read(t, client)
	}
	envelope := `"root":` + strconv.Quote(root) + `,"agent":"pj-l-01","role":"worker","version":` +
		strconv.Quote(version.Version) + `,"call_id":"p1"`
	for _, c := range []struct{ name, line, want string }{
		{"no call", `{"method":"pause"}`, "pause: no call"},
		{"a null call", `{"method":"pause","call":null}`, "pause: no call"},
		{"an unknown method", `{"method":"pauses","call":{` + envelope + `}}`, "unknown method pauses"},
		{"a field of the wrong type", `{"method":"pause","call":{` + envelope + `,"until":"soon"}}`, "unreadable request"},
		{"an old line", `{"method":"pause","call":{` + envelope + `,"task":"T-9","patch":"p","to":"x","note":"why"}}`,
			"pause: the lead or the owner pauses, not pj-l-01 (role worker)"},
	} {
		if ans := ask(c.line); ans.Error != c.want || ans.Reply != "" {
			t.Errorf("%s: %+v, want the error %q", c.name, ans, c.want)
		}
	}
	// The handler got the old line's pause: the owner's (no role) is recorded with its why.
	owner := `"root":` + strconv.Quote(root) + `,"agent":"","role":"","version":` + strconv.Quote(version.Version) +
		`,"call_id":"p2"`
	if ans := ask(`{"method":"pause","call":{` + owner + `,"task":"T-9","patch":"p","note":"lunch"}}`); ans.Error != "" ||
		ans.Reply != "PAUSED until cleared" {
		t.Errorf("the owner's old pause line: %+v, want PAUSED until cleared", ans)
	}
}

// T-806: the herdr notice follows the server serve is connected to: each snapshot records its version and protocol,
// a change is logged once, and the status note comes from that state.
func TestHerdrNoteFollowsTheConnectedServer(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	a := rootApp(t.TempDir(), &out)
	snap := func(version string, protocol int) port.Snapshot {
		return port.Snapshot{Version: version, Protocol: protocol, Speaks: 22}
	}
	lines := func() int {
		return strings.Count(out.String(), "herdr: ") - strings.Count(out.String(), "herdr: connected")
	}
	ctx := context.Background()

	if n := a.Status().HerdrNote; n != "" || a.Status().Connected {
		t.Fatalf("before any connect: note %q, connected %v; want none", n, a.Status().Connected)
	}
	// Serve started with herdr down; herdr comes up newer than obor's protocol.
	a.connect(ctx, snap("0.9.4", 23))
	want := "herdr 0.9.4, protocol 23 (newer than obor's 22)"
	if n := a.Status().HerdrNote; n != want {
		t.Errorf("note after herdr came up = %q, want %q", n, want)
	}
	if got := strings.Count(out.String(), "herdr: 0.9.4 protocol 23\n"); got != 1 {
		t.Errorf("log names the first version %d times, want once:\n%s", got, &out)
	}
	// The same version again (a reconnect): no new line.
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()
	if n := a.Status().HerdrNote; n != "" {
		t.Errorf("note while disconnected = %q, want none", n)
	}
	a.connect(ctx, snap("0.9.4", 23))
	if got := strings.Count(out.String(), "herdr: 0.9.4 protocol 23\n"); got != 1 {
		t.Errorf("a reconnect to the same version logged it %d times in all, want 1:\n%s", got, &out)
	}
	if lines() != 1 {
		t.Errorf("version lines = %d, want 1:\n%s", lines(), &out)
	}
	// A different (newer) version: the note changes and the line is logged once.
	a.connect(ctx, snap("0.9.5", 24))
	if n, w := a.Status().HerdrNote, "herdr 0.9.5, protocol 24 (newer than obor's 22)"; n != w {
		t.Errorf("note after the upgrade = %q, want %q", n, w)
	}
	if got := strings.Count(out.String(), "herdr: 0.9.5 protocol 24\n"); got != 1 {
		t.Errorf("log names the upgraded version %d times, want once:\n%s", got, &out)
	}
	// Back to obor's own protocol: no note, and the change is logged.
	a.connect(ctx, snap("0.8.0", 22))
	if n := a.Status().HerdrNote; n != "" {
		t.Errorf("note on obor's own protocol = %q, want none", n)
	}
	if got := strings.Count(out.String(), "herdr: 0.8.0 protocol 22\n"); got != 1 {
		t.Errorf("log names the downgraded version %d times, want once:\n%s", got, &out)
	}
}

// #4: a waiting task has no lane, so its needs-you names the task in the drop command, never <LANE>.
func TestWaitingNeedsYouDropCommand(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(event.FactData{Merge: true, Cause: causeAfter, Reason: "after: T-8 waits for T-1, which is dropped"})
	r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
	path := answerPath(nil, r, mtask{task: "T-8", state: stWaiting, needs: block{fact: r}})
	t.Log(path)
	if strings.Contains(path, "<LANE>") {
		t.Error("waiting task has no lane; advertised drop command cannot run")
	}
}
