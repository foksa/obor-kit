package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
)

// fakePruner is port.Pruner over lists: what it holds, what it was asked to delete.
type fakePruner struct {
	branches []port.PruneBranch
	checked  map[string]bool
	trees    []port.PruneTree
	dirt     map[string]string // dir → why it must stay
	refuse   map[string]error  // branch → git's refusal

	deleted, removed []string
	treePrunes       []string // dirs whose registration was dropped
	treeFail         map[string]error
}

func (f *fakePruner) Branches(context.Context) ([]port.PruneBranch, error) { return f.branches, nil }
func (f *fakePruner) CheckedOut(context.Context) (map[string]bool, error)  { return f.checked, nil }
func (f *fakePruner) Trees(context.Context) ([]port.PruneTree, error)      { return f.trees, nil }
func (f *fakePruner) Dirt(_ context.Context, dir string) (string, error)   { return f.dirt[dir], nil }
func (f *fakePruner) PruneTrees(_ context.Context, dirs []string) map[string]error {
	failed := map[string]error{}
	for _, d := range dirs {
		if err := f.treeFail[d]; err != nil {
			failed[d] = err
			continue
		}
		f.treePrunes = append(f.treePrunes, d)
	}
	return failed
}

func (f *fakePruner) DeleteBranch(_ context.Context, b port.PruneBranch) error {
	if err := f.refuse[b.Name]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, b.Name)
	return nil
}

func (f *fakePruner) RemoveTree(_ context.Context, dir string) error {
	f.removed = append(f.removed, dir)
	return nil
}

// pruneRig is a connected harness on configJSON (lanes L-01, L-02) whose loop carries f.
func pruneRig(t *testing.T, f *fakePruner) *harness {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), configJSON)
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Prune: f}, nil }
	h.lanes.lanes[0].Task = ""
	h.connect()
	return h
}

// ended records the end of task, age ago: merged, or dropped (its branch is the same either way).
func (h *harness) ended(task, step string, age time.Duration) {
	h.t.Helper()
	now := h.now
	h.now = now.Add(-age)
	h.fact(task, step, event.FactData{Reason: "why"})
	h.now = now
}

func (h *harness) prune(c wire.PruneReq) string {
	h.t.Helper()
	c.Root = h.root
	reply, err := h.a.pruneCall(context.Background(), c)
	if err != nil {
		h.t.Fatalf("prune %+v: %v", c, err)
	}
	return reply
}

func (f *fakePruner) branch(name string) {
	f.branches = append(f.branches, port.PruneBranch{Name: name, SHA: fmt.Sprintf("a%06d", len(f.branches)+1) + strings.Repeat("f", 33)})
}

const day = 24 * time.Hour

