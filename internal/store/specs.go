package store

import (
	"database/sql"
	"errors"
	"fmt"
	"obor/internal/spec"
	"os"
	"strings"
	"time"
)

// The specs table (T-440): each task's spec, the text the lead set last (obor spec set); serve writes it (SetSpec),
// hooks and CLIs read it read-only (ReadSpecAt, ReadSpecsAt): the guard's protected unlocks, whoami, obor spec show and
// list, the flakes rule. A later set with another text is the next rev; the gate puts the current rev into every
// merge message of the task (T-521).

// SpecRow is one task's spec: its text and rev (1 for the first set), who set it and when (RFC 3339), the note
// the set that made this rev carried (T-819: what changed; "" none, and for a rev set before it), and the text's
// parsed head (T-1022).
type SpecRow struct {
	Task, Text   string
	Rev          int
	SetBy, SetAt string
	Note         string
	Spec         spec.Spec
}

// specCols is the select list of a spec row in db: a specs table from before schema 10 (a serve not restarted since
// T-819, read by a newer CLI) has no note column, its rows read none.
func specCols(db *sql.DB) (string, error) {
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('specs') WHERE name = 'note'").Scan(&n); err != nil {
		return "", fmt.Errorf("specs: %w", err)
	}
	note := "note"
	if n == 0 {
		note = "''"
	}
	return "task, text, rev, set_by, set_at, " + note, nil
}

// scanSpec reads one row of specCols' select list into r, parsing its text (T-1022).
func scanSpec(scan func(...any) error, r *SpecRow) error {
	if err := scan(&r.Task, &r.Text, &r.Rev, &r.SetBy, &r.SetAt, &r.Note); err != nil {
		return err
	}
	r.Spec = spec.Parse(r.Text)
	return nil
}

// SetSpec stores text as task's spec: rev 1 for a new task, the next rev when the text differs from the stored one,
// the stored rev unchanged when it does not (a repeated set; its note stays the rev's). note is what changed (T-819,
// the line a working worker gets and task_done's refusal name). Its rev and whether it changed anything.
func SetSpec(db *sql.DB, task, text, note, by string, now time.Time) (int, bool, error) {
	switch {
	case task == "":
		return 0, false, errors.New("specs: no task")
	case strings.TrimSpace(text) == "":
		return 0, false, fmt.Errorf("specs: %s: an empty spec", task)
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("specs: %w", err)
	}
	defer tx.Rollback()
	var old string
	rev := 0
	err = tx.QueryRow("SELECT text, rev FROM specs WHERE task = ?", task).Scan(&old, &rev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return 0, false, fmt.Errorf("specs: %w", err)
	case old == text:
		return rev, false, nil
	}
	rev++
	if _, err := tx.Exec(`INSERT INTO specs (task, text, rev, set_by, set_at, note) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (task) DO UPDATE SET text = excluded.text, rev = excluded.rev, set_by = excluded.set_by,
		set_at = excluded.set_at, note = excluded.note`, task, text, rev, by, now.UTC().Format(time.RFC3339),
		strings.TrimSpace(note)); err != nil {
		return 0, false, fmt.Errorf("specs: %s: %w", task, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("specs: %w", err)
	}
	return rev, true, nil
}

// ReadSpec is task's spec in db: ok false when it has none.
func ReadSpec(db *sql.DB, task string) (SpecRow, bool, error) {
	cols, err := specCols(db)
	if err != nil {
		return SpecRow{}, false, err
	}
	var r SpecRow
	err = scanSpec(db.QueryRow("SELECT "+cols+" FROM specs WHERE task = ?", task).Scan, &r)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SpecRow{}, false, nil
	case err != nil:
		return SpecRow{}, false, fmt.Errorf("specs: %w", err)
	}
	return r, true, nil
}

// ReadSpecs is db's specs, by task.
func ReadSpecs(db *sql.DB) ([]SpecRow, error) {
	cols, err := specCols(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT " + cols + " FROM specs ORDER BY task")
	if err != nil {
		return nil, fmt.Errorf("specs: %w", err)
	}
	defer rows.Close()
	var out []SpecRow
	for rows.Next() {
		var r SpecRow
		if err := scanSpec(rows.Scan, &r); err != nil {
			return nil, fmt.Errorf("specs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReadSpecAt is task's spec in the project at root, read-only (hooks, CLIs: serve may be down). No database yet: none.
// An error: the database or its specs table cannot be read (a serve not restarted since T-440 has no specs table).
func ReadSpecAt(root, task string) (SpecRow, bool, error) {
	db, err := openROAt(root)
	if err != nil || db == nil {
		return SpecRow{}, false, err
	}
	defer db.Close()
	return ReadSpec(db, task)
}

// ReadSpecsAt is the specs of the project at root, by task, read-only (ReadSpecAt).
func ReadSpecsAt(root string) ([]SpecRow, error) {
	db, err := openROAt(root)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	return ReadSpecs(db)
}

// openROAt opens the database of the project at root read-only; nil, nil when it has none yet.
func openROAt(root string) (*sql.DB, error) {
	path := File(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return OpenRO(path)
}
