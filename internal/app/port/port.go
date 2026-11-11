// Package port holds the ports of obor's core (docs/module-map.md): the interfaces the core drives its adapters
// through and the plain types they exchange. It is a leaf below package app, so an adapter reaches the core's contract
// without importing the core: the terminal's events and snapshot (Pane, Snapshot, Event, Stream, Panes) and the one
// value cmd/obor builds of them (Terminal), the lane table (Lane, Lanes) and the effects of effects.go (Agents, Typer,
// PaneControl, Starter, Scratch, LaneSetup and their types).
package port

import (
	"context"
	"errors"
	"time"
)

// Pane is one terminal pane as the core sees it: the pane's own fields and those of the agent that runs in it (none:
// the agent fields are empty). It is the one shape the terminal adapter's agent and pane answers, its snapshot and its
// stream's events use, in terminal-neutral terms (the herdr adapter keeps herdr's own tab and workspace
// ids to itself).
type Pane struct {
	ID string
	// Group is the group the terminal shows the pane in, by the name a person reads (herdr: its workspace's label):
	// the app serves a pane whose group is its project's name. Set by Panes' snapshot and stream; "" in PaneControl's
	// answers and when the terminal reports none.
	Group string
	Cwd   string
	Label string // the pane's label (obor's agent name, set by its launch); kept when an event has none
	Agent string // the agent's kind ("" when none runs)
	// Name is the name the terminal runs the agent under (PaneControl.Start's name, Agent's target), "" when it has
	// none (it exited, or a restore dropped it): launch and setup find a running agent by it; the app never identifies
	// a pane by it (registry.go).
	Name       string
	Status     string // idle, working, blocked, done, unknown
	Transcript string // the agent's transcript file when the terminal reports one, else ""
	// Session is the agent's raw session as the terminal reports it (an id, or a path), "" when none: the session's
	// identity, independent of whether its transcript file is found.
	Session string
	// Terminal is the terminal's own id of what runs in the pane, kept when the pane moves (herdr: terminal_id): the
	// identity table's key (registry.go); "" when the terminal reports none. Kept when an event has none.
	Terminal string
	// Launched is the terminal's word that no agent launch it made in the pane is pending (herdr:
	// launch_pending false, or interactive_ready true): read after a start the terminal accepted, an agent listed with
	// it is the one that start launched, however stale its status. false while the launch is pending, and for a
	// terminal that reports no such fact: a start never counts a listing without it (it fails at its bound), so the
	// default never reads "up".
	Launched bool
}

// Ready: the pane's agent is idle: it takes a prompt now (done is idle with an unviewed reply).
func (p Pane) Ready() bool { return p.Status == "idle" || p.Status == "done" }

// Busy: the pane's agent works or waits on a person (working, blocked).
func (p Pane) Busy() bool { return p.Status == "working" || p.Status == "blocked" }

// Snapshot is the terminal's session at one moment: every pane.
type Snapshot struct {
	Version  string // the terminal's version and protocol as the snapshot said (Protocol 0: not read)
	Protocol int
	Speaks   int // the protocol obor itself speaks: Protocol above it is a newer terminal
	// Started is when the terminal's server started (herdr: its socket file's modification time, set when the server
	// creates the socket at its start and untouched while it runs; herdr's API names no instance): a terminal restart
	// changes it, so it is the instance serve's restore compares (internal/app/restore.go). Zero: not known.
	Started time.Time
	Panes   []Pane
}

// Event kinds.
const (
	EventStatus  = "status"  // Pane's agent, its status or its metadata changed: New
	EventCreated = "created" // a new pane: New
	EventClosed  = "closed"  // Pane was closed
	EventExited  = "exited"  // Pane's shell exited and herdr removed it
)

// Event is one terminal event. New is the whole pane as herdr reported it after the event (status, created): it replaces
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

// Refreshed: err ended a stream only so the caller reads a new snapshot and subscribes again (a layout change the
// subscription cannot follow): the app subscribes again with no line in Out.
func Refreshed(err error) bool {
	var e interface{ Refreshed() bool }
	return errors.As(err, &e) && e.Refreshed()
}

// Terminal is the project's terminal as cmd/obor builds it from the project's terminal kind (config.TerminalKind): the
// panes and their events (Panes), the pane operations launch needs (PaneControl), the wakes and lines typed into agents
// (Typer) and what setup asks of it (TerminalSetup). The four are fields rather than one interface because
// PaneControl.Type and Typer.Type are one name with two signatures; a terminal implements all four.
type Terminal struct {
	Panes   Panes
	Control PaneControl
	Typer   Typer
	Setup   TerminalSetup
}

// Panes is the terminal's panes and events (the herdr adapter), as the core uses it.
type Panes interface {
	Subscribe(ctx context.Context) (*Stream, error)
	// Snapshot reads herdr's session now: one session.snapshot, the same source Subscribe's Snapshot is built from.
	// A pane's cwd can change with no event (a cd in it, obor setup pane moving it into a lane), so the snapshot the
	// stream gave can be stale.
	Snapshot(ctx context.Context) (Snapshot, error)
	// Screen is pane's visible text, its last lines: what a blocked pane asks a person.
	Screen(ctx context.Context, pane string, lines int) (string, error)
	Notify(ctx context.Context, title, body string) error
}

// Lane is one lane of a project, the lane table's row (checks.LaneTable; obor status --json's lane, which view.Lane
// embeds): its worktree, branch and the task id in it ("": detached, no task id), its state (the Lane* constants;
// Reason says why it is unknown), the model it runs (Type: the dispatch's while it works, else none),
// how long ago its task's task_done was recorded (nil: none), uncommitted changes, a live warning ("": none), its
// agent (derived from the lane) and its mode (config.LaneKeep for a keep lane, "" a fresh one). Lanes,
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

// The lane states Lane.State holds (FreeState's rule; checks.LaneTable.LaneState's doc): a lane is free once
// no task holds it (its row's task landed at the lane's HEAD, or it has none) and nothing is uncommitted,
// dirty-free when no task holds it but it has uncommitted changes, working while its row's task has not landed or the
// lane's HEAD is not what landed, missing when its dir is no worktree,
// unknown when git or the store cannot say. The page's free set (web/src/lib/format.ts) is the two free
// ones, and internal/web's TestTSStringSetsMatchGo keeps it so.
const (
	LaneFree      = "free"
	LaneDirtyFree = "dirty-free"
	LaneWorking   = "working"
	LaneMissing   = "missing"
	LaneUnknown   = "unknown"
)

// Lanes reads a project's lanes (its git reads end with ctx).
type Lanes interface {
	Lanes(ctx context.Context, root string) ([]Lane, error)
}
