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

// Pane is one herdr pane as the app sees it.
type Pane struct {
	ID, Workspace string // Workspace: herdr's workspace id
	Cwd           string
	Agent         string // the agent's kind ("" when none runs); herdr's agent name is never read (registry.go)
	Label         string // the pane's label (obor's agent name, set by its launch); kept when an event has none
	Status        string // idle, working, blocked, done, unknown
	Transcript    string // the agent's transcript file when herdr reports one, else ""
	// Session is herdr's raw agent session (agent_session: an id, or a path), "" when herdr reports none: the
	// session's identity, independent of whether its transcript file is found.
	Session string
	// Terminal is herdr's terminal_id of the pane: kept across pane.move, the identity table's key (registry.go); ""
	// when herdr reports none. Kept when an event has none.
	Terminal string
}

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
	// Activity is when pane's agent last wrote its transcript; known is false when obor cannot tell.
	Activity(ctx context.Context, pane string) (last time.Time, known bool)
	// Turn is the last turn pane's agent's transcript records, for a kind whose turns are read (agents.Caps.Turns):
	// the file transcript names (its agent's recorded session's, T-577), else the one herdr's session names; ok false
	// when obor cannot tell.
	Turn(ctx context.Context, pane, transcript string) (t agents.Turn, ok bool)
	Notify(ctx context.Context, title, body string) error
}

// Lane is one lane of a project: its worktree, the task its branch holds open ("" for none), the lane's worker agent
// and its status state (checks.LaneStatus: free, dirty-free, working, unknown, …), with why it is unknown ("" otherwise,
// T-871). Every lane is pooled: the model it runs is its dispatch's (T-765).
type Lane struct {
	Name, Dir, Task string
	Agent, State    string
	Reason          string
}

// Lanes reads a project's lanes.
type Lanes interface {
	Lanes(root string) ([]Lane, error)
}
