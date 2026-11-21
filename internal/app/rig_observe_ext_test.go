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
	"obor/internal/store"
	"obor/internal/testkit"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type rig struct {
	*term
	t     *testing.T
	root  string // the project "pj"
	wt    string // its lanes' parent dir
	lanes *laneTable
	clock *clock

	mu    sync.Mutex
	procs []background.Proc // the process table Busy reads

	at      *adapter                        // the terminal adapter the app is built on (nil: the rig's term)
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
	r := &rig{term: newTerm(map[string]string{"w1": "pj", "w2": "other"}), t: t, root: filepath.Join(base, "pj"),
		wt:    filepath.Join(base, "pj-wt"),
		lanes: &laneTable{},
		clock: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}}
	if err := os.MkdirAll(filepath.Join(r.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	for i, lane := range []string{"L-01", "L-02"} {
		task := []string{"T-1", "T-2"}[i]
		r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: lane, Dir: r.lane(lane), Task: task})
		// labelled by its launch: serve's identity of it
		r.addPane(agentPane("w1:p"+lane[3:], "pj-l-0"+lane[3:], r.lane(lane)))
	}
	r.procs = []background.Proc{{PID: 2, PPID: 1, PGID: 2}} // the agent (pid 2, the fake's foreground group), nothing under it
	return r
}

// term is the terminal a rig scripts: testkit.Term, an in-memory terminal in the port's words (its pane table, its
// one stream and its call log), and what the rig scripts on it. Its methods say what a test does to the terminal (set
// a pane, add an agent, end the stream, the calls an operation made, a notification sent), so a flow test names no
// adapter and none of a terminal's wire: a scenario stated as port.Pane, port.Event and a call of a port operation is
// one every adapter has to meet.
type term struct {
	tt *testkit.Term

	mu       sync.Mutex
	groups   map[string]string // by group id (a pane id's part before the colon): the group's name
	fg       map[string]int    // by pane: the foreground process group setForeground set (0: the rig's default)
	gone     bool              // terminalGone: every call fails as the terminal's not running
	down     string            // terminalDown's message: the terminal answers no snapshot
	blocked  string            // promptBlocked's message: every prompt refused
	onPrompt func(to string)   // beforePrompt's
	holds    map[string]*held  // by operation: its calls wait for the hold's release
}

// newTerm is a terminal with groups (by group id, the group's name) and no pane.
func newTerm(groups map[string]string) *term {
	tm := &term{tt: testkit.NewTerm(), groups: groups, fg: map[string]int{}, holds: map[string]*held{}}
	tm.tt.Before(tm.before)
	return tm
}

// op is a terminal operation the app's calls are counted by, as the port names it.
type op string

const (
	opSnapshot    op = "snapshot"     // Panes.Snapshot, and the one a Subscribe reads: the terminal's whole session
	opNotify      op = "notify"       // Panes.Notify: a desktop notification
	opKeys        op = "keys"         // PaneControl.SendKeys, and a prompt's clear of the input line
	opProcessInfo op = "process-info" // PaneControl.ProcessInfo
	opScreen      op = "screen"       // PaneControl.Screen: the pane's visible text
)

// opCalls is the terminal's operations each op counts.
var opCalls = map[op][]string{
	opSnapshot: {testkit.OpSnapshot, testkit.OpSubscribe}, opNotify: {testkit.OpNotify},
	opKeys: {testkit.OpKeys}, opProcessInfo: {testkit.OpProcessInfo}, opScreen: {testkit.OpScreen},
}

// refreshEnds is the event kind of the subscription a stream ends as a refresh: a moved pane or a new group, which
// the port has no event kind for (the app reads a new snapshot instead).
const refreshEnds = "refresh"

// held is a hold on an operation's calls: reached closes at the first call, release lets them answer.
type held struct {
	reached, release chan struct{}
	once             sync.Once
}

