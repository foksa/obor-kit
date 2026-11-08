// Package port holds the ports of obor's core (docs/module-map.md): the interfaces the core drives its adapters
// through and the plain types they exchange. It is a leaf below package app, so an adapter reaches the core's contract
// without importing the core: herdr's events and snapshot (Pane, Snapshot, Event, Stream, Panes), the lane table (Lane,
// Lanes) and the effects of effects.go (Agents, Typer, PaneControl, Starter, Scratch, LaneSetup and their types).
package port

import (
	"context"
	"time"

	"obor/internal/agents"
)

// Pane is one herdr pane as the app sees it: the pane's own fields and those of the agent that runs in it (none: the
// agent fields are empty). It is the one shape herdr's agent and pane answers, its snapshot and its stream's events
// use (T-888).
type Pane struct {
	ID, Tab   string
	Workspace string // herdr's workspace id
	Cwd       string
	Label     string // the pane's label (obor's agent name, set by its launch); kept when an event has none
	Agent     string // the agent's kind ("" when none runs)
	// Name is herdr's agent name, "" when it has none (it exited, or a restore dropped it); the app never
	// identifies a pane by it (registry.go).
	Name       string
	Status     string // idle, working, blocked, done, unknown
	Transcript string // the agent's transcript file when herdr reports one, else ""
	// Session is herdr's raw agent session (agent_session: an id, or a path), "" when herdr reports none: the
	// session's identity, independent of whether its transcript file is found.
	Session string
	// Terminal is herdr's terminal_id of the pane: kept across pane.move, the identity table's key (registry.go); ""
	// when herdr reports none. Kept when an event has none.
	Terminal string
	Seq      int64 // state_change_seq: grows with each status change
}

// Ready: the pane's agent is idle: it takes a prompt now (done is idle with an unviewed reply).
func (p Pane) Ready() bool { return p.Status == "idle" || p.Status == "done" }

// Busy: the pane's agent works or waits on a person (working, blocked).
func (p Pane) Busy() bool { return p.Status == "working" || p.Status == "blocked" }

// Snapshot is herdr's session at one moment: the workspace labels by id and every pane.
type Snapshot struct {
	Version    string // herdr's version and protocol as the snapshot said (Protocol 0: not read)
	Protocol   int
	Speaks     int // the protocol obor itself speaks: Protocol above it is a newer herdr
	Workspaces map[string]string
	Panes      []Pane
}

// Event kinds.
const (
	EventStatus  = "status"  // Pane's agent, its status or its metadata changed: New
	EventCreated = "created" // a new pane: New
	EventClosed  = "closed"  // Pane was closed
	EventExited  = "exited"  // Pane's shell exited and herdr removed it
)

// Event is one herdr event. New is the whole pane as herdr reported it after the event (status, created): it replaces
// what the app knew of the pane, metadata too. An event without it (New.ID "") carries Status and Agent only.
type Event struct {
	Kind, Pane    string
	Status, Agent string
	New           Pane
}

// Stream is one subscription: Snapshot was read once it was live, C carries the later events and closes when the
// stream ends (Err says why). Close ends it.
type Stream struct {
	Snapshot Snapshot
	C        <-chan Event
	Err      func() error
	Close    func()
}

// Panes is herdr, as the core uses it.
type Panes interface {
	Subscribe(ctx context.Context) (*Stream, error)
	// Snapshot reads herdr's session now: one session.snapshot, the same source Subscribe's Snapshot is built from.
	// A pane's cwd can change with no event (a cd in it, obor setup pane moving it into a lane), so the snapshot the
	// stream gave can be stale (T-745).
	Snapshot(ctx context.Context) (Snapshot, error)
	// Busy: the agent in pane still runs a child besides the agent CLI (a command, a background job). An error means
	// unknown, not idle.
	Busy(ctx context.Context, pane string) (bool, error)
	// Activity is when pane's agent last wrote its transcript; known is false when obor cannot tell. pane is the one
	// the caller observed (its Session and Transcript): no herdr call (T-951).
	Activity(ctx context.Context, pane Pane) (last time.Time, known bool)
	// Turn is the last turn pane's agent's transcript records, for a kind whose turns are read (agents.Caps.Turns):
	// the file transcript names (its agent's recorded session's, T-577), else the one herdr's session names; ok false
	// when obor cannot tell. pane is the one the caller observed (T-951).
	Turn(ctx context.Context, pane Pane, transcript string) (t agents.Turn, ok bool)
	Notify(ctx context.Context, title, body string) error
}

// Lane is one lane of a project, the lane table's row (checks.LaneTable; obor status --json's lane, which view.Lane
// embeds): its worktree, branch and the task id in it ("": detached, no task id), its state (the Lane* constants;
// Reason says why it is unknown, T-871), the model it runs (Type: the dispatch's while it works, T-765, else none),
// how long ago its task's task_done was recorded (nil: none), uncommitted changes, a live warning ("": none), its
// agent (derived from the lane, T-1031) and its mode (config.LaneKeep for a keep lane, "" a fresh one; T-1031). Lanes,
// the app's read of it, holds Task only while the lane works and Dir as the physical path a pane's cwd reports.
type Lane struct {
	Name    string `json:"lane"`
	Type    string `json:"type"`
	Dir     string `json:"dir"`
	Branch  string `json:"branch"`
	Task    string `json:"task"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	DoneAge *int64 `json:"done_age"`
	Dirty   bool   `json:"dirty"`
	Warning string `json:"warning"`
	Agent   string `json:"agent,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// The lane states Lane.State holds (FreeState's rule, T-1012; checks.LaneTable.LaneState's doc): a lane is free once
// no task holds it (its row's task landed at the lane's HEAD, T-1015, or it has none) and nothing is uncommitted,
// dirty-free when no task holds it but it has uncommitted changes, working while its row's task has not landed or the
// lane's HEAD is not what landed, missing when its dir is no worktree,
// unknown when git or the store cannot say. The page's free set (web/src/lib/format.ts) is the two free
// ones, and internal/web's TestTSStringSetsMatchGo keeps it so (T-885).
const (
	LaneFree      = "free"
	LaneDirtyFree = "dirty-free"
	LaneWorking   = "working"
	LaneMissing   = "missing"
	LaneUnknown   = "unknown"
)

// Lanes reads a project's lanes (its git reads end with ctx, T-873).
type Lanes interface {
	Lanes(ctx context.Context, root string) ([]Lane, error)
}