// A merged task's branch is deleted; an open task's, a task serve does not know, a branch outside task/, a branch
// checked out in a worktree and a task dropped within 14 days stay, and a dropped one over 14 days goes only with
// --unmerged. Each deletion is a line with its tip, and fact pruned lists it with the full sha.
func TestPruneBranches(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		req      wire.PruneReq
		wantGone []string
	}{
		{"by hand", wire.PruneReq{}, []string{"task/T-1"}},
		{"--unmerged", wire.PruneReq{Unmerged: true}, []string{"task/T-1", "task/T-4"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fakePruner{checked: map[string]bool{"task/T-3": true}}
			for _, b := range []string{"task/T-1", "task/T-2", "task/T-3", "task/T-4", "task/T-5", "task/T-9999", "keep/T-1", "audit/x", "main"} {
				f.branch(b)
			}
			h := pruneRig(t, f)
			h.ended("T-1", event.StepMerged, 1*day)  // merged: goes
			h.setLane("T-2", true)                   // open (submitted): stays
			h.ended("T-3", event.StepMerged, 30*day) // merged, but checked out in a worktree: stays
			h.ended("T-4", event.StepDropped, 20*day)
			h.ended("T-5", event.StepDropped, 10*day) // dropped within 14 days: stays, even with --unmerged
			reply := h.prune(c.req)
			if !slices.Equal(f.deleted, c.wantGone) {
				t.Errorf("deleted %v, want %v", f.deleted, c.wantGone)
			}
			for i, name := range c.wantGone {
				sha := f.branches[slices.IndexFunc(f.branches, func(b port.PruneBranch) bool { return b.Name == name })].SHA
				if want := fmt.Sprintf("deleted %s (was %s)", name, sha[:7]); !strings.Contains(reply, want) {
					t.Errorf("reply %d has no %q:\n%s", i, want, reply)
				}
			}
			if want := fmt.Sprintf("PRUNE deleted %d branches, 0 worktrees", len(c.wantGone)); !strings.HasSuffix(reply, want) {
				t.Errorf("reply %q does not end with %q", reply, want)
			}
			facts := prunedFacts(t, h)
			if len(facts) != 1 || len(facts[0].Branches) != len(c.wantGone) {
				t.Fatalf("pruned facts %+v, want one listing %v", facts, c.wantGone)
			}
			for i, name := range c.wantGone {
				b := facts[0].Branches[i]
				if b.Name != name || len(b.SHA) != 40 || facts[0].Unmerged != c.req.Unmerged {
					t.Errorf("fact branch %+v, want %s with its full sha (unmerged %v)", b, name, c.req.Unmerged)
				}
			}
		})
	}
}

// --dry-run prints the same list as the run (as "would delete") and deletes and records nothing.
func TestPruneDryRunListsAndDeletesNothing(t *testing.T) {
	t.Parallel()
	f := &fakePruner{}
	f.branch("task/T-1")
	f.branch("task/T-2")
	h := pruneRig(t, f)
	f.trees = []port.PruneTree{{Dir: filepath.Join(lanesDirOf(t, h.root), "L-07"), SHA: "abcdef1234567"}}
	h.ended("T-1", event.StepMerged, 2*day)
	h.ended("T-2", event.StepMerged, 3*day)
	dry := h.prune(wire.PruneReq{DryRun: true})
	if len(f.deleted)+len(f.removed) != 0 || len(f.treePrunes) != 0 || len(prunedFacts(t, h)) != 0 {
		t.Errorf("dry run deleted %v, removed %v, recorded %v", f.deleted, f.removed, prunedFacts(t, h))
	}
	if !strings.Contains(dry, "would delete task/T-1 (was a000001)") || !strings.Contains(dry, "would remove worktree") {
		t.Fatalf("dry run precondition: its list is empty of the would-be deletions:\n%s", dry)
	}
	real := h.prune(wire.PruneReq{}) // the dry run changed nothing: the same state
	if len(f.deleted) != 2 || len(f.removed) != 1 {
		t.Fatalf("precondition: the real run deleted %v, removed %v", f.deleted, f.removed)
	}
	asDry := strings.NewReplacer("deleted task", "would delete task", "removed worktree", "would remove worktree",
		"PRUNE deleted", "PRUNE would delete")
	if asDry.Replace(real) != dry {
		t.Errorf("dry-run list differs from the run's:\n%s\n---\n%s", dry, real)
	}
}

