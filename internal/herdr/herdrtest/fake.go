// Package herdrtest is a fake herdr for tests. A Fake holds scripted panes (each may host an agent) and plugins;
// Serve puts it behind a herdr socket a herdr client (the one client, *herdr.Socket) talks to, answering herdr's
// methods and recording every request.
package herdrtest

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"obor/internal/herdr"
)

// Pane is a scripted pane: herdr's own Wire fields (embedded, so the fake cannot drift from the protocol, T-890) and
// what the fake does with them. Kind != "" means an agent runs in it (Name may still be "": herdr lost the name).
type Pane struct {
	herdr.Wire
	// Session is the agent_session herdr reports for the agent as an id; Transcript is it as a path (Kind "path")
	// instead. Neither: herdr reports no session.
	Session, Transcript string
	W, H                int // the size pane.layout gives; a split halves it
	Shell               int // the pane's shell pid (pane.process_info; 0: 1)
	// Ready is when the pane's shell becomes an available shell (zero: at once). Until then pane.process_info
	// reports no shell in the foreground and agent.start answers agent_pane_busy, as herdr does for a pane a new tab or
	// a split just made (T-628).
	Ready time.Time
	Procs []herdr.Process // its foreground processes (pane process-info)
	// Foreground is the pane's foreground process group (pane process-info; the shell is pid 1): 0 = the agent's (2)
	// when one runs, else the shell's.
	Foreground int
	Screen     string // what pane read shows; agent read shows it above the input box
	// OnQuit is what /exit (Claude) or /quit (Codex) does: "" the agent exits (herdr drops it and its name, keeps the
	// session); "stay": nothing; "linger": it exits, but herdr still lists it and its name for now; "dialog": Claude's
	// background-task dialog opens on the Screen, its "1. Exit and stop tasks" exits, esc closes it.
	OnQuit string
	// OnEsc is what esc does to a working or blocked agent: "" it goes idle; "ignore": nothing.
	OnEsc string
	// Input is the agent's input box: Escape on a working Claude puts the interrupted prompt back there; text typed
	// into a non-empty input is sent with it as a message (no command); ctrl+c clears it.
	Input string
	// LeaveAfter is how many checks of herdr's agent.wait (agent.wait) the agent survives before it exits the pane,
	// as an agent that leaves while a wait runs: 0 it stays. A check counts when the agent is seen still there, so
	// LeaveAfter 2 has the wait see it once, then find it gone (agent_not_running).
	LeaveAfter int
	// OnPrompt is what a prompt does to the agent: "" or "take" (working, seq+1); "quick" (a turn so short it is idle
	// again before anyone looks: idle, seq+2); "typed" (typed into the input box, never submitted: only Draft
	// changes; with --wait herdr answers agent_prompt_stalled). With --wait also: "busy" (taken, the wait times out:
	// error timeout), "dialog" (taken, then a dialog: blocked), "refused" (agent_blocked, nothing typed), "lost"
	// (agent_not_found), "prompted-exit" (agent_prompted with exit 1), "stalled" (agent_prompt_stalled, nothing
	// typed), "fail" (no answer, exit 1), "fail-typed" (no answer, exit 1, the text left in the input box).
	// Prompts, when not empty, gives the outcome of each next prompt in order (then OnPrompt).
	OnPrompt string
	Prompts  []string
	// OnStart is what agent start does: "" the agent starts, named and idle; "unnamed": it starts idle and herdr
	// answers success but keeps no name for it; else the agent starts but is never ready for input (working on a
	// first prompt, blocked at a dialog): herdr gives up (error timeout) and keeps no name for it, which is left in
	// status OnStart (T-387). "never": herdr answers agent_started (launch_pending, as protocol 22 does at once) but no
	// agent ever comes up: the pane stays at its shell (T-544: the command typed while the shell was busy, cut off).
	OnStart string
	// StartPending: agent start answers at once with launch_pending, as herdr 0.9.5 does (live H-L1, H-L12, T-1058): the
	// pane then lists the start's kind with a stale idle status and launch_pending for this many agent.get reads of it,
	// after which the agent is up (idle, interactive_ready). Ahead of OnStart while > 0.
	StartPending int
	// PendingStatus is the status the pending launch lists (StartPending): "" the stale idle; "working": the agent busy
	// on its first prompt while herdr keeps its launch pending (live H-L14, T-1059).
	PendingStatus string
	// StartBusy is how many agent start calls yet answer agent_pane_busy: herdr has not noticed the pane's old agent
	// leave, though pane.process_info may already show its shell (T-1053: the exit not yet reaped). Each call spends
	// one.
	StartBusy int
	// OnRun is what the pane's shell does with a command line typed at it (pane.send_input with Enter): "" it succeeds,
	// printing the word its "&& echo <word>" clause echoes (quotes removed) and a new prompt "% " on the Screen; "fail"
	// it fails before that clause (a cd into a missing dir), printing its "|| echo <word>" word instead; "hang": it
	// never finishes (no output, no prompt).
	OnRun string
	Draft string   // the input box's text (`agent read` shows it on a "❯ " line after Screen)
	Keys  []string // send-keys received
	// Submitted is every message the agent took (agent.prompt, agent.send_keys): the input box's text
	// with the prompt; a "typed" prompt adds to a non-empty Draft. Enter submits a non-empty Draft (working, seq+1)
	// unless OnEnter is "lost"; ctrl+u clears the Draft.
	Submitted []string
	OnEnter   string
	// Pending is the text pane.send_input typed that its keys have not submitted yet: Enter runs it (the command
	// recipe), Escape clears it.
	Pending string
	// Placeholder: the input box's hint while it is empty (faint with --format ansi), e.g. "Ask Codex to do anything".
	Placeholder string
}

