package herdr

// setup.go: port.TerminalSetup over herdr. What obor setup and obor init ask of the terminal in neutral terms
// (a pane for a role near something, herdr's own setting for a restart, a project rename) is laid out here in
// herdr's: tabs, workspaces, splits, the workspace label, and its config.toml.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"obor/internal/app/port"
	"obor/internal/project"
)

// Setup is port.TerminalSetup over the herdr socket: one obor setup or obor init run (it remembers the workspace the
// run made, and the panes its last Panes answer listed).
type Setup struct {
	*Socket
	// Env is the run's environment: PATH finds the herdr CLI (a project rename), HERDR_CONFIG_PATH and HOME herdr's
	// config (the resume hint).
	Env project.Env

	made string // the workspace this run created for the project, "" when none
	last []Pane // the panes Panes listed last, herdr's fields kept: Place lays out against them
}

var _ port.TerminalSetup = (*Setup)(nil)

// Panes is pane.list; the answer, with each pane's tab and workspace, is what the next Place lays out against (setup
// reads the panes once per start and places against that moment).
func (h *Setup) Panes(ctx context.Context) ([]port.Pane, error) {
	ps, err := h.panesAt(ctx)
	if err != nil {
		return nil, err
	}
	h.last = ps
	return corePanes(ps), nil
}

// --- placement -----------------------------------------------------------------------------

// workersTab is the tab the lanes' workers share (obor init's first worker opens it, L-02 and the integrator go beside).
const workersTab = "workers"

// Place makes a new pane for p (see port.Placement): Group or Own → a new tab in the project's workspace; In → a split
// of that tab; Beside → a split of that pane's tab when it is a project tab, else a new workers tab; none → a split of
// the caller's tab when it is a project tab in the project's workspace, else of that workspace's busiest project tab,
// else a new tab of the role's own. A new tab goes to the project's workspace (the one whose panes work in the project),
// a new one named after the project when none has a pane of it; never the caller's or herdr's active workspace.
func (h *Setup) Place(ctx context.Context, p port.Placement) (string, error) {
	panes := h.last
	if panes == nil {
		var err error
		if panes, err = h.panesAt(ctx); err != nil {
			return "", project.Failf("herdr pane list failed")
		}
	}
	switch {
	case p.Group != "":
		return h.newTab(ctx, panes, p, p.Group)
	case p.Own:
		return h.newTab(ctx, panes, p, ownTab(p))
	case p.In != "":
		return h.split(ctx, panes, p, p.In)
	case p.Beside != "":
		if tab := projectTabOf(panes, p.Project.Has, p.Beside); tab != "" {
			return h.split(ctx, panes, p, tab)
		}
		return h.newTab(ctx, panes, p, workersTab)
	}
	if tab := defaultTab(panes, p.Project); tab != "" {
		return h.split(ctx, panes, p, tab)
	}
	return h.newTab(ctx, panes, p, ownTab(p)) // no tab of the project to split: a new one in its workspace
}

// ownTab is the label of a tab of p's role's own: the lanes' workers share one, obor init's reviewer has "review",
// any other role its name.
func ownTab(p port.Placement) string {
	switch {
	case p.Lane != "":
		return workersTab
	case p.Own && p.Role == "reviewer":
		return "review"
	}
	return p.Role
}

// say is one progress line of p.
func say(p port.Placement, format string, a ...any) {
	if p.Say != nil {
		p.Say(fmt.Sprintf(format, a...))
	}
}

// projectWorkspace is the herdr workspace of this project ("" when none): one with a pane whose cwd is the project
// root or under its lanes dir and no pane with a known cwd elsewhere (a shell of another project's workspace
// that cd's into this one does not make that workspace the project's); an unknown cwd counts neither way. Several:
// the one with most project panes, the first of equals in herdr's order. Placement follows it, never the caller's
// workspace when that is another project's (live: obor init from obor-kit's lead pane put axis-maps' panes in
// obor-kit's w5).
func projectWorkspace(panes []Pane, has func(string) bool) string {
	count, foreign := map[string]int{}, map[string]bool{}
	for _, p := range panes {
		switch {
		case p.Workspace == "" || p.Cwd == "":
		case has(p.Cwd):
			count[p.Workspace]++
		default:
			foreign[p.Workspace] = true
		}
	}
	ws, most := "", 0
	for _, p := range panes { // in herdr's order: the first of equals
		if c := count[p.Workspace]; c > most && !foreign[p.Workspace] {
			ws, most = p.Workspace, c
		}
	}
	return ws
}

// projectTab: every pane of tab has a known cwd in this project, so placement may pick it on its own. A pane
// with an unknown cwd may be another project's.
func projectTab(panes []Pane, has func(string) bool, tab string) bool {
	n := 0
	for _, p := range panes {
		if p.Tab != tab {
			continue
		}
		if p.Cwd == "" || !has(p.Cwd) {
			return false
		}
		n++
	}
	return n > 0
}

