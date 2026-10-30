package setup

// `obor setup rename --name N --prefix P`: rename a project and its agents (T-587). Steps 0, 1, 3, 4, 5 in order, each one
// `OK <n> <name>: …` (`(already)` when there was nothing to do) or the one `FAIL <n> <name>: …` that stops the run; a
// rerun after the fix is the repair path. The project's history is never rewritten.
//
//	0 stop     no serve holds .obor/state/serve.flock (a flock held, or a pid in the file that still lives) and no
//	           herdr agent runs in the root or the lanes dir (panes may stay)
//	1 config   .obor/config.json and .obor/local.json: name, prefix, and every agent name that is <old prefix>-… or the
//	           old lead's (roles.*.agent, lanes.*.agent, guards.allow_push) → the new default; names that follow
//	           neither are kept
//	           (no store step: serve rebuilds the lane rows from the log, whose old agent names serve ignores once they
//	           are no name of the lane under the config: internal/app rowAgent)
//	3 files    ~/.obor/agents/<old>.{sid,inbox} copied to <new>.… (no overwrite)
//	4 herdr    the workspace labelled with the old name is renamed (`herdr workspace rename`); pane labels are set by
//	           the next start
//	5 finish   the old → new agent table, the lanes dir moves when its path has the old name, what to do next

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"obor/internal/config"
	"obor/internal/herdr"
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
	r := &renamer{Setup: s, name: name, prefix: prefix, oldName: s.cfg.Data.Name, oldPrefix: s.cfg.Data.Prefix}
	r.oldLead = s.cfg.AgentName("lead", "")
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"0 stop", r.stop}, {"1 config", r.config}, {"3 files", r.files},
		{"4 herdr", r.workspace}, {"5 finish", r.finish},
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
	running, err := r.H.Agents(ctx)
	var he *herdr.Error
	switch {
	case errors.As(err, &he) && he.Kind == herdr.Unavailable: // herdr is not running: no agent either
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

// renameDoc is a config file with the new name and prefix and the mapped agent names; the other members keep their
// values. forceNames: set name and prefix even when the file has none (the main config; local.json only changes the
// ones it overrides). A changed file is written indented (EditJSON leaves what it edits compact).
func (r *renamer) renameDoc(doc []byte, forceNames bool) ([]byte, error) {
	out := doc
	set := func(path []string, v any) error {
		var err error
		out, err = config.EditJSON(out, path, v)
		return err
	}
	for _, kv := range [][2]string{{"name", r.name}, {"prefix", r.prefix}} {
		cur, ok := stringAt(out, kv[0])
		if (ok || forceNames) && cur != kv[1] {
			if err := set([]string{kv[0]}, kv[1]); err != nil {
				return nil, err
			}
		}
	}
	for _, role := range roles.All() {
		p := []string{"roles", role.Name, "agent"}
		if cur, ok := stringAt(out, p...); ok && r.mapAgent(cur) != cur {
			if err := set(p, r.mapAgent(cur)); err != nil {
				return nil, err
			}
		}
	}
	var lanes map[string]json.RawMessage
	if raw, _ := config.MemberAt(out, []string{"lanes"}); raw != nil {
		_ = json.Unmarshal(raw, &lanes)
	}
	names := make([]string, 0, len(lanes))
	for l := range lanes {
		names = append(names, l)
	}
	sort.Strings(names)
	for _, l := range names {
		p := []string{"lanes", l, "agent"}
		if cur, ok := stringAt(out, p...); ok && r.mapAgent(cur) != cur {
			if err := set(p, r.mapAgent(cur)); err != nil {
				return nil, err
			}
		}
	}
	if raw, _ := config.MemberAt(out, []string{"guards", "allow_push"}); raw != nil {
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			mapped := make([]string, len(list))
			changed := false
			for i, a := range list {
				mapped[i] = r.mapAgent(a)
				changed = changed || mapped[i] != a
			}
			if changed {
				if err := set([]string{"guards", "allow_push"}, mapped); err != nil {
					return nil, err
				}
			}
		}
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

// stringAt is the string member of doc at path; ok false when there is none or it is no string.
func stringAt(doc []byte, path ...string) (string, bool) {
	raw, err := config.MemberAt(doc, path)
	if err != nil || raw == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
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
		old := r.cfg.LaneAgentAliases(l.Name, rows[l.Name].Agent)[0]
		next := r.mapAgent(old)
		if next != old {
			next = r.prefix + "-" + strings.ToLower(l.Name)
			if conf := r.cfg.Data.Lanes.M[l.Name].Agent; conf != "" {
				next = r.mapAgent(conf)
			}
		}
		out = append(out, agentPair{old, next})
	}
	return out
}

// 3 ---------------------------------------------------------------------------------------------------------------

func (r *renamer) files(context.Context) error {
	var did []string
	for _, p := range r.pairs {
		if p.old == p.new {
			continue
		}
		for _, f := range []func(string, project.Env) string{project.SidFile, project.InboxFile} {
			src, dst := f(p.old, r.Env), f(p.new, r.Env)
			b, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			if _, err := os.Lstat(dst); err == nil {
				continue // never overwritten
			}
			info, err := os.Stat(src)
			if err != nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return project.Failf("%v", err)
			}
			if err := os.WriteFile(dst, b, info.Mode().Perm()); err != nil {
				return project.Failf("%v", err)
			}
			did = append(did, filepath.Base(src)+" → "+filepath.Base(dst))
		}
	}
	if len(did) == 0 {
		r.ok("3 files", true, "no agent file left to copy")
	} else {
		r.ok("3 files", false, "copied %s (the old ones stay)", strings.Join(did, ", "))
	}
	return nil
}

// 4 ---------------------------------------------------------------------------------------------------------------

func (r *renamer) workspace(ctx context.Context) error {
	if r.oldName == r.name {
		r.ok("4 herdr", true, "no workspace labelled with another name")
		return nil
	}
	herdrBin, err := lookPath(r.Env.Get("PATH"), "herdr")
	if err != nil {
		r.say("WARN 4 herdr: herdr is not on PATH: rename the workspace %s to %s by hand", r.oldName, r.name)
		return nil
	}
	call := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, herdrBin, args...)
		cmd.Env = append(os.Environ(), "PATH="+r.Env.Get("PATH"))
		return cmd.Output()
	}
	out, err := call("workspace", "list")
	if err != nil { // herdr is not running: no workspace either
		r.ok("4 herdr", true, "herdr lists no workspaces")
		return nil
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
		return project.Failf("herdr workspace list: unreadable answer: %v", err)
	}
	var renamed []string
	for _, w := range list.Result.Workspaces {
		if w.Label != r.oldName {
			continue
		}
		if _, err := call("workspace", "rename", w.ID, r.name); err != nil {
			return project.Failf("herdr workspace rename %s %s failed: %v", w.ID, r.name, err)
		}
		renamed = append(renamed, w.ID)
	}
	if len(renamed) == 0 {
		r.ok("4 herdr", true, "no workspace is labelled %s", r.oldName)
	} else {
		r.ok("4 herdr", false, "workspace %s: %s → %s", strings.Join(renamed, ", "), r.oldName, r.name)
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
	if lanes := config.AbsUnder(r.root, r.cfg.Data.LanesDir); r.oldName != r.name && r.oldName != "" &&
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