// Plugin is a scripted plugin.list entry.
type Plugin struct{ ID, Root string }

// Fake is the fake herdr. Its fields may be changed between calls (under the test's own ordering).
type Fake struct {
	mu         sync.Mutex
	Panes      []*Pane
	Plugins    []Plugin          // nil: plugin.list answers an empty list
	Workspaces []herdr.Workspace // session.snapshot's workspaces
	TabLabels  map[string]string // tab id → the label tab.list shows (tab.create's label)
	made       int               // panes made by pane.split and tab.create
	// NewPaneBusy: a pane pane.split or tab.create makes is not an available shell for this long (0: at once, T-628).
	NewPaneBusy time.Duration
}

// New is an empty fake.
func New() *Fake {
	return &Fake{}
}

// Agent adds a pane with an agent and returns it. A pane's workspace is its id's part before the colon ("w1:p1" is in
// w1), as the fake's own new panes are.
func (f *Fake) Agent(pane, name, status, cwd string) *Pane {
	p := &Pane{Wire: herdr.Wire{ID: pane, Workspace: spaceOf(pane), Kind: "claude", Name: name, Status: status, Cwd: cwd,
		Seq: 1}}
	f.mu.Lock()
	f.Panes = append(f.Panes, p)
	f.mu.Unlock()
	return p
}

// Shell adds a pane with no agent and returns it.
func (f *Fake) Shell(pane, label, cwd string) *Pane {
	p := &Pane{Wire: herdr.Wire{ID: pane, Workspace: spaceOf(pane), Label: label, Cwd: cwd}}
	f.mu.Lock()
	f.Panes = append(f.Panes, p)
	f.mu.Unlock()
	return p
}

// Tab adds shell panes ids to tab, each w×h.
func (f *Fake) Tab(tab string, w, h int, ids ...string) {
	f.mu.Lock()
	for _, id := range ids {
		f.Panes = append(f.Panes, &Pane{Wire: herdr.Wire{ID: id, Tab: tab, Workspace: spaceOf(id)}, W: w, H: h})
	}
	f.mu.Unlock()
}

// spaceOf is the workspace of pane id: its part before the colon ("" when none).
func spaceOf(id string) string {
	ws, _, ok := strings.Cut(id, ":")
	if !ok {
		return ""
	}
	return ws
}

// Group labels workspace id label (session.snapshot's workspaces): the group the core sees its panes in
// (port.Pane.Group, T-1044). A workspace already listed is relabelled.
func (f *Fake) Group(id, label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Workspaces {
		if f.Workspaces[i].ID == id {
			f.Workspaces[i].Label = label
			return
		}
	}
	f.Workspaces = append(f.Workspaces, herdr.Workspace{ID: id, Label: label})
}

// Ungroup drops workspace id from session.snapshot's workspaces: its panes are in no group the core sees.
func (f *Fake) Ungroup(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []herdr.Workspace
	for _, w := range f.Workspaces {
		if w.ID != id {
			kept = append(kept, w)
		}
	}
	f.Workspaces = kept
}

// Pane is the pane id; nil when none.
func (f *Fake) Pane(id string) *Pane {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pane(id)
}

