package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// TestWorkflowContractFake runs the workflow contract table (testkit.WorkflowContract, T-1029) on FakeWorkflow: its
// arrangements are scripts (a target move over a file the lane changed: the next gate conflicts there; a dirty main
// checkout: the publish holds), as the rows' git is internal/checks' (TestWorkflowContractScratchFF).
func TestWorkflowContractFake(t *testing.T) {
	t.Parallel()
	testkit.WorkflowContract(t, func(t *testing.T) testkit.WorkflowRig {
		f := NewFakeWorkflow("")
		f.Dir = filepath.Join(t.TempDir(), "merge")
		r := &fakeRig{t: t, f: f, lane: filepath.Join(t.TempDir(), "L-01"), worked: map[string]bool{}}
		if err := os.MkdirAll(r.lane, 0o755); err != nil {
			t.Fatal(err)
		}
		f.AddLane("L-01", r.lane, "task/"+testkit.ContractTask)
		return r
	})
}

// fakeRig is FakeWorkflow under the contract table.
type fakeRig struct {
	t           *testing.T
	f           *FakeWorkflow
	lane, lane2 string
	worked      map[string]bool // the files Work changed in the lane
}

func (r *fakeRig) Workflow() port.Workflow { return r.f }

func (r *fakeRig) Lane() (string, string, string) {
	return "L-01", r.lane, "task/" + testkit.ContractTask
}

func (r *fakeRig) Lane2() (string, string, string) {
	if r.lane2 == "" {
		r.lane2 = filepath.Join(r.t.TempDir(), "L-02")
		if err := os.MkdirAll(r.lane2, 0o755); err != nil {
			r.t.Fatal(err)
		}
		r.f.AddLane("L-02", r.lane2, "task/"+testkit.ContractTask2)
	}
	return "L-02", r.lane2, "task/" + testkit.ContractTask2
}

func (r *fakeRig) Work(file, text string) {
	if err := os.WriteFile(filepath.Join(r.lane, file), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.worked[file] = true
}

func (r *fakeRig) MoveTarget(file, _ string) {
	if r.worked[file] {
		r.f.Script("Gate", testkit.ContractTask, port.OutcomeConflict+" "+file)
	}
	r.f.Move()
}

func (r *fakeRig) DirtyMain(file string, dirty bool) {
	outcome := ""
	if dirty {
		outcome = port.PublishHold + " " + file
	}
	r.f.Script("Publish", testkit.ContractTask, outcome)
}

func (r *fakeRig) Resolve() string { return r.f.Resolve() }

func (r *fakeRig) Head() string { return r.f.TargetHead() }

func (r *fakeRig) Pins(task string) string {
	var out []string
	for _, ref := range []string{"submitted/" + task + "/1", "candidate/" + task, "resolved/" + task} {
		name, _, _ := strings.Cut(ref, "/")
		out = append(out, name+"="+cmpDash(r.f.Pin(ref)))
	}
	return strings.Join(out, " ")
}

func (r *fakeRig) Parents(sha string) []string { return slices.Clone(r.f.Parents(sha)) }

func (r *fakeRig) Saw(*testing.T, string, any) {}

func cmpDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
