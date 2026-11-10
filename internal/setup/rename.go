package setup

// `obor setup rename --name N --prefix P`: rename a project and its agents. Refused before any step while the
// project's serve LaunchAgent is installed under the old name (its label would outlive the
// rename). Steps 0, 1, 3, 4, 5 in order, each one `OK <n> <name>: …` (`(already)` when there was nothing to do) or the
// one `FAIL <n> <name>: …` that stops the run; a rerun after the fix is the repair path. The project's history is
// never rewritten. The inbox records are keyed by slot in the project: nothing to move.
//
//	0 stop     no serve holds .obor/state/serve.flock (a flock held, or a pid in the file that still lives) and no
//	           herdr agent runs in the root or the lanes dir (panes may stay)
//	1 config, .obor/config.json and .obor/local.json: name and prefix (agent names are derived from them;
//	           a derived name in guards.allow_push becomes its role)
//	           (no store step: a lane's agent is derived from the config, no lane row stores one)
//	3 files    each lane worktree's opencode.json with a path to another kit's bin/obor (an MCP server) pointing at this kit's bin/obor
//	4 herdr    the workspace labelled with the old name is renamed (`herdr workspace rename`), and every pane of it
//	           whose label follows the old prefix (mapAgent) is relabelled (`herdr pane rename`)
//	5 finish   the old → new agent table, the lanes dir moves when its path has the old name, what to do next

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/store"
)

var renameWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// renamer is one rename run: the old names (from the config as it was) and the new.
type renamer struct {
	*Setup
	name, prefix       string // new
	oldName, oldPrefix string
	oldLead            string // the lead's agent name before
	pairs              []agentPair
}

// agentPair is an agent's name before and after; old == new: it keeps its name.
type agentPair struct{ old, new string }

func (s *Setup) renameCmd(ctx context.Context, args []string) error {
	var name, prefix string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--name" && i+1 < len(args):
			i++
			name = args[i]
		case args[i] == "--prefix" && i+1 < len(args):
			i++
			prefix = args[i]
		default:
			return errUsage
		}
	}
	if name == "" || prefix == "" {
		return errUsage
	}
	if !renameWord.MatchString(name) || !renameWord.MatchString(prefix) {
		return project.Failf("--name and --prefix are words of letters, digits, . _ - (got %q, %q)", name, prefix)
	}
	if err := s.load(); err != nil {
		return err
	}
	if s.Installed != nil {
		if plist, ok := s.Installed(s.cfg); ok {
			return project.Failf("the serve LaunchAgent %s is installed under the old name: run `obor serve uninstall` first, then obor setup rename again", plist)
		}
	}
	r := &renamer{Setup: s, name: name, prefix: prefix, oldName: s.cfg.Name(), oldPrefix: s.cfg.Prefix()}
	r.oldLead = s.cfg.AgentName("lead", "")
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"0 stop", r.stop}, {"1 config", r.config}, {"3 files", r.files},
		{"4 herdr", r.terminal}, {"5 finish", r.finish},
	}
	for _, st := range steps {
		if err := st.run(ctx); err != nil {
			return project.Failf("%s: %v", st.name, err)
		}
	}
	return nil
}

// mapAgent is the name a keeps or gets: the old lead's and every <old prefix>-… name follow the new prefix.
func (r *renamer) mapAgent(a string) string {
	switch {
	case a == "":
		return a
	case a == r.oldLead:
		return r.prefix + "-lead"
	case r.oldPrefix != "" && strings.HasPrefix(a, r.oldPrefix+"-"):
		return r.prefix + a[len(r.oldPrefix):]
	}
	return a
}

// 0 ---------------------------------------------------------------------------------------------------------------

