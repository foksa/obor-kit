package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// The lanes table (T-439): each lane's dispatched task, a projection serve rewrites from the event log's fold on load
// and on every change (WriteLanes); no other writer. Hooks and CLIs read it read-only (ReadLanesAt, ReadLaneAt):
// the guard's protected unlocks, whoami, the session hooks, the worker Stop hook, obor status.

// LaneRow is one lane's row: the task its last prepared dispatch put there (lane.prepare's branch, agent, base: the
// target sha, head: merge-base(HEAD, target)) and when it was dispatched (unix seconds). Finished is when the attempt's
// task_done was recorded (unix seconds; 0: the task awaits it).
type LaneRow struct {
	Lane, Task, Branch, Agent string
	Base, Head                string
	DispatchedAt, Finished    int64
}

// ReadLanes is db's lane rows, by lane.
func ReadLanes(db *sql.DB) ([]LaneRow, error) {
	rows, err := db.Query("SELECT lane, task, branch, agent, base, head, dispatched_at, finished FROM lanes ORDER BY lane")
	if err != nil {
		return nil, fmt.Errorf("lanes: %w", err)
	}
	defer rows.Close()
	var out []LaneRow
	for rows.Next() {
		var r LaneRow
		if err := rows.Scan(&r.Lane, &r.Task, &r.Branch, &r.Agent, &r.Base, &r.Head, &r.DispatchedAt,
			&r.Finished); err != nil {
			return nil, fmt.Errorf("lanes: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WriteLanes replaces db's lane rows with rows, in one transaction (serve's projection).
func WriteLanes(db *sql.DB, rows []LaneRow) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("lanes: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM lanes"); err != nil {
		return fmt.Errorf("lanes: %w", err)
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO lanes (lane, task, branch, agent, base, head, dispatched_at, finished)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.Lane, r.Task, r.Branch, r.Agent, r.Base, r.Head, r.DispatchedAt,
			r.Finished); err != nil {
			return fmt.Errorf("lanes: row %s: %w", r.Lane, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lanes: %w", err)
	}
	return nil
}

// ReadLanesAt is the lane rows of the project at root by lane, read-only (hooks, CLIs: serve may be down). No
// database yet: none. An error: the database or its lanes table cannot be read (a serve not restarted since T-439
// has no lanes table).
func ReadLanesAt(root string) (map[string]LaneRow, error) {
	path := File(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return map[string]LaneRow{}, nil
	}
	db, err := OpenRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := ReadLanes(db)
	if err != nil {
		return nil, err
	}
	out := make(map[string]LaneRow, len(rows))
	for _, r := range rows {
		out[r.Lane] = r
	}
	return out, nil
}

// ReadLaneAt is lane's row in the project at root (ReadLanesAt): ok false when it has none.
func ReadLaneAt(root, lane string) (LaneRow, bool, error) {
	path := File(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return LaneRow{}, false, nil
	}
	db, err := OpenRO(path)
	if err != nil {
		return LaneRow{}, false, err
	}
	defer db.Close()
	var r LaneRow
	err = db.QueryRow("SELECT lane, task, branch, agent, base, head, dispatched_at, finished FROM lanes WHERE lane = ?",
		lane).Scan(&r.Lane, &r.Task, &r.Branch, &r.Agent, &r.Base, &r.Head, &r.DispatchedAt, &r.Finished)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return LaneRow{}, false, nil
	case err != nil:
		return LaneRow{}, false, fmt.Errorf("lanes: %w", err)
	}
	return r, true, nil
}

// HasBlockedReportAt reports whether task's latest worker Blocked: report is still awaiting the owner's answer.
// A blocked task_done is recorded as a needs-you fact rather than a submitted fact, so it does not set lanes.finished.
// The report lives one attempt (docs/state-model.md rows 12, 16, 16a): obor answer (an answered fact) and an unblocked
// fact end it, as do the lifecycle facts that begin another attempt — a task_done submission, a drop, a dispatch and
// its lane.prepare result, and a sendback. A resumed fact does not: ordinary worker activity is not an answer (T-511).
func HasBlockedReportAt(root, task string) (bool, error) {
	path := File(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	db, err := OpenRO(path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var asked, ended int64
	err = db.QueryRow(`SELECT
		COALESCE(MAX(CASE WHEN task = ? AND kind = 'fact' AND step = 'needs-you' AND
			json_extract(data, '$.reason') LIKE 'asks:%' THEN seq END), 0),
		COALESCE(MAX(CASE WHEN
			(task = ? AND kind = 'fact' AND step IN ('unblocked', 'submitted', 'dropped', 'dispatch', 'sendback'))
			OR (task = ? AND kind = 'result' AND (step = 'lane.prepare' OR step LIKE 'lane.prepare.%'))
			OR (task = 'msg' AND kind = 'fact' AND step = 'answered' AND json_extract(data, '$.task') = ?)
			THEN seq END), 0)
		FROM events WHERE task = ? OR task = 'msg'`, task, task, task, task, task).Scan(&asked, &ended)
	if err != nil {
		return false, fmt.Errorf("blocked report: %w", err)
	}
	return asked > ended, nil
}
