package setup

// obor init (P7-2, docs/plan/phase-7-advice.md §1): set a project up with no questions, by chaining what setup
// already does. Every step is idempotent, so a rerun after a fix is the repair path; the summary lists each step as
// done, already or skipped (why).
//
//	herdr      herdr unreachable → FAIL, before any write (nothing later works without it); no claude on PATH →
//	           FAIL (the lead runs it).
//	git        no commit yet: only a directory holding nothing but .git (or init's own untouched files, a rerun)
//	           gets git init (when in no repository) and, after the project step, the "obor init" commit of init's
//	           files; anything else there → FAIL before any write (T-631). A repository with a commit: untouched.
//	project    init's files (.obor/config.json, .gitignore lines, docs); a new config's prefix is made unique against
//	           the running herdr agents' names (<prefix>-…).
//	setup      setup.confirmed: false when the config has no key (T-647): the models, effort and lanes obor init left
//	           are a starting point the lead goes through with the owner at its first session.
//	projects   the project in the machine's list of obor projects by name (T-646, project.Register): obor send <name>
//	           and obor projects read it.
//	githooks   obor setup githooks: core.hooksPath at the kit's hooks, or at its chain/ set when there is something to
//	           chain (T-623).
//	commands   detect: a Makefile with a check: target → make check; go.mod → go test ./...; package.json → its
//	           lockfile picks bun / pnpm / npm, the check script else the test script, lane_setup the install,
//	           lane_ready test -d node_modules. Only keys still null are written; nothing found → they stay null.
//	lanes      L-01 and L-02 (setup lane: lanes 2; a task's Model:/Difficulty: picks the model); roles.lead,
//	           roles.integrator and roles.reviewer keep their model when its kind's CLI is on PATH, else opus (the
//	           example's reviewer is a Codex model: no codex → opus). A
//	           configured lane whose dir is a worktree of this repo and whose setup finished (lane_ready, else the
//	           lane_setup marker) is skipped; one whose lane_setup failed the last time (or whose lane_ready does) is
//	           set up again, and lane_ready must pass after that, else the lane's step FAILs.
//	plugin     claude plugin marketplace add <kit>, claude plugin install obor@<the kit's marketplace> --scope
//	           project (exit codes only, under a timeout); skipped when the project's settings enable it. Before the
//	           panes: an agent started without the plugin runs without obor's hooks and MCP tools.
//	events     gone (T-1117): obor serve restarts its own panes, so init only removes the obor.events plugin an older
//	           obor linked in herdr (setup herdr-plugin, once): a list error is a WARN, not a step, and never a FAIL.
//	panes      L-01 in a new tab "workers", L-02 and the integrator beside it (the integrator in the caller's tab when
//	           that is the project's, T-628), the reviewer in a new tab "review". A pane held by a trust dialog is
//	           listed, not fatal: Enter there, then obor init again.
//	serve      obor serve install (Setup.Serve; off darwin it says to run obor serve yourself).
//
// The output ends with the setup table (T-647, setup show): per difficulty level each of its model refs with what runs
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
const InitUsage = "usage: obor init [--name N] [--prefix P]"

// pluginTimeout bounds each claude plugin call.
var pluginTimeout = 2 * time.Minute

// step is one line of obor init's summary.
type step struct{ name, state, what string }