func (r *renamer) stop(ctx context.Context) error {
	path := filepath.Join(r.root, ".obor", "state", "serve.flock")
	l := project.NewLock(path)
	if l.Held() {
		return project.Failf("a serve holds %s (pid %s): stop it, then run obor setup rename again", path, l.Holder())
	}
	if pid, _ := strconv.Atoi(l.Holder()); project.PIDAlive(pid) {
		return project.Failf("%s names pid %d, which still runs: stop that serve, then run obor setup rename again", path, pid)
	}
	running, err := r.T.Agents(ctx)
	switch {
	case port.NotRunning(err): // herdr is not running: no agent either
	case err != nil:
		return project.Failf("cannot ask herdr for the running agents: %v", err)
	}
	for _, a := range running {
		if r.inProject(a.Cwd) {
			return project.Failf("agent %s runs in %s: quit it (its pane may stay), then run obor setup rename again", a.Name, a.Cwd)
		}
	}
	r.ok("0 stop", true, "no serve holds the flock, no agent of the project runs")
	return nil
}

// 1 ---------------------------------------------------------------------------------------------------------------

func (r *renamer) config(context.Context) error {
	rows, _ := store.ReadLanesAt(r.root)
	r.pairs = r.agentPairs(rows)
	var did []string
	for _, f := range []string{"config.json", "local.json"} {
		path := filepath.Join(r.root, ".obor", f)
		doc, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return project.Failf("%v", err)
		}
		out, err := r.renameDoc(doc, f == "config.json")
		if err != nil {
			return project.Failf("%s: %v", r.rel(path), err)
		}
		if bytes.Equal(out, doc) {
			continue
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return project.Failf("%v", err)
		}
		did = append(did, f)
	}
	if len(did) == 0 {
		r.ok("1 config", true, "name %s, prefix %s, agent names follow it", r.name, r.prefix)
	} else {
		r.ok("1 config", false, "%s: name %s, prefix %s, agent names follow it", strings.Join(did, ", "), r.name, r.prefix)
	}
	return nil
}

// renameDoc is a config file with the new name and prefix; the other members keep their values. forceNames: set name and prefix even when the file has none (the main config; local.json only changes the
// ones it overrides). A changed file is written indented (EditJSON leaves what it edits compact).
func (r *renamer) renameDoc(doc []byte, forceNames bool) ([]byte, error) {
	out := doc
	if err := r.pushRoles(&out); err != nil {
		return nil, err
	}
	out, err := config.RenameProject(out, r.name, r.prefix, forceNames)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(out, doc) {
		return doc, nil
	}
	var ind bytes.Buffer
	if err := json.Indent(&ind, out, "", "  "); err != nil {
		return nil, err
	}
	return append(ind.Bytes(), '\n'), nil
}