// A structured dry run answers the branches serve takes (merged) and the task branches it keeps (unmerged), each with
// its full tip sha, as JSON and not lines: nothing is deleted; a branch checked out and one outside task/ are in neither.
func TestPruneStructuredReplyListsMergedAndUnmerged(t *testing.T) {
	t.Parallel()
	f := &fakePruner{checked: map[string]bool{"task/T-3": true}}
	for _, b := range []string{"task/T-1", "task/T-2", "task/T-3", "task/T-5", "keep/T-1"} {
		f.branch(b)
	}
	h := pruneRig(t, f)
	h.ended("T-1", event.StepMerged, 1*day)
	h.setLane("T-2", true)                  // open
	h.ended("T-3", event.StepMerged, 1*day) // merged, but checked out: in neither list
	reply := h.prune(wire.PruneReq{DryRun: true, Structured: true})
	got, err := wire.ParsePruneReply(reply)
	if err != nil {
		t.Fatalf("reply %q: %v", reply, err)
	}
	full := func(name string) wire.PruneRef {
		i := slices.IndexFunc(f.branches, func(b port.PruneBranch) bool { return b.Name == name })
		if i < 0 || len(f.branches[i].SHA) != 40 {
			t.Fatalf("precondition: no branch %s with a full sha in %v", name, f.branches)
		}
		return wire.PruneRef{Name: name, SHA: f.branches[i].SHA}
	}
	want := wire.PruneReply{Merged: []wire.PruneRef{full("task/T-1")},
		Unmerged: []wire.PruneRef{full("task/T-2"), full("task/T-5")}}
	if !slices.Equal(got.Merged, want.Merged) || !slices.Equal(got.Unmerged, want.Unmerged) {
		t.Errorf("reply %+v, want %+v", got, want)
	}
	if strings.Contains(reply, "PRUNE") || len(f.deleted) != 0 {
		t.Errorf("reply %q is not data only, or deleted %v", reply, f.deleted)
	}
}

// The nightly takes the branches of tasks merged over 7 days ago only: 8 days goes, 6 days stays; never a worktree
// (an extra clean lane stays) and never a dropped task's branch. Its reply is the one non-gating line.
func TestPruneNightlyTakesWeekOldMergesOnly(t *testing.T) {
	t.Parallel()
	f := &fakePruner{}
	for _, b := range []string{"task/T-1", "task/T-2", "task/T-3"} {
		f.branch(b)
	}
	h := pruneRig(t, f)
	f.trees = []port.PruneTree{{Dir: filepath.Join(lanesDirOf(t, h.root), "L-07"), SHA: "abc"}}
	h.ended("T-1", event.StepMerged, 8*day)
	h.ended("T-2", event.StepMerged, 6*day)
	h.ended("T-3", event.StepDropped, 40*day)
	reply := h.prune(wire.PruneReq{Nightly: true})
	if !slices.Equal(f.deleted, []string{"task/T-1"}) || len(f.removed) != 0 || len(f.treePrunes) != 0 {
		t.Errorf("deleted %v, removed %v: want only task/T-1", f.deleted, f.removed)
	}
	if want := "prune: 1 branches (oldest merge 2026-10-16)"; reply != want {
		t.Errorf("reply %q, want %q", reply, want)
	}
	if facts := prunedFacts(t, h); len(facts) != 1 || !facts[0].Nightly {
		t.Errorf("pruned facts %+v, want one marked nightly", facts)
	}
}

// A branch git refuses to delete is kept with git's reason, not counted and not in the fact.
func TestPruneKeepsABranchGitRefuses(t *testing.T) {
	t.Parallel()
	f := &fakePruner{refuse: map[string]error{"task/T-1": errors.New("used by worktree at /x")}}
	f.branch("task/T-1")
	h := pruneRig(t, f)
	h.ended("T-1", event.StepMerged, 2*day)
	reply := h.prune(wire.PruneReq{})
	if !strings.Contains(reply, "kept task/T-1: used by worktree at /x") || !strings.HasSuffix(reply, "PRUNE deleted 0 branches, 0 worktrees") {
		t.Errorf("reply:\n%s", reply)
	}
	if facts := prunedFacts(t, h); len(facts) != 0 {
		t.Errorf("a run that deleted nothing recorded %+v", facts)
	}
}

