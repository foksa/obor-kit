package launch_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"obor/internal/app/port"
)

// term is a small in-memory terminal for launch's tests: port.PaneControl and port.AgentStarter over scripted panes,
// in the port's own terms (no terminal's protocol). Every call is recorded as a line (Calls: the method, then its
// arguments as sorted key=value pairs, "Write pane=w1:p3 submit=true text=/exit"); Fail, Once and Before script a
// method's errors and run a hook before each call. Its fields and its panes' change only through Set, Locked or the
// setters Agent and Shell take while a launcher runs: a plain write races the launcher's goroutines.
type term struct {
	mu     sync.Mutex
	All    []*mpane // the panes, in their order
	calls  []string
	fail   map[string]error   // method → the error every call returns
	once   map[string][]error // method → the next calls' errors, one each
	before func(call string)
}

var (
	_ port.PaneControl  = (*term)(nil)
	_ port.AgentStarter = (*term)(nil)
)

// mpane is a scripted pane: the pane's and its agent's facts as the port reports them (Kind != "": an agent runs in
// it) and what the pane does with what it is sent.
type mpane struct {
	ID, Label, Cwd, Terminal string
	Kind, Status             string // the agent's kind ("": none runs) and status
	// Name is the terminal's own name of the agent (Named finds it; "": it keeps none).
	Name string
	// Session is the session the terminal reports for the agent as an id; Transcript is it as a path instead.
	Session, Transcript string
	Shell               int            // the pane's shell pid (0: 1)
	Procs               []port.Process // its foreground processes
	// Foreground is the pane's foreground process group: 0 = the agent's (2) when one runs, else the shell's.
	Foreground int
	// Ready is when the pane's shell becomes an available shell (zero: at once): until then ProcessInfo shows no shell
	// in the foreground and a start is refused PaneBusy.
	Ready  time.Time
	Screen string // what Screen shows; Recent shows it too, Later after it
	// Stale: the visible screen went stale (after an agent exited, T-970): what the shell prints from now on reaches
	// the pane's recent output only (Later), never Screen.
	Stale bool
	Later string // what the shell printed while Stale: Recent shows it after Screen
	// OnQuit is what /exit (Claude) or /quit (Codex) does: "" the agent exits; "stay": nothing; "linger": it exits,
	// but the terminal still lists it for now; "dialog": Claude's background-task dialog opens on the Screen, its
	// "1. Exit and stop tasks" exits, esc closes it.
	OnQuit string
	OnEsc  string // what esc does to a working or blocked agent: "" it goes idle; "ignore": nothing
	// Input is the agent's input box: Escape on a working Claude puts the interrupted prompt back there; text written
	// with Enter into a non-empty input is sent with it as a message (no command); ctrl+c and ctrl+u clear it.
	Input string
	// OnRun is what the pane's shell does with a command line written with Enter: "" it succeeds, printing the word
	// its "&& echo <word>" clause echoes (quotes removed) and a new prompt "% " on the Screen; "fail" it fails before
	// that clause, printing its "|| echo <word>" word instead; "hang": it never finishes.
	OnRun string
	// OnStart is what a start does: "" the agent starts, named and idle; "never": no agent comes up (TimedOut, the
	// start accepted); "notready": the agent runs at a startup dialog, blocked (NotReady, accepted); any other word: the
	// agent starts in that status and the terminal gives up on it (TimedOut, accepted), except an agent working on a
	// first prompt, which is a start.
	OnStart string
	// StartBusy is how many starts yet are refused PaneBusy (each spends one).
	StartBusy int
	// Says is a line the terminal's start says on its Say (an adapter's own line: a name it kept and cleared).
	Says string
	// OnPrompt is what a prompt does: "" the agent takes it (working); "refused": refused before any typing.
	OnPrompt  string
	Keys      []string // keys received
	Submitted []string // every message the agent took (Prompt)
	Pending   string   // text Write typed and did not submit
}

// newTerm is a terminal with no panes.
func newTerm() *term { return &term{fail: map[string]error{}, once: map[string][]error{}} }