// pushRoles rewrites each guards.allow_push entry that is a role's derived name under the old prefix ("ob-lead") to the
// role ("lead"): a derived name would resolve against the new prefix and name no role. Role entries and unrelated
// entries stay. It runs before the prefix changes.
func (r *renamer) pushRoles(out *[]byte) error {
	entries := config.PushEntries(*out)
	if entries == nil {
		return nil
	}
	changed := false
	for i, e := range entries {
		if _, isRole := r.cfg.Role(e); isRole {
			continue
		}
		for _, rc := range r.cfg.Roles() {
			if !roles.LaneBound(rc.Role.Name) && rc.Agent == e {
				entries[i], changed = rc.Role.Name, true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	doc, err := config.SetPushEntries(*out, entries)
	*out = doc
	return err
}

// roleAgents is the agent name of every role that counts (config.RoleAgents).
func roleAgents(cfg *config.Config) []string {
	var out []string
	for _, rc := range cfg.RoleAgents() {
		out = append(out, rc.Agent)
	}
	return out
}

// agentPairs are the project's agents with the name each had and gets: the roles' agents and every lane's (its row's
// agent, else its configured or default one). A lane whose agent follows the old prefix gets the new default
// <prefix>-<lane lower> (hk-o1 → ob-o-01); an explicit name that follows neither keeps its own.
func (r *renamer) agentPairs(rows map[string]store.LaneRow) []agentPair {
	var out []agentPair
	for _, a := range roleAgents(r.cfg) {
		out = append(out, agentPair{a, r.mapAgent(a)})
	}
	for _, l := range r.cfg.Lanes() {
		old := r.cfg.LaneAgentAliases(l.Name)[0]
		next := r.mapAgent(old)
		if next != old {
			next = r.prefix + "-" + strings.ToLower(l.Name)
		}
		out = append(out, agentPair{old, next})
	}
	return out
}

// 3 ---------------------------------------------------------------------------------------------------------------

func (r *renamer) files(context.Context) error {
	moved, err := r.kitPaths()
	if err != nil {
		return err
	}
	if len(moved) == 0 {
		r.ok("3 files", true, "no kit path to correct")
	} else {
		r.ok("3 files", false, "kit path in %s", strings.Join(moved, ", "))
	}
	return nil
}

// kitPath matches a quoted path to a kit's shim (bin/obor): where an OpenCode lane's MCP server command points.
var kitPath = regexp.MustCompile(`"((?:[^"\\]|\\.)*/bin/obor)"`)

// kitPaths points each lane worktree's opencode.json (per-worktree config, git-excluded: setup does not write it, so
// it keeps the kit it was written for, which may be gone) at this kit's bin/obor. It returns the opencode.json it
// changed, as "<lane>/opencode.json"; a lane without one is skipped.
func (r *renamer) kitPaths() ([]string, error) {
	shim := filepath.Join(r.Kit, "bin", "obor")
	var moved []string
	for _, l := range r.cfg.Lanes() {
		path := filepath.Join(l.Dir, "opencode.json")
		doc, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out := kitPath.ReplaceAllFunc(doc, func(m []byte) []byte {
			var p string
			if json.Unmarshal(m, &p) != nil || p == shim {
				return m
			}
			b, _ := json.Marshal(shim)
			return b
		})
		if bytes.Equal(out, doc) {
			continue
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return nil, project.Failf("%v", err)
		}
		moved = append(moved, l.Name+"/opencode.json")
	}
	return moved, nil
}

// 4 ---------------------------------------------------------------------------------------------------------------

// terminal is step 4: the terminal's share of the rename (port.TerminalSetup.RenameProject; herdr: the workspace
// labelled with the old name, and its panes' labels that follow the old prefix).
func (r *renamer) terminal(ctx context.Context) error {
	done, err := r.T.RenameProject(ctx, r.oldName, r.name, r.mapAgent)
	switch {
	case err != nil:
		return err
	case done.Warn:
		r.say("WARN 4 herdr: %s", done.Line)
	default:
		r.ok("4 herdr", done.Already, "%s", done.Line)
	}
	return nil
}

// 5 ---------------------------------------------------------------------------------------------------------------

func (r *renamer) finish(context.Context) error {
	r.say("agents (old → new):")
	for _, p := range r.pairs {
		if p.old == p.new {
			r.say("  %s (kept)", p.old)
		} else {
			r.say("  %s → %s", p.old, p.new)
		}
	}
	if lanes := r.cfg.LanesDir(); r.oldName != r.name && r.oldName != "" &&
		strings.Contains(filepath.Base(lanes), r.oldName) {
		to := filepath.Join(filepath.Dir(lanes), strings.Replace(filepath.Base(lanes), r.oldName, r.name, 1))
		r.say("lanes_dir %s has the old name; by hand (the lanes' worktrees move with it):", lanes)
		r.say("  mv %s %s", lanes, to)
		r.say("  git -C %s worktree repair %s/*", r.root, to)
		r.say("  then set lanes_dir in .obor/config.json to the new path")
	}
	r.ok("5 finish", false, "start serve, then restart each agent fresh by its new name (obor restart <agent> --fresh)")
	return nil
}
