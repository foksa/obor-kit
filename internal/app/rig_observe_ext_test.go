package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/background"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type rig struct {
	t     *testing.T
	f     *herdrtest.Fake
	srv   *herdrtest.Server
	root  string // the project "pj"
	wt    string // its lanes' parent dir
	lanes *laneTable
	clock *clock

	mu    sync.Mutex
	procs []background.Proc // the process table Busy reads

	loop    func(string) (*app.Loop, error) // Options.Loop (nil: observe only)
	restore port.Restorer                   // Options.Restore (nil: none)
	app     *app.App
	out     *syncBuf
	cancel  context.CancelFunc
	done    chan struct{}
	// The app's socket once serve opened it: sock outside a bubble, mem in one.
	sock string
	mem  *app.MemServe
}

// newRig is project pj (workspace w1) with lanes L-01 (T-1) and L-02 (T-2), each with an idle claude, a workspace
// w2 labelled "other", and the app not started yet.
func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{t: t, f: herdrtest.New(), root: filepath.Join(base, "pj"), wt: filepath.Join(base, "pj-wt"),
		lanes: &laneTable{},
		clock: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}}
	if err := os.MkdirAll(filepath.Join(r.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	r.f.Group("w1", "pj")
	r.f.Group("w2", "other")
	for i, lane := range []string{"L-01", "L-02"} {
		task := []string{"T-1", "T-2"}[i]
		r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: lane, Dir: r.lane(lane), Task: task})
		p := r.f.Agent("w1:p"+lane[3:], "pj-l-0"+lane[3:], "idle", r.lane(lane))
		p.Label = "pj-l-0" + lane[3:] // labelled by its launch: serve's identity of it
	}
	r.srv = serveHerdr(t, r.f)
	r.srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	r.procs = []background.Proc{{PID: 2, PPID: 1, PGID: 2}} // the agent (pid 2, the fake's foreground group), nothing under it
	return r
}

func (r *rig) lane(name string) string { return filepath.Join(r.wt, name) }

// submitted records task's task_done from lane in the project's log before the app starts (fact submitted).
func (r *rig) submitted(task, lane string) {
	r.t.Helper()
	db, err := store.OpenRW(event.DBFile(r.root))
	if err != nil {
		r.t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err != nil {
		r.t.Fatal(err)
	}
	defer log.Close()
	if _, err := log.Append(event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact,
		Data: []byte(`{"lane":"` + lane + `"}`)}, r.clock.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) setProcs(procs ...background.Proc) {
	r.mu.Lock()
	r.procs = procs
	r.mu.Unlock()
}

// quietProcs is the process table of the fake herdr's agent (pid 2, its foreground group) with nothing under it.
func quietProcs(context.Context) ([]background.Proc, error) {
	return []background.Proc{{PID: 2, PPID: 1, PGID: 2}}, nil
}

// procsFn is the machine's process table as the app's activity reads it (Options.Procs).
func (r *rig) procsFn(context.Context) ([]background.Proc, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.procs), nil
}

// start runs the app until the test ends (or stop); it returns after the app's first full check (every project and
// open task sampled once).
func (r *rig) start() {
	r.t.Helper()
	c := r.srv.Client()
	panes := herdr.Panes{S: c}
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.root, Tick: 2 * time.Millisecond, Retry: 5 * time.Millisecond,
		StepRetry: 5 * time.Millisecond, StepRetryMax: 40 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Info: c.ProcessInfo, Screen: c.Screen, Procs: r.procsFn, Refresh: func(err error) bool {
			var e *herdr.Error
			return errors.As(err, &e) && e.Code == "refresh"
		}, Loop: r.loop, Restore: r.restore}, panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
}

func (r *rig) waitConnected() {
	r.t.Helper()
	r.waitFor("connected", func() bool { return r.app.Status().Connected })
}

func (r *rig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	trail := func() string { return "app output:\n" + r.out.String() }
	if how := poll(r.t, what, 5*time.Second, time.Millisecond, trail, ok); how != "" {
		r.t.Fatalf("%s waiting for %s; %s", how, what, trail())
	}
}