// projectTabOf is the tab of pane when it is the project's (projectTab), "" otherwise or when unknown: obor init never
// puts L-02 and the integrator beside a pane in a foreign or unknown-cwd tab, a new tab in the project's workspace then
func projectTabOf(panes []Pane, has func(string) bool, pane string) string {
	for _, p := range panes {
		if p.ID == pane && projectTab(panes, has, p.Tab) {
			return p.Tab
		}
	}
	return ""
}

// defaultTab is the tab a placement with no place named goes to: the caller's when it is a project tab in the
// project's workspace, else that workspace's project tab with most panes; "" when none (a new tab then).
func defaultTab(panes []Pane, pr port.PlaceProject) string {
	proj := projectWorkspace(panes, pr.Has)
	if proj == "" {
		return ""
	}
	if pr.Self != "" {
		for _, p := range panes {
			if p.ID == pr.Self && p.Workspace == proj && projectTab(panes, pr.Has, p.Tab) {
				return p.Tab
			}
		}
	}
	return busiestTab(panes, pr.Has, proj)
}

// busiestTab is the project tab of ws with the most panes ("" when none).
func busiestTab(panes []Pane, has func(string) bool, ws string) string {
	count := map[string]int{}
	for _, p := range panes {
		if p.Workspace == ws {
			count[p.Tab]++
		}
	}
	tab, most := "", 0
	for _, p := range panes { // in herdr's order: the first of equals
		if c := count[p.Tab]; p.Workspace == ws && c > most && projectTab(panes, has, p.Tab) {
			tab, most = p.Tab, c
		}
	}
	return tab
}

// workspace is where this run's new tabs go: the project's workspace; none (no herdr workspace holds a pane of the
// project) → a new workspace named after the project, its first pane a shell in the root, made once a run.
// The caller's or herdr's active workspace is never a fallback: it may be another project's.
func (h *Setup) workspace(ctx context.Context, panes []Pane, p port.Placement) (string, error) {
	if ws := projectWorkspace(panes, p.Project.Has); ws != "" {
		return ws, nil
	}
	if h.made != "" {
		return h.made, nil
	}
	ws, err := h.NewWorkspace(ctx, p.Project.Name, p.Project.Root)
	if err != nil {
		return "", project.Failf("herdr workspace create failed: %v", err)
	}
	h.made = ws
	say(p, "workspace %s created for %s", ws, p.Project.Name)
	return ws, nil
}

// newTab makes a tab labelled label in the project's workspace (made first when there is none), its pane in p's cwd
// with p's env; the pane's id.
func (h *Setup) newTab(ctx context.Context, panes []Pane, p port.Placement, label string) (string, error) {
	ws, err := h.workspace(ctx, panes, p)
	if err != nil {
		return "", err
	}
	pane, err := h.NewTab(ctx, ws, label, p.Cwd, p.Env...)
	if err != nil {
		return "", project.Failf("herdr tab create failed: %v", err)
	}
	say(p, "tab %s: pane %s", label, pane)
	return pane, nil
}

// split splits the largest pane of tab a split may use for p's new pane: to the right when it is wider than tall on
// screen (a cell is about twice as tall as wide), else down. Another project's pane is never split: a pane
// may be split when its cwd is unknown or in the project. A tab holding MaxPanes panes is full.
func (h *Setup) split(ctx context.Context, panes []Pane, p port.Placement, tab string) (string, error) {
	var in []string
	n := 0
	for _, q := range panes {
		if q.Tab != tab {
			continue
		}
		n++
		if q.Cwd == "" || p.Project.Has(q.Cwd) {
			in = append(in, q.ID)
		}
	}
	if n == 0 {
		return "", project.Failf("no panes in tab %s", tab)
	}
	if len(in) == 0 {
		return "", project.Failf("tab %s holds no pane of this project: pass --new-tab <label> (or --pane <id>)", tab)
	}
	if limit := p.Project.MaxPanes; n >= limit {
		return "", project.Failf("tab %s is full (%d of %d panes; --new-tab <label>)", tab, n, limit)
	}
	layout, err := h.Layout(ctx, in[0])
	if err != nil {
		return "", project.Failf("herdr pane layout failed: %v", err)
	}
	may := map[string]bool{}
	for _, id := range in {
		may[id] = true
	}
	target, w, ht := "", 0, 0
	for _, r := range layout {
		if !may[r.Pane] {
			continue
		}
		if r.W*r.H >= w*ht { // the last of equals: the newest
			target, w, ht = r.Pane, r.W, r.H
		}
	}
	if target == "" {
		return "", project.Failf("herdr pane layout of tab %s lists no panes", tab)
	}
	dir := "down"
	if w > 2*ht {
		dir = "right"
	}
	pane, err := h.Split(ctx, target, dir, p.Cwd, p.Env...)
	if err != nil {
		return "", project.Failf("herdr pane split %s failed: %v", target, err)
	}
	say(p, "split %s in tab %s: pane %s", target, tab, pane)
	return pane, nil
}

