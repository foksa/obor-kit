package setup

// `obor migrate` (P7-0b; docs/plan/phase-7-advice.md §5; deleted in v0.17 with the herd fallbacks): move a herd
// project, and once the user's machine, to obor. Seven steps in order, each one `OK <n> <name>: …` (`(already)` when
// there was nothing to do) or the one `FAIL <n> <name>: …` that stops the run; a rerun after the fix is the repair
// path, so every step looks at what is already done. Nothing is ever deleted: files move (`git mv` for tracked ones),
// ~/.herd is copied.
//
//	1 stop      no serve holds .obor/state/serve.flock or .herd/state/serve.flock (a flock held, or a pid in the
//	            file that still lives) and no herdr agent runs in the root or the lanes dir (panes may stay)
//	2 files     .herd/{config.json,local.json,drafts,state,…} → .obor/, herd.db* → obor.db*, the .gitignore lines,
//	            the config's top-level "herd" key → "obor" and HERD_CHANGED_FILES → OBOR_CHANGED_FILES (name, prefix,
//	            lanes and agent names never change)
//	3 plugin    claude plugin uninstall herd@<marketplace> (names from .claude/settings.json), marketplace add <kit>,
//	            install obor@obor-kit, all --scope project; only exit codes are read
//	4 githooks  obor setup githooks (re-points a core.hooksPath that ends in a herd or obor kit's /githooks)
//	5 user      ~/.herd copied to ~/.obor (no overwrite, never moved: another project may still run herd), the
//	            obor.events herdr plugin linked (herd.events stays: v0.17 removes it),
//	            obor setup agents when an OpenCode entry exists (the herd entry stays)
//	6 prose     the project's own prose files that still say herd: listed, never edited
//	7 finish    what to do next: start serve, restart every labelled pane fresh, the lead at a handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"obor/internal/agents"
	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/project"
)

func (s *Setup) migrate(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return project.Fail("usage: obor migrate")
	}
	if err := s.findRoot(); err != nil {
		return err
	}
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"1 stop", s.migrateStop}, {"2 files", s.migrateFiles}, {"3 plugin", s.migratePlugin},
		{"4 githooks", s.migrateGithooks}, {"5 user", s.migrateUser}, {"6 prose", s.migrateProse},
		{"7 finish", s.migrateFinish},
	}
	for _, st := range steps {
		if err := st.run(ctx); err != nil {
			return project.Failf("%s: %v", st.name, err)
		}
	}
	return nil
}

// ok says one step's OK line; already (nothing was left to do) adds the suffix.
func (s *Setup) ok(step string, already bool, format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	if already {
		line += " (already)"
	} else {
		s.changed = true
	}
	s.say("OK %s: %s", step, line)
}

// 1 ---------------------------------------------------------------------------------------------------------------

func (s *Setup) migrateStop(ctx context.Context) error {
	for _, dir := range []string{".obor", ".herd"} {
		path := filepath.Join(s.root, dir, "state", "serve.flock")
		l := project.NewLock(path)
		if l.Held() {
			return project.Failf("a serve holds %s (pid %s): stop it, then run obor migrate again", path, l.Holder())
		}
		if pid, _ := strconv.Atoi(l.Holder()); project.PIDAlive(pid) {
			return project.Failf("%s names pid %d, which still runs: stop that serve, then run obor migrate again", path, pid)
		}
	}
	if !s.hasConfig(".obor") && !s.hasConfig(".herd") {
		return project.Failf("%s has neither .herd/config.json nor .obor/config.json: not a herd or obor project", s.root)
	}
	running, err := s.H.Agents(ctx)
	switch {
	case app.NotRunning(err): // herdr is not running: no agent either
	case err != nil:
		return project.Failf("cannot ask herdr for the running agents: %v", err)
	}
	for _, a := range running {
		if s.inProject(a.Cwd) {
			return project.Failf("agent %s runs in %s: quit it (its pane may stay), then run obor migrate again", a.Name, a.Cwd)
		}
	}
	s.ok("1 stop", true, "no serve holds the flock, no agent of the project runs")
	return nil
}

func (s *Setup) hasConfig(dir string) bool {
	_, err := os.Stat(filepath.Join(s.root, dir, "config.json"))
	return err == nil
}