// wire is the pane in herdr's own Wire struct: the protocol's fields plus the agent_session its two fields script.
func (p *Pane) wire() herdr.Wire {
	w := p.Wire
	switch {
	case p.Transcript != "":
		w.AgentSession = &herdr.AgentSession{Agent: p.Kind, Kind: "path", Value: p.Transcript}
	case p.Session != "":
		w.AgentSession = &herdr.AgentSession{Agent: p.Kind, Kind: "id", Value: p.Session}
	}
	return w
}

// agentJSON is herdr's agent object: the agent's own fields; the label, tab and terminal are the pane's, not its.
func (p *Pane) agentJSON() herdr.Wire {
	w := p.wire()
	w.Label, w.Tab, w.Terminal = "", "", ""
	return w
}

func (p *Pane) paneJSON() herdr.Wire { return p.wire() }

// processInfo is herdr's process_info of the pane (the shell is pid 1).
func (p *Pane) processInfo() map[string]any {
	procs := p.Procs
	if procs == nil {
		procs = []herdr.Process{}
	}
	shell := p.Shell
	if shell == 0 {
		shell = 1
	}
	return map[string]any{"pane_id": p.ID, "shell_pid": shell, "foreground_process_group_id": p.foreground(),
		"foreground_processes": procs}
}

// foreground is the pane's foreground process group: Foreground, else the agent's (2) or the shell's (1). A pane whose
// shell is not up yet has neither: the terminal's own process leads.
func (p *Pane) foreground() int {
	sh := p.Shell
	if sh == 0 {
		sh = 1
	}
	switch {
	case !p.ready():
		return sh + 1
	case p.Foreground != 0:
		return p.Foreground
	case p.Kind != "":
		return 2
	}
	return 1
}

// ready: the pane's shell is an available shell now.
func (p *Pane) ready() bool { return p.Ready.IsZero() || !time.Now().Before(p.Ready) }

// atShell: the pane's shell (pid 1, or Shell) has the foreground.
func (p *Pane) atShell() bool {
	f := p.foreground()
	return f == 1 || f == p.Shell
}

// exit: the agent quit; herdr drops it and its name, the pane is at its shell.
func (p *Pane) exit() {
	p.Kind, p.Name, p.Status, p.Foreground, p.Screen, p.Input = "", "", "", 0, "", ""
	p.LaunchPending, p.InteractiveReady = false, false
}

const exitDialog = " Background tasks are running\n ❯ 1. Exit and stop tasks\n   2. Move to background\n   3. Stay\n"

// agentPane is the pane hosting the agent target (a name or a pane id); nil when none.
func (f *Fake) agentPane(target string) *Pane {
	for _, p := range f.Panes {
		if p.Kind != "" && (p.ID == target || (p.Name != "" && p.Name == target)) {
			return p
		}
	}
	return nil
}

