//go:build integration

package checks

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// TestWorkflowContractScratchFF runs the workflow contract table (testkit.WorkflowContract, T-1029) on scratch-ff over
// real git: the rows' arrangements are made in git (a commit on main, an untracked file in the main checkout's way,
// the integrator's resolution committed in serve's worktree), and Saw checks the git behind each result (T-1011's
// scenario table: the commits' subjects and bodies, serve's worktree, the candidate's diff). The review git is
// cmd/obor's (its own tests).
func TestWorkflowContractScratchFF(t *testing.T) {
	t.Parallel()
	testkit.WorkflowContract(t, func(t *testing.T) testkit.WorkflowRig {
		y := newSx(t, cmd("check", "echo 1 pass"))
		return &ffRig{y: y, wf: NewScratchFF(y.s, nil)}
	})
}

// ffRig is ScratchFF on the scratch fixture (lane L-01 on task/T-05, its feature.txt uncommitted).
type ffRig struct {
	y  *sx
	wf ScratchFF
}

func (r *ffRig) Workflow() port.Workflow { return r.wf }

func (r *ffRig) Lane() (string, string, string) {
	return "L-01", r.y.lane, "task/" + testkit.ContractTask
}

func (r *ffRig) Lane2() (string, string, string) {
	return "L-02", r.y.lane2(), "task/" + testkit.ContractTask2
}

func (r *ffRig) Work(file, text string) { r.y.Write(filepath.Join(r.y.lane, file), text) }

func (r *ffRig) MoveTarget(file, text string) {
	r.y.Write(file, text)
	r.y.Git(r.y.Root, "add", file)
	r.y.Git(r.y.Root, "commit", "-qm", "main edit")
}

// DirtyMain puts an untracked file in the fast-forward's way (or removes it).
func (r *ffRig) DirtyMain(file string, dirty bool) {
	if dirty {
		r.y.Write(file, "mine\n")
		return
	}
	if err := os.Remove(r.y.Path(file)); err != nil {
		r.y.t.Fatal(err)
	}
}

func (r *ffRig) Resolve() string {
	r.y.Write(filepath.Join(r.y.serve, "shared.txt"), "both\n")
	r.y.Git(r.y.serve, "add", "shared.txt")
	r.y.Git(r.y.serve, "commit", "-qm", "Merge T-05: resolved\n\nkept both sides")
	return r.y.head(r.y.serve)
}

func (r *ffRig) Head() string            { return r.y.head(r.y.Root) }
func (r *ffRig) Pins(task string) string { return r.y.pins(task) }

func (r *ffRig) Parents(sha string) []string {
	return strings.Fields(r.y.Git(r.y.Root, "rev-parse", sha+"^1", sha+"^2"))
}

// Saw is the git behind a step's result (T-1011's scenario table).
func (r *ffRig) Saw(t *testing.T, step string, v any) {
	t.Helper()
	y := r.y
	switch step {
	case "Gate lane green":
		c := v.(port.Candidate)
		sha := y.Git(y.Root, "rev-parse", c.SHA+"^2")
		y.eq(y.Git(y.lane, "log", "-1", "--format=%s", sha), "T-05: feature", step+": the lane commit's subject")
		y.eq(y.Git(y.lane, "status", "--porcelain"), "", step+": the lane committed")
		y.eq(y.Git(y.Root, "log", "-1", "--format=%s", c.SHA), "Merge T-05: feature", step+": the subject")
		y.eq(y.head(y.serve), c.SHA, step+": serve's worktree at the candidate")
		y.eq(y.Git(y.serve, "status", "--porcelain"), "", step+": serve's worktree clean")
		y.eq(c.Base, y.Git(y.Root, "merge-base", sha, c.Target), step+": the base, the lane's fork point")
		y.eq(c.Patch, fmt.Sprintf("%x", sha256.Sum256([]byte(y.Git(y.Root, "diff", c.Base, sha)))),
			step+": the patch, the own diff's hash")
		y.contains(c.Diff, "+feature", step+": the diff kept")
		y.eq(c.Diff, y.Git(y.Root, "diff", c.Target, c.SHA), step+": the candidate's diff")
		y.contains(c.Stat, "feature.txt", step+": the stat")
	case "Gate resolved green":
		c := v.(port.Candidate)
		res := y.ref(resolvedRefs + "T-05")
		y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^{tree}"), y.Git(y.Root, "rev-parse", res+"^{tree}"),
			step+": the amended merge keeps the resolution's tree")
		y.eq(y.Git(y.Root, "log", "-1", "--format=%B", c.SHA), "Merge T-05: feature\n\nkept both sides",
			step+": the amended message")
	case "Gate moved green":
		c := v.(port.Candidate)
		y.contains(y.Git(y.Root, "log", "-1", "--format=%b", c.SHA), "Merged onto the moved main at "+c.Target, step+": the body")
	case "Gate lane conflict", "Gate moved conflict":
		o := v.(port.GateOutcome)
		y.eq(o.Dir, y.serve, step+": left open in serve's worktree")
		y.eq(y.Git(y.serve, "rev-parse", "MERGE_HEAD"), y.ref(submittedRefs+"T-05/1"), step+": the submitted sha's merge open")
	}
}

// Unpin deletes the task's pins only: a task id it prefixes keeps its own (T-05's unpin, T-050's pin).
func TestUnpinKeepsALongerTaskID(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	head := y.head(y.Root)
	y.Git(y.Root, "update-ref", submittedRefs+"T-05/2", head)
	y.Git(y.Root, "update-ref", submittedRefs+"T-050/1", head)
	if got := y.Git(y.Root, "for-each-ref", "--format=%(refname)", "refs/obor/"); !strings.Contains(got, "T-05/2") {
		t.Fatalf("precondition: T-05's pin is there: %q", got)
	}
	if err := y.s.Unpin(ctx, "T-05"); err != nil {
		t.Fatal(err)
	}
	y.eq(y.Git(y.Root, "for-each-ref", "--format=%(refname)", "refs/obor/"), submittedRefs+"T-050/1",
		"Unpin: T-05's pins gone, T-050's kept")
}