// termErr is the terminal's error, read through the port's predicates: kind is "not found", "busy", "not ready",
// "timeout", "blocked", "held" or none.
type termErr struct {
	op, kind, msg string
	accepted      bool
}

func (e *termErr) Error() string {
	if e.kind == "" {
		return "terminal " + e.op + ": " + e.msg
	}
	return "terminal " + e.op + ": " + e.msg + " (" + e.kind + ")"
}
func (e *termErr) NotFound() bool { return e.kind == "not found" }
func (e *termErr) PaneBusy() bool { return e.kind == "busy" }
func (e *termErr) NotReady() bool { return e.kind == "not ready" }
func (e *termErr) TimedOut() bool { return e.kind == "timeout" }
func (e *termErr) Answered() bool { return e.kind != "" }
func (e *termErr) Accepted() bool { return e.accepted }

// heldErr is a start refused for good: a live agent in another pane holds the name (port.NameHeld).
type heldErr struct {
	*termErr
	pane string
}

func (e *heldErr) NameHeld() string { return e.pane }

// termError is an error of op of kind (see termErr) saying msg.
func termError(op, kind, msg string) error { return &termErr{op: op, kind: kind, msg: msg} }

// notReady is a start the terminal accepted whose agent waits at a startup dialog.
func notReady() error {
	return &termErr{op: "start", kind: "not ready", msg: "blocked during startup", accepted: true}
}

// Agent adds a pane with a claude agent named name in status and returns it; a pane already at that id becomes this
// agent's in place. The setters run under the terminal's lock.
func (f *term) Agent(pane, name, status, cwd string, set ...func(*mpane)) *mpane {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.add(pane)
	p.Kind, p.Name, p.Status, p.Cwd = "claude", name, status, cwd
	for _, fn := range set {
		fn(p)
	}
	return p
}

// Shell adds a pane with no agent, labelled label, and returns it; a pane already at that id becomes this shell in
// place. The setters run as Agent's do.
func (f *term) Shell(pane, label, cwd string, set ...func(*mpane)) *mpane {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.add(pane)
	p.Kind, p.Name, p.Status, p.Label, p.Cwd = "", "", "", label, cwd
	for _, fn := range set {
		fn(p)
	}
	return p
}

func (f *term) add(id string) *mpane {
	if p := f.pane(id); p != nil {
		return p
	}
	p := &mpane{ID: id}
	f.All = append(f.All, p)
	return p
}

// Set runs fn on pane id under the terminal's lock (nothing when there is none).
func (f *term) Set(id string, fn func(*mpane)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.pane(id); p != nil {
		fn(p)
	}
}