// inProject: cwd is in the main checkout or under the project's lanes dir (the config's, read raw: a herd-era one does
// not load).
func (s *Setup) inProject(cwd string) bool {
	if cwd == "" {
		return false
	}
	c := project.Physical(cwd)
	in := func(d string) bool { d = project.Physical(d); return c == d || strings.HasPrefix(c, d+"/") }
	if in(s.root) {
		return true
	}
	var raw struct {
		Name     string `json:"name"`
		LanesDir string `json:"lanes_dir"`
	}
	for _, dir := range []string{".obor", ".herd"} {
		if b, err := os.ReadFile(filepath.Join(s.root, dir, "config.json")); err == nil && json.Unmarshal(b, &raw) == nil {
			break
		}
	}
	lanes := raw.LanesDir
	if lanes == "" {
		lanes = "../" + raw.Name + "-wt"
	}
	return raw.Name != "" && strings.HasPrefix(c, project.Physical(config.AbsUnder(s.root, lanes))+"/")
}

// 2 ---------------------------------------------------------------------------------------------------------------

func (s *Setup) migrateFiles(context.Context) error {
	var did []string
	herd, obor := filepath.Join(s.root, ".herd"), filepath.Join(s.root, ".obor")
	if _, err := os.Stat(herd); err == nil {
		if err := os.MkdirAll(obor, 0o755); err != nil {
			return project.Failf("%v", err)
		}
		names := []string{"config.json", "local.json", "drafts", "state"}
		if entries, err := os.ReadDir(herd); err == nil {
			for _, e := range entries {
				if n := e.Name(); !contains(names, n) {
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			moved, err := s.move(filepath.Join(herd, n), filepath.Join(obor, n))
			if err != nil {
				return err
			}
			if moved {
				did = append(did, n)
			}
		}
		if rest, _ := os.ReadDir(herd); len(rest) == 0 {
			os.Remove(herd) // empty: nothing is lost
		}
	}
	dbs, err := s.renameDB()
	if err != nil {
		return err
	}
	if len(dbs) > 0 {
		did = append(did, strings.Join(dbs, ", "))
	}
	if changed, err := s.gitignoreHerd(); err != nil {
		return err
	} else if changed {
		did = append(did, ".gitignore")
	}
	for _, name := range []string{"config.json", "local.json"} {
		changed, err := rewriteConfig(filepath.Join(obor, name))
		if err != nil {
			return err
		}
		if changed {
			did = append(did, name+" keys")
		}
	}
	if len(did) == 0 {
		s.ok("2 files", true, ".obor/ holds config, state and database; .gitignore and config keys are obor's")
	} else {
		s.ok("2 files", false, "%s", strings.Join(did, "; "))
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// move renames src to dst (git mv when git tracks src); false when src is not there (moved before). dst taken: an
// empty directory makes room, anything else is a conflict for the owner to resolve.
func (s *Setup) move(src, dst string) (bool, error) {
	if _, err := os.Lstat(src); err != nil {
		return false, nil
	}
	if fi, err := os.Lstat(dst); err == nil {
		if entries, rerr := os.ReadDir(dst); !fi.IsDir() || rerr != nil || len(entries) > 0 {
			return false, project.Failf("%s and %s both exist: merge them by hand (keep the right one at the second), then run obor migrate again",
				s.rel(src), s.rel(dst))
		}
		os.Remove(dst)
	}
	if tracked, _ := project.Git(s.root, "ls-files", "--", src); strings.TrimSpace(tracked) != "" {
		if _, err := project.Git(s.root, "mv", src, dst); err != nil {
			return false, project.Failf("git mv %s %s failed", s.rel(src), s.rel(dst))
		}
		return true, nil
	}
	if err := os.Rename(src, dst); err != nil {
		return false, project.Failf("%v", err)
	}
	return true, nil
}

func (s *Setup) rel(p string) string {
	if r, err := filepath.Rel(s.root, p); err == nil {
		return r
	}
	return p
}

// renameDB: .obor/state/herd.db and its -wal, -shm, -journal files become obor.db… (nothing holds the database: step 1).
func (s *Setup) renameDB() ([]string, error) {
	var did []string
	state := filepath.Join(s.root, ".obor", "state")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		from, to := filepath.Join(state, "herd.db"+suffix), filepath.Join(state, "obor.db"+suffix)
		if _, err := os.Lstat(from); err != nil {
			continue
		}
		if _, err := os.Lstat(to); err == nil {
			return did, project.Failf("%s and %s both exist: keep the right one at obor.db%s, then run obor migrate again",
				s.rel(from), s.rel(to), suffix)
		}
		if err := os.Rename(from, to); err != nil {
			return did, project.Failf("%v", err)
		}
		did = append(did, "herd.db"+suffix+" → obor.db"+suffix)
	}
	return did, nil
}

var gitignoreHerd = regexp.MustCompile(`^(!?/?)\.herd(/|$)`)

// gitignoreHerd: the lines about .herd/ become .obor/ lines (a line the file has already is not added twice).
func (s *Setup) gitignoreHerd() (bool, error) {
	path := filepath.Join(s.root, ".gitignore")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, project.Failf("%v", err)
	}
	lines := strings.Split(string(b), "\n")
	have := map[string]bool{}
	for _, l := range lines {
		have[l] = true
	}
	var out []string
	changed := false
	for _, l := range lines {
		if gitignoreHerd.MatchString(l) {
			changed = true
			l = gitignoreHerd.ReplaceAllString(l, "${1}.obor${2}")
			if have[l] {
				continue
			}
			have[l] = true
		}
		out = append(out, l)
	}
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return false, project.Failf("%v", err)
	}
	return true, nil
}

var topHerdKey = regexp.MustCompile(`(?m)^( {0,4}|\t?)"herd"(\s*:)`)

// rewriteConfig: a config file's top-level "herd" member (the version pin) becomes "obor" and HERD_CHANGED_FILES
// OBOR_CHANGED_FILES; nothing else is touched. Absent file: no change.
func rewriteConfig(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, project.Failf("%v", err)
	}
	out := bytes.ReplaceAll(b, []byte("HERD_CHANGED_FILES"), []byte("OBOR_CHANGED_FILES"))
	herdPin, _ := config.MemberAt(out, []string{"herd"})
	oborPin, _ := config.MemberAt(out, []string{"obor"})
	if herdPin != nil && oborPin == nil {
		if topHerdKey.Match(out) {
			out = topHerdKey.ReplaceAll(out, []byte(`${1}"obor"${2}`))
		} else if edited, err := config.EditJSON(out, []string{"obor"}, herdPin); err == nil {
			if edited, err = config.EditJSON(edited, []string{"herd"}, nil); err == nil {
				var ind bytes.Buffer
				if json.Indent(&ind, edited, "", "  ") == nil {
					out = append(ind.Bytes(), '\n')
				}
			}
		}
	}
	if bytes.Equal(out, b) {
		return false, nil
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, project.Failf("%v", err)
	}
	return true, nil
}

