package view

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
)

// reading is a load's outside facts beyond the project's files and store: serve's status (ask), the lane table's git
// and the agents' one ps. The zero value is the real thing; a test sets them to count what a read spawns (T-880).
type reading struct {
	ask   func(ctx context.Context, path string) (wire.Status, error)
	git   func(ctx context.Context, dir string, args ...string) (string, error)
	procs func(ctx context.Context) ([]Proc, error)
}

// inputs is what the reads share: one config snapshot (cfg; warn is its line for a key this binary does not know), the
// lane table (rows are the store's; States runs git), serve's answer (serr: it gave none) and the store (nil: none yet).
// close ends the store; err is the config's: the project cannot be read at all.
type inputs struct {
	p     *checks.LaneTable
	cfg   *config.Config
	warn  string
	ss    wire.Status
	serr  error
	db    *sql.DB
	close func()
	err   error
}

// open reads one load's shared inputs (T-880: one config snapshot per request).
func (r reading) open(ctx context.Context, root string) inputs {
	in := inputs{close: func() {}}
	cfg, warn, err := config.LoadReader(root)
	if err != nil {
		in.err = err
		return in
	}
	in.cfg, in.warn, in.p = cfg, warn, r.lanes(ctx, cfg)
	in.ss, in.serr = r.status(ctx, cfg.Root)
	in.db, in.close = storeAt(cfg.Root)
	return in
}

// Load is reading{}.load: the project's whole view, built as `obor status --json` builds it. `obor web` builds every
// project of the machine this way; the CLI builds the same Input itself, since it also needs the config for serve's
// launchd line.
func Load(ctx context.Context, root string, now time.Time, events int) (Project, error) {
	return reading{}.load(ctx, root, now, events)
}

// load is Load: serve is asked (down: its error, not an error here), the lane table is read from git and the store
// read-only (closed before it returns). A config key this binary does not know is a note, not an error
// (config.LoadReader, T-709).
func (r reading) load(ctx context.Context, root string, now time.Time, events int) (Project, error) {
	in := r.open(ctx, root)
	defer in.close()
	if in.err != nil {
		return Project{}, in.err
	}
	return Build(Input{Status: in.ss, Err: in.serr, Lanes: in.p, DB: in.db, Now: now, Events: events,
		Notes: Notes(in.cfg.Root, in.warn), Background: r.background(ctx, in.cfg.Root, in.ss, in.serr)}), nil
}

// LoadTask is the task pages' read, narrow (T-880): serve's answer, one config read and the store's lane rows and
// specs. It runs no git and no ps: a task page needs its task and its needs-you; the lanes' git facts are the
// overview's.
func LoadTask(ctx context.Context, root string) (Detail, error) {
	return reading{}.task(ctx, root)
}

func (r reading) task(ctx context.Context, root string) (Detail, error) {
	in := r.open(ctx, root)
	defer in.close()
	if in.err != nil {
		return Detail{}, in.err
	}
	rows, _ := in.p.Rows()
	v := Project{}
	ps := serveOf(&v, in.ss, in.serr, in.p, nil)
	v.Tasks = tasks(ps, rowLanes(in.cfg, rows), readSpecs(in.db))
	return Detail{Root: in.cfg.Root, Project: v}, nil
}

// LoadAgent is the agent pages' read, narrow (T-880): serve's answer, one config read and its role rows. It runs no
// git and no ps: the page's history is the store's (Detail.Agent) and the agent's long-running processes are the
// overview's.
func LoadAgent(ctx context.Context, root string) (Detail, error) {
	return reading{}.agent(ctx, root)
}

func (r reading) agent(ctx context.Context, root string) (Detail, error) {
	in := r.open(ctx, root)
	defer in.close()
	if in.err != nil {
		return Detail{}, in.err
	}
	v := Project{}
	serveOf(&v, in.ss, in.serr, in.p, nil)
	return Detail{Root: in.cfg.Root, Project: v}, nil
}

// status is wire.Ask for the serve of the project at root (a test's own when set).
func (r reading) status(ctx context.Context, root string) (wire.Status, error) {
	if r.ask != nil {
		return r.ask(ctx, wire.SocketPath(root))
	}
	return wire.Ask(ctx, wire.SocketPath(root))
}

// lanes is cfg's lane table, its git reads run by r.git (project.GitCtx when nil).
func (r reading) lanes(ctx context.Context, cfg *config.Config) *checks.LaneTable {
	p := checks.NewLaneTable(ctx, cfg)
	p.Git = r.git
	return p
}

// storeAt is the project's store at root, read-only, and the func that closes it (no project root or no store yet: a
// nil handle and a no-op, as the readers of the view read).
func storeAt(root string) (*sql.DB, func()) {
	if root == "" {
		return nil, func() {}
	}
	db, _ := store.OpenAt(root)
	if db == nil {
		return nil, func() {}
	}
	return db, func() { db.Close() }
}

// rowLanes is the store's lanes rows as lane statuses: what the lanes table alone says of a lane (no git), for a read
// that needs a task's lane and agent, not a lane's state or dirty worktree.
func rowLanes(cfg *config.Config, rows map[string]store.LaneRow) []port.Lane {
	out := make([]port.Lane, 0, len(rows))
	for _, lane := range slices.Sorted(maps.Keys(rows)) {
		out = append(out, port.Lane{Name: lane, Task: rows[lane].Task, Agent: cfg.AgentName("worker", lane)})
	}
	return out
}
