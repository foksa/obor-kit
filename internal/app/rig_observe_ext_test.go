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
	term
	t     *testing.T
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
	r := &rig{term: term{f: herdrtest.New()}, t: t, root: filepath.Join(base, "pj"), wt: filepath.Join(base, "pj-wt"),
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

// term is the terminal a rig scripts: the fake herdr's pane table and its socket. Its methods say what a test does to
// the terminal in the port's words (set a pane, add an agent, end the stream, the calls an operation made, a
// notification sent), so a flow test names no adapter, no fake and none of the terminal's wire: a scenario stated as
// port.Pane, port.Event and a call of a port operation is one every adapter has to meet.
type term struct {
	f   *herdrtest.Fake
	srv *herdrtest.Server
}

// op is a terminal operation the app's calls are counted by, as the port names it.
type op string

const (
	opSnapshot    op = "snapshot"     // Panes.Snapshot: the terminal's whole session
	opNotify      op = "notify"       // Panes.Notify: a desktop notification
	opKeys        op = "keys"         // PaneControl.SendKeys
	opProcessInfo op = "process-info" // PaneControl.ProcessInfo
	opScreen      op = "screen"       // PaneControl.Screen: the pane's visible text
	opAgent       op = "agent"        // the terminal's read of the pane's agent
)

// opMethod is the terminal's own request behind each operation.
var opMethod = map[op]string{
	opSnapshot: "session.snapshot", opNotify: "notification.show", opKeys: "agent.send_keys",
	opProcessInfo: "pane.process_info", opScreen: "pane.read", opAgent: "agent.get",
}

// refreshEnds is the event kind of the subscription a stream ends as a refresh: a moved pane or a new workspace, which
// the port has no event kind for (the app reads a new snapshot instead).
const refreshEnds = "refresh"

// eventType is the terminal's subscription type of a port event kind ("" for none).
func eventType(kind string) string {
	switch kind {
	case port.EventStatus:
		return herdr.EventStatus
	case port.EventCreated:
		return herdr.EventCreated
	case port.EventClosed:
		return herdr.EventClosed
	case port.EventExited:
		return herdr.EventExited
	case refreshEnds:
		return "workspace.created"
	}
	return ""
}

// paneUnder is fake pane p as the port has it (the fake's lock is held).
func (tm *term) paneUnder(p *herdrtest.Pane) port.Pane {
	out := port.Pane{ID: p.ID, Cwd: p.Cwd, Label: p.Label, Agent: p.Kind, Status: p.Status,
		Transcript: p.Transcript, Session: p.Session, Terminal: p.Terminal}
	if p.Transcript != "" { // the terminal reports a transcript as the session itself (herdr: its kind is path)
		out.Session = p.Transcript
	}
	for _, w := range tm.f.Workspaces {
		if w.ID == p.Workspace {
			out.Group = w.Label
		}
	}
	return out
}

// script writes q back onto fake pane p: the port's fields, and the name the terminal runs the agent under dropped
// when no agent runs (as herdr drops a listed agent's name when the agent exits).
func (tm *term) script(p *herdrtest.Pane, q port.Pane) {
	p.Cwd, p.Label, p.Kind, p.Status, p.Terminal = q.Cwd, q.Label, q.Agent, q.Status, q.Terminal
	switch {
	case q.Transcript != "":
		p.Transcript, p.Session = q.Transcript, ""
	case q.Session != "":
		p.Transcript, p.Session = "", q.Session
	default:
		p.Transcript, p.Session = "", ""
	}
	if q.Agent == "" {
		p.Name = ""
	}
}

// paneOf is pane id as the port has it now (zero: no such pane).
func (tm *term) paneOf(id string) port.Pane {
	var out port.Pane
	tm.f.Locked(func() {
		for _, p := range tm.f.Panes {
			if p.ID == id {
				out = tm.paneUnder(p)
			}
		}
	})
	return out
}

// panes is the terminal's table as the port has it, for a failure's message.
func (tm *term) panes() []port.Pane {
	var out []port.Pane
	tm.f.Locked(func() {
		for _, p := range tm.f.Panes {
			out = append(out, tm.paneUnder(p))
		}
	})
	return out
}

// setPane changes pane id as the port has it: its label, the agent in it, its status, cwd and session (Agent "" leaves
// the pane at its shell).
func (tm *term) setPane(id string, fn func(*port.Pane)) {
	tm.f.Set(id, func(p *herdrtest.Pane) {
		q := tm.paneUnder(p)
		fn(&q)
		tm.script(p, q)
	})
}

// addPane puts p in the terminal's table: an agent of kind p.Agent in it when one is set, else a shell. The agent's
// name is the pane's label, as a start labels the pane it runs in ("" when the label is none).
func (tm *term) addPane(p port.Pane) {
	if p.Agent != "" {
		tm.f.Agent(p.ID, p.Label, p.Status, p.Cwd)
	} else {
		tm.f.Shell(p.ID, p.Label, p.Cwd)
	}
	tm.setPane(p.ID, func(q *port.Pane) { *q = p })
}

// removePane drops pane id from the table (closed or exited behind the stream's back: closePane and exitPane send its
// event).
func (tm *term) removePane(id string) { tm.f.Remove(id) }

// setName sets the name the terminal runs pane id's agent under ("" none): what its holder lookup answers by, which the
// port keeps out of a pane (Pane.Label is the pane's label).
func (tm *term) setName(id, name string) { tm.f.Set(id, func(p *herdrtest.Pane) { p.Name = name }) }

// nameOf is the name the terminal runs pane id's agent under ("" none, or no such pane).
func (tm *term) nameOf(id string) string {
	name := ""
	tm.f.Locked(func() {
		for _, p := range tm.f.Panes {
			if p.ID == id {
				name = p.Name
			}
		}
	})
	return name
}

// setScreen sets what pane id shows: its visible text (PaneControl.Screen).
func (tm *term) setScreen(id, text string) { tm.f.Set(id, func(p *herdrtest.Pane) { p.Screen = text }) }

// setForeground sets the foreground process group of pane id's shell (PaneControl.ProcessInfo): the app reads the pane
// as busy while it is neither the agent's group nor the shell's.
func (tm *term) setForeground(id string, pgid int) {
	tm.f.Set(id, func(p *herdrtest.Pane) { p.Foreground = pgid })
}

// group labels the terminal's group id label: the group the app sees a pane of it in (port.Pane.Group).
func (tm *term) group(id, label string) { tm.f.Group(id, label) }

// ungroup drops group id: its panes are in no group the app sees.
func (tm *term) ungroup(id string) { tm.f.Ungroup(id) }

// endStream ends the stream serve works from: the terminal drops its connections. The terminal is still there, so
// serve subscribes again.
func (tm *term) endStream() { tm.srv.DropConns() }

// streamLost ends the stream with the terminal's own error (herdr: events_lost), which the app logs and subscribes
// again after.
func (tm *term) streamLost(code, msg string) { tm.srv.PushError(code, msg) }

// terminalGone is the terminal down: its socket dies, so the app's calls and subscribes fail until terminalBack.
func (tm *term) terminalGone() { tm.srv.Die() }

// terminalBack is the terminal up again on the same panes, after terminalGone.
func (tm *term) terminalBack(t testing.TB) {
	t.Helper()
	tm.srv.Revive(t)
}

// terminalDown scripts a terminal that answers no snapshot and drops the stream: a down terminal the app cannot dial
// as hers (its socket is there, its answers are not).
func (tm *term) terminalDown(msg string) {
	tm.srv.Fail("session.snapshot", "unavailable", msg)
	tm.srv.DropConns()
}

// statusEvent sets pane's status in the table and sends the status event that carries it.
func (tm *term) statusEvent(pane, status, agent string) {
	tm.f.Set(pane, func(p *herdrtest.Pane) { p.Status = status })
	tm.srv.Push(herdr.EventStatus, map[string]any{"pane_id": pane, "agent_status": status, "agent": agent})
}

// closePane drops pane from the table and sends the event of a pane closed by a person.
func (tm *term) closePane(pane string) {
	tm.removePane(pane)
	tm.srv.Push("pane_closed", map[string]any{"pane_id": pane})
}

// exitPane drops pane and sends the event of its shell exiting.
func (tm *term) exitPane(pane string) {
	tm.removePane(pane)
	tm.srv.Push("pane_exited", map[string]any{"pane_id": pane})
}

// refresh sends the layout event that ends serve's stream: serve reads a new snapshot and subscribes again
// (port.Refreshed).
func (tm *term) refresh() { tm.srv.Push("workspace_created", workspaceEvent) }

// waitSubscribed waits (up to 5 s, else the test fails) until the stream has a live subscription of kind for pane
// ("" for a lifecycle kind; refreshEnds for the subscription a refresh ends).
func (tm *term) waitSubscribed(t testing.TB, kind, pane string) {
	t.Helper()
	typ := eventType(kind)
	if typ == "" {
		t.Fatalf("no such event kind %q", kind)
	}
	tm.srv.WaitSubscribed(t, typ, pane)
}

// subscribed is how many event subscriptions the terminal has live (0: none: the stream ended and serve has not
// subscribed again).
func (tm *term) subscribed() int { return len(tm.srv.Subscriptions()) }

// calls is how many calls of the port operation o the app made.
func (tm *term) calls(o op) int {
	method, ok := opMethod[o]
	if !ok {
		panic("the rig counts no terminal operation " + string(o))
	}
	n := 0
	for _, req := range tm.srv.Requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}

// callLines is every call the terminal got, as the rig reads it, for a failure's message.
func (tm *term) callLines() []string { return tm.srv.Calls() }

// notification is one notification the app asked the terminal to show.
type notification struct{ Title, Body string }

// notifications is the notifications the app asked the terminal to show, in order.
func (tm *term) notifications() []notification {
	var out []notification
	for _, req := range tm.srv.Requests() {
		if req.Method != opMethod[opNotify] {
			continue
		}
		var n notification
		_ = json.Unmarshal(req.Params, &n)
		out = append(out, n)
	}
	return out
}

// agentStarts is every agent start the app asked the terminal for, as the port's AgentStart: the terminal's own name as the
// label, the pane, the kind and the argv (its first prompt in it).
func (tm *term) agentStarts() []port.AgentStart {
	var out []port.AgentStart
	for _, req := range tm.srv.Requests() {
		if req.Method != "agent.start" {
			continue
		}
		var p struct {
			Name string   `json:"name"`
			Kind string   `json:"kind"`
			Pane string   `json:"pane_id"`
			Args []string `json:"args"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			out = append(out, port.AgentStart{Pane: p.Pane, Label: p.Name, Kind: p.Kind, Argv: p.Args})
		}
	}
	return out
}

// touched is the calls that change a pane: a start of an agent, a rename, keys, input. A refused start leaves it empty.
func (tm *term) touched() []string {
	var out []string
	for _, req := range tm.srv.Requests() {
		switch req.Method {
		case "agent.start", "agent.rename", "pane.rename", "pane.send_keys", "pane.send_input":
			out = append(out, req.String())
		}
	}
	return out
}

// holdSubscribe holds the next subscribe's answer until release: the stream serve opens after it is not live yet.
func (tm *term) holdSubscribe() (release func(), reached <-chan struct{}) {
	return tm.srv.Hold("events.subscribe")
}

// holdProcessInfo holds the next process-info answer until release: the watchdogs' sample in flight.
func (tm *term) holdProcessInfo() (release func(), reached <-chan struct{}) {
	return tm.srv.Hold("pane.process_info")
}

// promptBlocked makes every prompt refused as a blocked agent's.
func (tm *term) promptBlocked(msg string) { tm.srv.Fail("agent.prompt", "agent_blocked", msg) }

// promptTaken makes prompts taken again (the agent working on the text).
func (tm *term) promptTaken() { tm.srv.Reply("agent.prompt", prompted) }

// beforePrompt runs fn before each prompt is answered, with the agent it addresses.
func (tm *term) beforePrompt(fn func(to string)) {
	tm.srv.Before(func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		var p struct{ Target string }
		if json.Unmarshal(req.Params, &p) == nil {
			fn(p.Target)
		}
	})
}

// client is the terminal's client, for a launcher a test runs itself.
func (tm *term) client() *herdr.Socket { return tm.srv.Client() }

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
	r.waitFor("a new snapshot", func() bool { return r.calls(opSnapshot) > before })
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
