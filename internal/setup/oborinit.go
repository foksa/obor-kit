package setup

// obor init (P7-2, docs/plan/phase-7-advice.md §1): set a project up with no questions, by chaining what setup
// already does. Every step is idempotent, so a rerun after a fix is the repair path; the summary lists each step as
// done, already or skipped (why).
//
//	herdr      herdr unreachable → FAIL, before any write (nothing later works without it); no claude on PATH →
//	           FAIL (the lead runs it).
//	project    init's files (.obor/config.json, .gitignore lines, docs); a new config's prefix is made unique against
//	           the running herdr agents' names (<prefix>-…).
//	commands   detect: a Makefile with a check: target → make check; go.mod → go test ./...; package.json → its
//	           lockfile picks bun / pnpm / npm, the check script else the test script, lane_setup the install,
//	           lane_ready test -d node_modules. Only keys still null are written; nothing found → they stay null.
//	lanes      O-01 opus, S-01 sonnet (setup lane); roles.integrator and roles.reviewer keep their type when its
//	           kind's CLI is on PATH, else opus (the example's reviewer is a Codex type: no codex → opus).
//	plugin     claude plugin marketplace add <kit>, claude plugin install obor@<the kit's marketplace> --scope
//	           project (exit codes only, under a timeout); skipped when the project's settings enable it. Before the
//	           panes: an agent started without the plugin runs without obor's hooks and MCP tools.
//	events     obor.events linked in herdr (setup herdr-plugin, no question).
//	panes      O-01 in a new tab "workers", S-01 and the integrator beside it (the integrator in the caller's tab when
//	           run in a herdr pane), the reviewer in a new tab "review". A pane held by a trust dialog is listed, not
//	           fatal: Enter there, then obor init again.
//	serve      obor serve install (Setup.Serve; off darwin it says to run obor serve yourself).

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
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app"
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
	if err := s.findRoot(); err != nil {
		return err
	}
	running, err := s.H.Agents(ctx)
	if err != nil {
		return project.Failf("herdr is not reachable (%v): start herdr, then obor init again", err)
	}
	claude, err := lookPath(s.Env.Get("PATH"), agents.Default)
	if err != nil {
		return project.Failf("%s is not on PATH (the lead runs it): install it, then obor init again", agents.Default)
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
	add("project", doneOr(s.wrote), "%s (prefix %s)", s.cfg.Data.Name, s.cfg.Data.Prefix)

	// commands
	set, err := s.initCommands()
	if err != nil {
		return err
	}
	switch {
	case len(set) > 0:
		add("commands", "done", "%s", strings.Join(set, "; "))
	case !s.anyCommand():
		add("commands", "skipped", "none detected (no Makefile check:, go.mod or package.json): set commands.check, "+
			"lane_setup, lane_ready and lane_links in .obor/config.json")
	default:
		add("commands", "already", "%s", s.commandsLine())
	}

	// lanes and role types
	types, err := s.initRoleTypes()
	if err != nil {
		return err
	}
	var made, had []string
	for _, l := range [][2]string{{"O-01", "opus"}, {"S-01", "sonnet"}} {
		// A configured lane keeps its type (the user may have changed it): only a missing lane gets the default.
		if have := s.cfg.Data.Lanes.M[l[0]].Type; have != "" {
			l[1] = have
			if _, err := os.Stat(filepath.Join(s.cfg.LanesDir(), l[0])); err == nil {
				had = append(had, l[0]+" "+l[1])
				continue
			}
		}
		if err := s.lane(ctx, l[:]); err != nil {
			return err
		}
		made = append(made, l[0]+" "+l[1])
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

	// events
	linked := s.eventsLinked(ctx)
	if err := s.events(ctx); err != nil {
		return err
	}
	add("events", doneOr(!linked), "obor.events linked in herdr (user-global)")

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
	s.say("obor init %s:", s.cfg.Data.Name)
	for _, st := range steps {
		s.say("  %-10s %-8s %s", st.name, st.state, st.what)
	}
	s.say("detected: %s", s.detectedLine())
	s.say("change any of it in .obor/config.json (then obor init again, or obor setup lane / pane / role)")
	if len(waiting) > 0 {
		s.say("waiting on a \"Trust this folder?\" dialog: %s: press Enter in each, then obor init again",
			strings.Join(waiting, ", "))
	}
	for _, h := range hints {
		s.say("%s", h)
	}
	s.say("nothing committed: review, then commit .obor/config.json, .gitignore, BACKLOG.md, docs/decisions.md, %s/",
		s.agentsDir())
	s.say("OK init %s (prefix %s); next: obor lead", s.cfg.Data.Name, s.cfg.Data.Prefix)
	return nil
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
func uniquePrefix(p string, running []app.AgentInfo) string {
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
		if err := s.editConfig([]string{"commands", kv[0]}, kv[1]); err != nil {
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

// editConfig sets key path to val in .obor/config.json (not local.json), uncommitted.
func (s *Setup) editConfig(key []string, val any) error {
	path := filepath.Join(s.root, ".obor", "config.json")
	doc, err := os.ReadFile(path)
	if err != nil {
		return project.Failf("cannot read %s", path)
	}
	doc, err = config.EditJSON(doc, key, val)
	if err != nil || writeJSON(path, doc) != nil {
		return project.Failf("cannot update %s", path)
	}
	s.say("config %s = %v (uncommitted)", strings.Join(key, "."), val)
	return nil
}

// detectedLine is the summary's "detected": the CLIs on PATH and the files the commands came from.
func (s *Setup) detectedLine() string {
	var clis []string
	for _, k := range agents.Names() {
		if _, err := lookPath(s.Env.Get("PATH"), k); err == nil {
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

// initRoleTypes keeps the integrator's and the reviewer's type when it is a lane type whose kind's CLI is on PATH,
// else sets it to opus: "integrator opus, reviewer sol" (a change written "reviewer sol → opus").
func (s *Setup) initRoleTypes() (string, error) {
	var parts []string
	changed := false
	for _, role := range []string{"integrator", "reviewer"} {
		rc, _ := s.cfg.Role(role)
		lt, _ := s.cfg.LaneType(rc.Type)
		if rc.Type != "" && lt.Kind != "" {
			if _, err := lookPath(s.Env.Get("PATH"), lt.Kind); err == nil {
				parts = append(parts, role+" "+rc.Type)
				continue
			}
		}
		if err := s.editConfig([]string{"roles", role, "type"}, "opus"); err != nil {
			return "", err
		}
		was := rc.Type
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
	cleanup := run.Group(cmd)
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

// --- events -----------------------------------------------------------------------------------------------------

// eventsLinked: herdr runs this kit's obor.events.
func (s *Setup) eventsLinked(ctx context.Context) bool {
	plugins, err := s.H.Plugins(ctx)
	if err != nil {
		return false
	}
	mine := project.Physical(filepath.Join(s.Kit, "herdr-plugin"))
	for _, p := range plugins {
		if p.ID == "obor.events" && (p.Root == "" || project.Physical(p.Root) == mine) {
			return true
		}
	}
	return false
}

// --- panes ------------------------------------------------------------------------------------------------------

// initPanes starts O-01, S-01, the integrator and the reviewer (each skipped when it runs): one summary line each
// through add, and the agents a trust dialog holds ("name (pane id)").
func (s *Setup) initPanes(ctx context.Context, add func(name, state, format string, a ...any)) ([]string, error) {
	var waiting []string
	place := func(what, agent string, f func(w []string) error, w []string) error {
		err := f(w)
		switch {
		case err != nil && strings.HasPrefix(err.Error(), "agent not ready"):
			waiting = append(waiting, agent+" (pane "+s.placed+")")
			add(what, "waiting", "%s in pane %s: %v", agent, s.placed, err)
		case err != nil:
			return err
		case s.running:
			add(what, "already", "%s running (pane %s)", agent, s.placed)
		default:
			add(what, "done", "%s started in pane %s", agent, s.placed)
		}
		return nil
	}
	lane := func(name string) func([]string) error {
		return func(w []string) error { return s.pane(ctx, append([]string{name}, w...)) }
	}
	role := func(name string) func([]string) error {
		return func(w []string) error { return s.role(ctx, append([]string{name}, w...)) }
	}
	if err := place("O-01", s.laneAgent("O-01"), lane("O-01"), []string{"--new-tab", "workers"}); err != nil {
		return nil, err
	}
	tab := s.tabOf(ctx, s.placed)
	beside := []string{"--new-tab", "workers"}
	if tab != "" {
		beside = []string{"--tab", tab}
	}
	if err := place("S-01", s.laneAgent("S-01"), lane("S-01"), beside); err != nil {
		return nil, err
	}
	integrator := beside
	if s.Env.Get("HERDR_PANE_ID") != "" {
		integrator = nil // the caller's tab
	}
	if err := place("integrator", s.cfg.AgentName("integrator", ""), role("integrator"), integrator); err != nil {
		return nil, err
	}
	if err := place("reviewer", s.cfg.AgentName("reviewer", ""), role("reviewer"), []string{"--new-tab", "review"}); err != nil {
		return nil, err
	}
	return waiting, nil
}

// laneAgent is the lane's worker agent name.
func (s *Setup) laneAgent(lane string) string {
	if a := s.cfg.Data.Lanes.M[lane].Agent; a != "" {
		return a
	}
	return s.cfg.AgentName("worker", lane)
}

// tabOf is the tab of pane ("": unknown).
func (s *Setup) tabOf(ctx context.Context, pane string) string {
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return ""
	}
	for _, p := range panes {
		if p.ID == pane {
			return p.Tab
		}
	}
	return ""
}