// Worktrees: an extra lane that is clean goes (its head in the line and the fact); an extra lane that is dirty, a
// configured lane, the merge scratch, a worktree outside lanes.dir and a role worktree of a configured role stay (the
// dirty one listed); a registered worktree whose dir is gone is git worktree prune's.
func TestPruneWorktrees(t *testing.T) {
	t.Parallel()
	f := &fakePruner{}
	h := pruneRig(t, f)
	dir := lanesDirOf(t, h.root)
	f.trees = []port.PruneTree{
		{Dir: filepath.Join(dir, "L-03"), SHA: "1111111aaaa"},       // beyond lanes.count, clean: removed
		{Dir: filepath.Join(dir, "L-04"), SHA: "2222222bbbb"},       // beyond lanes.count, dirty: kept, listed
		{Dir: filepath.Join(dir, "L-02"), SHA: "3333333cccc"},       // configured: kept
		{Dir: filepath.Join(dir, "merge"), SHA: "4444444dddd"},      // the scratch: kept
		{Dir: filepath.Join(dir, "deep_dive"), SHA: "5555555eeee"},  // role not in agents.roles: removed
		{Dir: filepath.Join(dir, "integrator"), SHA: "6666666ffff"}, // role in agents.roles: kept
		{Dir: filepath.Join(filepath.Dir(dir), "elsewhere", "L-09"), SHA: "7777777gggg"},
		{Dir: filepath.Join(dir, "L-08"), SHA: "8888888hhhh", Gone: true},
	}
	f.dirt = map[string]string{filepath.Join(dir, "L-04"): "dirty (2 paths, as a.go)"}
	reply := h.prune(wire.PruneReq{})
	wantRemoved := []string{filepath.Join(dir, "L-03"), filepath.Join(dir, "deep_dive")}
	if !slices.Equal(f.removed, wantRemoved) {
		t.Errorf("removed %v, want %v", f.removed, wantRemoved)
	}
	if want := []string{filepath.Join(dir, "L-08")}; !slices.Equal(f.treePrunes, want) {
		t.Errorf("registrations dropped %v, want %v (L-08's dir is gone; elsewhere/L-09 is not asked for)", f.treePrunes, want)
	}
	for _, want := range []string{
		"removed worktree " + filepath.Join(dir, "L-03") + " (was 1111111)",
		"kept worktree " + filepath.Join(dir, "L-04") + ": dirty (2 paths, as a.go)",
		"pruned worktree " + filepath.Join(dir, "L-08") + " (its dir is gone) (was 8888888)",
		"PRUNE deleted 0 branches, 3 worktrees",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply has no %q:\n%s", want, reply)
		}
	}
	if strings.Contains(reply, "elsewhere") || strings.Contains(reply, "L-02") {
		t.Errorf("reply names a worktree that is nobody's to prune:\n%s", reply)
	}
	facts := prunedFacts(t, h)
	if len(facts) != 1 || len(facts[0].Worktrees) != 3 || facts[0].Worktrees[0].SHA != "1111111aaaa" {
		t.Errorf("pruned facts %+v, want one listing both worktrees with their heads", facts)
	}
}

// Only the owner and the lead prune, and the nightly's call takes neither --unmerged nor --dry-run.
func TestPruneRefusals(t *testing.T) {
	t.Parallel()
	h := pruneRig(t, &fakePruner{})
	for _, c := range []wire.PruneReq{
		{Envelope: wire.Envelope{Root: h.root, Role: "worker", Agent: "pj-l-01"}},
		{Envelope: wire.Envelope{Root: h.root}, Nightly: true, Unmerged: true},
		{Envelope: wire.Envelope{Root: h.root}, Nightly: true, DryRun: true},
	} {
		if _, err := h.a.pruneCall(context.Background(), c); err == nil {
			t.Errorf("%+v was not refused", c)
		}
	}
}

// lanesDirOf is the lanes dir of the project at root.
func lanesDirOf(t *testing.T, root string) string {
	t.Helper()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.LanesDir()
}

