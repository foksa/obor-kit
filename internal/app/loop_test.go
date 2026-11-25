package app

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/review"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// App.do's contract, the one write path every merge-loop step takes: the intent (its class and data) written and
// folded before the effect runs, the effect's result after it, and the effect run once; a stored result answers the
// key with no run. A failed effect writes no result (its intent stays pending) and a retry runs it again. A process
// killed between the intent and its result replays as the intent pending, so the step runs again.
func TestDoContract(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("OBOR_TEST_DO_CRASH"); root != "" {
		a, p := blockProjAt(t, root)
		_, _ = a.do(context.Background(), p, event.Intent{Key: event.Key{Task: "T-1", Attempt: 2, Step: "start"},
			Class: event.ClassStart, Data: map[string]string{"pane": "w1:p1"}},
			func(context.Context) (any, error) {
				os.WriteFile(filepath.Join(root, "effect"), nil, 0o644)
				os.Exit(3) // killed after the effect, before the result
				return nil, nil
			})
		os.Exit(4)
	}
	root := logRoot(t)
	a, p := blockProjAt(t, root)
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: "publish"}, Class: event.ClassGit,
		Data: map[string]string{"target": "main"}}
	runs := 0
	effect := func(context.Context) (any, error) { runs++; return map[string]string{"sha": "abc"}, nil }
	got, err := a.do(context.Background(), p, in, effect)
	if err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Fatalf("do = %s, %v, runs %d; want the effect's result once", got, err, runs)
	}
	recs, err := event.ReadLog(root)
	if err != nil || len(recs) != 2 || recs[0].Kind != event.KindIntent || recs[0].Class != event.ClassGit ||
		string(recs[0].Data) != `{"target":"main"}` || recs[1].Kind != event.KindResult ||
		string(recs[1].Data) != `{"sha":"abc"}` {
		t.Fatalf("do's records = %+v, %v; want the intent, then its result", recs, err)
	}
	if got, err := a.do(context.Background(), p, in, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("a repeat = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}

	// A failed effect writes no result; the retry runs it again.
	failing := event.Intent{Key: event.Key{Task: "T-2", Attempt: 1, Step: "prompt"}, Class: event.ClassPrompt}
	if _, err := a.do(context.Background(), p, failing, func(context.Context) (any, error) {
		return nil, errors.New("herdr down")
	}); err == nil {
		t.Fatal("a failed effect returned no error")
	}
	if pending := p.log.Pending(); len(pending) != 1 || pending[0].Key() != (event.Key{Task: "T-2", Attempt: 1, Step: "prompt"}) {
		t.Errorf("after a failed effect: pending %+v, want its intent", pending)
	}
	runs = 0
	if got, err := a.do(context.Background(), p, failing, func(context.Context) (any, error) {
		runs++
		return "sent", nil
	}); err != nil || string(got) != `"sent"` || runs != 1 {
		t.Errorf("the retry = %s, %v, runs %d; want the result once", got, err, runs)
	}
	if pending := p.log.Pending(); len(pending) != 0 {
		t.Errorf("after the retry: pending %+v", pending)
	}

	// The child's process, killed inside the effect: the replay leaves its intent pending, so the step runs again.
	crashRoot := logRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDoContract$")
	cmd.Env = append(os.Environ(), "OBOR_TEST_DO_CRASH="+crashRoot)
	var ee *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("child = %v, want exit 3 inside the effect", err)
	}
	if _, err := os.Stat(filepath.Join(crashRoot, "effect")); err != nil {
		t.Fatalf("precondition: the effect ran: %v", err)
	}
	b, q := blockProjAt(t, crashRoot)
	pending := q.log.Pending()
	if len(pending) != 1 || pending[0].Key() != (event.Key{Task: "T-1", Attempt: 2, Step: "start"}) ||
		pending[0].Class != event.ClassStart || string(pending[0].Data) != `{"pane":"w1:p1"}` {
		t.Fatalf("replay pending = %+v; want the intent the crash left", pending)
	}
	runs = 0
	got, err = b.do(context.Background(), q, event.Intent{Key: pending[0].Key(), Class: pending[0].Class},
		func(context.Context) (any, error) { runs++; return "running", nil })
	if err != nil || string(got) != `"running"` || runs != 1 {
		t.Errorf("the step after the crash = %s, %v, runs %d; want the effect once", got, err, runs)
	}
}

