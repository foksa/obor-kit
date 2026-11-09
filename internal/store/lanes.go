package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// The lanes table (T-439): each lane's dispatched task, a projection serve rewrites from the event log's fold on load
// and on every change (WriteLanes); no other writer. Hooks and CLIs read it read-only (ReadLanesAt, ReadLaneAt):
// the guard's protected unlocks, whoami, the session hooks, the worker Stop hook, obor status.

// LaneRow is one lane's row: the task its last prepared dispatch put there (lane.prepare's branch, base: the target
// sha; the lane's agent is derived from the lane, roles.Role.AgentName, never stored: T-1031) and when it was dispatched (unix seconds). Head is the lane commit the task's last submit pinned (T-1015:
// what the gate merges; "": no submission since the prepare): port.FreeState frees a landed lane only at that HEAD. Finished is when the attempt's
// task_done was recorded (unix seconds; 0: the task awaits it). Type is the model ref its dispatch chose,
// `<model>[:<effort>]` (T-721; "": a dispatch recorded before T-721). Blocked is serve's answer whether the task's
// worker has a question in force (its Blocked: report; T-894: the fold's, not a hook's own reading of the log).
// Landed is serve's answer whether the row's task has landed (T-1012: merged, by the gate or by hand as serve's
// merge-evidence read records it, or dropped): the one free rule, port.FreeState, reads it instead of the main
// checkout's history.
type LaneRow struct {
	Lane, Task, Branch     string
	Base, Head             string
	DispatchedAt, Finished int64
	Type                   string
	Blocked                bool
	Landed                 bool
}

// laneCols is the select list of a lane row in db: a lanes table from before schema 9 (a serve not restarted since
// T-721) has no type column, one from before schema 12 (T-894) no blocked one and one from before schema 13 (T-1012)
// no landed one; its rows read none, false and false.
func laneCols(db *sql.DB) (string, error) {
	typ, err := laneCol(db, "type", "''")
	if err != nil {
		return "", err
	}
	blocked, err := laneCol(db, "blocked", "0")
	if err != nil {
		return "", err
	}
	landed, err := laneCol(db, "landed", "0")
	if err != nil {
		return "", err
	}
	return "lane, task, branch, base, head, dispatched_at, finished, " + typ + ", " + blocked + ", " + landed, nil
}

// laneCol is col, or missing when the lanes table does not have it.
func laneCol(db *sql.DB, col, missing string) (string, error) {
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('lanes') WHERE name = ?", col).Scan(&n); err != nil {
		return "", fmt.Errorf("lanes: %w", err)
	}
	if n == 0 {
		return missing, nil
	}
	return col, nil
}

// scanLane reads one row of laneCols' select list into r.
func scanLane(scan func(...any) error, r *LaneRow) error {
	return scan(&r.Lane, &r.Task, &r.Branch, &r.Base, &r.Head, &r.DispatchedAt, &r.Finished, &r.Type,
		&r.Blocked, &r.Landed)
}

// ReadLanes is db's lane rows, by lane.
func ReadLanes(db *sql.DB) ([]LaneRow, error) {
	cols, err := laneCols(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT " + cols + " FROM lanes ORDER BY lane")
	if err != nil {
		return nil, fmt.Errorf("lanes: %w", err)
	}
	defer rows.Close()
	var out []LaneRow
	for rows.Next() {
		var r LaneRow
		if err := scanLane(rows.Scan, &r); err != nil {
			return nil, fmt.Errorf("lanes: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WriteLanes replaces db's lane rows with rows, in one transaction (serve's projection). The agent column gets ”
// (T-1031: the agent is derived; the column stays for v0.31's readers of an older row and goes in a later schema).
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
		if _, err := tx.Exec(`INSERT INTO lanes (lane, task, branch, agent, base, head, dispatched_at, finished, type,
			blocked, landed) VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`, r.Lane, r.Task, r.Branch, r.Base,
			r.Head, r.DispatchedAt, r.Finished, r.Type, r.Blocked, r.Landed); err != nil {
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
	db, err := openROAt(root)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return map[string]LaneRow{}, nil
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
	db, err := openROAt(root)
	if err != nil {
		return LaneRow{}, false, err
	}
	if db == nil {
		return LaneRow{}, false, nil
	}
	defer db.Close()
	cols, err := laneCols(db)
	if err != nil {
		return LaneRow{}, false, err
	}
	var r LaneRow
	err = scanLane(db.QueryRow("SELECT "+cols+" FROM lanes WHERE lane = ?", lane).Scan, &r)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return LaneRow{}, false, nil
	case err != nil:
		return LaneRow{}, false, fmt.Errorf("lanes: %w", err)
	}
	return r, true, nil
}
