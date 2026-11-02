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

// Pane is a scripted pane. Kind != "" means an agent runs in it (Name may still be "": herdr lost the name).
type Pane struct {
	ID, Label, Cwd     string
	Tab                string // pane list's tab_id; pane layout lists the panes of a tab
	Terminal           string // pane list's terminal_id ("": none), kept by a pane move
	W, H               int    // the size pane.layout gives; a split halves it
	Shell              int    // the pane's shell pid (pane.process_info; 0: 1)
	Kind, Name, Status string // the agent: kind (claude, codex…), herdr name, status
	Session            string
	Workspace          string // the workspace id (session.snapshot's workspace_id), "" for none
	Transcript         string // agent_session as a path (kind path) instead of the Session id
	Seq                int64
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

// Agent adds a pane with an agent and returns it.
func (f *Fake) Agent(pane, name, status, cwd string) *Pane {
	p := &Pane{ID: pane, Kind: "claude", Name: name, Status: status, Cwd: cwd, Seq: 1}
	f.mu.Lock()
	f.Panes = append(f.Panes, p)
	f.mu.Unlock()
	return p
}

// Shell adds a pane with no agent and returns it.
func (f *Fake) Shell(pane, label, cwd string) *Pane {
	p := &Pane{ID: pane, Label: label, Cwd: cwd}
	f.mu.Lock()
	f.Panes = append(f.Panes, p)
	f.mu.Unlock()
	return p
}

// Tab adds shell panes ids to tab, each w×h.
func (f *Fake) Tab(tab string, w, h int, ids ...string) {
	f.mu.Lock()
	for _, id := range ids {
		f.Panes = append(f.Panes, &Pane{ID: id, Tab: tab, W: w, H: h})
	}
	f.mu.Unlock()
}

// Pane is the pane id; nil when none.
func (f *Fake) Pane(id string) *Pane {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pane(id)
}

// orNull is s, nil for "" (herdr's null for a cleared name, label or session).
func orNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (p *Pane) agentJSON() map[string]any {
	m := map[string]any{"name": orNull(p.Name), "pane_id": p.ID, "agent": p.Kind, "agent_status": p.Status,
		"cwd": p.Cwd, "state_change_seq": p.Seq}
	p.common(m)
	return m
}

func (p *Pane) paneJSON() map[string]any {
	m := map[string]any{"pane_id": p.ID, "label": orNull(p.Label), "cwd": p.Cwd}
	if p.Tab != "" {
		m["tab_id"] = p.Tab
	}
	if p.Terminal != "" {
		m["terminal_id"] = p.Terminal
	}
	if p.Kind != "" {
		m["agent"] = p.Kind
	}
	if p.Kind != "" || p.Status != "" { // a status without an agent: herdr's view of a pane it lost the agent of
		m["agent_status"] = p.Status
	}
	p.common(m)
	return m
}

// common adds what agent and pane JSON share: agent_session (an id, or the transcript path) and workspace_id.
func (p *Pane) common(m map[string]any) {
	switch {
	case p.Transcript != "":
		m["agent_session"] = map[string]any{"agent": p.Kind, "kind": "path", "value": p.Transcript}
	case p.Session != "":
		m["agent_session"] = map[string]any{"agent": p.Kind, "kind": "id", "value": p.Session}
	}
	if p.Workspace != "" {
		m["workspace_id"] = p.Workspace
	}
}

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
	n := &Pane{ID: fmt.Sprintf("%s:n%d", ws, f.made), Tab: t.Tab, Workspace: t.Workspace, Cwd: cwd, W: t.W, H: t.H}
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
	n := &Pane{ID: fmt.Sprintf("%s:n%d", ws, f.made), Tab: fmt.Sprintf("%s:t%d", ws, f.made), Workspace: ws, Cwd: cwd,
		W: 200, H: 50}
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
	n := &Pane{ID: ws + ":p1", Tab: ws + ":t1", Workspace: ws, Cwd: cwd, W: 200, H: 50}
	f.Panes = append(f.Panes, n)
	return n
}

// tabList is herdr's tab.list: each tab of the panes, in their order, its label ("" when none), number and pane
// count.
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

// link links the plugin in dir, as obor.events.
func (f *Fake) link(dir string) { f.Plugins = append(f.Plugins, Plugin{ID: "obor.events", Root: dir}) }

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