// A lane-row write that fails after do's intent is appended: the error is do's, no effect runs, and the log's claim
// on the key is released (the intent pending, open). A retry while the write still fails reuses the intent and runs
// nothing; once the write works the retry writes the rows and runs the effect exactly once, never refused as "its
// effect is running".
func TestDoRowErrorReleasesClaim(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	a, p := blockProjAt(t, root)
	p.folded.lr.live = true
	p.folded.lr.rows = map[string]store.LaneRow{"L-01": {Lane: "L-01", Task: "T-1"}} // a row to write: the write fails
	if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes BEGIN SELECT RAISE(ABORT, 'injected lane-row failure'); END`); err != nil {
		t.Fatal(err)
	}
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: event.StepGate}, Class: event.ClassGit}
	runs := 0
	effect := func(context.Context) (any, error) { runs++; return "ok", nil }
	intents := func() int {
		recs, err := event.ReadLog(root)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range recs {
			if r.Kind == event.KindIntent && r.Key() == in.Key {
				n++
			}
		}
		return n
	}

	if _, err := a.do(context.Background(), p, in, effect); err == nil || !strings.Contains(err.Error(), "injected lane-row failure") {
		t.Fatalf("do on a failing row write = %v, want the lane-row error", err)
	}
	if runs != 0 || intents() != 1 || !p.log.Open(in.Key) || len(p.exec) != 0 {
		t.Fatalf("after the row error: runs %d, intents %d, open %v, running %v; want no run, the intent pending and open",
			runs, intents(), p.log.Open(in.Key), p.exec)
	}
	if _, err := a.do(context.Background(), p, in, effect); err == nil || !strings.Contains(err.Error(), "injected lane-row failure") {
		t.Fatalf("a retry while the write fails = %v, want the lane-row error", err)
	}
	if runs != 0 || intents() != 1 || !p.log.Open(in.Key) {
		t.Fatalf("after the failing retry: runs %d, intents %d, open %v; want no run, the one intent open",
			runs, intents(), p.log.Open(in.Key))
	}

	if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.do(context.Background(), p, in, effect); err != nil || string(got) != `"ok"` || runs != 1 {
		t.Fatalf("the retry once the write works = %s, %v, runs %d; want the effect's result once", got, err, runs)
	}
	if rows, err := store.ReadLanes(p.log.DB()); err != nil || len(rows) != 1 || rows[0].Task != "T-1" {
		t.Errorf("lanes table after the retry = %+v, %v; want L-01's row", rows, err)
	}
	if got, err := a.do(context.Background(), p, in, effect); err != nil || string(got) != `"ok"` || runs != 1 || intents() != 1 {
		t.Errorf("a repeat = %s, %v, runs %d, intents %d; want the stored result, no run, one intent", got, err, runs, intents())
	}
}

// The replay probe (docs/design.md §6.6 rule 2): a store's records folded the way a restart folds them
// (foldRecord, as load does), the folded state written as text. Run on the same store before and after a change to a
// fold (a compat branch deleted, a record shape merged), the two texts must be equal: the change folds every record
// the store holds the same. The probe reads a copy of a store as `obor serve dump` writes it (JSONL, one record per
// line); it is skipped without one:
//
//	obor serve dump > /tmp/store.jsonl
//	OBOR_REPLAY_DUMP=/tmp/store.jsonl OBOR_REPLAY_OUT=/tmp/before.txt go test ./internal/app -run TestReplayProbe
//	# change the folds, then:
//	OBOR_REPLAY_DUMP=/tmp/store.jsonl OBOR_REPLAY_WANT=/tmp/before.txt go test ./internal/app -run TestReplayProbe

// replayState is the state recs fold into: a project as load builds it (lead the lead agent's name), every record
// folded by foldRecord, written by stateText.
func replayState(recs []event.Record, lead string) string {
	p := &proj{root: "/replay", name: "replay", tasks: map[string]*watch{},
		out: outbox.New(lead, "claude"), timers: map[string]*jobTimer{}, replies: map[string]string{},
		rules: map[string]ruleLive{}, nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		exec: map[event.Key]time.Time{}, held: map[string]string{}, ended: map[string]bool{},
		roleFail: map[string]time.Time{}, reg: identity.New(), folded: newFolded()}
	for _, r := range recs {
		p.foldRecord(r)
	}
	return stateText(reflect.ValueOf(p).Elem())
}

// stateText writes v as one "path = value" line per leaf: pointers followed (a pointer met again on its own path is
// "<cycle>"), maps in the order of their keys' text, times in RFC 3339; funcs, chans, mutexes and the event log are
// left out (live, not folded).
func stateText(v reflect.Value) string {
	var b strings.Builder
	var walk func(path string, v reflect.Value, seen []uintptr)
	walk = func(path string, v reflect.Value, seen []uintptr) {
		if v.CanAddr() { // unexported fields: readable through their address
			v = reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
		}
		switch v.Kind() {
		case reflect.Func, reflect.Chan, reflect.UnsafePointer:
			return
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				fmt.Fprintf(&b, "%s = nil\n", path)
				return
			}
			if v.Kind() == reflect.Pointer {
				if v.Type() == reflect.TypeFor[*event.Log]() {
					return
				}
				if slices.Contains(seen, v.Pointer()) {
					fmt.Fprintf(&b, "%s = <cycle>\n", path)
					return
				}
				seen = append(seen, v.Pointer())
			}
			walk(path, v.Elem(), seen)
		case reflect.Struct:
			switch t := v.Type(); {
			case t == reflect.TypeFor[time.Time]():
				fmt.Fprintf(&b, "%s = %s\n", path, v.Interface().(time.Time).UTC().Format(time.RFC3339Nano))
			case t.PkgPath() == "sync":
			default:
				for i := range v.NumField() {
					walk(path+"."+t.Field(i).Name, v.Field(i), seen)
				}
			}
		case reflect.Map:
			keys := v.MapKeys()
			text := func(k reflect.Value) string { return fmt.Sprint(k.Interface()) }
			slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(text(a), text(b)) })
			fmt.Fprintf(&b, "%s = map[%d]\n", path, len(keys))
			for _, k := range keys {
				e := reflect.New(v.Type().Elem()).Elem() // addressable, so its unexported fields read too
				e.Set(v.MapIndex(k))
				walk(path+"["+text(k)+"]", e, seen)
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 { // json.RawMessage and other bytes: as text
				fmt.Fprintf(&b, "%s = %q\n", path, v.Bytes())
				return
			}
			fmt.Fprintf(&b, "%s = len %d\n", path, v.Len())
			for i := range v.Len() {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i), seen)
			}
		default:
			fmt.Fprintf(&b, "%s = %v\n", path, v.Interface())
		}
	}
	walk("p", v, nil)
	return b.String()
}

// readDump is the records of a JSONL dump (obor serve dump), in its order.
func readDump(t *testing.T, path string) []event.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []event.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var r event.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("%s: record %d: %v", path, len(recs)+1, err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

// TestReplayProbe folds the dump OBOR_REPLAY_DUMP names (lead: OBOR_REPLAY_LEAD, default "lead"): its state written to
// OBOR_REPLAY_OUT, and compared with OBOR_REPLAY_WANT's (the first differing line named). Skipped without a dump.
func TestReplayProbe(t *testing.T) {
	dump := os.Getenv("OBOR_REPLAY_DUMP")
	if dump == "" {
		t.Skip("no OBOR_REPLAY_DUMP: the replay probe runs on a store's dump by hand")
	}
	recs := readDump(t, dump)
	if len(recs) == 0 {
		t.Fatalf("precondition: %s holds no record", dump)
	}
	got := replayState(recs, cmp.Or(os.Getenv("OBOR_REPLAY_LEAD"), "lead"))
	if out := os.Getenv("OBOR_REPLAY_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d records folded, %d state lines", len(recs), strings.Count(got, "\n"))
	if want := os.Getenv("OBOR_REPLAY_WANT"); want != "" {
		b, err := os.ReadFile(want)
		if err != nil {
			t.Fatal(err)
		}
		if diff := firstDiff(string(b), got); diff != "" {
			t.Errorf("the folded state differs from %s: %s", want, diff)
		}
	}
}

// firstDiff names the first line where want and got differ ("": equal) and how many lines differ in all.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	first, n := "", 0
	for i := range max(len(w), len(g)) {
		a, b := "", ""
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			if n == 0 {
				first = fmt.Sprintf("line %d: want %q, got %q", i+1, a, b)
			}
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%s (%d lines differ)", first, n)
}

// The probe's own oracle: the same records fold to the same text, a record that changes a fold changes it (a verify's
// brief: an open verify brief in the review fold), and the verify records of an older obor (review.fix,
// review.verify.request) fold as nothing.
func TestReplayStateText(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	startedReview(t, r)
	recs := readLog(t, r.p.root)
	if len(recs) == 0 {
		t.Fatal("precondition: the rig's log holds no record")
	}
	before := replayState(recs, "pj-lead")
	if again := replayState(recs, "pj-lead"); again != before {
		t.Fatalf("the same records folded twice differ: %s", firstDiff(before, again))
	}
	if !strings.Contains(before, "p.folded.rv.reviews[1].id = RV-5\n") || !strings.Contains(before, "p.folded.rv.open = map[0]\n") {
		t.Fatalf("the review start is not in the state:\n%s", before)
	}
	for _, step := range []string{"review.fix", "review.verify.request"} {
		r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: step, Kind: event.KindFact},
			review.Data{ID: "RV-5", Tasks: []string{"T-a"}, Note: "check #2", By: "obor", Call: "f1", Reply: "FIX LINKED"})
	}
	old := readLog(t, r.p.root)
	if len(old) != len(recs)+2 {
		t.Fatalf("precondition: %d records after the old ones, want %d", len(old), len(recs)+2)
	}
	if d := firstDiff(before, replayState(old, "pj-lead")); d != "" {
		t.Errorf("an older obor's review.fix and review.verify.request changed the state: %s", d)
	}
	if _, ok := r.a.recordBrief(r.p, loadCfg(t, r.p.root), "pj-rev", "obor", "verify", "", "", false,
		&verifyBrief{tasks: []string{"T-a"}, head: headSHA}); !ok {
		t.Fatal("the verify brief was not recorded")
	}
	after := replayState(readLog(t, r.p.root), "pj-lead")
	if d := firstDiff(before, after); d == "" || !strings.Contains(after, "p.folded.rv.open = map[1]\n") {
		t.Errorf("a verify's brief left the state as it was (diff %q)", d)
	}
}

// A pending self-restart holds nothing back. While it waits, lane tests start and end in turn, and the restart
// goes in the first pass where none runs, never cutting one.
func TestPendingRestartGoesInTheFirstGap(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	f := r.fakes["pj"]
	// T-1's lane test waits for its own release; T-5's gate waits, so its candidate comes later, while the restart
	// is pending.
	f.hold, f.holdTest = "T-5", "T-1"
	f.unbased = false // a green gate's candidate has a Base: its lane test is next
	f.testRelease = make(chan struct{})

	// T-1's gate is green with a Base: its lane test is the work running when the new binary appears.
	r.add("pj", "T-1", event.StepGate, event.KindIntent, event.GateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", "T-1", event.StepGate, event.KindResult, event.GateOut{Outcome: outGreen, Candidate: port.Candidate{
		Task: "T-1", Lane: "L-01", SHA: "c-T-1", Target: "b", Base: "b0", Patch: "p1"}})

	var calls atomic.Int32
	r.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Kit: r.kit,
			// serve ends on the restart, as its exec does.
			Restart: func(context.Context) error { calls.Add(1); r.cancel(); return nil }}), nil
	}
	r.a.opt.Binary = func() (NewBinary, bool) { return NewBinary{Path: "/kit/dist/obor"}, true }
	r.run()
	defer r.stop()

	// Precondition: T-1's lane test runs, its result not yet folded; no restart.
	r.waitFor("T-1's lane test", func() bool { return f.count(f.tests, "T-1") == 1 })
	if s := r.state("pj", "T-1"); s != stCandidate {
		t.Fatalf("precondition: T-1 is %s, want candidate", s)
	}

	// A tick sees a new binary: the restart is pending, and it waits for the running test.
	r.a.mu.Lock()
	r.a.follows++
	r.a.mu.Unlock()
	r.wake()
	r.waitFor("the pending restart shown", func() bool { return r.showsRestart("pj") })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times with T-1's lane test running, want 0", n)
	}

	// T-5's gate goes green while the restart waits: its lane test starts beside the pending restart (no
	// drain), runs and ends, and the restart holds for it.
	close(f.release)
	r.waitFor("T-5's lane test", func() bool { return f.count(f.tests, "T-5") == 1 })
	r.waitFor("T-5 tested", func() bool { return r.count("pj", "T-5", event.KindResult, event.StepTest) == 1 })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times while T-1's lane test runs, want 0", n)
	}

	// A task submitted now (T-6) goes through its gate to a lane test beside the pending restart, in turn with
	// T-5's: the restart still waits.
	r.add("pj", "T-6", event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-06"})
	r.add("pj", "T-6", event.StepSubmit, event.KindResult, event.SubmitOut{SHA: "s6"})
	r.waitFor("T-6's lane test", func() bool { return f.count(f.tests, "T-6") == 1 })
	r.waitFor("T-6 tested", func() bool { return r.count("pj", "T-6", event.KindResult, event.StepTest) == 1 })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times with a lane test running or just ended, want 0", n)
	}

	// T-1's test ends: the first pass where nothing runs, and the restart goes (a test still running at followWait
	// would be cut; these end first).
	close(f.testRelease)
	r.waitFor("the restart", func() bool { return calls.Load() == 1 })

	// No lane test was cut: each one's green result is folded, and none failed.
	for _, task := range []string{"T-1", "T-5", "T-6"} {
		if n := r.count("pj", task, event.KindResult, event.StepTest); n != 1 {
			t.Errorf("%s's test results %d, want the one green result it folded", task, n)
		}
	}
	if n := r.count("pj", "T-1", event.KindFact, event.StepFailed) +
		r.count("pj", "T-5", event.KindFact, event.StepFailed) +
		r.count("pj", "T-6", event.KindFact, event.StepFailed); n != 0 {
		t.Errorf("failed steps %d, want none: a cut lane test would be one", n)
	}
}

// What follows an appended record is decided from the record and the fold's change alone (App.appended), at every
// site that appends: the test hook on every record but an intent; the task's step logs when the fold ended it (a
// rejected task and a merged one with after-merge steps to run are not ended); a fact's waits (cutEndFact: every wait
// of its task) and a result's own step's wait; the pending intents of the task a merged or dropped fact ends; and the
// lane rows' write, whose failure is the call's error whatever the record.
func TestAppendedReactions(t *testing.T) {
	t.Parallel()
	open := func(t *testing.T, h *harness) { // T-1 submitted, its submit intent pending
		addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-01", Agent: "pj-l-01"})
		addRecord(t, h, "T-1", event.StepSubmit, event.KindIntent, map[string]string{"lane": "L-01"})
	}
	merged := func(t *testing.T, h *harness) { // T-1 merged, after_merge done, its stop's intent pending
		open(t, h)
		addRecord(t, h, "T-1", event.StepMerged, event.KindFact, event.MergedData{Head: "abcdef0"})
		addRecord(t, h, "T-1", event.StepAfterMerge, event.KindIntent, nil)
		addRecord(t, h, "T-1", event.StepAfterMerge, event.KindResult, "done")
		addRecord(t, h, "T-1", event.StepStop, event.KindIntent, nil)
	}
	reviewing := func(t *testing.T, h *harness) { // T-1's review job out, a superseded intent pending
		addRecord(t, h, "T-1", event.StepDispatched, event.KindFact,
			event.Dispatch{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01", Branch: "task/T-1"}})
		addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-01", Agent: "pj-l-01"})
		addRecord(t, h, "T-1", event.StepSubmit, event.KindIntent, map[string]string{"lane": "L-01"})
		addRecord(t, h, "T-1", event.StepGate, event.KindIntent, event.GateIn{})
		addRecord(t, h, "T-1", event.StepGate, event.KindResult,
			event.GateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-1", SHA: "c", Patch: "p"}})
		addRecord(t, h, "T-1", event.StepReview, event.KindIntent, event.Job{Patch: "p"})
	}
	reject := verdictStep(wire.MethodReject, "rj")
	for _, c := range []struct {
		name      string
		seed      func(*testing.T, *harness)
		r         event.Record
		data      any
		failLanes bool   // the lane rows' write fails
		state     string // T-1's after the fold
		hooked    bool   // hookAppended fired for r
		logsGo    bool   // T-1's step logs are removed
		cuts      string // the waits that end: "" none, "own" r's step's, "task" every wait of T-1
		settled   int    // T-1's pending intents r's fold settles as not run
	}{
		{name: "an intent", seed: open, r: event.Record{Step: event.StepGate, Kind: event.KindIntent}, data: event.GateIn{},
			state: stSubmitted},
		{name: "a result that ends nothing", seed: open, r: event.Record{Step: event.StepSubmit, Kind: event.KindResult},
			data: event.SubmitOut{Refused: "no"}, state: stSubmitted, hooked: true, cuts: "own"},
		{name: "the last after-merge step's result: the task ended", seed: merged,
			r: event.Record{Step: event.StepStop, Kind: event.KindResult}, data: "done", state: stMerged, hooked: true,
			logsGo: true, cuts: "own"},
		{name: "a reject's result: not ended, nothing settled", seed: reviewing,
			r: event.Record{Step: reject, Kind: event.KindResult}, data: event.VerdictResult{Job: "T-1/1/review", Note: "no"},
			state: stRejected, hooked: true, cuts: "own"},
		{name: "a watchdog fact", seed: open, r: event.Record{Step: event.StepNeedsYou, Kind: event.KindFact},
			data: event.FactData{Lane: "L-01", Reason: reasonAsks + "which file"}, state: stSubmitted, hooked: true,
			cuts: "task"},
		{name: "a merged fact: after-merge steps to run", seed: open,
			r: event.Record{Step: event.StepMerged, Kind: event.KindFact}, data: event.MergedData{Head: "abcdef0"},
			state: stMerged, hooked: true, cuts: "task", settled: 1},
		{name: "a dropped fact: the task ended", seed: open,
			r: event.Record{Step: event.StepDropped, Kind: event.KindFact}, data: event.FactData{Reason: "not needed"},
			state: stDropped, hooked: true, logsGo: true, cuts: "task", settled: 1},
		{name: "an intent, the lane rows' write failing", seed: open,
			r: event.Record{Step: event.StepGate, Kind: event.KindIntent}, data: event.GateIn{}, failLanes: true,
			state: stSubmitted},
		{name: "a fact, the lane rows' write failing", seed: open,
			r:    event.Record{Step: event.StepNeedsYou, Kind: event.KindFact},
			data: event.FactData{Lane: "L-01", Reason: reasonAsks + "which file"}, failLanes: true, state: stSubmitted,
			hooked: true, cuts: "task"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := logsHarness(t)
			p := h.proj()
			c.seed(t, h)
			c.r.Task, c.r.Attempt = "T-1", 1
			log := logFile(t, h, "T-1", "check")
			own, sibling, foreign := c.r.Key(), event.Key{Task: "T-1", Attempt: 1, Step: "sibling"},
				event.Key{Task: "T-2", Attempt: 1, Step: event.StepGate}
			for _, k := range []event.Key{own, sibling, foreign} {
				p.cuts[k] = &cutWait{n: 1, timer: time.NewTimer(time.Hour)}
			}
			pending := len(p.log.Pending())
			if pending == 0 || p.taskEnded("T-1") || !p.cutWaits(own) || !p.cutWaits(sibling) {
				t.Fatalf("precondition: %d pending intents, T-1 ended %v, waits %v/%v; want pending, open, waiting",
					pending, p.taskEnded("T-1"), p.cutWaits(own), p.cutWaits(sibling))
			}
			if c.failLanes {
				p.folded.lr.rows["L-09"] = store.LaneRow{Lane: "L-09", Task: "T-9"} // a row to write: the write fails
				if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes BEGIN SELECT RAISE(ABORT, 'injected lane-row failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			var hooks atomic.Int32
			hook := func(point string) {
				if point == hookAppended {
					hooks.Add(1)
				}
			}
			h.a.hooked.Store(&hook)
			c.r.Data, _ = json.Marshal(c.data)
			settledSoFar := func() int {
				n := 0
				for _, x := range h.records() {
					if x.Task == "T-1" && notRunResult(x) {
						n++
					}
				}
				return n
			}
			before := settledSoFar()
			_, err := Append(h.a, h.root, c.r, h.now)
			none := func(string) {}
			h.a.hooked.Store(&none)

			if got := p.folded.loop["T-1"].state; got != c.state {
				t.Errorf("T-1 is %s, want %s", got, c.state)
			}
			if (err != nil) != c.failLanes {
				t.Errorf("appended: %v; want an error %v (the lane rows' write fails)", err, c.failLanes)
			}
			settled := settledSoFar() - before
			if settled != c.settled {
				t.Errorf("settled %d of T-1's pending intents, want %d", settled, c.settled)
			}
			// The hook fires for r and for each not-run result its settlement appends (results, through appended).
			if want := settled; c.hooked && hooks.Load() != int32(1+want) || !c.hooked && hooks.Load() != 0 {
				t.Errorf("hookAppended fired %d times, want r's (%v) and %d settlements'", hooks.Load(), c.hooked, want)
			}
			if _, err := os.Stat(log); os.IsNotExist(err) != c.logsGo {
				t.Errorf("T-1's step log removed: %v, want %v", os.IsNotExist(err), c.logsGo)
			}
			if got, want := !p.cutWaits(own), c.cuts != ""; got != want {
				t.Errorf("r's own wait ended: %v, want %v", got, want)
			}
			if got, want := !p.cutWaits(sibling), c.cuts == "task"; got != want {
				t.Errorf("T-1's other wait ended: %v, want %v", got, want)
			}
			if !p.cutWaits(foreign) {
				t.Error("T-2's wait ended")
			}
		})
	}
}
