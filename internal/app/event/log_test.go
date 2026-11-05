package event

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"obor/internal/store"
)

// Dump writes the table as JSONL, one record per line in seq order, each line the record's JSON.
func TestDump(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	want := []Record{
		{Seq: 1, TS: "2026-10-24T11:00:00Z", Task: "T-1", Attempt: 1, Step: "gate", Kind: KindIntent, Class: ClassGit,
			Data: json.RawMessage(`{"kind":"lane","lane":"L-01"}`)},
		{Seq: 2, TS: "2026-10-24T11:02:00Z", Task: "T-1", Attempt: 1, Step: "gate", Kind: KindResult,
			Data: json.RawMessage(`{"outcome":"green"}`)},
		{Seq: 3, TS: "2026-10-24T11:03:00Z", Task: "T-2", Step: StepResumed, Kind: KindFact},
	}
	if err := insert(l.db, want...); err != nil {
		t.Fatal(err)
	}
	var dump bytes.Buffer
	if err := Dump(&dump, root); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, r := range want {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b = append(append(b, line...), '\n')
	}
	if dump.String() != string(b) {
		t.Errorf("dump =\n%s\nwant\n%s", dump.Bytes(), b)
	}
}

// logRoot is a project root with an empty state dir: no database yet.
func logRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// openLog opens root's database as serve does and replays it; closed when the test ends.
func openLog(t *testing.T, root string) (*Log, []Record) {
	t.Helper()
	db, err := store.OpenRW(DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l, recs, err := OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	return l, recs
}

// T-881: ReadMerges takes the merge facts only (the store filters them before decoding), in seq order: a merge
// recorded outside the app (no head) is none, and other records' steps are not read. No store: no merges.
func TestReadMerges(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	if ms, err := ReadMerges(root); err != nil || len(ms) != 0 {
		t.Errorf("no database: %+v, %v; want none", ms, err)
	}
	l, _ := openLog(t, root)
	at := func(min string) time.Time {
		ts, err := time.Parse(time.RFC3339, "2026-11-05T10:"+min+":00Z")
		if err != nil {
			t.Fatalf("time %s: %v", min, err)
		}
		return ts
	}
	if err := insert(l.db,
		Record{Seq: 1, TS: "2026-11-05T10:00:00Z", Task: "T-1", Attempt: 1, Step: StepMerged, Kind: KindIntent,
			Data: json.RawMessage(`{"head":"ab"}`)}, // an intent that names a head: no fact
		Record{Seq: 2, TS: "2026-11-05T10:01:00Z", Task: "T-1", Attempt: 1, Step: StepMerged, Kind: KindFact,
			Data: json.RawMessage(`{"head":"cd"}`)},
		Record{Seq: 3, TS: "2026-11-05T10:02:00Z", Task: "T-2", Attempt: 1, Step: StepMerged, Kind: KindFact,
			Data: json.RawMessage(`{"outside":true}`)}, // a merge outside the app: no sha
		Record{Seq: 4, TS: "2026-11-05T10:03:00Z", Task: "T-2", Attempt: 1, Step: StepMerged, Kind: KindFact,
			Data: json.RawMessage(`{"head":"ef"}`)},
		Record{Seq: 5, TS: "2026-11-05T10:04:00Z", Task: "T-2", Attempt: 1, Step: StepResumed, Kind: KindFact},
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ms, err := ReadMerges(root)
	if err != nil {
		t.Fatalf("ReadMerges: %v", err)
	}
	want := []Merge{{Task: "T-1", SHA: "cd", At: at("01")}, {Task: "T-2", SHA: "ef", At: at("03")}}
	if !slices.Equal(ms, want) {
		t.Errorf("merges %+v, want %+v", ms, want)
	}
}
