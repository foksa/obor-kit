package port

import (
	"context"
	"time"
)

// LaneSetup prepares a lane for a dispatched task (internal/checks implements it).
type LaneSetup interface {
	// Prepare puts the lane at in.Dir on in.Branch at in.Target (in.Redo: the branch is kept as it is), runs
	// lane_ready, then lane_setup and lane_ready again when it was red. It returns the base sha: its lane row's
	// (serve's projection; its head is the submitted head). Repeating it converges on the same state.
	// A final refusal (no lane dir, lane_setup red, lane_ready still red) is a Refused error; anything else (git I/O)
	// is an error to retry.
	Prepare(ctx context.Context, in PrepareInput) (Prepared, error)
	// Free gives a dropped task's lane back (obor drop): refused (Refused) when it has uncommitted changes or
	// its branch has commits not in in.Target, unless in.Discard (then they are discarded); the lane is detached at
	// in.Target and its branch deleted. Repeating it converges (a missing branch is gone already). Its line says
	// what it discarded ("" nothing).
	Free(ctx context.Context, in FreeInput) (line string, err error)
}

// Prepared is lane.prepare's outcome: the base sha the task branch is on.
type Prepared struct{ Base string }

// Dispatch is what a dispatch chose for the lane: the lane and its worktree, its worker agent, the task branch and the
// target sha read when the dispatch fact was recorded (Redo: the branch is kept as it is). The app's dispatch fact
// embeds it first, so its tags are that record's; the lane intents (PrepareInput, FreeInput) embed it too.
type Dispatch struct {
	Lane   string `json:"lane"`
	Dir    string `json:"dir"`
	Agent  string `json:"agent,omitempty"`
	Branch string `json:"branch"`
	Target string `json:"target"`
	Redo   bool   `json:"redo,omitempty"`
}

// FreeInput is one lane freed by a drop: the lane's worktree, the task branch and the base it goes back to (Target:
// read when the step runs; Agent and Redo unused).
type FreeInput struct {
	Dispatch
	Discard bool
}

// PrepareInput is one lane.prepare step: the task's dispatch, and when its fact was recorded.
type PrepareInput struct {
	Task string
	Dispatch
	At time.Time
}
