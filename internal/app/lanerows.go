package app

import (
	"encoding/json"
	"slices"

	"obor/internal/store"
)

// The lane projection (T-439): each lane's row in the store's lanes table, derived from the log alone. A successful
// lane.prepare puts its task on the lane (and off any other lane), as does a submission from a lane whose row does not
// hold its task (a task dispatched before serve dispatched: no branch or base known), a dropped fact takes it off; finished is when the
// attempt's task_done was recorded. serve is the only writer: it writes the table after the load's replay and after
// every record that changes a row (syncLanes), so hooks and CLIs read one row instead of folding the log.

// laneRows are p's projected rows (under mu).
type laneRows struct {
	rows      map[string]store.LaneRow // by lane, Finished not yet set (rows)
	submitted map[string]int64         // by task: when its last submitted fact was recorded (unix seconds)
	written   []store.LaneRow          // the table as last written (nil and !synced: not yet)
	synced    bool
	err       error // the last write's error (nil: the table holds written)
	live      bool  // the load's replay is done: each fold writes the change
}

// foldLanes folds r into the projection (after foldLoop folded it).
func (p *proj) foldLanes(r Record) {
	if p.lr.rows == nil {
		p.lr.rows, p.lr.submitted = map[string]store.LaneRow{}, map[string]int64{}
	}
	switch {
	case r.Kind == KindFact && r.Step == StepDropped:
		for lane, row := range p.lr.rows {
			if row.Task == r.Task {
				delete(p.lr.rows, lane)
			}
		}
	case r.Kind == KindFact && r.Step == StepSubmitted:
		p.lr.submitted[r.Task] = recordTime(r).Unix()
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		mt := p.loop[r.Task]
		if d.Lane == "" || mt == nil || mt.state == stDropped || p.lr.rows[d.Lane].Task == r.Task {
			return
		}
		// A submission from a lane no row gives the task (dispatched before serve dispatched): the task is there.
		for lane, row := range p.lr.rows {
			if row.Task == r.Task {
				delete(p.lr.rows, lane)
			}
		}
		p.lr.rows[d.Lane] = store.LaneRow{Lane: d.Lane, Task: r.Task, Agent: d.Agent}
	case r.Kind == KindResult:
		step, _ := stepN(r.Step)
		mt := p.loop[r.Task]
		if step != StepPrepare || mt == nil || mt.attempt != r.Attempt || mt.state != stDispatched {
			return
		}
		var d prepareOut
		_ = json.Unmarshal(r.Data, &d)
		if d.Base == "" || d.Refused != "" {
			return
		}
		for lane, row := range p.lr.rows {
			if row.Task == r.Task {
				delete(p.lr.rows, lane) // a task is in one lane
			}
		}
		p.lr.rows[mt.disp.Lane] = store.LaneRow{Lane: mt.disp.Lane, Task: r.Task, Branch: mt.disp.Branch,
			Agent: mt.disp.Agent, Base: d.Base, Head: d.Head, DispatchedAt: mt.at.Unix()}
	}
}

// laneTable is p's rows by lane, finished set: 0 while the task awaits its task_done, else when its last submission
// was recorded (the dispatch's time when it ended without one: a merge outside, a reject).
func (p *proj) laneTable() []store.LaneRow {
	out := make([]store.LaneRow, 0, len(p.lr.rows))
	for _, row := range p.lr.rows {
		if mt := p.loop[row.Task]; mt != nil && !mt.awaitsTaskDone() {
			row.Finished = p.lr.submitted[row.Task]
			if row.Finished == 0 {
				row.Finished = max(row.DispatchedAt, 1)
			}
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b store.LaneRow) int {
		if a.Lane < b.Lane {
			return -1
		}
		if a.Lane > b.Lane {
			return 1
		}
		return 0
	})
	return out
}

// syncLanes writes p's rows when they differ from the table as last written (under mu); a failed write is kept in
// p.lr.err and tried again on the next change or check.
func (p *proj) syncLanes() error {
	if p.log == nil {
		return nil
	}
	rows := p.laneTable()
	if p.lr.synced && p.lr.err == nil && slices.Equal(rows, p.lr.written) {
		return nil
	}
	if p.lr.err = store.WriteLanes(p.log.db, rows); p.lr.err != nil {
		return p.lr.err
	}
	p.lr.written, p.lr.synced = rows, true
	return nil
}

// laneRowsErr is the error of the lanes table's last write, after one more try (under mu): a step whose record
// changed a row does not go on (a worker is not started on a missing or stale row) until the table is written.
func (p *proj) laneRowsErr() error {
	if p.lr.err == nil {
		return nil
	}
	return p.syncLanes()
}

// laneTask is the task lane's row holds ("" none; under mu): obor mcp's task_done names only the caller's lane.
func (p *proj) laneTask(lane string) string { return p.lr.rows[lane].Task }