// before is the rig's script of each call, before the terminal answers it: down, a hold, a refused prompt, an agent's
// quit typed into its pane (the agent exits to its shell), and the shell's answer to a command line (its success word
// printed, as a shell runs an env line's "&& echo <word>").
func (tm *term) before(c testkit.Call) error {
	tm.mu.Lock()
	gone, down, blocked, onPrompt, h := tm.gone, tm.down, tm.blocked, tm.onPrompt, tm.holds[c.Op]
	tm.mu.Unlock()
	switch {
	case gone:
		return &testkit.TermError{Msg: "the terminal is not running", Gone: true}
	case down != "" && (c.Op == testkit.OpSubscribe || c.Op == testkit.OpSnapshot):
		return &testkit.TermError{Msg: "unavailable: " + down, Answer: true}
	}
	if h != nil {
		h.once.Do(func() { close(h.reached) })
		<-h.release
	}
	switch {
	case c.Op == testkit.OpPrompt && onPrompt != nil:
		onPrompt(c.Pane)
	case c.Op == testkit.OpWrite && c.Submit && (c.Text == "/exit" || c.Text == "/quit"):
		tm.setPane(c.Pane, func(p *port.Pane) { p.Agent, p.Status = "", "" }) // the agent quits to its shell
	case c.Op == testkit.OpWrite && c.Submit:
		if _, rest, ok := strings.Cut(c.Text, "&& echo "); ok {
			word, _, _ := strings.Cut(rest, " ")
			tm.tt.SetScreen(c.Pane, strings.ReplaceAll(word, `"`, "")+"\n% ")
		}
	}
	if c.Op == testkit.OpPrompt && blocked != "" {
		return &testkit.TermError{Msg: "agent blocked: " + blocked, Answer: true}
	}
	return nil
}

// hold holds the calls of operation o until release; reached closes once the first one came.
func (tm *term) hold(o string) (release func(), reached <-chan struct{}) {
	h := &held{reached: make(chan struct{}), release: make(chan struct{})}
	tm.mu.Lock()
	tm.holds[o] = h
	tm.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			tm.mu.Lock()
			if tm.holds[o] == h {
				delete(tm.holds, o)
			}
			tm.mu.Unlock()
			close(h.release)
		})
	}, h.reached
}

// info is the process info of pane p as the rig scripts it: the shell (pid 1) in the foreground, or the agent's group
// (2) while an agent runs, unless setForeground set another group.
func (tm *term) info(p port.Pane) port.ProcessInfo {
	tm.mu.Lock()
	fg := tm.fg[p.ID]
	tm.mu.Unlock()
	switch {
	case fg != 0:
	case p.Agent != "":
		fg = 2
	default:
		fg = 1
	}
	return port.ProcessInfo{ShellPID: 1, FgPGID: fg}
}

// groupOf is the name of the group pane id is in ("" none).
func (tm *term) groupOf(id string) string {
	g, _, _ := strings.Cut(id, ":")
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.groups[g]
}

// paneOf is pane id as the port has it now (zero: no such pane).
func (tm *term) paneOf(id string) port.Pane {
	for _, p := range tm.tt.Table() {
		if p.ID == id {
			return p
		}
	}
	return port.Pane{}
}

// panes is the terminal's table as the port has it, for a failure's message.
func (tm *term) panes() []port.Pane { return tm.tt.Table() }

// edited is p after a change: a transcript is the session the terminal reports, and its process info follows the
// agent in it; with no agent the terminal keeps no name for one.
func (tm *term) edited(p *port.Pane) {
	if p.Transcript != "" {
		p.Session = p.Transcript
	}
	tm.tt.SetInfo(p.ID, tm.info(*p))
	if p.Agent == "" {
		tm.tt.SetName(p.ID, "")
	}
}

// setPane changes pane id as the port has it: its label, the agent in it, its status, cwd and session (Agent "" leaves
// the pane at its shell). No event: serve sees it at its next snapshot (push, refresh) or by an event the test sends.
func (tm *term) setPane(id string, fn func(*port.Pane)) bool {
	q := tm.paneOf(id)
	if q.ID == "" {
		return false
	}
	fn(&q)
	tm.edited(&q)
	return tm.tt.Edit(id, func(p *port.Pane) { *p = q })
}

// addPane puts p in the terminal's table, in its id's group unless p names one: an agent of kind p.Agent in it when
// one is set, else a shell. The agent's name is the pane's label, as a start labels the pane it runs in. No event (see
// setPane).
func (tm *term) addPane(p port.Pane) {
	if p.Group == "" {
		p.Group = tm.groupOf(p.ID)
	}
	tm.edited(&p)
	if p.Agent != "" {
		tm.tt.SetName(p.ID, p.Label)
	}
	tm.tt.Put(p)
}

// agentPane is an idle claude agent named and labelled label in pane id, cwd cwd.
func agentPane(id, label, cwd string) port.Pane {
	return port.Pane{ID: id, Label: label, Agent: "claude", Status: "idle", Cwd: cwd}
}

// removePane drops pane id from the table (closed or exited behind the stream's back: closePane and exitPane send its
// event).
func (tm *term) removePane(id string) { tm.tt.Drop(id) }

