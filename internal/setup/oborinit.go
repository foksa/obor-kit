package setup

// obor init (docs/design.md "Setup"): set a project up with no questions, by chaining what setup
// already does. Every step is idempotent, so a rerun after a fix is the repair path; the summary lists each step as
// done, already or skipped (why).
//
//	terminal   the terminal's own check, first, on a terminal that has one (hc: the core's environment holds no
//	           HERDR_ or OBOR_ name, else FAIL with hc.LeakLine, before any write); the kind is --terminal's (cmd/obor
//	           passes the one it detected, else herdr) and a new config's project.terminal.
//	herdr      herdr unreachable → FAIL, before any write (nothing later works without it); no claude on PATH →
//	           FAIL (the lead runs it).
//	git        no commit yet: a directory holding files only at init's own paths (.obor/config.json, .gitignore, the
//	           docs, .claude/settings.json; whatever their bytes) gets git init (when in no repository) and, after
//	           the config edits and the plugin install, the one "obor init" commit holding them, before the
//	           worktrees: the tree is clean after init. Any other file there → FAIL before any write. A repository
//	           with a commit: untouched.
//	project    init's files (.obor/config.json, .gitignore lines, docs); a new config's prefix is made unique against
//	           the running herdr agents' names (<prefix>-…).
//	space      the caller's pane, decided before any pane is placed: when it qualifies as the lead's (no OBOR_ROLE,
//	           TakeLead's rules, the obor command itself in its foreground), the terminal readies its group for the
//	           project (port.TerminalSetup.LeadSpace; hc: the caller's space named after the project), so the panes
//	           go there and the lead's start takes the pane; listed only when it did something.
//	setup      setup.confirmed: false when the config has no key: the models, effort and lanes obor init left
//	           are a starting point the lead goes through with the owner at its first session.
//	projects   the project in the machine's list of obor projects by name (project.Register): obor send <name>
//	           and obor projects read it.
//	githooks   obor setup githooks: core.hooksPath at the kit's hooks, or at its chain/ set when there is something to
//	           chain.
//	commands   detect: a Makefile with a check: target → make check; go.mod → go test ./...; package.json → its
//	           lockfile picks bun / pnpm / npm, check = its lint, typecheck, check and test scripts, each that the
//	           check script does not already run, lane_setup the install, lane_ready test -d node_modules; a script
//	           that writes files and a check with no test step are warnings; compile stays unset, so a moved
//	           candidate re-runs check, and a typecheck script is suggested for it; no compile:
//	           config.NoCompileWarning. Only keys still null are written; nothing found → they stay null.
//	lanes      L-01 … L-max(2, lanes.count): at least two (setup lane: lanes 2; a task's Model:/Difficulty: picks
//	           the model), and every lane an existing config counts, so init leaves no configured lane without a
//	           worktree; roles.lead,
//	           roles.integrator and roles.reviewer keep their model when its kind's CLI is on PATH, else opus (the
//	           example's reviewer is a Codex model: no codex → opus), whose starter entry init adds when the config has
//	           none, so the role it writes resolves. A
//	           configured lane whose dir is a worktree of this repo and whose setup finished (lane_ready, else the
//	           lane_setup marker) is skipped; one whose lane_setup failed the last time (or whose lane_ready does) is
//	           set up again, and lane_ready must pass after that, else the lane's step FAILs.
//	plugin     claude plugin marketplace add <kit>, claude plugin install obor@<the kit's marketplace> --scope
//	           project (exit codes only, under a timeout); skipped when the project's settings enable it. Before the
//	           commit and the panes: an agent started without the plugin runs without obor's hooks and MCP tools.
//	herdr      serve restores its own panes after a herdr restart, replacing an agent herdr resumed there: when
//	           herdr's config.toml does not set `[session] resume_agents_on_restore = false`, the summary says that
//	           line saves the double start (optional; user-global: it affects every herdr pane; obor never edits
//	           that config).
//	panes      the first two configured lanes (lanes.list's, else L-01 … L-max(2, lanes.count)) in a lanes tab, the
//	           integrator and the reviewer in a roles tab (the terminal's placement by kind: a tab of the project
//	           whose panes are all of that kind, else a new one, "lanes" or "roles"; never the caller's tab). A pane
//	           held by a trust dialog is listed, not fatal: Enter there, then obor init again.
//	serve      obor serve install (Setup.Serve; off darwin it says to run obor serve yourself).
//
// The output ends with the setup table (setup show): per difficulty level each of its model refs with what runs
// it, the lanes, the roles' models, and whether the owner confirmed them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

