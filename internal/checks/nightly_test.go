package checks

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/project"
	"obor/internal/store"
)

// T-1090: the nightly's merge reader. A project serve never made a store for is an error, so the sweep removes no
// merged task's log and prints that line; a store's merged facts come in seq order (the sweep keeps each task's last).
func TestSweepMerges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if ms, err := sweepMerges(root); err == nil || ms != nil {
		t.Fatalf("sweepMerges without a store = %+v, %v; want nil, an error", ms, err)
	}
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, r := range []struct{ task, ts, head string }{
		{"T-1", "2026-11-05T10:00:00Z", "ab"},
		{"T-2", "2026-11-06T10:00:00Z", "cd"},
		{"T-1", "2026-11-07T10:00:00Z", "ef"}, // its last merge
	} {
		b, _ := json.Marshal(map[string]string{"head": r.head})
		if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
			VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, 'fact', '', ?)`,
			r.ts, r.task, event.StepMerged, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	ms, err := sweepMerges(root)
	if err != nil {
		t.Fatalf("sweepMerges: %v", err)
	}
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	want := []project.Merge{
		{Task: "T-1", At: at("2026-11-05T10:00:00Z")},
		{Task: "T-2", At: at("2026-11-06T10:00:00Z")},
		{Task: "T-1", At: at("2026-11-07T10:00:00Z")},
	}
	if !slices.Equal(ms, want) {
		t.Errorf("sweepMerges = %+v, want %+v", ms, want)
	}
}
