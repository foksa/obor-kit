package checks

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/testkit"
)

// laneAt is the fixed dispatch time of the lane tests.
var laneAt = time.Unix(1760000000, 0)

// laneInput is the lane.prepare step of task T-01 in lane L-01 at laneAt.
func laneInput(lane, target string) port.PrepareInput {
	return port.PrepareInput{Task: "T-01", Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target,
		Agent: "pj-l-01", At: laneAt}
}

// lk is a lane fixture whose Lane works on fakeGit: a plain project dir (no git, testkit.NewDir) with lane L-01 on
// task/T-01 at main0, clean, and main moved on to target since (what a lane.prepare finds, as laneFx's config commit).
type lk struct {
	*testkit.Project
	t      testing.TB
	g      *fakeGit
	l      Lane
	lane   string
	target string
}

// newLk is the fake lane fixture; edits change its config.
func newLk(t testing.TB, edits ...func(map[string]any)) *lk {
	t.Helper()
	p := testkit.NewDir(t)
	lane := p.Lane("L-01", "task/T-01")
	p.Config(edits...)
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGit(p.Root, lane)
	delete(g.refs, "refs/heads/task/T-05")
	delete(g.status, lane)
	g.refs["refs/heads/task/T-01"], g.branch[lane] = "main0", "task/T-01"
	target := g.commitOn(p.Root, "config", 110)
	return &lk{Project: p, t: t, g: g, l: Lane{Cfg: cfg, Out: io.Discard, fake: g}, lane: lane, target: target}
}

// branchRef is task/T-01's ref.
const branchRef = "refs/heads/task/T-01"

// prepare is a fresh lane.prepare that must pass.
func (k *lk) prepare() {
	k.t.Helper()
	if _, err := k.l.Prepare(context.Background(), laneInput(k.lane, k.target)); err != nil {
		k.t.Fatalf("prepare: %v", err)
	}
}

// commit is the lane's worker committing on the branch: the new head.
func (k *lk) commit() string { return k.g.commitOn(k.lane, "work in L-01", 50) }

// dirty is an untracked file in the lane.
func (k *lk) dirty() { k.g.status[k.lane] = []string{"?? scratch.txt"} }

// free is the drop of T-01's lane onto the target.
func (k *lk) free(discard bool) (string, error) {
	in := port.FreeInput{Lane: "L-01", Dir: k.lane, Branch: "task/T-01", Target: k.target, Discard: discard}
	return k.l.Free(context.Background(), in)
}

// writes is the logged writes, "|"-joined.
func (k *lk) writes() string { return strings.Join(k.g.writeCalls(), "|") }