// InitUsage is obor init's usage line.
const InitUsage = "usage: obor init [--name N] [--prefix P] [--terminal herdr|hc|rex]"

// pluginTimeout bounds each claude plugin call.
var pluginTimeout = 2 * time.Minute

// step is one line of obor init's summary.
type step struct{ name, state, what string }

func (s *Setup) oborInit(ctx context.Context, args []string) error {
	name, prefix, kind, err := initArgs(args)
	if err != nil {
		return err
	}
	s.terminal = kind
	// terminal check: on a terminal with one (hc, Rex) the first step, before any write: a leaked core, or no rex on
	// PATH, is a FAIL.
	var termLine string
	if s.Check != nil {
		if termLine, err = s.Check(ctx); err != nil {
			return project.Failf("terminal %s: %v", s.initTerminal(), err)
		}
	}
	if r, err := s.git().Find(s.Dir); err == nil && project.Stopped(r.Root) {
		return project.Failf("%s is stopped (obor stop): obor start wakes it", r.Root)
	}
	running, err := s.T.Agents(ctx)
	if err != nil {
		return project.Failf("the terminal (%s) is not reachable (%v): start it, then obor init again", s.initTerminal(), err)
	}
	claude, err := project.LookPath(s.Env.Get("PATH"), agents.Default)
	if err != nil {
		return project.Failf("%s is not on PATH (the lead runs it): install it, then obor init again", agents.Default)
	}
	// git: a directory in no repository gets one; a repository without a commit gets its first below, from
	// init's own files (the lanes branch from it).
	// Decided before any write: init commits only in a directory whose files are all at init's own paths; a
	// repository with no commit and any other file in it is a FAIL.
	root, repo := s.Dir, true
	switch r, err := s.git().Find(s.Dir); {
	case errors.Is(err, project.ErrNotGit):
		repo = false
	case err != nil:
		return project.Failf("not in a git repository")
	default:
		root = r.Root
	}
	if _, err := os.Stat(filepath.Join(root, ".obor", "config.json")); err == nil {
		// Before any write: init edits the config it finds, so one this obor cannot read refuses here, with
		// the load's own error.
		if _, err := config.Load(root); err != nil {
			return project.Fail(strings.TrimPrefix(err.Error(), "obor: "))
		}
	}
	auto, err := s.autoCommit(root, repo)
	if err != nil {
		return err
	}
	if !repo {
		if err := s.gitInit(); err != nil {
			return err
		}
	}
	if err := s.findRoot(); err != nil {
		return err
	}
	var hints []string
	var steps []step
	add := func(name, state, format string, a ...any) {
		steps = append(steps, step{name, state, fmt.Sprintf(format, a...)})
	}

	if termLine != "" {
		add("terminal", "done", "%s: %s", s.initTerminal(), termLine)
	}

	// project
	existed := s.configExists()
	if prefix == "" && !s.configHasPrefix() {
		prefix = uniquePrefix(defaultPrefix(nameOr(name, s.root)), running)
	}
	wrote, err := s.initProject(name, prefix)
	if err != nil {
		return err
	}
	add("project", doneOr(wrote), "%s (prefix %s)", s.cfg.Name(), s.cfg.Prefix())

	// the caller's pane, decided before any pane is placed: when it qualifies as the lead's, the panes go to its
	// group (hc: its space, named after the project) and the lead's start takes it
	if line, err := s.initLeadSpace(ctx); err != nil {
		return err
	} else if line != "" {
		add("space", "done", "%s", line)
	}

	// setup: models, effort and difficulty are a starting point until the lead confirms them with the owner.
	confirmed, err := s.initConfirmed()
	if err != nil {
		return err
	}
	add("setup", doneOr(confirmed || !existed), "%s", confirmedLine(s.cfg))

	// projects: the machine's list of obor projects by name: obor send <name> and obor projects read it.
	changed, err := project.Register(s.cfg.Root, s.Env)
	if err != nil {
		return project.Failf("the machine's project list: %v", err)
	}
	add("projects", doneOr(changed), "%s in %s", s.cfg.Name(), project.ProjectsFile(s.Env))

	// commands
	set, warns, _, err := s.initCommands()
	if err != nil {
		return err
	}
	switch {
	case len(set) > 0:
		add("commands", "done", "%s", strings.Join(set, "; "))
	case !s.anyCommand():
		add("commands", "skipped", "none detected (no Makefile check:, go.mod or package.json): set merge.check, "+
			"lanes.setup, lanes.ready and lanes.links in .obor/config.json")
	default:
		add("commands", "already", "%s", s.commandsLine())
	}

	// kinds: each plugin kind's manifest cached, before anything reads it: the guard, the agent starts of the
	// panes step, obor setup mcp (the kit's kinds are answered in process: nothing to cache)
	kinds, err := s.cachePlugins()
	if err != nil {
		return err
	}
	if err := s.load(); err != nil {
		return err
	}
	if len(kinds) == 0 {
		add("kinds", "skipped", "no plugin kinds to cache")
	} else {
		add("kinds", "done", "%s: manifest cached", strings.Join(kinds, ", "))
	}

	// models, roles, difficulty and the lane count: config edits, all before the commit
	if err := s.initModels(); err != nil {
		return err
	}
	types, err := s.initRoleTypes()
	if err != nil {
		return err
	}
	if err := s.initDifficulty(); err != nil {
		return err
	}
	if err := s.initLaneCount(); err != nil {
		return err
	}

	// plugin: before the commit, so .claude/settings.json is in it and every worktree gets it from git
	state, what, err := s.initPlugin(ctx, claude)
	if err != nil {
		return err
	}
	add("plugin", state, "%s", what)

	// commit: a repository with no commit, empty before init, gets init's own files as its first (the lanes branch
	// from it); the tree is clean after init.
	committed, err := s.initCommit(auto)
	if err != nil {
		return err
	}

	// githooks
	hooksChanged, err := s.githooksStep(nil)
	if err != nil {
		return err
	}
	add("githooks", doneOr(hooksChanged), "%s", s.hooksLine(s.root))

	// lanes: the worktrees
	var made, had []string
	for _, l := range initLaneNames(s.cfg) {
		if s.cfg.HasLane(l) {
			done, err := s.laneDone(ctx, l, filepath.Join(s.cfg.LanesDir(), l))
			if err != nil {
				return err
			}
			if done {
				had = append(had, l)
				continue
			}
		}
		if err := s.lane(ctx, []string{l}); err != nil {
			return err
		}
		if err := s.load(); err != nil { // the next lane follows the count this one wrote
			return err
		}
		// The rerun's lane_setup must leave the lane ready: a lane the user is told "done" is one they can use.
		if c, out, ok := s.laneReady(ctx, filepath.Join(s.cfg.LanesDir(), l)); !ok {
			return project.Failf("lane %s: lane_ready still fails after lane_setup (%s): %s", l, c.Cmd, out)
		}
		made = append(made, l)
	}
	if err := s.load(); err != nil {
		return err
	}
	lanes := strings.Join(append(made, had...), ", ") + "; " + types
	add("lanes", doneOr(len(made) > 0 || strings.Contains(types, "→")), "%s", lanes)

	// plugin links: an untracked settings file (a repository with a commit) reaches the lanes by link
	state, what, err = s.initPluginLinks()
	if err != nil {
		return err
	}
	add("plugin-links", state, "%s", what)

	// herdr: serve restores its own panes after a herdr restart, replacing an agent herdr resumed there, so herdr's
	// own resume is a double start; herdr's config is never edited, and the hint that saves it goes to the summary.
	if h := s.T.ResumeHint(); h != "" {
		hints = append(hints, h)
	}

	// panes
	if err := s.initPanes(ctx, add); err != nil {
		return err
	}

	// serve
	if s.Serve == nil {
		add("serve", "skipped", "no service installer")
	} else {
		state, line, err := s.Serve(ctx, s.cfg)
		if err != nil {
			return project.Failf("obor serve install: %v", err)
		}
		add("serve", state, "%s", line)
	}

	s.say("")
	s.say("obor init %s:", s.cfg.Name())
	for _, st := range steps {
		s.say("  %-10s %-8s %s", st.name, st.state, st.what)
	}
	s.say("detected: %s", s.detectedLine())
	for _, w := range warns {
		s.say("warning: %s", w)
	}
	for _, h := range hints {
		s.say("%s", h)
	}
	if len(committed) > 0 {
		s.say("committed obor init: %s", s.commitLine(committed))
	} else {
		s.say("nothing committed: review, then commit .obor/config.json, .gitignore, BACKLOG.md, docs/decisions.md, %s/",
			s.cfg.AgentsDir())
	}
	s.say("OK init %s (prefix %s): the lead starts in its own lead tab", s.cfg.Name(), s.cfg.Prefix())
	return nil
}