// 3 ---------------------------------------------------------------------------------------------------------------

const (
	oborPlugin      = "obor@obor-kit"
	oborMarketplace = "obor-kit"
)

func (s *Setup) migratePlugin(ctx context.Context) error {
	var settings struct {
		Enabled map[string]any `json:"enabledPlugins"`
	}
	if b, err := os.ReadFile(filepath.Join(s.root, ".claude", "settings.json")); err == nil {
		json.Unmarshal(b, &settings)
	}
	var herd []string
	for k := range settings.Enabled {
		if strings.HasPrefix(k, "herd@") {
			herd = append(herd, k)
		}
	}
	sort.Strings(herd)
	if len(herd) == 0 && settings.Enabled[oborPlugin] == true {
		s.ok("3 plugin", true, "%s is enabled for the project, no herd plugin", oborPlugin)
		return nil
	}
	claude, err := lookPath(s.Env.Get("PATH"), agents.Default) // the Claude CLI
	if err != nil {
		return project.Failf("claude is not on PATH: run `claude plugin uninstall %s --scope project`, `claude plugin marketplace add %s` "+
			"and `claude plugin install %s --scope project` in %s, then obor migrate again", strings.Join(append(herd, ""), " "), s.Kit, oborPlugin, s.root)
	}
	call := func(args ...string) error { return claudeCall(ctx, claude, s.root, s.Env, args...) }
	exit := exitText
	for _, k := range herd {
		if err := call("plugin", "uninstall", k, "--scope", "project"); err != nil {
			return project.Failf("claude plugin uninstall %s --scope project failed (%s)", k, exit(err))
		}
	}
	// A marketplace of that name may be registered already, which can end non-zero: the install decides.
	if err := call("plugin", "marketplace", "add", s.Kit); err != nil {
		s.say("WARN 3 plugin: claude plugin marketplace add %s ended with %s", s.Kit, exit(err))
	}
	if err := call("plugin", "install", oborPlugin, "--scope", "project"); err != nil {
		return project.Failf("claude plugin install %s --scope project failed (%s): fix it, then run obor migrate again",
			oborPlugin, exit(err))
	}
	s.ok("3 plugin", false, "uninstalled %s; installed %s from marketplace %s (%s; other projects' marketplaces stay)",
		strings.Join(append(herd, ""), " "), oborPlugin, oborMarketplace, s.Kit)
	return nil
}

