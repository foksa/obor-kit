// lanesync.go: lane.sync (row 24a): the target merged into the lane of an attempt sent back to its worker, before
// its fix job.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// syncOut is lane.sync's result: its outcome (port.Sync*: merged, current, conflict, dirty), the merge commit's sha
// (merged: the lane's new head), the target sha it merged or found, and the files a conflict or the lane's uncommitted
// changes name.
type syncOut struct {
	Outcome string   `json:"outcome"`
	SHA     string   `json:"sha,omitempty"`
	Target  string   `json:"target"`
	Files   []string `json:"files,omitempty"`
}

// syncDue: mt's attempt merges the target into its lane before its fix job (mt.sync: next started it on its worker)
// and lane.sync has no result yet, its fix job not out.
func (mt mtask) syncDue() bool { return mt.sync && mt.synced == nil && !mt.fixed }

// laneSync runs lane.sync (row 24a, G): the workflow's Sync merges the target into the task branch in the lane's
// worktree; every outcome is final and the fix job's text says it (syncLine). A pending intent runs again with its
// recorded input (the same target sha).
func (a *App) laneSync(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepSync}
	in, err := a.syncInput(ctx, p, fx, mt, key)
	if err != nil {
		return a.failed(ctx, p, mt, key.Step, err)
	}
	if _, err := a.do(ctx, p, event.Intent{Key: key, Class: event.ClassGit, Data: in}, func(ctx context.Context) (any, error) {
		o, err := fx.Workflow.Sync(ctx, in)
		if err != nil {
			return nil, err
		}
		switch o.Kind {
		case port.SyncMerged, port.SyncCurrent, port.SyncConflict, port.SyncDirty:
			return syncOut{Outcome: o.Kind, SHA: o.SHA, Target: in.Target, Files: o.Files}, nil
		}
		return nil, fmt.Errorf("unknown lane.sync outcome %q", o.Kind)
	}); err != nil {
		return a.failed(ctx, p, mt, key.Step, err)
	}
	return true
}

// syncInput is lane.sync's input under key: a pending intent's recorded one, else mt's lane, its worktree (the
// config's), the task branch and the target's sha read now. A lane the config does not name is an error.
func (a *App) syncInput(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key) (port.SyncInput, error) {
	var in port.SyncInput
	if r, ok := p.log.Intent(key); ok {
		if err := json.Unmarshal(r.Data, &in); err != nil {
			return in, fmt.Errorf("its intent's data: %w", err)
		}
		return in, nil
	}
	for _, l := range fx.Cfg.Lanes() {
		if l.Name == mt.lane {
			in.Dir = l.Dir
		}
	}
	if in.Dir == "" {
		return in, fmt.Errorf("lane %s is not in the config", Dash(mt.lane))
	}
	target, _, err := fx.Workflow.Target(ctx, mt.task)
	if err != nil {
		return in, err
	}
	in.Task, in.Attempt, in.Lane, in.Branch, in.Target = mt.task, mt.attempt, mt.lane, fx.Cfg.TaskBranch(mt.task), target
	return in, nil
}

// syncLine is the fix job's line on the attempt's lane.sync s, main the target branch ("": no lane.sync ran).
func syncLine(s *syncOut, main string) string {
	if s == nil {
		return ""
	}
	target := fmt.Sprintf("%s (%s)", main, sha7(s.Target))
	switch s.Outcome {
	case port.SyncMerged:
		return "serve merged " + target + " into your branch first."
	case port.SyncCurrent:
		return "Your branch already holds " + target + "."
	case port.SyncConflict:
		return fmt.Sprintf("serve did not merge %s into your branch: it conflicts in %s. Fix it on your branch as it "+
			"is; the integrator resolves the conflict at the gate.", target, strings.Join(s.Files, ", "))
	case port.SyncDirty:
		return fmt.Sprintf("serve did not merge %s into your branch: your lane has uncommitted changes (%s). Fix it "+
			"on your branch as it is.", target, strings.Join(s.Files, ", "))
	}
	return ""
}