// initLaneNames are the lanes obor init sets up: L-01 … L-max(2, lanes.count), so a fresh config gets two and a config
// that counts more gets every one, none left without a worktree; a lanes.list names its own lanes.
func initLaneNames(cfg *config.Config) []string {
	if cfg.LanesListed() {
		names := make([]string, 0, cfg.LaneCount())
		for _, l := range cfg.Lanes() {
			names = append(names, l.Name)
		}
		return names
	}
	names := make([]string, 0, 2)
	for i := 1; i <= max(2, cfg.LaneCount()); i++ {
		names = append(names, config.LaneName(i))
	}
	return names
}

// initLaneCount raises lanes.count to the lanes init sets up (initLaneNames) when the config counts its lanes: the
// worktrees come after the commit, the count is in it. A lanes.list names its own.
func (s *Setup) initLaneCount() error {
	n := len(initLaneNames(s.cfg))
	if s.cfg.LanesListed() || s.cfg.LaneCount() >= n {
		return nil
	}
	if err := s.addLane(n); err != nil {
		return err
	}
	return s.load()
}

// commitLine names the files init committed, its agents dir as one entry.
func (s *Setup) commitLine(files []string) string {
	var parts []string
	agents := false
	for _, f := range files {
		if strings.HasPrefix(f, s.cfg.AgentsDir()+"/") {
			agents = true
			continue
		}
		parts = append(parts, f)
	}
	if agents {
		parts = append(parts, s.cfg.AgentsDir()+"/")
	}
	return strings.Join(parts, ", ")
}