func (s *Setup) oborInit(ctx context.Context, args []string) error {
	name, prefix, err := initArgs(args)
	if err != nil {
		return err
	}
	if r, err := project.Find(s.Dir); err == nil && project.Stopped(r.Root) {
		return project.Failf("%s is stopped (obor stop): obor start wakes it", r.Root)
	}
	running, err := s.T.Agents(ctx)
	if err != nil {
		return project.Failf("herdr is not reachable (%v): start herdr, then obor init again", err)
	}
	claude, err := project.LookPath(s.Env.Get("PATH"), agents.Default)
	if err != nil {
		return project.Failf("%s is not on PATH (the lead runs it): install it, then obor init again", agents.Default)
	}
	// git: a directory in no repository gets one (T-629); a repository without a commit gets its first below, from
	// init's own files (the lanes branch from it).
	// Decided before any write (T-631): init commits only in a directory that held nothing but .git (or init's own
	// untouched files, a rerun); a repository with no commit and anything else in it is a FAIL.
	root, repo := s.Dir, true
	switch r, err := project.Find(s.Dir); {
	case errors.Is(err, project.ErrNotGit):
		repo = false
	case err != nil:
		return project.Failf("not in a git repository")
	default:
		root = r.Root
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
	s.hints = &hints
	defer func() { s.hints = nil }()
	var steps []step
	add := func(name, state, format string, a ...any) {
		steps = append(steps, step{name, state, fmt.Sprintf(format, a...)})
	}

	// project
	existed := s.configExists()
	if !existed && prefix == "" {
		prefix = uniquePrefix(defaultPrefix(nameOr(name, s.root)), running)
	}
	s.wrote = false
	if err := s.initProject(name, prefix); err != nil {
		return err
	}
	add("project", doneOr(s.wrote), "%s (prefix %s)", s.cfg.Name(), s.cfg.Prefix())

	// setup: models, effort and difficulty are a starting point until the lead confirms them with the owner (T-647).
	confirmed, err := s.initConfirmed()
	if err != nil {
		return err
	}
	add("setup", doneOr(confirmed || !existed), "%s", confirmedLine(s.cfg))

	// projects: the machine's list of obor projects by name (T-646): obor send <name> and obor projects read it.
	changed, err := project.Register(s.cfg.Root, s.Env)
	if err != nil {
		return project.Failf("the machine's project list: %v", err)
	}
	add("projects", doneOr(changed), "%s in %s", s.cfg.Name(), project.ProjectsFile(s.Env))

	// commit: a repository with no commit, empty before init, gets init's own files as its first (the lanes branch
	// from it).
	committed, err := s.initCommit(auto)
	if err != nil {
		return err
	}

	// githooks
	hooksChanged, err := s.githooksStep(nil)
	if err != nil {
		return err
	}
	add("githooks", doneOr(hooksChanged), "%s", hooksLine(s.root))

	// commands
	set, err := s.initCommands()
	if err != nil {
		return err
	}
	var skippedCommands bool
	switch {
	case len(set) > 0:
		add("commands", "done", "%s", strings.Join(set, "; "))
	case !s.anyCommand():
		skippedCommands = true
		add("commands", "skipped", "none detected (no Makefile check:, go.mod or package.json): set commands.check, "+
			"lane_setup, lane_ready and lane_links in .obor/config.json")
	default:
		add("commands", "already", "%s", s.commandsLine())
	}

	// kinds: each plugin kind's manifest cached (T-1054), before anything reads it: the guard, the agent starts of the
	// panes step, obor setup mcp
	kinds, err := s.cachePlugins()
	if err != nil {
		return err
	}
	if err := s.load(); err != nil {
		return err
	}
	if len(kinds) == 0 {
		add("kinds", "skipped", "no plugin kinds")
	} else {
		add("kinds", "done", "%s: manifest cached", strings.Join(kinds, ", "))
	}

	// lanes and role types
	types, err := s.initRoleTypes()
	if err != nil {
		return err
	}
	if err := s.initDifficulty(); err != nil {
		return err
	}
	var made, had []string
	// Two lanes: a task's Model:/Difficulty: picks the model per dispatch (T-724, T-765).
	for _, l := range []string{"L-01", "L-02"} {
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
		// The rerun's lane_setup must leave the lane ready: a lane the user is told "done" is one they can use (T-613).
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

	// plugin
	state, what, err := s.initPlugin(ctx, claude)
	if err != nil {
		return err
	}
	add("plugin", state, "%s", what)
	state, what, err = s.initPluginLinks()
	if err != nil {
		return err
	}
	add("plugin-links", state, "%s", what)

	// The obor.events plugin an older obor linked is gone (T-1117: serve restarts its own panes): one removal, best
	// effort, so a herdr that cannot list its plugins never fails the project's setup; its hint goes to the summary.
	if _, err := s.removeIntegration(ctx); err != nil {
		s.say("WARN events: %v", err)
	}

	// panes
	waiting, err := s.initPanes(ctx, add)
	if err != nil {
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
	s.say("commands.check is what every merge must pass, keep it to a few minutes; slower tests go in check_milestone " +
		"(the nightly), and obor nightly off if CI on main already does that")
	// T-1115: obor's lane and merge commits pass --no-verify (internal/checks/gitio.go:Commit), so an adopter's hooks
	// never run on them.
	s.say("obor commits with --no-verify: your pre-commit and commit-msg hooks do not run on them; put formatting " +
		"and secret scans in commands.check (the merge check)")
	s.say("change any of it in .obor/config.json (then obor init again, or obor setup lane / pane / role)")
	if len(waiting) > 0 {
		s.say("waiting on a \"Trust this folder?\" dialog: %s: press Enter in each, then obor init again",
			strings.Join(waiting, ", "))
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
	next := "obor lead"
	if skippedCommands {
		next = "first task for the lead: set up the stack and `commands.check` (lane_setup, lane_ready); until then " +
			"every gate stops on \"missing command\""
	}
	s.say("OK init %s (prefix %s); next: %s", s.cfg.Name(), s.cfg.Prefix(), next)
	s.say("")
	s.printTable()
	return nil
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

// uniquePrefix is p, else p2 … p9, the first no running agent's name starts with (<prefix>-): another project's
// agents keep theirs.
func uniquePrefix(p string, running []port.Pane) string {
	taken := func(c string) bool {
		for _, a := range running {
			if strings.HasPrefix(a.Name, c+"-") {
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

// detected is what detectCommands found: the commands by key ("": not found) and where from.
type detected struct {
	check, laneSetup, laneReady string
	from                        []string
}

var checkTarget = regexp.MustCompile(`(?m)^check\s*:`)

// detectCommands reads root: a Makefile with a check: target, go.mod, package.json (its lockfile picks the package
// manager). The first of the three gives check; package.json gives lane_setup and lane_ready.
func detectCommands(root string) detected {
	var d detected
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
		for _, script := range []string{"check", "test"} {
			if pkg.Scripts[script] != "" {
				d.check = pm + " run " + script
				break
			}
		}
	}
	d.laneSetup, d.laneReady = install, "test -d node_modules"
	return d
}

// initCommands writes the detected commands whose key is still null in .obor/config.json: "key = cmd" each.
func (s *Setup) initCommands() ([]string, error) {
	d := detectCommands(s.root)
	var set []string
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
			return nil, err
		}
		set = append(set, kv[0]+" = "+kv[1])
	}
	if len(set) > 0 {
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func (s *Setup) anyCommand() bool { return s.commandsLine() != "" }

// commandsLine is the configured check, lane_setup and lane_ready ("key = cmd; …").
func (s *Setup) commandsLine() string {
	var have []string
	for _, k := range []string{"check", "lane_setup", "lane_ready"} {
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
	if err != nil || writeJSON(path, w.Doc) != nil {
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
	for _, k := range agents.Names() {
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
// CLI is on PATH, else sets it to opus: "lead opus, integrator opus, reviewer sol" (a change written "reviewer sol →
// opus").
func (s *Setup) initRoleTypes() (string, error) {
	var parts []string
	changed := false
	for _, role := range []string{"lead", "integrator", "reviewer"} {
		rc, _ := s.cfg.Role(role)
		lt, _ := s.cfg.Model(rc.Type)
		if rc.Type != "" && lt.Kind != "" {
			if _, err := project.LookPath(s.Env.Get("PATH"), lt.Kind); err == nil {
				parts = append(parts, role+" "+config.ModelRef(rc.Type, rc.Effort))
				continue
			}
		}
		if err := s.editConfig(func(doc []byte) (config.Write, error) {
			return config.SetRoleModel(doc, role, "opus")
		}); err != nil {
			return "", err
		}
		was := config.ModelRef(rc.Type, rc.Effort)
		if was == "" {
			was = "unset"
		}
		parts = append(parts, role+" "+was+" → opus")
		changed = true
	}
	if changed {
		if err := s.load(); err != nil {
			return "", err
		}
	}
	return strings.Join(parts, ", "), nil
}

// initDifficulty writes `difficulty` from the kinds init detected (RV-88 #1): the built-in lists keep only the models
// of the Claude kind, which init requires on PATH. A non-Claude model (deepseek on OpenCode) joins a list only when the
// user lists it: init has no flag or prompt for that, so a machine without OpenCode never sends its first simple task
// to a CLI that cannot start. An existing `difficulty` is kept.
func (s *Setup) initDifficulty() error {
	if s.cfg.HasDifficulty() {
		return nil
	}
	out := map[string][]string{}
	for level, listed := range config.DifficultyDefault {
		for _, ref := range listed {
			typ, _ := config.ParseModelRef(ref)
			if lt, ok := s.cfg.Model(typ); ok && lt.Kind == agents.Default {
				out[level] = append(out[level], ref)
			}
		}
	}
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

// initPanes labels the panes of L-01, L-02, the integrator and the reviewer and starts no agent (T-708; each skipped when it runs): one summary line each
// through add, and the agents a trust dialog holds ("name (pane id)").
func (s *Setup) initPanes(ctx context.Context, add func(name, state, format string, a ...any)) ([]string, error) {
	var waiting []string
	place := func(what, agent string, f func(w where) error, w where) error {
		err := f(w)
		switch {
		case err != nil && strings.HasPrefix(err.Error(), "agent not ready"):
			waiting = append(waiting, agent+" (pane "+s.placed+")")
			add(what, "waiting", "%s in pane %s: %v", agent, s.placed, err)
		case err != nil:
			return err
		case s.running:
			add(what, "already", "%s running (pane %s)", agent, s.placed)
		case s.idle:
			add(what, "already", "pane %s labelled %s, no agent (started on first use)", s.placed, agent)
		case !s.started:
			add(what, "done", "pane %s labelled %s, no agent (started on first use)", s.placed, agent)
		default:
			add(what, "done", "%s started in pane %s", agent, s.placed)
		}
		return nil
	}
	lane := func(name string) func(where) error {
		return func(w where) error { return s.lanePane(ctx, name, w, false) }
	}
	role := func(name string) func(where) error {
		return func(w where) error { return s.rolePane(ctx, name, w, false) }
	}
	// The layout is the terminal's (T-1044): L-01 in a group of its own, L-02 and the integrator beside it (the
	// integrator by the caller's own pane when it has one), the reviewer in a group of its own.
	if err := place("L-01", s.laneWorker("L-01"), lane("L-01"), where{own: true}); err != nil {
		return nil, err
	}
	beside := where{beside: s.placed} // L-01's pane: every outcome of its step but an error placed one
	if err := place("L-02", s.laneWorker("L-02"), lane("L-02"), beside); err != nil {
		return nil, err
	}
	integrator := beside
	if _, err := project.Pane(s.Env); err == nil {
		integrator = where{} // the default placement: the caller's tab when it is the project's (T-628)
	}
	if err := place("integrator", s.cfg.AgentName("integrator", ""), role("integrator"), integrator); err != nil {
		return nil, err
	}
	if err := place("reviewer", s.cfg.AgentName("reviewer", ""), role("reviewer"), where{own: true}); err != nil {
		return nil, err
	}
	return waiting, nil
}

// laneWorker is the lane's worker agent name (derived).
func (s *Setup) laneWorker(lane string) string {
	return s.cfg.AgentName("worker", lane)
}

// --- plugin reaches the lanes (T-705) ----------------------------------------------------------------------------

// initPluginLinks makes the project's .claude/settings.json reach every lane: lanes are worktrees, so a file the
// repository does not track is not in them. Untracked → it joins lane_links (and .gitignore, which keeps git clean
// from removing the links) and is linked into the existing lanes; tracked or absent → nothing to do.
func (s *Setup) initPluginLinks() (state, what string, err error) {
	if !s.settingsUntracked() {
		return "already", pluginSettings + " is tracked or absent: lanes get it from git", nil
	}
	changed := false
	if !slices.Contains(s.cfg.LaneLinks(), pluginSettings) {
		links := append(slices.Clone(s.cfg.LaneLinks()), pluginSettings)
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
	was := s.wrote
	if err := s.gitignore(); err != nil {
		return "", "", err
	}
	changed = changed || s.wrote != was
	for _, l := range s.cfg.Lanes() {
		dst := filepath.Join(l.Dir, pluginSettings)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if _, err := os.Stat(l.Dir); err != nil {
			continue
		}
		if os.MkdirAll(filepath.Dir(dst), 0o755) != nil || os.Symlink(filepath.Join(s.root, pluginSettings), dst) != nil {
			return "", "", project.Failf("cannot link %s", dst)
		}
		s.say("link %s → %s", dst, filepath.Join(s.root, pluginSettings))
		changed = true
	}
	return doneOr(changed), pluginSettings + " is untracked: in lane_links, linked into the lanes (commit it to share it by git instead)", nil
}