// Locked runs fn under the terminal's lock (a read of a pane while a launcher runs).
func (f *term) Locked(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// At is pane id; nil when none.
func (f *term) At(id string) *mpane {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pane(id)
}

func (f *term) pane(id string) *mpane {
	for _, p := range f.All {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Fail makes every call of method fail with err; Clear drops it.
func (f *term) Fail(method string, err error) {
	f.mu.Lock()
	f.fail[method] = err
	f.mu.Unlock()
}

func (f *term) Clear(method string) {
	f.mu.Lock()
	delete(f.fail, method)
	f.mu.Unlock()
}

// Once makes the next calls of method fail with errs, one each (the panes are not touched).
func (f *term) Once(method string, errs ...error) {
	f.mu.Lock()
	f.once[method] = append(f.once[method], errs...)
	f.mu.Unlock()
}

// Before runs fn with each call's line before the call acts.
func (f *term) Before(fn func(call string)) {
	f.mu.Lock()
	f.before = fn
	f.mu.Unlock()
}

// Calls is every call so far, one line each.
func (f *term) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// call records method with args (key, value pairs), runs the Before hook, and returns the scripted error of method
// (nil: none). The caller holds no lock; call returns holding it.
func (f *term) call(method string, args ...any) error {
	var parts []string
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprint(args[i])+"="+arg(args[i+1]))
	}
	slices.Sort(parts)
	line := strings.Join(append([]string{method}, parts...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	before := f.before
	f.mu.Unlock()
	if before != nil {
		before(line)
	}
	f.mu.Lock()
	if errs := f.once[method]; len(errs) > 0 {
		f.once[method] = errs[1:]
		return errs[0]
	}
	return f.fail[method]
}

// arg is v as a call line shows it: a list in [a b].
func arg(v any) string {
	if l, ok := v.([]string); ok {
		return "[" + strings.Join(l, " ") + "]"
	}
	return fmt.Sprint(v)
}

// port is p as the port reports it: its agent's facts when one runs there, the session as the terminal reports it.
func (p *mpane) port() port.Pane {
	out := port.Pane{ID: p.ID, Label: p.Label, Cwd: p.Cwd, Terminal: p.Terminal, Agent: p.Kind, Status: p.Status,
		Session: p.Session}
	if p.Transcript != "" {
		out.Session, out.Transcript = p.Transcript, p.Transcript
	}
	return out
}

func (f *term) Pane(ctx context.Context, id string) (port.Pane, error) {
	err := f.call("Pane", "pane", id)
	defer f.mu.Unlock()
	if err != nil {
		return port.Pane{}, err
	}
	p := f.pane(id)
	if p == nil {
		return port.Pane{}, termError("pane", "not found", "pane "+id+" not found")
	}
	return p.port(), nil
}

func (f *term) Panes(ctx context.Context) ([]port.Pane, error) {
	err := f.call("Panes")
	defer f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]port.Pane, 0, len(f.All))
	for _, p := range f.All {
		out = append(out, p.port())
	}
	return out, nil
}

func (f *term) Named(ctx context.Context, name string) (port.Pane, error) {
	err := f.call("Named", "name", name)
	defer f.mu.Unlock()
	if err != nil {
		return port.Pane{}, err
	}
	for _, p := range f.All {
		if p.Kind != "" && p.Name != "" && p.Name == name {
			a := p.port()
			a.Label = ""
			return a, nil
		}
	}
	return port.Pane{}, termError("named", "not found", "agent "+name+" not found")
}

// shell is p's shell pid.
func (p *mpane) shell() int {
	if p.Shell == 0 {
		return 1
	}
	return p.Shell
}

// foreground is p's foreground process group: Foreground, else the agent's (2) or the shell's. A pane whose shell is
// not up yet has neither: the terminal's own process leads.
func (p *mpane) foreground() int {
	switch {
	case !p.ready():
		return p.shell() + 1
	case p.Foreground != 0:
		return p.Foreground
	case p.Kind != "":
		return 2
	}
	return p.shell()
}

func (p *mpane) ready() bool   { return p.Ready.IsZero() || !time.Now().Before(p.Ready) }
func (p *mpane) atShell() bool { return p.foreground() == p.shell() }

func (f *term) ProcessInfo(ctx context.Context, id string) (port.ProcessInfo, error) {
	err := f.call("ProcessInfo", "pane", id)
	defer f.mu.Unlock()
	if err != nil {
		return port.ProcessInfo{}, err
	}
	p := f.pane(id)
	if p == nil {
		return port.ProcessInfo{}, termError("process info", "not found", "pane "+id+" not found")
	}
	return port.ProcessInfo{Pane: p.ID, ShellPID: p.shell(), FgPGID: p.foreground(),
		Processes: append([]port.Process(nil), p.Procs...)}, nil
}

func (f *term) Screen(ctx context.Context, id string, lines int) (string, error) {
	err := f.call("Screen", "pane", id)
	defer f.mu.Unlock()
	if err != nil {
		return "", err
	}
	p := f.pane(id)
	if p == nil {
		return "", termError("screen", "not found", "pane "+id+" not found")
	}
	return p.Screen, nil
}

func (f *term) Recent(ctx context.Context, id string, lines int) (string, error) {
	err := f.call("Recent", "pane", id)
	defer f.mu.Unlock()
	if err != nil {
		return "", err
	}
	p := f.pane(id)
	if p == nil {
		return "", termError("recent", "not found", "pane "+id+" not found")
	}
	return p.Screen + p.Later, nil
}

func (f *term) RenamePane(ctx context.Context, id, label string) error {
	err := f.call("RenamePane", "pane", id, "label", label)
	defer f.mu.Unlock()
	if err != nil {
		return err
	}
	p := f.pane(id)
	if p == nil {
		return termError("rename", "not found", "pane "+id+" not found")
	}
	p.Label = label
	return nil
}

// WaitStatus waits until the agent in pane is in one of until, timeout as TimedOut; no agent there is NotFound, an
// agent that left during the wait another error.
func (f *term) WaitStatus(ctx context.Context, id string, until []string, timeout time.Duration) error {
	err := f.call("WaitStatus", "pane", id, "until", until, "timeout", timeout)
	f.mu.Unlock()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for saw := false; ; saw = true {
		f.mu.Lock()
		p := f.pane(id)
		var kind, status string
		if p != nil {
			kind, status = p.Kind, p.Status
		}
		f.mu.Unlock()
		switch {
		case kind != "" && slices.Contains(until, status):
			return nil
		case kind == "" && !saw:
			return termError("wait", "not found", "no agent in "+id)
		case kind == "":
			return termError("wait", "", "the agent left "+id)
		case !time.Now().Before(deadline):
			return termError("wait", "timeout", "timed out waiting for "+strings.Join(until, " or "))
		case ctx.Err() != nil:
			return ctx.Err()
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *term) Write(ctx context.Context, id, text string, submit bool) error {
	err := f.call("Write", "pane", id, "text", text, "submit", submit)
	defer f.mu.Unlock()
	if err != nil {
		return err
	}
	p := f.pane(id)
	if p == nil {
		return termError("write", "not found", "pane "+id+" not found")
	}
	p.Pending = text
	if submit {
		p.Pending = ""
		p.run(text)
	}
	return nil
}

func (f *term) SendKeys(ctx context.Context, id string, keys ...string) error {
	err := f.call("SendKeys", "pane", id, "keys", keys)
	defer f.mu.Unlock()
	if err != nil {
		return err
	}
	p := f.pane(id)
	if p == nil {
		return termError("keys", "not found", "pane "+id+" not found")
	}
	p.Keys = append(p.Keys, keys...)
	p.sendKeys(keys)
	return nil
}

// Prompt types text to the agent in pane: none there is NotFound; OnPrompt "refused" refuses it before any typing.
func (f *term) Prompt(ctx context.Context, id, text string, clear bool) (bool, error) {
	err := f.call("Prompt", "pane", id, "text", text, "clear", clear)
	defer f.mu.Unlock()
	if err != nil {
		return false, err
	}
	p := f.pane(id)
	if p == nil || p.Kind == "" {
		return false, termError("prompt", "not found", "no agent in "+id)
	}
	if p.OnPrompt == "refused" {
		return false, termError("prompt", "blocked", "the agent in "+id+" is blocked")
	}
	if clear {
		p.Input = ""
	}
	p.Submitted = append(p.Submitted, text)
	p.Status = "working"
	return true, nil
}

// Start is the terminal's agent start (port.AgentStarter), as OnStart and StartBusy script it.
func (f *term) Start(ctx context.Context, s port.AgentStart) (port.Pane, error) {
	err := f.call("Start", "pane", s.Pane, "label", s.Label, "kind", s.Kind, "argv", s.Argv)
	defer f.mu.Unlock()
	if err != nil {
		return port.Pane{}, err
	}
	p := f.pane(s.Pane)
	switch {
	case p == nil:
		return port.Pane{}, termError("start", "not found", "pane "+s.Pane+" not found")
	case p.StartBusy > 0:
		p.StartBusy--
		return port.Pane{}, termError("start", "busy", "pane "+p.ID+" is not an available shell")
	case !p.ready():
		return port.Pane{}, termError("start", "busy", "pane "+p.ID+" is not an available shell")
	case p.Kind != "" && !p.atShell():
		return port.Pane{}, termError("start", "busy", "pane "+p.ID+" already hosts an agent")
	}
	for _, o := range f.All {
		if o != p && o.Kind != "" && o.Name == s.Label {
			return port.Pane{}, &heldErr{&termErr{op: "start", kind: "held", msg: "agent name " + s.Label + " is taken by pane " + o.ID}, o.ID}
		}
	}
	if p.Says != "" && s.Say != nil {
		s.Say("%s", p.Says)
	}
	accepted := func(kind, msg string) error { return &termErr{op: "start", kind: kind, msg: msg, accepted: true} }
	switch p.OnStart {
	case "never":
		return port.Pane{}, accepted("timeout", fmt.Sprintf("no %s agent came up in %s within %s", s.Kind, p.ID, s.Bound))
	case "notready":
		p.Kind, p.Name, p.Status, p.Foreground = s.Kind, s.Label, "blocked", 0
		return port.Pane{}, accepted("not ready", "blocked during startup")
	case "":
		p.Kind, p.Name, p.Status, p.Foreground = s.Kind, s.Label, "idle", 0
		return p.port(), nil
	}
	p.Kind, p.Name, p.Status, p.Foreground = s.Kind, "", p.OnStart, 0
	if s.Prompt != "" && p.Status == "working" {
		return p.port(), nil
	}
	return port.Pane{}, accepted("timeout", "agent "+s.Label+" not ready for input")
}

const exitDialog = " Background tasks are running\n ❯ 1. Exit and stop tasks\n   2. Move to background\n   3. Stay\n"

// exit: the agent quit; the terminal drops it and its name, the pane is at its shell.
func (p *mpane) exit() {
	p.Kind, p.Name, p.Status, p.Foreground, p.Screen, p.Input = "", "", "", 0, "", ""
}

// run is text written with Enter: an agent with input takes it as a message; /exit or /quit makes an agent quit as
// OnQuit says; a pane at its shell runs it as OnRun says.
func (p *mpane) run(text string) {
	switch {
	case p.Kind != "" && p.Input != "":
		p.Input, p.Status = "", "working"
	case p.Kind != "" && (text == "/exit" || text == "/quit"):
		switch p.OnQuit {
		case "stay":
		case "linger":
			p.Foreground = p.shell()
		case "dialog":
			p.Screen = exitDialog
		default:
			p.exit()
		}
	case p.atShell():
		switch p.OnRun {
		case "":
			if w := echoWord(text, "&& echo "); w != "" {
				p.print(w + "\n% \n")
			}
		case "fail":
			if w := echoWord(text, "|| echo "); w != "" {
				p.print(w + "\n% \n")
			}
		}
	}
}

// print is text the pane's shell printed: on the Screen, or only in its recent output (Later) while the Screen is
// Stale.
func (p *mpane) print(text string) {
	if p.Stale {
		p.Later += text
		return
	}
	p.Screen += text
}

// echoWord is the word line echoes after sep ("&& echo " when it succeeded, "|| echo " when it failed), quotes
// removed: the env line's recipe. "" when line has no such clause.
func echoWord(line, sep string) string {
	i := strings.LastIndex(line, sep)
	if i < 0 {
		return ""
	}
	w := line[i+len(sep):]
	if j := strings.IndexByte(w, ' '); j >= 0 {
		w = w[:j]
	}
	return strings.ReplaceAll(w, `"`, "")
}

// sendKeys is keys sent to the pane: the exit dialog's 1 and esc, esc on a working or blocked agent (OnEsc), ctrl+c
// and ctrl+u clearing the input.
func (p *mpane) sendKeys(keys []string) {
	for _, k := range keys {
		if k == port.KeyEsc { // Escape drops what Write typed and did not submit
			p.Pending = ""
		}
		switch {
		case p.Screen == exitDialog && k == "1":
			p.exit()
		case p.Screen == exitDialog && k == port.KeyEsc:
			p.Screen = ""
		case k == port.KeyEsc && (p.Status == "working" || p.Status == "blocked") && p.OnEsc == "":
			p.Status = "idle"
			if p.Kind == "claude" {
				p.Input = "(the interrupted prompt)"
			}
		case k == port.KeyCtrlC, k == port.KeyCtrlU:
			p.Input = ""
		}
	}
}

// control is what a launcher's H is in these tests: the pane operations and the terminal's agent start (a wrapper
// embeds it, so the launcher still finds the AgentStarter).
type control interface {
	port.PaneControl
	port.AgentStarter
}
