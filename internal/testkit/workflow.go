package testkit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// The workflow contract table: one table of rows, each a run of port.Workflow calls with each call's expected
// result, run against internal/app's FakeWorkflow (plain go test) and against scratch-ff on real git (internal/checks,
// behind the integration tag). A row's arrangements (a target move, a dirty main checkout, the integrator's resolution)
// are each implementation's own: the fake scripts them, the git rig makes them in git. The git behind a result that the
// port does not show (a commit's subject, serve's worktree clean) is the git rig's Saw.

// WorkflowRig is one port.Workflow under the contract table, fresh per row: lane Lane on task ContractTask's branch at
// the target, nothing of its own yet, merge.check passing.
type WorkflowRig interface {
	Workflow() port.Workflow
	// Lane is the lane the rows work in: its name, dir and branch (task ContractTask's); Lane2 another on
	// ContractTask2's branch at the target, made when a row first asks for it.
	Lane() (name, dir, branch string)
	Lane2() (name, dir, branch string)
	// Work writes file in the lane (uncommitted).
	Work(file, text string)
	// MoveTarget is a commit on the target outside the workflow: file set to text.
	MoveTarget(file, text string)
	// DirtyMain changes file in the main checkout (uncommitted), or restores it (dirty false).
	DirtyMain(file string, dirty bool)
	// Resolve is the integrator's resolution of the open conflict, committed in serve's worktree: its sha.
	Resolve() string
	// Head is the target's head.
	Head() string
	// Pins is task's pins, "submitted=<sha> candidate=<sha> resolved=<sha>" ("-": none; submitted: submit id "1").
	Pins(task string) string
	// Parents is a merge's parents: the target it went onto, then the submitted sha.
	Parents(sha string) []string
	// Saw checks what the implementation does behind step's result v that the port does not show (nothing: none).
	Saw(t *testing.T, step string, v any)
}

// The tasks the contract's rows submit.
const (
	ContractTask  = "T-05"
	ContractTask2 = "T-20"
)

// WorkflowContract runs the contract table, each row on a fresh rig of newRig.
func WorkflowContract(t *testing.T, newRig func(t *testing.T) WorkflowRig) {
	for _, row := range contractRows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			row.run(&contract{t: t, r: newRig(t), ctx: context.Background()})
		})
	}
}

// contract is one row's run: its rig and the checks every row shares.
type contract struct {
	t   *testing.T
	r   WorkflowRig
	ctx context.Context
}

