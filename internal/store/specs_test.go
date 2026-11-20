package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSpecsSetShowList: a first set is rev 1, the same text again changes nothing, another text is the next
// rev (who and when with it, and the note it carried); readers see the current rev read-only, also once the
// writer is closed (serve down); a project without a database has none; an empty spec is refused.
func TestSpecsSetShowList(t *testing.T) {
	root := t.TempDir()
	if row, ok, err := ReadSpecAt(root, "T-1"); err != nil || ok {
		t.Fatalf("no database: %+v %v %v; want none", row, ok, err)
	}
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		task, text, note, by string
		rev                  int
		changed              bool
	}{
		{"T-1", "# T-1 — A\n", "", "lead", 1, true},
		{"T-1", "# T-1 — A\n", "", "lead", 1, false},
		{"T-2", "# T-2 — Other\n", "", "lead", 1, true},
		{"T-1", "# T-1 — B\n", "widen the guard", "hk-lead", 2, true},
		{"T-1", "# T-1 — B\n", "another note", "hk-lead", 2, false}, // the rev's note stays
	} {
		rev, changed, err := SetSpec(db, c.task, c.text, c.note, c.by, at)
		if err != nil || rev != c.rev || changed != c.changed {
			t.Errorf("SetSpec %s %q: rev %d changed %v %v; want rev %d changed %v", c.task, c.text, rev, changed, err,
				c.rev, c.changed)
		}
	}
	if _, _, err := SetSpec(db, "T-3", " \n", "", "lead", at); err == nil {
		t.Errorf("an empty spec stored")
	}
	db.Close() // serve down: the readers open the file read-only
	row, ok, err := ReadSpecAt(root, "T-1")
	if err != nil || !ok || row.Rev != 2 || row.Text != "# T-1 — B\n" || row.SetBy != "hk-lead" ||
		row.SetAt != "2026-10-27T09:00:00Z" || row.Note != "widen the guard" || row.Spec.Title != "B" {
		t.Errorf("ReadSpecAt T-1: %+v %v %v", row, ok, err)
	}
	if _, ok, err := ReadSpecAt(root, "T-3"); err != nil || ok {
		t.Errorf("ReadSpecAt T-3 (refused): %v %v; want none", ok, err)
	}
	rows, err := ReadSpecsAt(root)
	var got []string
	for _, r := range rows {
		got = append(got, r.Task+"@"+strconv.Itoa(r.Rev))
	}
	if err != nil || strings.Join(got, " ") != "T-1@2 T-2@1" {
		t.Errorf("ReadSpecsAt: %q %v; want T-1@2 T-2@1", got, err)
	}
}

// TestSpecsTableMissing: a database with no specs table (a serve that has not restarted) is a read error, never
// "no spec" (the file guard denies on it).
func TestSpecsTableMissing(t *testing.T) {
	root := t.TempDir()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE specs"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(filepath.Join(root, ".obor", "state", "obor.db")); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if _, ok, err := ReadSpecAt(root, "T-1"); err == nil {
		t.Errorf("ReadSpecAt without a specs table: ok %v, no error", ok)
	}
}
