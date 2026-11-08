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
	"obor/internal/store"
)

func clock() time.Time { return logNow }

// logDo is one effect under its key, the shape serve uses (App.do): Begin, the effect outside the log, Finish; a key
// with a result gets it and runs nothing, a failed effect writes no result (the intent stays pending).
func logDo(l *event.Log, in event.Intent, effect func(context.Context) (any, error)) (json.RawMessage, error) {
	r, done, _, err := l.Begin(in, clock())
	if err != nil {
		return nil, err
	}
	if done {
		return r.Data, nil
	}
	v, err := effect(context.Background())
	if err != nil {
		l.Finish(in.Key, nil, false, clock())
		return nil, err
	}
	res, err := l.Finish(in.Key, v, true, clock())
	return res.Data, err
}

func TestDoRunsOnceAndReturnsStoredResult(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	runs := 0
	effect := func(context.Context) (any, error) {
		runs++
		return map[string]string{"sha": "abc"}, nil
	}
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: "publish"}, Class: event.ClassGit, Data: map[string]string{"target": "main"}}
	got, err := logDo(l, in, effect)
	if err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Fatalf("Do = %s, %v, runs %d", got, err, runs)
	}
	if got, err := logDo(l, in, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("repeated Do = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}

	// After a restart the stored result still answers the key; nothing is pending.
	l, recs := openLog(t, root)
	if len(recs) != 2 || recs[0].Kind != event.KindIntent || recs[0].Class != event.ClassGit ||
		string(recs[0].Data) != `{"target":"main"}` || recs[1].Kind != event.KindResult {
		t.Fatalf("replay = %+v", recs)
	}
	if p := l.Pending(); len(p) != 0 {
		t.Errorf("pending after a result = %+v", p)
	}
	if got, err := logDo(l, in, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("Do after replay = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}
}

// A failed effect writes no result: the intent is pending and a retry runs the effect again.
func TestDoFailedEffectStaysPending(t *testing.T) {
	t.Parallel()
	l, _ := openLog(t, logRoot(t))
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: "prompt"}, Class: event.ClassPrompt}
	if _, err := logDo(l, in, func(context.Context) (any, error) { return nil, errors.New("herdr down") }); err == nil {
		t.Fatal("a failed effect returned no error")
	}
	if p := l.Pending(); len(p) != 1 || p[0].Step != "prompt" || p[0].Class != event.ClassPrompt {
		t.Errorf("pending = %+v", p)
	}
	runs := 0
	if _, err := logDo(l, in, func(context.Context) (any, error) { runs++; return "sent", nil }); err != nil || runs != 1 {
		t.Errorf("retry = %v, runs %d", err, runs)
	}
	if p := l.Pending(); len(p) != 0 {
		t.Errorf("pending after the retry = %+v", p)
	}
}

// A process killed after the effect and before its result: replay lists the intent as pending with its class.
func TestDoCrashBetweenEffectAndResult(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("OBOR_TEST_DO_CRASH"); root != "" {
		db, err := store.OpenRW(event.DBFile(root))
		if err != nil {
			os.Exit(2)
		}
		l, _, err := event.OpenLog(db)
		if err != nil {
			os.Exit(2)
		}
		path := filepath.Join(root, "effect")
		logDo(l, event.Intent{Key: event.Key{Task: "T-1", Attempt: 2, Step: "start"}, Class: event.ClassStart, Data: map[string]string{"pane": "w1:p1"}},
			func(context.Context) (any, error) {
				os.WriteFile(path, nil, 0o644)
				os.Exit(3) // killed after the effect, before the result
				return nil, nil
			})
		os.Exit(4)
	}
	root := logRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDoCrashBetweenEffectAndResult$")
	cmd.Env = append(os.Environ(), "OBOR_TEST_DO_CRASH="+root)
	var ee *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("child = %v, want exit 3 inside the effect", err)
	}
	if _, err := os.Stat(filepath.Join(root, "effect")); err != nil {
		t.Fatalf("precondition: the effect ran: %v", err)
	}
	l, recs := openLog(t, root)
	if len(recs) != 1 || recs[0].Kind != event.KindIntent {
		t.Fatalf("replay = %+v; want the intent only", recs)
	}
	p := l.Pending()
	if len(p) != 1 || p[0].Task != "T-1" || p[0].Attempt != 2 || p[0].Step != "start" || p[0].Class != event.ClassStart ||
		string(p[0].Data) != `{"pane":"w1:p1"}` {
		t.Errorf("pending = %+v", p)
	}
}

// The replay probe (T-869, DD-architecture R3, §6.6 rule 2): a store's records folded the way a restart folds them
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
// brief: an open verify brief in the review fold), and the verify records of an older obor (T-924: review.fix,
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
			ReviewData{ID: "RV-5", Tasks: []string{"T-a"}, Note: "check #2", By: "obor", Call: "f1", Reply: "FIX LINKED"})
	}
	old := readLog(t, r.p.root)
	if len(old) != len(recs)+2 {
		t.Fatalf("precondition: %d records after the old ones, want %d", len(old), len(recs)+2)
	}
	if d := firstDiff(before, replayState(old, "pj-lead")); d != "" {
		t.Errorf("an older obor's review.fix and review.verify.request changed the state: %s", d)
	}
	if _, ok := r.a.recordBrief(r.p, "pj-rev", "obor", "verify", "", "", false,
		&verifyBrief{tasks: []string{"T-a"}, head: headSHA}); !ok {
		t.Fatal("the verify brief was not recorded")
	}
	after := replayState(readLog(t, r.p.root), "pj-lead")
	if d := firstDiff(before, after); d == "" || !strings.Contains(after, "p.folded.rv.open = map[1]\n") {
		t.Errorf("a verify's brief left the state as it was (diff %q)", d)
	}
}

// T-972: a pending self-restart holds nothing back. While it waits, lane tests start and end in turn, and the restart
// goes in the first pass where none runs, never cutting one. (T-965's drain and the 10-minute deadline are gone.)
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
	r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{
		Task: "T-1", Lane: "L-01", SHA: "c-T-1", Target: "b", Base: "b0", Patch: "p1"}})

	var calls atomic.Int32
	r.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f, Kit: r.kit,
			// serve ends on the restart, as its exec does.
			Restart: func(context.Context) error { calls.Add(1); r.cancel(); return nil }}, nil
	}
	r.a.opt.Binary = func() bool { return true }
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

	// T-5's gate goes green while the restart waits: its lane test starts beside the pending restart (T-972: no
	// drain), runs and ends, and the restart holds for it.
	close(f.release)
	r.waitFor("T-5's lane test", func() bool { return f.count(f.tests, "T-5") == 1 })
	r.waitFor("T-5 tested", func() bool { return r.count("pj", "T-5", event.KindResult, event.StepTest) == 1 })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times while T-1's lane test runs, want 0", n)
	}

	// A task submitted now (T-6) goes through its gate to a lane test beside the pending restart, in turn with
	// T-5's: the restart still waits.
	r.add("pj", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06"})
	r.add("pj", "T-6", event.StepSubmit, event.KindResult, submitOut{SHA: "s6"})
	r.waitFor("T-6's lane test", func() bool { return f.count(f.tests, "T-6") == 1 })
	r.waitFor("T-6 tested", func() bool { return r.count("pj", "T-6", event.KindResult, event.StepTest) == 1 })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times with a lane test running or just ended, want 0", n)
	}

	// T-1's test ends: the first pass where nothing runs, however long it took, and the restart goes.
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