// lookPath finds name in the directories of the PATH value path.
func lookPath(path, name string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// 4 ---------------------------------------------------------------------------------------------------------------

func (s *Setup) migrateGithooks(context.Context) error {
	dir := filepath.Join(s.Kit, "githooks")
	cur, _ := project.Git(s.root, "config", "--local", "--get", "core.hooksPath")
	if strings.TrimSpace(cur) == dir {
		s.ok("4 githooks", true, "core.hooksPath is %s", dir)
		return nil
	}
	var out bytes.Buffer
	sub := *s
	sub.Out = &out
	if err := sub.githooks(nil); err != nil {
		return err
	}
	s.ok("4 githooks", false, "%s", strings.TrimPrefix(strings.TrimSpace(out.String()), "OK githooks: "))
	return nil
}

// 5 ---------------------------------------------------------------------------------------------------------------

func (s *Setup) migrateUser(ctx context.Context) error {
	var did []string
	n, err := copyTree(herdHome(s.Env), project.StateHome(s.Env))
	if err != nil {
		return project.Failf("copying %s to %s: %v", herdHome(s.Env), project.StateHome(s.Env), err)
	}
	if n > 0 {
		did = append(did, fmt.Sprintf("copied %d file(s) of %s to %s (kept: other projects may still run herd)",
			n, herdHome(s.Env), project.StateHome(s.Env)))
	}
	plug, err := s.migrateHerdrPlugin(ctx)
	if err != nil {
		return err
	}
	did = append(did, plug...)
	if open, err := s.hasAgentEntry(); err != nil {
		return err
	} else if open {
		var out bytes.Buffer
		sub := *s
		sub.Out = &out
		if err := sub.agentsCmd(nil); err != nil {
			return err
		}
		if doneAgents(out.String()) {
			did = append(did, "obor setup agents (OpenCode: obor entry added, the herd entry stays)")
		}
	}
	if len(did) == 0 {
		s.ok("5 user", true, "~/.obor, the obor.events herdr plugin and the OpenCode entry are in place")
	} else {
		s.ok("5 user", false, "%s", strings.Join(did, "; "))
	}
	return nil
}

// herdHome is the old machine-wide state dir: ${HERD_STATE_HOME:-$HOME/.herd}.
func herdHome(env project.Env) string {
	if h, _ := env("HERD_STATE_HOME"); h != "" {
		return h
	}
	return filepath.Join(env.Get("HOME"), ".herd")
}

// copyTree copies the files and links of src that dst lacks (never overwrites; the old release binaries in bin/ stay
// behind: obor fetches its own); the number of entries copied. No src: none.
func copyTree(src, dst string) (int, error) {
	if project.Physical(src) == project.Physical(dst) {
		return 0, nil
	}
	if _, err := os.Stat(src); err != nil {
		return 0, nil
	}
	n := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() && rel == "bin" {
			return filepath.SkipDir
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			if _, err := os.Lstat(to); err == nil {
				return nil
			}
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			n++
			return os.Symlink(target, to)
		case !d.Type().IsRegular():
			return nil
		}
		if _, err := os.Lstat(to); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		n++
		return os.WriteFile(to, b, fi.Mode().Perm())
	})
	return n, err
}

// migrateHerdrPlugin: obor.events linked (setup herdr-plugin). herd.events is never unlinked: it acts only on .herd/
// projects, so it does nothing for a migrated one, and v0.17's cleanup removes it.
func (s *Setup) migrateHerdrPlugin(ctx context.Context) ([]string, error) {
	plugins, err := s.H.Plugins(ctx)
	if err != nil {
		if app.NotRunning(err) {
			s.say("hint: herdr is not running; start it, then run obor migrate again to link obor.events")
			return nil, nil
		}
		return nil, project.Failf("cannot read herdr's plugins: %v", err)
	}
	var did []string
	linked, herd := false, false
	for _, p := range plugins {
		herd = herd || p.ID == "herd.events"
		if p.ID == "obor.events" && (p.Root == "" || project.Physical(p.Root) == project.Physical(filepath.Join(s.Kit, "herdr-plugin"))) {
			linked = true
		}
	}
	if !linked {
		var out bytes.Buffer
		sub := *s
		sub.Out = &out
		if err := sub.events(ctx); err != nil {
			return nil, project.Failf("%s", strings.TrimSpace(out.String()))
		}
		did = append(did, "linked obor.events (user-global)")
	}
	if herd {
		s.say("herd.events left linked (removed in v0.17)")
	}
	return did, nil
}