// setName sets the name the terminal runs pane id's agent under ("" none): what its holder lookup answers by, which the
// port keeps out of a pane (Pane.Label is the pane's label).
func (tm *term) setName(id, name string) { tm.tt.SetName(id, name) }

// nameOf is the name the terminal runs pane id's agent under ("" none, or no such pane).
func (tm *term) nameOf(id string) string {
	if tm.paneOf(id).ID == "" {
		return ""
	}
	return tm.tt.Name(id)
}

// setScreen sets what pane id shows: its visible text (PaneControl.Screen).
func (tm *term) setScreen(id, text string) { tm.tt.SetScreen(id, text) }

// setForeground sets the foreground process group of pane id's shell (PaneControl.ProcessInfo): the app reads the pane
// as busy while it is neither the agent's group nor the shell's.
func (tm *term) setForeground(id string, pgid int) {
	tm.mu.Lock()
	tm.fg[id] = pgid
	tm.mu.Unlock()
	tm.tt.SetInfo(id, tm.info(tm.paneOf(id)))
}

// group names the terminal's group id label: the group the app sees its panes in (port.Pane.Group). No event (see
// setPane).
func (tm *term) group(id, label string) {
	tm.mu.Lock()
	tm.groups[id] = label
	tm.mu.Unlock()
	for _, p := range tm.tt.Table() {
		if g, _, _ := strings.Cut(p.ID, ":"); g == id {
			tm.tt.Edit(p.ID, func(p *port.Pane) { p.Group = label })
		}
	}
}

// ungroup drops group id: its panes are in no group the app sees.
func (tm *term) ungroup(id string) { tm.group(id, "") }

// endStream ends the stream serve works from as a lost connection. The terminal is still there, so serve subscribes
// again.
func (tm *term) endStream() { tm.tt.End(&testkit.TermError{Msg: "connection closed", Gone: true}) }

// streamLost ends the stream with the terminal's own answer (code: msg), which the app logs and subscribes again
// after.
func (tm *term) streamLost(code, msg string) {
	tm.tt.End(&testkit.TermError{Msg: code + ": " + msg, Answer: true})
}

// terminalGone is the terminal down: the stream ends and the app's calls and subscribes fail until terminalBack.
func (tm *term) terminalGone() {
	tm.mu.Lock()
	tm.gone = true
	tm.mu.Unlock()
	tm.endStream()
}

// terminalBack is the terminal up again on the same panes, after terminalGone.
func (tm *term) terminalBack(t testing.TB) {
	t.Helper()
	tm.mu.Lock()
	tm.gone = false
	tm.mu.Unlock()
}

// terminalDown scripts a terminal that answers no snapshot and drops the stream: a down terminal the app cannot dial
// as hers (it answers, but no snapshot).
func (tm *term) terminalDown(msg string) {
	tm.mu.Lock()
	tm.down = msg
	tm.mu.Unlock()
	tm.endStream()
}

// statusEvent sets pane's status in the table and sends the status event that carries it.
func (tm *term) statusEvent(pane, status, agent string) {
	q := tm.paneOf(pane)
	q.Status = status
	tm.edited(&q)
	tm.tt.Set(pane, func(p *port.Pane) { *p = q })
}

// closePane drops pane from the table and sends the event of a pane closed by a person.
func (tm *term) closePane(pane string) { tm.tt.Remove(pane, false) }

// exitPane drops pane and sends the event of its shell exiting.
func (tm *term) exitPane(pane string) { tm.tt.Remove(pane, true) }

// refresh ends serve's stream as a layout change does: serve reads a new snapshot and subscribes again
// (port.Refreshed).
func (tm *term) refresh() { tm.tt.End(testkit.Refresh()) }

// announce sends pane's created event with the pane as the table has it: serve reads it in place, no refresh.
func (tm *term) announce(pane string) { tm.tt.Add(tm.paneOf(pane)) }

// waitSubscribed waits (up to 5 s, else the test fails) until the stream serve reads events of kind for pane from is
// live ("" for a lifecycle kind; refreshEnds for the stream a refresh ends): the terminal's one stream carries every
// kind for every pane.
func (tm *term) waitSubscribed(t testing.TB, kind, pane string) {
	t.Helper()
	switch kind {
	case port.EventStatus, port.EventCreated, port.EventClosed, port.EventExited, refreshEnds:
	default:
		t.Fatalf("no such event kind %q", kind)
	}
	what := "a live stream for " + kind + " events of " + pane
	if how := poll(t.(*testing.T), what, 5*time.Second, time.Millisecond, nil, tm.tt.Streaming); how != "" {
		t.Fatalf("%s waiting for %s: %q", how, what, tm.callLines())
	}
}