// prunedFacts is the log's pruned facts' data.
func prunedFacts(t *testing.T, h *harness) []event.Pruned {
	t.Helper()
	var out []event.Pruned
	for _, r := range readLog(t, h.root) {
		if r.Task == event.PruneTask && r.Step == event.StepPruned && r.Kind == event.KindFact {
			var d event.Pruned
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
	}
	return out
}

// A hand run logs each successful deletion (branch merged, branch --unmerged, worktree removed, registration pruned)
// with its sha, and nothing it kept; a dry run logs none; the nightly's lines are as before.
func TestPruneHandRunLogsEachDeletion(t *testing.T) {
	t.Parallel()
	setup := func() (*fakePruner, *harness, *bytes.Buffer) {
		f := &fakePruner{treeFail: map[string]error{}}
		f.branch("task/T-1")
		f.branch("task/T-2")
		f.branch("task/T-3")
		h := pruneRig(t, f)
		out := &bytes.Buffer{}
		h.a.opt.Out = out
		dir := lanesDirOf(t, h.root)
		f.trees = []port.PruneTree{
			{Dir: filepath.Join(dir, "L-03"), SHA: "1111111aaaa"},
			{Dir: filepath.Join(dir, "L-04"), SHA: "2222222bbbb"},
			{Dir: filepath.Join(dir, "L-08"), SHA: "8888888hhhh", Gone: true},
		}
		f.dirt = map[string]string{filepath.Join(dir, "L-04"): "dirty (2 paths, as a.go)"}
		h.ended("T-1", event.StepMerged, 2*day)
		h.ended("T-2", event.StepDropped, 40*day)
		h.ended("T-3", event.StepMerged, 8*day)
		return f, h, out
	}
	f, h, out := setup()
	dry := h.prune(wire.PruneReq{DryRun: true, Unmerged: true})
	if !strings.Contains(dry, "would delete task/T-2") || !strings.Contains(dry, "would remove worktree") {
		t.Fatalf("precondition: the dry run lists no would-be deletions:\n%s", dry)
	}
	if out.Len() != 0 {
		t.Errorf("a dry run logged %q, want nothing", out.String())
	}
	f, h, out = setup()
	reply := h.prune(wire.PruneReq{Unmerged: true})
	if len(f.deleted) != 3 || len(f.removed) != 1 || len(f.treePrunes) != 1 {
		t.Fatalf("precondition: deleted %v, removed %v, pruned %v", f.deleted, f.removed, f.treePrunes)
	}
	dir := lanesDirOf(t, h.root)
	log := out.String()
	for _, want := range []string{
		"prune of pj: deleted task/T-1 (was a000001)",
		"prune of pj: deleted task/T-2 (was a000002)",
		"prune of pj: removed worktree " + filepath.Join(dir, "L-03") + " (was 1111111)",
		"prune of pj: pruned worktree " + filepath.Join(dir, "L-08") + " (its dir is gone) (was 8888888)",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log has no %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "kept") || strings.Contains(log, "PRUNE") || strings.Count(log, "\n") != 5 {
		t.Errorf("log has more than the five deletion lines:\n%s", log)
	}
	if !strings.Contains(reply, "kept worktree "+filepath.Join(dir, "L-04")) {
		t.Errorf("reply lost the kept line:\n%s", reply)
	}
	// a registration git cannot drop is kept in the reply, not logged, not in the fact
	f, h, out = setup()
	dir = lanesDirOf(t, h.root)
	f.treeFail[filepath.Join(dir, "L-08")] = errors.New("it is locked")
	reply = h.prune(wire.PruneReq{})
	if !strings.Contains(reply, "kept worktree "+filepath.Join(dir, "L-08")+": it is locked") || strings.Contains(out.String(), "L-08") {
		t.Errorf("a failed registration drop: reply\n%s\nlog\n%s", reply, out.String())
	}
	// nightly: its lines (branches only) are logged as before
	f, h, out = setup()
	h.prune(wire.PruneReq{Nightly: true})
	if got := out.String(); got != "12:00:00 prune of pj: deleted task/T-3 (was a000003)\n" && !strings.Contains(got, "prune of pj: deleted task/T-3 (was a000003)\n") {
		t.Errorf("nightly log %q", got)
	}
}