// hasAgentEntry: some kind's user config (OpenCode's) has an MCP entry (herd's or obor's) or its plugin link is
// there: `obor setup agents` has something to add.
func (s *Setup) hasAgentEntry() (bool, error) {
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		in := k.Install(s.Kit, s.Env.Get)
		if in.Plugin != nil {
			dir, base := filepath.Dir(in.Plugin.To), filepath.Base(in.Plugin.To)
			for _, b := range []string{base, strings.Replace(base, "obor", "herd", 1)} {
				if _, err := os.Lstat(filepath.Join(dir, b)); err == nil {
					return true, nil
				}
			}
		}
		if in.MCP.Where != agents.Config {
			continue
		}
		doc, err := readConfig(configFile(in.MCP))
		if errors.Is(err, errComments) {
			return true, nil // `obor setup agents` prints what to paste
		}
		if err != nil {
			return false, project.Failf("%s: %v: left alone", configFile(in.MCP), err)
		}
		for _, entry := range []string{"herd", "obor"} {
			key := append(append([]string{}, in.MCP.Key[:len(in.MCP.Key)-1]...), entry)
			if cur, err := config.MemberAt(doc, key); err == nil && cur != nil {
				return true, nil
			}
		}
	}
	return false, nil
}

// 6 ---------------------------------------------------------------------------------------------------------------

var saysHerd = regexp.MustCompile(`(^|[^A-Za-z0-9_])herd([^A-Za-z0-9_]|$)|HERD_`)

func (s *Setup) migrateProse(context.Context) error {
	var files []string
	for _, pat := range []string{"*.md", filepath.Join("docs", "*.md"), filepath.Join("docs", "agents", "*.md")} {
		m, _ := filepath.Glob(filepath.Join(s.root, pat))
		files = append(files, m...)
	}
	var found []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var lines []string
		for i, l := range strings.Split(string(b), "\n") {
			if saysHerd.MatchString(l) {
				lines = append(lines, strconv.Itoa(i+1))
			}
		}
		if len(lines) > 0 {
			found = append(found, fmt.Sprintf("%s (line %s)", s.rel(f), strings.Join(lines, ", ")))
		}
	}
	if len(found) == 0 {
		s.ok("6 prose", true, "no root, docs/ or docs/agents/ markdown line says herd")
		return nil
	}
	s.ok("6 prose", false, "%d file(s) still say herd (listed, never edited): %s", len(found), strings.Join(found, ", "))
	return nil
}

// 7 ---------------------------------------------------------------------------------------------------------------

func (s *Setup) migrateFinish(ctx context.Context) error {
	if err := s.load(); err != nil {
		return project.Failf("migrated, but the config does not load: %v (obor config validate)", err)
	}
	s.ok("7 finish", !s.changed, "%s is migrated. Next:", s.cfg.Data.Name)
	s.say("  1. start serve: obor serve (or obor serve install)")
	panes, err := s.H.Panes(ctx)
	if err != nil {
		s.say("  2. obor restart <agent> --fresh for each labelled pane (herdr is not reachable: obor status lists them)")
		return nil
	}
	names := roleAgents(s.cfg)
	for _, l := range s.cfg.Lanes() {
		names = append(names, s.cfg.LaneAgentAliases(l.Name, project.LaneAgent(s.root, l.Name))...)
	}
	lead := s.cfg.AgentName("lead", "")
	var workers, leads []string
	seen := map[string]bool{}
	for _, p := range panes {
		if p.Label == "" || seen[p.Label] || !contains(names, p.Label) || !project.LabelOurs(s.cfg, p.Label, p.Cwd, "") {
			continue
		}
		seen[p.Label] = true
		if p.Label == lead {
			leads = append(leads, p.Label)
		} else {
			workers = append(workers, p.Label)
		}
	}
	sort.Strings(workers)
	for _, a := range workers {
		s.say("  2. obor restart %s --fresh", a)
	}
	for _, a := range leads {
		s.say("  3. obor restart %s --fresh   (the lead loses its context: at a handoff)", a)
	}
	if len(workers)+len(leads) == 0 {
		s.say("  2. no labelled pane: nothing to restart")
	}
	return nil
}

// doneAgents: obor setup agents' output has an OK line that did something (not an "already").
func doneAgents(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "OK ") && !strings.Contains(l, "already") {
			return true
		}
	}
	return false
}