func doneOr(done bool) string {
	if done {
		return "done"
	}
	return "already"
}

func nameOr(name, root string) string {
	if name == "" {
		return filepath.Base(root)
	}
	return name
}

func (s *Setup) configExists() bool {
	_, err := os.Stat(filepath.Join(s.root, ".obor", "config.json"))
	return err == nil
}

// uniquePrefix is p, else p2 … p9, the first no running agent's label starts with (<prefix>-): another project's
// agents keep theirs.
func uniquePrefix(p string, running []port.Pane) string {
	taken := func(c string) bool {
		for _, a := range running {
			if strings.HasPrefix(a.Label, c+"-") {
				return true
			}
		}
		return false
	}
	if !taken(p) {
		return p
	}
	for i := 2; i <= 9; i++ {
		if c := p + strconv.Itoa(i); !taken(c) && config.PrefixRe.MatchString(c) {
			return c
		}
	}
	return p
}

// --- commands ---------------------------------------------------------------------------------------------------

// detected is what detectCommands found: the commands by key ("": not found), where from, the warnings init prints
// (a chosen script that writes files, a check with no test step), and the suggestion for merge.compile (the typecheck
// script: a script name is no proof a moved gate's command is enough, so detection never sets it).
type detected struct {
	check, laneSetup, laneReady, suggest string
	from                                 []string
	warn                                 []string
}

var checkTarget = regexp.MustCompile(`(?m)^check\s*:`)

// composedScripts are the scripts a check is composed from, in the order they run: a check script goes before test.
var composedScripts = []string{"lint", "typecheck", "check", "test"}