// records is pj's event log (readStore).
func (r *rig) records() []event.Record {
	r.t.Helper()
	var recs []event.Record
	err := readStore(r.app, r.done, r.root, func(db *sql.DB) (err error) {
		recs, err = allRecords(db)
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return recs
}

// readStore runs read on the database of the project at root. While a serves it (done open, the project loaded),
// that is serve's own connection, its log's one writer: a second connection to the file takes a lock for a moment
// (a read-only open's first read can), and serve's write then waits in SQLite's busy sleep on the bubble's clock,
// holding the app's lock. A goroutine waiting for that lock is not durably blocked, so the clock stands still and the
// sleep never ends. Without a serving app (none started, Serve returned, its database closed) read gets a read-only
// open of the file; fs.ErrNotExist when there is no database yet.
func readStore(a *app.App, done <-chan struct{}, root string, read func(*sql.DB) error) error {
	if db := servingDB(a, done, root); db != nil {
		// database/sql's error for a closed handle: the app is past its last write (closeLog)
		if err := read(db); err == nil || !strings.Contains(err.Error(), "sql: database is closed") {
			return err
		}
	}
	db, err := store.OpenAt(root)
	if err != nil {
		return err
	}
	if db == nil {
		return fs.ErrNotExist
	}
	defer db.Close()
	return read(db)
}

// servingDB is serve's connection to root's database while a serves it (nil: no app, Serve returned, or the project
// not loaded yet).
func servingDB(a *app.App, done <-chan struct{}, root string) *sql.DB {
	if a == nil || done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	return app.DB(a, root)
}

// allRecords is db's whole event log in seq order (LIMIT -1: SQLite's no limit).
func allRecords(db *sql.DB) ([]event.Record, error) { return event.Since(db, 0, -1) }

// facts is the log's facts as "task step{reason}" lines (not the notifications' intents and results).
func (r *rig) facts() []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Kind != event.KindFact {
			continue
		}
		out = append(out, rec.Task+" "+rec.String())
	}
	return out
}

func (r *rig) waitFacts(want ...string) {
	r.t.Helper()
	r.waitFor("facts "+strings.Join(want, ", "), func() bool { return strings.Join(r.facts(), "|") == strings.Join(want, "|") })
}

func (r *rig) count(method string) int {
	n := 0
	for _, req := range r.srv.Requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}

// waitQuiet waits until task's stall timer has its baseline at at: the watchdogs took that quiet sample.
func (r *rig) waitQuiet(task string, at time.Time) {
	r.t.Helper()
	r.waitFor(task+"'s quiet baseline at "+at.Format("15:04"), func() bool {
		quiet, _ := app.Quiet(r.app, r.root, task)
		return quiet.Equal(at)
	})
}

// settle waits for two more full checks: the second one started after the call, so it saw the clock and the state
// the test set before it.
func (r *rig) settle() {
	r.t.Helper()
	n, _ := app.Counts(r.app)
	r.waitFor("two more checks", func() bool { checks, _ := app.Counts(r.app); return checks >= n+2 })
}

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// callID is the call id of a request line.
func callID(line []byte) string {
	var req struct {
		Call struct {
			CallID string `json:"call_id"`
		} `json:"call"`
	}
	_ = json.Unmarshal(line, &req)
	return req.Call.CallID
}

// settled waits until serve has subscribed again after the snapshot count before and has run some checks on that
// stream: a restore it started on the reconnect has been called by then.
func (r *rig) settled(before int) {
	r.t.Helper()
	r.waitFor("a new snapshot", func() bool { return r.count("session.snapshot") > before })
	r.waitConnected()
	checks, _ := app.Counts(r.app)
	r.waitFor("checks on the new stream", func() bool { c, _ := app.Counts(r.app); return c >= checks+3 })
}

// serve opens the running app's socket for ask: in memory in a bubble (mem), on a new socket path outside one
// (sock).
func (r *rig) serve() {
	r.t.Helper()
	if inBubble(r.t) {
		r.mem = app.ServeMem(r.t, r.app)
		return
	}
	r.sock = r.listen()
}

// ask sends c to the app's socket (serve opened it) as wire.Verdict does; see loopRig.ask.
func (r *rig) ask(c wire.Req) (string, error) {
	r.t.Helper()
	if r.mem != nil {
		return r.mem.Call(r.t, c)
	}
	return wire.Verdict(context.Background(), r.sock, c)
}

// listen answers the app socket of the running app on a new socket path. Not in a bubble (a unix socket's accept
// stalls its clock): serve and ask are the pair that follows the bubble.
func (r *rig) listen() string {
	r.t.Helper()
	if inBubble(r.t) {
		r.t.Fatal("listen opens a unix socket: in a bubble use serve and ask")
	}
	dir, err := os.MkdirTemp("", "app") // short: a unix socket path is limited
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { ln.Close() })
	go r.app.Listen(ln)
	return sock
}

// call is an integrator's verdict verb on T-1 from pj-int; edit changes it.
func (r *rig) call(verb, id string, edit ...func(*wire.VerdictReq)) *wire.VerdictReq {
	c := &wire.VerdictReq{Verb: verb, Envelope: wire.Envelope{Root: r.root, Agent: "pj-int", Role: "integrator",
		CallID: id},
		Task: "T-1", Job: "T-1/2/review", Patch: "p1", Note: "why"}
	for _, e := range edit {
		e(c)
	}
	return c
}

// record is the log's record of kind and step (zero when none).
func (r *rig) record(kind, step string) event.Record {
	for _, rec := range r.records() {
		if rec.Kind == kind && rec.Step == step {
			return rec
		}
	}
	return event.Record{}
}

// verdicts is the log's verdict records as "kind step".
func (r *rig) verdicts() []string {
	var out []string
	for _, rec := range r.records() {
		if strings.Contains(rec.Step, ".c") {
			out = append(out, rec.Kind+" "+rec.Step)
		}
	}
	return out
}
