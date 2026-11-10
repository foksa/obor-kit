package checks

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
)

// restoreWait bounds the restore of a lane's branch after its test, also when serve's ctx ended (a stop cut the test:
// the lane goes back to its branch all the same).
const restoreWait = 30 * time.Second

// Test runs the class's checks for the candidate in.SHA (the slot's merge of the task onto in.Target) in the task's
// lane worktree (docs/state-model.md row 18b), the lane analogue of Gate:
//
//  1. The lane's dir from the config; uncommitted changes there are an error (the step runs again on a later pass;
//     the lane is not touched). Its branch is the one checked out, or in.Branch when the lane is detached already (a
//     test cut before its restore).
//  2. checkout --detach in.SHA (ignored files, the lane's build outputs, stay), lanes.links.
//  3. The changed files in.Target..in.SHA (OBOR_CHANGED_FILES), classified; lane_setup; the class's checks and the
//     guard (runChecks, as the gate runs them), holding a pool slot (prio worker, never the merge
//     marker: slots.max bounds how many lanes test at once), the lane's CPU budget in their environment. A red whose
//     failing tests and packages it names is rerun once by commands.retest at the same commit: green then is
//     the step's green, the flake logged as retried and passed.
//  4. What the checks changed discarded, checkout <branch>: the lane is where the worker left it (restore). A step that
//     generates or stages a file does not land it in the merge: the slot committed the candidate before any check.
//
// The outcome is final (green, red, fail); git I/O, the slot and a signal are errors (the step runs again: a function of
// the candidate, the target and the lane's ignored files).
func (s Scratch) Test(ctx context.Context, in port.TestInput) (out port.TestOutcome, err error) {
	s.gitCtx = ctx
	if s.Out == nil {
		s.Out = io.Discard
	}
	g := s.gio()
	dir := s.laneDir(in.Lane)
	if dir == "" {
		return port.TestOutcome{Kind: port.OutcomeFail, Line: "FAIL no lane " + in.Lane}, nil
	}
	st, err := g.Status(dir)
	if err != nil {
		return port.TestOutcome{}, err
	}
	if len(st) > 0 {
		return port.TestOutcome{}, fmt.Errorf("lane %s dirty (%d uncommitted path(s)): its test runs once it is clean",
			in.Lane, len(st))
	}
	branch, err := g.Branch(dir)
	if err != nil {
		return port.TestOutcome{}, err
	}
	if branch == "" {
		branch = in.Branch
	}
	if branch == "" {
		return port.TestOutcome{Kind: port.OutcomeFail,
			Line: fmt.Sprintf("FAIL lane %s is detached and its dispatch names no branch", in.Lane)}, nil
	}
	if err := g.Detach(dir, in.SHA); err != nil {
		return port.TestOutcome{}, err
	}
	defer func() {
		if rerr := s.restore(ctx, dir, branch); rerr != nil && err == nil {
			out, err = port.TestOutcome{}, rerr
		}
	}()
	linkLaneLinks(s.Cfg, s.Cfg.Root, dir)

	r := newRunner(ctx, &s.Options, dir, in.Task)
	r.changedFile = "lane-changed-files-" + in.Lane
	r.prio, r.pre, r.onCommand, r.retest = "worker", in.Target, in.Command, true
	// The lane's env: the generic facts and the project's lanes.env, and CHECK_CPUS the lane's CPU budget,
	// never the gate's cores / slots.max.
	r.env = append(r.env, config.LaneEnv(s.Cfg)...)
	r.env = append(r.env, "CHECK_CPUS="+strconv.Itoa(s.Cfg.LaneCPUs()))
	defer r.releaseSlot()
	for _, h := range []func() *halt{
		func() *halt { return r.changed(in.Task, in.Target+".."+in.SHA) },
		r.laneSetup,
		r.runChecks,
	} {
		if o, err := testHalt(&r, h()); o != nil || err != nil {
			return *o, err
		}
	}
	r.releaseSlot()
	checks := r.check
	return port.TestOutcome{Kind: port.OutcomeGreen, Checks: checks,
		Line: fmt.Sprintf("GREEN %s (%s, %s)", in.Task, checks, r.mode)}, nil
}

// testHalt is gateHalt's outcome as a lane test's: red or fail final, the rest errors; nil, nil when h is.
func testHalt(r *runner, h *halt) (*port.TestOutcome, error) {
	o, err := gateHalt(r, h)
	if o == nil {
		return nil, nil
	}
	return &port.TestOutcome{Kind: o.Kind, Line: o.Line}, err
}

// restore puts the lane back where the worker left it after its test, under a ctx of its own (restoreWait) when
// serve's ended: what the checks changed is discarded (reset --hard, clean -fd: the lane was clean when the test began,
// so a change there is the checks'; ignored files, build outputs, stay), then the lane's branch is checked out.
func (s Scratch) restore(ctx context.Context, dir, branch string) error {
	if ctx.Err() != nil {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreWait)
		defer cancel()
		s.gitCtx = c
	}
	g := s.gio() // after the ctx above
	if err := g.Reset(dir, ""); err != nil {
		return err
	}
	if err := g.Clean(dir); err != nil {
		return err
	}
	return g.Switch(dir, branch)
}

// laneDir is the configured lane's worktree ("": no such lane, or its dir is no directory).
func (s Scratch) laneDir(lane string) string {
	for _, l := range s.Cfg.Lanes() {
		if fi, err := os.Stat(l.Dir); l.Name == lane && l.Dir != "" && err == nil && fi.IsDir() {
			return l.Dir
		}
	}
	return ""
}