// detectCommands reads root: a Makefile with a check: target, go.mod, package.json (its lockfile picks the package
// manager). The first of the three gives check; package.json gives lane_setup and lane_ready, a check composed of
// its lint, typecheck, check and test scripts, and a suggestion for compile: its typecheck script (a build script is
// no proof it type-checks what merge.check covers).
func detectCommands(root string) (d detected) {
	if b, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil && checkTarget.Match(b) {
		d.check = "make check"
		d.from = append(d.from, "Makefile check:")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		if d.check == "" {
			d.check = "go test ./..."
		}
		d.from = append(d.from, "go.mod")
	}
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return d
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	_ = json.Unmarshal(b, &pkg)
	pm, install, lock := "npm", "npm install", ""
	for _, l := range []struct{ file, pm, install string }{
		{"bun.lock", "bun", "bun install --frozen-lockfile"}, {"bun.lockb", "bun", "bun install --frozen-lockfile"},
		{"pnpm-lock.yaml", "pnpm", "pnpm install --frozen-lockfile"}, {"package-lock.json", "npm", "npm ci"},
	} {
		if _, err := os.Stat(filepath.Join(root, l.file)); err == nil {
			pm, install, lock = l.pm, l.install, l.file
			break
		}
	}
	from := "package.json (" + pm
	if lock != "" {
		from += ", " + lock
	}
	d.from = append(d.from, from+")")
	if d.check == "" {
		if scripts := chosenScripts(pkg.Scripts); len(scripts) > 0 {
			var cmds []string
			hasTest := false
			for _, name := range scripts {
				cmds = append(cmds, pm+" run "+name)
				if name == "test" || strings.Contains(pkg.Scripts[name], "test") {
					hasTest = true
				}
				if w := scriptWrites(name, pkg.Scripts[name]); w != "" {
					d.warn = append(d.warn, w)
				}
			}
			d.check = strings.Join(cmds, " && ")
			if !hasTest {
				d.warn = append(d.warn, `no test step: add a "test" script to package.json: merge.check runs no tests`)
			}
		}
	}
	if pkg.Scripts["typecheck"] != "" {
		d.suggest = pm + " run typecheck"
	}
	d.laneSetup, d.laneReady = install, "test -d node_modules"
	return d
}