// --- herdr's setting for a restart ------------------------------------------------------------------------------

var resumeRE = regexp.MustCompile(`^\s*resume_agents_on_restore\s*=\s*([^#\s]*)`)

// ResumeHint: obor serve restarts obor's panes itself, so herdr must not resume agents on a restore.
// Unless herdr's config.toml ($HERDR_CONFIG_PATH, else ~/.config/herdr/config.toml) sets `[session]
// resume_agents_on_restore = false`: the one line to add, saying it is user-global; "" when it is set.
// Never edited.
func (h *Setup) ResumeHint() string {
	path := h.Env.Get("HERDR_CONFIG_PATH")
	if path == "" {
		path = filepath.Join(h.Env.Get("HOME"), ".config", "herdr", "config.toml")
	}
	value := ""
	if f, err := os.Open(path); err == nil {
		section := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") {
				head, _, _ := strings.Cut(line, "]")
				section = strings.TrimSpace(strings.TrimPrefix(head, "["))
			} else if m := resumeRE.FindStringSubmatch(line); m != nil && section == "session" {
				value = m[1]
			}
		}
		f.Close()
	}
	if value == "false" {
		return ""
	}
	return "hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false under [session] in " +
		path + " (user-global: affects every herdr pane, not only obor's; those come back as shells)"
}

// --- project rename ----------------------------------------------------------------------------------------------

// RenameProject renames the workspace labelled old to name (`herdr workspace rename`, the herdr CLI on Env's PATH) and
// relabels every pane of the project's workspaces (labelled old, or name: a rerun, or only the prefix changes) whose
// label relabel maps to another (`herdr pane rename`), so a later start in it is not refused for "labelled pj-o-01, not
// ob-o-01"; the other labels stay. No herdr on PATH: Warn, done by hand; herdr not running: nothing to do.
func (h *Setup) RenameProject(ctx context.Context, old, name string, relabel func(string) string) (port.Renamed, error) {
	bin, err := project.LookPath(h.Env.Get("PATH"), "herdr")
	if err != nil {
		return port.Renamed{Warn: true, Line: fmt.Sprintf("herdr is not on PATH: rename the workspace %s to %s and relabel "+
			"its panes by hand (herdr pane rename)", old, name)}, nil
	}
	call := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "PATH="+h.Env.Get("PATH"))
		return cmd.Output()
	}
	out, err := call("workspace", "list")
	if err != nil { // herdr is not running: no workspace either
		return port.Renamed{Already: true, Line: "herdr lists no workspaces"}, nil
	}
	var list struct {
		Result struct {
			Workspaces []struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return port.Renamed{}, project.Failf("herdr workspace list: unreadable answer: %v", err)
	}
	var renamed []string
	spaces := map[string]bool{}
	for _, w := range list.Result.Workspaces {
		switch w.Label {
		case name:
			spaces[w.ID] = true
		case old:
			spaces[w.ID] = true
			if _, err := call("workspace", "rename", w.ID, name); err != nil {
				return port.Renamed{}, project.Failf("herdr workspace rename %s %s failed: %v", w.ID, name, err)
			}
			renamed = append(renamed, w.ID)
		}
	}
	relabelled, err := relabelPanes(call, spaces, relabel)
	if err != nil {
		return port.Renamed{}, err
	}
	var did []string
	if len(renamed) > 0 {
		did = append(did, fmt.Sprintf("workspace %s: %s → %s", strings.Join(renamed, ", "), old, name))
	}
	if len(relabelled) > 0 {
		did = append(did, "pane labels "+strings.Join(relabelled, ", "))
	}
	if len(did) == 0 {
		return port.Renamed{Already: true,
			Line: fmt.Sprintf("no workspace is labelled %s, no pane label follows the old prefix", old)}, nil
	}
	return port.Renamed{Line: strings.Join(did, "; ")}, nil
}

// relabelPanes renames each pane of the workspaces in spaces whose label relabel maps to a new one; "<pane> <old> →
// <new>" for each.
func relabelPanes(call func(...string) ([]byte, error), spaces map[string]bool, relabel func(string) string) ([]string, error) {
	out, err := call("pane", "list")
	if err != nil {
		return nil, project.Failf("herdr pane list failed: %v", err)
	}
	var list struct {
		Result struct {
			Panes []struct {
				ID    string `json:"pane_id"`
				Space string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, project.Failf("herdr pane list: unreadable answer: %v", err)
	}
	var done []string
	for _, p := range list.Result.Panes {
		next := relabel(p.Label)
		if !spaces[p.Space] || next == p.Label {
			continue
		}
		if _, err := call("pane", "rename", p.ID, next); err != nil {
			return nil, project.Failf("herdr pane rename %s %s failed: %v", p.ID, next, err)
		}
		done = append(done, fmt.Sprintf("%s %s → %s", p.ID, p.Label, next))
	}
	return done, nil
}