func (c *contract) eq(got, want any, what string) {
	c.t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		c.t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func (c *contract) ok(err error, what string) {
	c.t.Helper()
	if err != nil {
		c.t.Fatalf("%s: %v", what, err)
	}
}

func (c *contract) refused(err error, what string) {
	c.t.Helper()
	if !errors.As(err, new(port.Refused)) {
		c.t.Errorf("%s: %v, want a Refused error", what, err)
	}
}

// submit is ContractTask's submit (id "1") of its lane: the sha.
func (c *contract) submit() string {
	c.t.Helper()
	name, _, _ := c.r.Lane()
	sha, err := c.r.Workflow().Submit(c.ctx, ContractTask, "1", name, "feature")
	c.ok(err, "Submit")
	if sha == "" {
		c.t.Fatal("Submit: no sha")
	}
	return sha
}

// gate is one gate of ContractTask: kind, input.
func (c *contract) gate(kind, input string) port.GateOutcome {
	c.t.Helper()
	name, _, _ := c.r.Lane()
	o, err := c.r.Workflow().Gate(c.ctx, port.GateInput{Task: ContractTask, Lane: name, Kind: kind, Input: input,
		Summary: "feature"})
	c.ok(err, "Gate "+kind)
	return o
}

// green is a gate that must be green: its candidate.
func (c *contract) green(step, kind, input string) port.Candidate {
	c.t.Helper()
	o := c.gate(kind, input)
	if o.Kind != port.OutcomeGreen {
		c.t.Fatalf("%s: %s %q, want green", step, o.Kind, o.Line)
	}
	c.r.Saw(c.t, step, o.Candidate)
	return o.Candidate
}

func (c *contract) publish(cand port.Candidate) port.PublishOutcome {
	c.t.Helper()
	o, err := c.r.Workflow().Publish(c.ctx, cand)
	c.ok(err, "Publish")
	return o
}

func (c *contract) landed(want bool, what string) {
	c.t.Helper()
	got, err := c.r.Workflow().Landed(c.ctx, ContractTask)
	c.ok(err, what)
	c.eq(got, want, what)
}

func (c *contract) target(sha string, ahead bool, what string) {
	c.t.Helper()
	got, a, err := c.r.Workflow().Target(c.ctx, ContractTask)
	c.ok(err, what)
	c.eq(got+fmt.Sprintf(" ahead=%v", a), sha+fmt.Sprintf(" ahead=%v", ahead), what)
}

// pins is the rig's pins of ContractTask, its shas by name.
func (c *contract) pins(submitted, candidate, resolved, what string) {
	c.t.Helper()
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	c.eq(c.r.Pins(ContractTask), "submitted="+dash(submitted)+" candidate="+dash(candidate)+" resolved="+dash(resolved), what)
}

// contractRows are the table (internal/checks' scratch scenario table and internal/app's FakeScratch cases, merged).
var contractRows = []struct {
	name string
	run  func(c *contract)
}{
	{"Submit, Gate lane green, Test green, Publish hold, merged, already, Landed, Target, Unpin", func(c *contract) {
		w := c.r.Workflow()
		head := c.r.Head()
		c.landed(false, "Landed before the merge")
		c.target(head, false, "Target before the work: the target's head, not ahead")
		c.r.Work("feature.txt", "feature\n")
		sha := c.submit()
		c.eq(c.submit(), sha, "Submit: a repeat of the id gives its pin")
		c.pins(sha, "", "", "Submit: pinned")
		c.target(head, true, "Target after the submit: the branch is ahead")
		name2, _, _ := c.r.Lane2()
		_, err := w.Submit(c.ctx, ContractTask2, "1", name2, "x")
		c.refused(err, "Submit of a lane already in the target")

		cand := c.green("Gate lane green", port.InputLane, sha)
		c.eq(cand.Target, head, "Gate lane green: onto the target's head")
		c.eq(c.r.Parents(cand.SHA), []string{head, sha}, "Gate lane green: the merge of the submitted sha onto the target")
		if cand.Base == "" || cand.Patch == "" {
			c.t.Errorf("Gate lane green: base %q, patch %q; want both", cand.Base, cand.Patch)
		}
		c.pins(sha, cand.SHA, "", "Gate lane green: pinned")
		c.eq(c.green("Gate lane green, again", port.InputLane, sha).SHA, cand.SHA, "Gate lane green: a repeat makes the same candidate")

		name, _, branch := c.r.Lane()
		to, err := w.Test(c.ctx, port.TestInput{Task: ContractTask, Lane: name, Branch: branch, SHA: cand.SHA, Target: cand.Target})
		c.ok(err, "Test")
		c.eq(to.Kind, port.OutcomeGreen, "Test green")

		c.r.DirtyMain("feature.txt", true)
		o := c.publish(cand)
		c.eq(o.Kind+" "+strings.Join(o.Files, " "), port.PublishHold+" feature.txt", "Publish hold: a dirty merged file")
		c.eq(c.r.Head(), head, "Publish hold: nothing merged")
		c.r.DirtyMain("feature.txt", false)
		c.eq(c.publish(cand).Kind, port.PublishMerged, "Publish merged")
		c.eq(c.r.Head(), cand.SHA, "Publish merged: the target at the candidate")
		c.pins("", "", "", "Publish merged: unpinned")
		c.landed(true, "Landed after the publish")
		c.target(cand.SHA, false, "Target after the publish: the target holds the branch")
		c.eq(c.publish(cand).Kind, port.PublishAlready, "Publish already")
		c.ok(w.Unpin(c.ctx, ContractTask), "Unpin with nothing pinned")
	}},
	{"Gate lane conflict, Resolved, Gate resolved green", func(c *contract) {
		c.r.Work("shared.txt", "lane\n")
		c.r.MoveTarget("shared.txt", "main\n")
		target := c.r.Head()
		sha := c.submit()
		o := c.gate(port.InputLane, sha)
		c.eq(o.Kind+" "+strings.Join(o.Files, " "), port.OutcomeConflict+" shared.txt", "Gate lane conflict: the conflicting file")
		c.r.Saw(c.t, "Gate lane conflict", o)
		_, err := c.r.Workflow().Resolved(c.ctx, ContractTask, sha, nil)
		c.refused(err, "Resolved with the merge still open")
		res := c.r.Resolve()
		got, err := c.r.Workflow().Resolved(c.ctx, ContractTask, sha, nil)
		c.ok(err, "Resolved")
		c.eq(got, res, "Resolved: the resolution")
		c.pins(sha, "", res, "Resolved: pinned")
		cand := c.green("Gate resolved green", port.InputResolved, res)
		c.eq(c.r.Parents(cand.SHA), []string{target, sha}, "Gate resolved green: the resolution's parents")
		c.eq(cand.Target, target, "Gate resolved green: the target")
		c.pins(sha, cand.SHA, res, "Gate resolved green: pinned")
	}},
	{"Publish moved, Gate moved green, Publish merged", func(c *contract) {
		c.r.Work("feature.txt", "feature\n")
		sha := c.submit()
		old := c.green("Gate lane green", port.InputLane, sha)
		c.r.MoveTarget("other.txt", "other\n")
		moved := c.r.Head()
		o := c.publish(old)
		c.eq(o.Kind+" "+o.Head, port.PublishMoved+" "+moved, "Publish moved: the target's head")
		c.pins(sha, old.SHA, "", "Publish moved: the candidate stays pinned")
		cand := c.green("Gate moved green", port.InputMoved, sha)
		c.eq(cand.Target, moved, "Gate moved green: onto the moved target")
		c.eq(c.r.Parents(cand.SHA), []string{moved, sha}, "Gate moved green: merges the submitted sha (T-403)")
		c.eq(cand.Patch, old.Patch, "Gate moved green: an unrelated move keeps the patch")
		c.eq(c.publish(cand).Kind, port.PublishMerged, "Publish merged")
		c.eq(c.r.Head(), cand.SHA, "Publish merged: the target at the moved candidate")
	}},
	{"Gate moved conflict", func(c *contract) {
		c.r.Work("shared.txt", "lane\n")
		sha := c.submit()
		c.green("Gate lane green", port.InputLane, sha)
		c.r.MoveTarget("shared.txt", "main\n")
		o := c.gate(port.InputMoved, sha)
		c.eq(o.Kind+" "+strings.Join(o.Files, " "), port.OutcomeConflict+" shared.txt", "Gate moved conflict: the conflicting file")
		c.r.Saw(c.t, "Gate moved conflict", o)
	}},
	{"Prepare, Free refused, Free discard, Free clean", func(c *contract) {
		w := c.r.Workflow()
		name, dir, branch := c.r.Lane()
		head := c.r.Head()
		d := port.Dispatch{Lane: name, Dir: dir, Branch: branch, Target: head}
		_, err := w.Prepare(c.ctx, port.PrepareInput{Task: ContractTask, Dispatch: port.Dispatch{Lane: name,
			Dir: filepath.Join(dir, "missing"), Branch: branch, Target: head}})
		c.refused(err, "Prepare with no lane dir")
		p, err := w.Prepare(c.ctx, port.PrepareInput{Task: ContractTask, Dispatch: d})
		c.ok(err, "Prepare")
		c.eq(p.Base, head, "Prepare: the base is the target")
		c.target(head, false, "Target after Prepare: not ahead")
		c.r.Work("feature.txt", "feature\n")
		_, err = w.Free(c.ctx, port.FreeInput{Dispatch: d})
		c.refused(err, "Free of a dirty lane")
		line, err := w.Free(c.ctx, port.FreeInput{Dispatch: d, Discard: true})
		c.ok(err, "Free discard")
		if line == "" {
			c.t.Error("Free discard: no line naming what it discarded")
		}
		_, err = w.Prepare(c.ctx, port.PrepareInput{Task: ContractTask, Dispatch: d})
		c.ok(err, "Prepare again")
		c.r.Work("feature.txt", "feature\n")
		c.submit()
		_, err = w.Free(c.ctx, port.FreeInput{Dispatch: d})
		c.refused(err, "Free of a lane with commits not in the target")
		_, err = w.Prepare(c.ctx, port.PrepareInput{Task: ContractTask, Dispatch: d})
		c.ok(err, "Prepare over the commits")
		line, err = w.Free(c.ctx, port.FreeInput{Dispatch: d})
		c.ok(err, "Free clean")
		c.eq(line, "", "Free clean: nothing discarded")
	}},
}