// chosenScripts are the package.json scripts a check is composed from, in the order they run: the lint, typecheck,
// check and test scripts it has, each that the check script does not already run.
func chosenScripts(scripts map[string]string) []string {
	var names []string
	for _, name := range composedScripts {
		if scripts[name] == "" || (name != "check" && runs(scripts["check"], name)) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// runs is whether a package.json script's text already runs the script name: `run <name>` or the name itself.
func runs(script, name string) bool {
	return strings.Contains(script, "run "+name) || strings.Contains(script, name)
}

// scriptWrites is the warning for a package.json script that writes files: which script, the text that writes, and
// the non-writing form (the script without the flag; format's check mode).
func scriptWrites(name, script string) string {
	for _, flag := range []string{"--write", "--fix", "-w"} {
		if strings.Contains(script, flag) {
			form := strings.Join(strings.Fields(strings.ReplaceAll(script, flag, "")), " ")
			return fmt.Sprintf("package.json script %q writes files (%s): run it without it, %q", name, flag, form)
		}
	}
	if strings.Contains(script, "format") {
		return fmt.Sprintf("package.json script %q writes files (format): use its check mode, e.g. --check", name)
	}
	return ""
}

// initCommands writes the detected commands whose key is still null in .obor/config.json: "key = cmd" each. Its
// warnings are the detection's when it wrote check, and config.NoCompileWarning (on the default branch, read once here
// through the git seam) when the config has no compile; its suggestion is the detected typecheck script while the config
// has no compile.
func (s *Setup) initCommands() (set, warn []string, suggest string, err error) {
	d := detectCommands(s.root)
	for _, kv := range [][2]string{{"check", d.check}, {"lane_setup", d.laneSetup}, {"lane_ready", d.laneReady}} {
		if kv[1] == "" {
			continue
		}
		if _, ok := run.Command(s.cfg, kv[0]); ok {
			continue
		}
		if err := s.editConfig(func(doc []byte) (config.Write, error) {
			return config.SetCommand(doc, kv[0], kv[1])
		}); err != nil {
			return nil, nil, "", err
		}
		set = append(set, kv[0]+" = "+kv[1])
		if kv[0] == "check" {
			warn = append(warn, d.warn...)
		}
	}
	if len(set) > 0 {
		if err := s.load(); err != nil {
			return nil, nil, "", err
		}
	}
	if _, ok := run.Command(s.cfg, "compile"); !ok {
		suggest = d.suggest
		if w := s.cfg.NoCompileWarning(s.defaultBranch()); w != "" {
			warn = append(warn, w)
		}
	}
	return set, warn, suggest, nil
}

func (s *Setup) anyCommand() bool { return s.commandsLine() != "" }

// commandsLine is the configured check, compile, lane_setup and lane_ready ("key = cmd; …").
func (s *Setup) commandsLine() string {
	var have []string
	for _, k := range []string{"check", "compile", "lane_setup", "lane_ready"} {
		if c, ok := run.Command(s.cfg, k); ok {
			have = append(have, k+" = "+c.Cmd)
		}
	}
	return strings.Join(have, "; ")
}

// editConfig applies edit to .obor/config.json (not local.json), leaves it uncommitted, and says
// "config <key> = <value> (uncommitted<note>)".
func (s *Setup) editConfig(edit func([]byte) (config.Write, error), note ...string) error {
	path := filepath.Join(s.root, ".obor", "config.json")
	doc, err := os.ReadFile(path)
	if err != nil {
		return project.Failf("cannot read %s", path)
	}
	w, err := edit(doc)
	if err != nil {
		return project.Failf("cannot update %s: %v", path, err)
	}
	if writeJSON(path, w.Doc) != nil {
		return project.Failf("cannot update %s", path)
	}
	suffix := ""
	if len(note) > 0 {
		suffix = note[0]
	}
	s.say("config %s = %s (uncommitted%s)", w.Key, w.Val, suffix)
	return nil
}

// detectedLine is the summary's "detected": the CLIs on PATH and the files the commands came from.
func (s *Setup) detectedLine() string {
	var clis []string
	for _, k := range s.cfg.Kinds().Names() {
		if _, err := project.LookPath(s.Env.Get("PATH"), k); err == nil {
			clis = append(clis, k)
		}
	}
	from := detectCommands(s.root).from
	if len(from) == 0 {
		from = []string{"no build files"}
	}
	return strings.Join(clis, ", ") + " on PATH; " + strings.Join(from, ", ")
}

// --- roles ------------------------------------------------------------------------------------------------------

// initRoleTypes keeps the lead's, the integrator's and the reviewer's model ref when it names a model whose kind's
// CLI is on PATH, else sets it to a Claude model (the kind init requires on PATH): "lead opus, integrator opus,
// reviewer sol" (a change written "reviewer sol → opus"). It decides from .obor/config.json alone, the file it
// writes (.obor/local.json left out), after the kind fill of `obor setup agents` (a former built-in entry with no
// kind has its kind). The Claude model a role falls back to is the first agents.models entry of that kind in config
// order, else the starter entry fallbackStarter, added; a config whose fallbackStarter name is another kind's has
// none to add: init fails naming the roles and the entry to add, and writes nothing for them.
func (s *Setup) initRoleTypes() (string, error) {
	shared, err := s.fillModelKinds()
	if err != nil {
		return "", err
	}
	roles := []string{"lead", "integrator", "reviewer"}
	was := map[string]string{} // the ref of each role that falls back
	var falls []string
	for _, role := range roles {
		rc, _ := shared.Role(role)
		if m, ok := shared.Model(rc.Type); ok {
			if _, err := project.LookPath(s.Env.Get("PATH"), m.Kind); err == nil {
				continue
			}
		}
		was[role] = config.ModelRef(rc.Type, rc.Effort)
		if was[role] == "" {
			was[role] = "unset"
		}
		falls = append(falls, role)
	}
	var to string
	if len(falls) > 0 {
		var add *config.NamedModel
		if to, add, err = fallbackModel(shared, falls); err != nil {
			return "", err
		}
		if add != nil {
			if err := s.editConfig(func(doc []byte) (config.Write, error) {
				return config.AddModels(doc, []config.NamedModel{*add})
			}); err != nil {
				return "", err
			}
		}
		for _, role := range falls {
			if err := s.editConfig(func(doc []byte) (config.Write, error) {
				return config.SetRoleModel(doc, role, to)
			}); err != nil {
				return "", err
			}
		}
		if err := s.load(); err != nil {
			return "", err
		}
	}
	var parts []string
	for _, role := range roles {
		if from, fell := was[role]; fell {
			parts = append(parts, role+" "+from+" → "+to)
			continue
		}
		rc, _ := shared.Role(role)
		parts = append(parts, role+" "+config.ModelRef(rc.Type, rc.Effort))
	}
	return strings.Join(parts, ", "), nil
}

// initDifficulty writes `difficulty` from the kinds init detected: the starter lists keep only the models
// of the Claude kind, which init requires on PATH (starterLevels). A non-Claude model (deepseek on OpenCode) joins a
// list only when the user lists it: init has no flag or prompt for that, so a machine without OpenCode never sends its
// first simple task to a CLI that cannot start. An existing `difficulty` is kept.
func (s *Setup) initDifficulty() error {
	if s.cfg.HasDifficulty() {
		return nil
	}
	out := starterLevels(s.cfg)
	if len(out) == 0 {
		return nil
	}
	if err := s.editConfig(func(doc []byte) (config.Write, error) {
		return config.SetDifficulty(doc, out)
	}); err != nil {
		return err
	}
	return s.load()
}

// --- plugin -----------------------------------------------------------------------------------------------------

// initPlugin installs obor@<the kit's marketplace> for the project unless its .claude/settings.json enables it.
func (s *Setup) initPlugin(ctx context.Context, claude string) (state, what string, err error) {
	mkt, err := kitMarketplace(s.Kit)
	if err != nil {
		return "", "", err
	}
	plugin := "obor@" + mkt
	if s.pluginEnabled(plugin) {
		return "already", plugin + " enabled for the project", nil
	}
	// A marketplace of that name may be registered already, which can end non-zero: the install decides.
	if err := claudeCall(ctx, claude, s.root, s.Env, "plugin", "marketplace", "add", s.Kit); err != nil {
		s.say("WARN plugin: claude plugin marketplace add %s ended with %s", s.Kit, exitText(err))
	}
	if err := claudeCall(ctx, claude, s.root, s.Env, "plugin", "install", plugin, "--scope", "project"); err != nil {
		return "", "", project.Failf("claude plugin install %s --scope project failed (%s): fix it, then obor init again",
			plugin, exitText(err))
	}
	return "done", "installed " + plugin + " (marketplace " + s.Kit + ")", nil
}

// kitMarketplace is the name in the kit's .claude-plugin/marketplace.json.
func kitMarketplace(kit string) (string, error) {
	var m struct {
		Name string `json:"name"`
	}
	path := filepath.Join(kit, ".claude-plugin", "marketplace.json")
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil || m.Name == "" {
		return "", project.Failf("cannot read the marketplace name from %s", path)
	}
	return m.Name, nil
}

// pluginEnabled: the project's .claude/settings.json enables plugin.
func (s *Setup) pluginEnabled(plugin string) bool {
	var settings struct {
		Enabled map[string]any `json:"enabledPlugins"`
	}
	b, err := os.ReadFile(filepath.Join(s.root, ".claude", "settings.json"))
	return err == nil && json.Unmarshal(b, &settings) == nil && settings.Enabled[plugin] == true
}

// claudeCall runs the Claude CLI at claude with args in dir, PATH from env, as a process group under pluginTimeout;
// its output is dropped (the exit code decides).
func claudeCall(ctx context.Context, claude, dir string, env project.Env, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, pluginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+env.Get("PATH"))
	var discard bytes.Buffer
	cmd.Stdout, cmd.Stderr = &discard, &discard
	cleanup := project.Group(cmd)
	defer cleanup()
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s", pluginTimeout)
	}
	return err
}

