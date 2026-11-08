package app

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"obor/internal/app/event"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

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
		out: newOutbox(lead, "claude"), timers: map[string]*jobTimer{}, replies: map[string]string{},
		rules: map[string]ruleLive{}, nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		exec: map[event.Key]time.Time{}, held: map[string]string{}, ended: map[string]bool{},
		roleFail: map[string]time.Time{}, reg: newRegistry(), folded: newFolded()}
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
