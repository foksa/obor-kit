package project

import (
	"os"
	"path/filepath"
	"testing"

	"obor/internal/store"
)

// T-577: TranscriptOf reads the transcript the project recorded for the session first; a recorded file that is gone
// (or no project) falls back to the kinds' own places.
func TestTranscriptOfRecorded(t *testing.T) {
	root := t.TempDir()
	tr := filepath.Join(t.TempDir(), "elsewhere", "sid-12345678.jsonl")
	if err := os.MkdirAll(filepath.Dir(tr), 0o755); err != nil {
		t.Fatal(err)
	}
	env := func(string) (string, bool) { return "", false }
	if got := TranscriptOf(root, "sid-12345678", env); got != "" {
		t.Fatalf("precondition: nothing recorded, no file: %q", got)
	}
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (ts, task, attempt, step, kind, class, data) VALUES ('t', ?, 0, ?, 'fact', 'F', ?)`,
		store.SessionTask, store.StepSession, `{"agent":"pj-lead","id":"sid-12345678","transcript":"`+tr+`","started":1}`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got := TranscriptOf(root, "sid-12345678", env); got != "" {
		t.Errorf("recorded but no file: %q, want none", got)
	}
	if err := os.WriteFile(tr, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := TranscriptOf(root, "sid-12345678", env); got != tr {
		t.Errorf("recorded: %q, want %q", got, tr)
	}
	if got := TranscriptOf("", "sid-12345678", env); got == tr {
		t.Error("no project: the store is not read")
	}
}