// exitText is "exit N" for a command's exit error, else the error.
func exitText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return "exit " + strconv.Itoa(ee.ExitCode())
	}
	return err.Error()
}

// --- panes ------------------------------------------------------------------------------------------------------

// initPanes labels the panes of the configured lanes (the first two), the integrator and the reviewer and starts no
// agent (each skipped when it runs): one summary line each through add.
func (s *Setup) initPanes(ctx context.Context, add func(name, state, format string, a ...any)) error {
	place := func(what, agent string, f func(w where) (placed, error), w where) error {
		p, err := f(w)
		switch {
		case err != nil:
			return err
		case p.running:
			add(what, "already", "%s running (pane %s)", agent, p.pane)
		case p.idle:
			add(what, "already", "pane %s labelled %s, no agent (started on first use)", p.pane, agent)
		default:
			add(what, "done", "pane %s labelled %s, no agent (started on first use)", p.pane, agent)
		}
		return nil
	}
	lane := func(name string) func(where) (placed, error) {
		return func(w where) (placed, error) { return s.lanePane(ctx, name, w, false) }
	}
	role := func(name string) func(where) (placed, error) {
		return func(w where) (placed, error) { return s.rolePane(ctx, name, w, false) }
	}
	// The layout is the terminal's, by each pane's kind: the lanes in a lanes tab, the integrator and the reviewer in
	// a roles tab. A config of fewer lanes places what it has, and more than two keep the panes they place at dispatch.
	for i, name := range initLaneNames(s.cfg) {
		if i >= 2 {
			break
		}
		if err := place(name, s.laneWorker(name), lane(name), where{}); err != nil {
			return err
		}
	}
	for _, name := range []string{"integrator", "reviewer"} {
		if err := place(name, s.cfg.AgentName(name, ""), role(name), where{}); err != nil {
			return err
		}
	}
	return nil
}

