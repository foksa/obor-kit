package event

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"obor/internal/store"
)

// Dump writes the table as JSONL, one record per line in seq order, each line the record's JSON.
func TestDump(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	want := []Record{
		{Seq: 1, TS: "2026-10-24T11:00:00Z", Task: "T-1", Attempt: 1, Step: "gate", Kind: KindIntent, Class: ClassGit,
			Data: json.RawMessage(`{"kind":"lane","lane":"O-01"}`)},
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