func (f *Fake) pane(id string) *Pane {
	for _, p := range f.Panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// run is text plus Enter typed into the pane (pane run; pane.send_input with Enter): an agent with input takes it as a
// message; /exit or /quit makes an agent quit as OnQuit says; a pane at its shell runs it as OnRun says.
func (p *Pane) run(text string) {
	if p.Kind != "" && p.Input != "" {
		p.Input, p.Status, p.Seq = "", "working", p.Seq+1
	} else if p.Kind != "" && (text == "/exit" || text == "/quit") {
		switch p.OnQuit {
		case "stay":
		case "linger":
			p.Foreground = 1
		case "dialog":
			p.Screen = exitDialog
		default:
			p.exit()
		}
	} else if p.atShell() {
		switch p.OnRun {
		case "":
			if w := echoWord(text, "&& echo "); w != "" {
				p.Screen += w + "\n% \n"
			}
		case "fail":
			if w := echoWord(text, "|| echo "); w != "" {
				p.Screen += w + "\n% \n"
			}
		}
	}
}

// echoWord is the word line echoes after sep ("&& echo " when it succeeded, "|| echo " when it failed), quotes
// removed: the env line's recipe of T-601. "" when line has no such clause.
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

// sendKeys is keys sent to the pane (pane send-keys, pane.send_keys): the exit dialog's 1 and esc, esc on a working
// or blocked agent (OnEsc), ctrl+c clearing the input.
func (p *Pane) sendKeys(keys []string) {
	for _, k := range keys {
		if k == "esc" { // Escape drops what pane.send_input typed and did not submit
			p.Pending = ""
		}
		switch {
		case p.Screen == exitDialog && k == "1":
			p.exit()
		case p.Screen == exitDialog && k == "esc":
			p.Screen = ""
		case k == "esc" && (p.Status == "working" || p.Status == "blocked") && p.OnEsc == "":
			p.Status, p.Seq = "idle", p.Seq+1
			if p.Kind == "claude" {
				p.Input = "(the interrupted prompt)"
			}
		case k == "ctrl+c", k == "ctrl+u":
			p.Input = ""
		}
	}
}

// layout is herdr's layout of at's tab: every pane in it with its size.
func (f *Fake) layout(at *Pane) map[string]any {
	panes := []any{}
	for _, p := range f.Panes {
		if p.Tab == at.Tab {
			panes = append(panes, map[string]any{"pane_id": p.ID, "rect": map[string]int{"width": p.W, "height": p.H}})
		}
	}
	return map[string]any{"panes": panes}
}

// split adds a pane beside t (dir right: halves its width, else its height) in cwd.
func (f *Fake) split(t *Pane, dir, cwd string) *Pane {
	f.made++
	ws, _, _ := strings.Cut(t.ID, ":")
	n := &Pane{Wire: herdr.Wire{ID: fmt.Sprintf("%s:n%d", ws, f.made), Tab: t.Tab, Workspace: t.Workspace, Cwd: cwd},
		W: t.W, H: t.H}
	if f.NewPaneBusy > 0 {
		n.Ready = time.Now().Add(f.NewPaneBusy)
	}
	if dir == "right" {
		t.W, n.W = t.W/2, t.W-t.W/2
	} else {
		t.H, n.H = t.H/2, t.H-t.H/2
	}
	f.Panes = append(f.Panes, n)
	return n
}

// newTab adds a tab labelled label in workspace ws ("": w1) and returns its root pane, in cwd.
func (f *Fake) newTab(ws, label, cwd string) *Pane {
	f.made++
	if ws == "" {
		ws = "w1"
	}
	n := &Pane{Wire: herdr.Wire{ID: fmt.Sprintf("%s:n%d", ws, f.made), Tab: fmt.Sprintf("%s:t%d", ws, f.made),
		Workspace: ws, Cwd: cwd}, W: 200, H: 50}
	if label != "" {
		if f.TabLabels == nil {
			f.TabLabels = map[string]string{}
		}
		f.TabLabels[n.Tab] = label
	}
	if f.NewPaneBusy > 0 {
		n.Ready = time.Now().Add(f.NewPaneBusy)
	}
	f.Panes = append(f.Panes, n)
	return n
}

// newWorkspace adds a workspace labelled label (wn1, wn2, …) with one tab whose root pane is a shell in cwd, and
// returns that pane.
func (f *Fake) newWorkspace(label, cwd string) *Pane {
	f.made++
	ws := fmt.Sprintf("wn%d", f.made)
	f.Workspaces = append(f.Workspaces, herdr.Workspace{ID: ws, Label: label})
	n := &Pane{Wire: herdr.Wire{ID: ws + ":p1", Tab: ws + ":t1", Workspace: ws, Cwd: cwd}, W: 200, H: 50}
	f.Panes = append(f.Panes, n)
	return n
}

// tabList is herdr's tab.list: each tab of the panes, in their order, its label ("" when none), number and pane
// count. obor no longer asks for it (T-776: the monitor's "system" tab is gone), but the upgrade check drives older
// obor binaries through this fake and they do.
func (f *Fake) tabList() []any {
	var tabs []any
	seen := map[string]bool{}
	count := map[string]int{}
	for _, p := range f.Panes {
		count[p.Tab]++
	}
	for _, p := range f.Panes {
		if p.Tab == "" || seen[p.Tab] {
			continue
		}
		seen[p.Tab] = true
		label := f.TabLabels[p.Tab]
		tabs = append(tabs, map[string]any{"tab_id": p.Tab, "workspace_id": p.Workspace, "number": len(tabs) + 1,
			"label": label, "focused": len(tabs) == 0, "pane_count": count[p.Tab], "agent_status": "unknown"})
	}
	return tabs
}

func (f *Fake) pluginList() []any {
	plugins := []any{}
	for _, pl := range f.Plugins {
		plugins = append(plugins, map[string]any{"plugin_id": pl.ID, "plugin_root": pl.Root, "enabled": true})
	}
	return plugins
}

// unlink drops plugin id (no such plugin: nothing changes).
func (f *Fake) unlink(id string) {
	var kept []Plugin
	for _, pl := range f.Plugins {
		if pl.ID != id {
			kept = append(kept, pl)
		}
	}
	f.Plugins = kept
}