// laneWorker is the lane's worker agent name (derived).
func (s *Setup) laneWorker(lane string) string {
	return s.cfg.AgentName("worker", lane)
}

// --- plugin reaches the lanes ----------------------------------------------------------------------------

// initPluginLinks makes the project's .claude/settings.json reach every lane: lanes are worktrees, so a file the
// repository does not track is not in them. Untracked → it joins lanes.links (and .gitignore, which keeps git clean
// from removing the links) and lanes.links is linked into the existing lanes; tracked or absent → nothing to do. A
// role worktree gets the links where it is made (rolePane; the launcher at its agent's start).
func (s *Setup) initPluginLinks() (state, what string, err error) {
	tracked := func(path string) bool { return project.TrackedIn(s.root, path, s.git().Git) }
	for _, w := range config.TrackedLinkWarnings(s.cfg.LaneLinks(), tracked) {
		s.say("WARN %s", w)
	}
	if !project.SettingsUntrackedWith(s.root, s.git().Git) {
		return "already", project.PluginSettings + " is tracked or absent: lanes get it from git", nil
	}
	changed := false
	if !slices.Contains(s.cfg.LaneLinks(), project.PluginSettings) {
		links := append(slices.Clone(s.cfg.LaneLinks()), project.PluginSettings)
		if err := s.editConfig(func(doc []byte) (config.Write, error) {
			return config.SetLaneLinks(doc, links)
		}); err != nil {
			return "", "", err
		}
		if err := s.load(); err != nil {
			return "", "", err
		}
		changed = true
	}
	ignored, err := s.gitignore()
	if err != nil {
		return "", "", err
	}
	changed = changed || ignored
	for _, l := range s.cfg.Lanes() {
		if _, err := os.Stat(l.Dir); err != nil {
			continue
		}
		linked, err := project.LinkInto(s.root, l.Dir, project.WorktreeLinksWith(s.cfg, l.Dir, s.git().Git))
		if err != nil {
			return "", "", project.Failf("%v", err)
		}
		for _, link := range linked {
			s.say("link %s → %s", filepath.Join(l.Dir, link), filepath.Join(s.root, link))
			changed = true
		}
	}
	return doneOr(changed), project.PluginSettings + " is untracked: in lanes.links, linked into the lanes and, as each is made, the role worktrees (commit it to share it by git instead)", nil
}

// configHasPrefix: the config init finds sets project.agent_prefix (a new config, or one this obor cannot read, has none
// to keep).
func (s *Setup) configHasPrefix() bool {
	cfg, err := config.Load(s.root)
	return err == nil && cfg.Prefix() != ""
}
