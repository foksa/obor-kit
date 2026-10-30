package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// The specs table (T-440): each task's spec, the text the lead set last (obor spec set); serve writes it (SetSpec),
// hooks and CLIs read it read-only (ReadSpecAt, ReadSpecsAt): the guard's protected unlocks, whoami, obor spec show and
// list, the flakes rule. A later set with another text is the next rev; the gate puts the current rev into every
// merge message of the task (T-521).

// SpecRow is one task's spec: its text and rev (1 for the first set), who set it and when (RFC 3339).
type SpecRow struct {
	Task, Text   string
	Rev          int
	SetBy, SetAt string
}

// SetSpec stores text as task's spec: rev 1 for a new task, the next rev when the text differs from the stored one,
// the stored rev unchanged when it does not (a repeated set). Its rev and whether it changed anything.
func SetSpec(db *sql.DB, task, text, by string, now time.Time) (int, bool, error) {
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
	if _, err := tx.Exec(`INSERT INTO specs (task, text, rev, set_by, set_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (task) DO UPDATE SET text = excluded.text, rev = excluded.rev, set_by = excluded.set_by,
		set_at = excluded.set_at`, task, text, rev, by, now.UTC().Format(time.RFC3339)); err != nil {
		return 0, false, fmt.Errorf("specs: %s: %w", task, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("specs: %w", err)
	}
	return rev, true, nil
}

// ReadSpec is task's spec in db: ok false when it has none.
func ReadSpec(db *sql.DB, task string) (SpecRow, bool, error) {
	var r SpecRow
	err := db.QueryRow("SELECT task, text, rev, set_by, set_at FROM specs WHERE task = ?", task).
		Scan(&r.Task, &r.Text, &r.Rev, &r.SetBy, &r.SetAt)
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
	rows, err := db.Query("SELECT task, text, rev, set_by, set_at FROM specs ORDER BY task")
	if err != nil {
		return nil, fmt.Errorf("specs: %w", err)
	}
	defer rows.Close()
	var out []SpecRow
	for rows.Next() {
		var r SpecRow
		if err := rows.Scan(&r.Task, &r.Text, &r.Rev, &r.SetBy, &r.SetAt); err != nil {
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