// subscribed is how many streams the terminal has live (0: none: the stream ended and serve has not subscribed
// again).
func (tm *term) subscribed() int {
	if tm.tt.Streaming() {
		return 1
	}
	return 0
}

// calls is how many calls of the port operation o the app made.
func (tm *term) calls(o op) int {
	ops, ok := opCalls[o]
	if !ok {
		panic("the rig counts no terminal operation " + string(o))
	}
	n := 0
	for _, c := range tm.tt.Calls("") {
		if slices.Contains(ops, c.Op) || (o == opKeys && c.Op == testkit.OpPrompt && c.Submit) {
			n++
		}
	}
	return n
}

// callLines is every call the terminal got, for a failure's message.
func (tm *term) callLines() []string {
	var out []string
	for _, c := range tm.tt.Calls("") {
		out = append(out, c.String())
	}
	return out
}

// notification is one notification the app asked the terminal to show.
type notification struct{ Title, Body string }

// notifications is the notifications the app asked the terminal to show, in order.
func (tm *term) notifications() []notification {
	var out []notification
	for _, c := range tm.tt.Calls(testkit.OpNotify) {
		out = append(out, notification{c.Title, c.Body})
	}
	return out
}

// agentStarts is every agent start the app asked the terminal for (port.AgentStart: the agent's name as the label, the
// pane, the kind and the argv, its first prompt in it).
func (tm *term) agentStarts() []port.AgentStart {
	var out []port.AgentStart
	for _, c := range tm.tt.Calls(testkit.OpStart) {
		out = append(out, c.Start)
	}
	return out
}

// touched is the calls that change a pane: a start of an agent, a rename, keys, input. A refused start leaves it empty.
func (tm *term) touched() []string {
	var out []string
	for _, c := range tm.tt.Calls("") {
		switch c.Op {
		case testkit.OpStart, testkit.OpRename, testkit.OpKeys, testkit.OpWrite:
			out = append(out, c.String())
		}
	}
	return out
}

// holdSubscribe holds the next subscribe's answer until release: the stream serve opens after it is not live yet.
func (tm *term) holdSubscribe() (release func(), reached <-chan struct{}) {
	return tm.hold(testkit.OpSubscribe)
}

// holdProcessInfo holds the next process-info answer until release: the watchdogs' sample in flight.
func (tm *term) holdProcessInfo() (release func(), reached <-chan struct{}) {
	return tm.hold(testkit.OpProcessInfo)
}

// promptBlocked makes every prompt refused as a blocked agent's.
func (tm *term) promptBlocked(msg string) {
	tm.mu.Lock()
	tm.blocked = msg
	tm.mu.Unlock()
}

// promptTaken makes prompts taken again (the agent working on the text).
func (tm *term) promptTaken() {
	tm.mu.Lock()
	tm.blocked = ""
	tm.mu.Unlock()
}

// beforePrompt runs fn before each prompt is answered, with the pane it addresses.
func (tm *term) beforePrompt(fn func(to string)) {
	tm.mu.Lock()
	tm.onPrompt = fn
	tm.mu.Unlock()
}

// adapter is what the app takes of a terminal: its panes and events, a pane's process info and its screen. A boundary
// test builds it on a real adapter (rig.at); the rig's own is its term's.
type adapter struct {
	Panes  port.Panes
	Info   func(ctx context.Context, pane string) (port.ProcessInfo, error)
	Screen func(ctx context.Context, pane string, lines int) (string, error)
}

// adapter is the app's terminal on tm.
func (tm *term) adapter() *adapter {
	return &adapter{Panes: tm.tt.Panes(), Info: tm.tt.ProcessInfo, Screen: tm.tt.Screen}
}

// client is the terminal's pane operations, for a launcher a test runs itself.
func (tm *term) client() port.PaneControl { return tm.tt.Control() }

// prompted is each prompt the terminal got: the pane it addresses, its text and whether it cleared the input line
// first.
func (tm *term) prompted() []testkit.Call { return tm.tt.Calls(testkit.OpPrompt) }

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

// quietProcs is the process table of the rig's agent (pid 2, its foreground group) with nothing under it.
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
	at := r.at
	if at == nil {
		at = r.term.adapter()
	}
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.root, Tick: 2 * time.Millisecond, Retry: 5 * time.Millisecond,
		StepRetry: 5 * time.Millisecond, StepRetryMax: 40 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Info: at.Info, Screen: at.Screen, Procs: r.procsFn, Refresh: port.Refreshed,
		Loop: r.loop, Restore: r.restore}, at.Panes, r.lanes)
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
