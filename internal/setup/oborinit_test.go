package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
)

// initFx is a bare project (no config) for obor init: a fake `claude` (and, with codex, a fake `codex`) first on the
// runs' PATH, a herdr with no pane, and a Serve that counts its calls. The fake claude logs its argv to claude.log; `plugin install <p>`
// enables <p> in the project's .claude/settings.json unless <bin>/fail exists (exit 7).
type initFx struct {
	*fx
	bin, log string
	serves   int
}

func newInitFx(t *testing.T, codex bool) *initFx {
	t.Helper()
	x := &initFx{fx: bare(t)}
	x.h.Panes = nil // no tab of its own: the fake numbers new tabs t1, t2 … (the caller's tab is none)
	x.bin = filepath.Join(x.Base, "fakebin")
	x.log = filepath.Join(x.bin, "claude.log")
	script := `#!/bin/sh
d=${0%/*}
echo "$*" >> "$d/claude.log"
case "$*" in
"plugin install "*)
	[ -e "$d/fail" ] && exit 7
	mkdir -p .claude
	printf '{"enabledPlugins":{"%s":true}}' "$3" > .claude/settings.json ;;
esac
`
	if err := os.MkdirAll(x.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	x.Write(filepath.Join(x.bin, "claude"), script)
	os.Chmod(filepath.Join(x.bin, "claude"), 0o755)
	if codex {
		x.Write(filepath.Join(x.bin, "codex"), "#!/bin/sh\nexit 0\n")
		os.Chmod(filepath.Join(x.bin, "codex"), 0o755)
	}
	x.env = append(x.env, "PATH="+x.bin+":/usr/bin:/bin")
	x.serve = func(context.Context, *config.Config) (string, string, error) {
		x.serves++
		if x.serves > 1 {
			return "already", "obor.serve.proj installed and loaded", nil
		}
		return "done", "INSTALLED obor.serve.proj", nil
	}
	return x
}

func (x *initFx) init(t *testing.T, args ...string) string {
	t.Helper()
	return x.ok(t, append([]string{"obor-init"}, args...)...)
}

func (x *initFx) claudeCalls() string { b, _ := os.ReadFile(x.log); return string(b) }

// summary is the step line of name in out ("" when none): "  name  state  what".
func summary(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 1 && strings.HasPrefix(l, "  ") && f[0] == name {
			return strings.Join(f[1:], " ")
		}
	}
	return ""
}

func stateOf(out, name string) string {
	f := strings.Fields(summary(out, name))
	return append(f, "")[0]
}

func (x *initFx) agents(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, p := range x.h.Panes {
		if p.Name != "" {
			got[p.Name] = p.Tab
		}
	}
	return got
}

// One run sets the whole project up: config (name, prefix), O-01 opus and S-01 sonnet with worktrees, the
// integrator opus and (codex on PATH) the example's Codex reviewer, the plugin installed from the kit's marketplace,
// obor.events linked, four agents in their tabs, serve installed; the summary says each step done, then next: obor
// lead.
func TestOborInitSetsUpTheProject(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	if x.Exists(".obor/config.json") || len(x.h.Plugins) != 0 {
		t.Fatal("precondition: no config, no herdr plugin")
	}
	out := x.init(t)
	if last(out) != "OK init proj (prefix proj); next: obor lead" {
		t.Errorf("last line %q\n%s", last(out), out)
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Name != "proj" || cfg.Data.Prefix != "proj" {
		t.Errorf("name %q prefix %q", cfg.Data.Name, cfg.Data.Prefix)
	}
	for lane, typ := range map[string]string{"O-01": "opus", "S-01": "sonnet"} {
		if got := cfg.Data.Lanes.M[lane].Type; got != typ {
			t.Errorf("lanes.%s = %q, want %s", lane, got, typ)
		}
		if _, err := os.Stat(filepath.Join(cfg.LanesDir(), lane, ".git")); err != nil {
			t.Errorf("no worktree for %s: %v", lane, err)
		}
	}
	if rc, _ := cfg.Role("integrator"); rc.Type != "opus" {
		t.Errorf("integrator type %q", rc.Type)
	}
	if rc, _ := cfg.Role("reviewer"); rc.Type != "sol" {
		t.Errorf("reviewer type %q, want the example's sol (codex on PATH)", rc.Type)
	}
	mkt, err := kitMarketplace(kit)
	if err != nil {
		t.Fatal(err)
	}
	want := "plugin marketplace add " + kit + "\nplugin install obor@" + mkt + " --scope project\n"
	if got := x.claudeCalls(); got != want {
		t.Errorf("claude calls:\n%s\nwant:\n%s", got, want)
	}
	if len(x.h.Plugins) != 1 || x.h.Plugins[0].ID != "obor.events" {
		t.Errorf("herdr plugins: %+v", x.h.Plugins)
	}
	// The fake numbers a new tab's pane n<k> and the tab t<k>.
	panes := map[string]string{}
	for _, p := range x.h.Panes {
		if p.Name != "" {
			panes[p.Name] = p.ID + " " + p.Tab
		}
	}
	o, s := panes["proj-o-01"], panes["proj-s-01"]
	i, r := panes[cfg.AgentName("integrator", "")], panes[cfg.AgentName("reviewer", "")]
	tab := func(v string) string { return append(strings.Fields(v), "", "")[1] }
	if len(panes) != 4 || !strings.HasPrefix(o, "w1:n") || !strings.HasPrefix(r, "w1:n") || tab(s) != tab(o) ||
		tab(i) != tab(o) || tab(r) == tab(o) {
		t.Errorf("agents (O-01 in a new tab, S-01 and the integrator beside it, the reviewer in a new tab): %v", panes)
	}
	var tabs []string
	for _, q := range x.reqs() {
		if strings.HasPrefix(q, "tab.create") {
			tabs = append(tabs, q)
		}
	}
	if len(tabs) != 2 || !strings.Contains(tabs[0], "label=workers") || !strings.Contains(tabs[1], "label=review") {
		t.Errorf("tabs created: %q", tabs)
	}
	if n := strings.Count(strings.Join(x.reqs(), "\n"), "plugin.list"); n != 1 {
		t.Errorf("plugin.list read %d times, want once for the events step (T-613)", n)
	}
	if x.serves != 1 {
		t.Errorf("serve install called %d times", x.serves)
	}
	for _, st := range []string{"project", "lanes", "plugin", "events", "O-01", "S-01", "integrator", "reviewer", "serve"} {
		if stateOf(out, st) != "done" {
			t.Errorf("step %s: %q, want done\n%s", st, summary(out, st), out)
		}
	}
	if stateOf(out, "commands") != "skipped" {
		t.Errorf("commands with no build files: %q", summary(out, "commands"))
	}
	has(t, out, "detected: claude, codex on PATH; no build files", "detected line")
	has(t, out, "change any of it in .obor/config.json", "the file to change")
	has(t, out, "nothing committed: review, then commit .obor/config.json, .gitignore, BACKLOG.md, docs/decisions.md, "+
		"docs/agents/", "the files to commit named")
	has(t, out, "hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false", "herdr hint")
	if n := strings.Count(out, "resume_agents_on_restore"); n != 1 {
		t.Errorf("the herdr hint %d times (once, in the summary)", n)
	}
	hasNot(t, out, "waiting on", "no trust dialog")
}

// A rerun is the repair path: no new lane, pane, plugin call or herdr link; every step says already.
func TestOborInitRerunChangesNothing(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.init(t)
	cfg := x.Read(".obor/config.json")
	calls, agents, wt := x.claudeCalls(), x.agents(t), x.Git(x.Root, "worktree", "list")
	x.reset()
	out := x.init(t)
	if x.Read(".obor/config.json") != cfg {
		t.Errorf("config changed on a rerun")
	}
	if x.claudeCalls() != calls {
		t.Errorf("plugin calls on a rerun:\n%s", x.claudeCalls())
	}
	if got := x.agents(t); len(got) != len(agents) {
		t.Errorf("agents %v, were %v", got, agents)
	}
	if got := x.Git(x.Root, "worktree", "list"); got != wt {
		t.Errorf("worktrees:\n%s\nwere:\n%s", got, wt)
	}
	for _, r := range x.reqs() {
		for _, m := range []string{"agent.start", "pane.split", "tab.create", "plugin.link", "plugin.unlink"} {
			if strings.HasPrefix(r, m) {
				t.Errorf("rerun sent %s", r)
			}
		}
	}
	for _, st := range []string{"project", "lanes", "plugin", "events", "O-01", "S-01", "integrator", "reviewer", "serve"} {
		if stateOf(out, st) != "already" {
			t.Errorf("rerun step %s: %q, want already\n%s", st, summary(out, st), out)
		}
	}
	if x.serves != 2 {
		t.Errorf("serve asked %d times (each run asks; it says already)", x.serves)
	}
}

// A lane whose type the user changed keeps it on a rerun (the summary's "change it, then obor init again"): exit 0,
// lanes already, no new lane or pane; a configured lane whose worktree is gone gets it back, with its own type.
func TestOborInitRerunKeepsAChangedLaneType(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.init(t)
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"lanes", "O-01"}, "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	agents, wt := x.agents(t), x.Git(x.Root, "worktree", "list")
	x.reset()
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil || cfg.Data.Lanes.M["O-01"].Type != "sonnet" {
		t.Fatalf("O-01 type after the rerun: %q (%v)", cfg.Data.Lanes.M["O-01"].Type, err)
	}
	if stateOf(out, "lanes") != "already" || !strings.Contains(summary(out, "lanes"), "O-01 sonnet") {
		t.Errorf("lanes step %q", summary(out, "lanes"))
	}
	if got := x.agents(t); len(got) != len(agents) {
		t.Errorf("agents %v, were %v", got, agents)
	}
	if got := x.Git(x.Root, "worktree", "list"); got != wt {
		t.Errorf("worktrees:\n%s\nwere:\n%s", got, wt)
	}
	for _, r := range x.reqs() {
		if strings.HasPrefix(r, "agent.start") || strings.HasPrefix(r, "tab.create") || strings.HasPrefix(r, "pane.split") {
			t.Errorf("rerun sent %s", r)
		}
	}
	x.Git(x.Root, "worktree", "remove", "--force", filepath.Join(cfg.LanesDir(), "O-01"))
	out = x.init(t)
	if _, err := os.Stat(filepath.Join(cfg.LanesDir(), "O-01", ".git")); err != nil || stateOf(out, "lanes") != "done" {
		t.Errorf("worktree not recreated (%v), lanes %q", err, summary(out, "lanes"))
	}
	if cfg, _ := config.Load(x.Root); cfg.Data.Lanes.M["O-01"].Type != "sonnet" {
		t.Errorf("recreated lane's type %q", cfg.Data.Lanes.M["O-01"].Type)
	}
}

// T-613 #3: obor init's rerun is the repair path for a lane, and the last run's outcome decides: a failed lane_setup
// runs again, a finished one is skipped, and a lane dir that is no worktree of the repo is a FAIL (never a skip).
func TestOborInitLaneSetupOnARerun(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		run  func(t *testing.T, x *initFx)
	}{
		{"lane_setup failed last run: the rerun runs it", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"commands", "lane_setup"}, "exit 3")
			x.failing(t, "FAIL lane_setup exited 3 in "+x.LaneDir("O-01")+": exit 3", "obor-init")
			for _, lane := range []string{"O-01", "S-01"} {
				if x.Exists(".obor/state/lanes/" + lane + ".setup") {
					t.Errorf("%s: a failed lane_setup left the done marker", lane)
				}
			}
			x.editConfig(t, []string{"commands", "lane_setup"}, "pwd > setup-ran")
			out := x.init(t)
			for _, lane := range []string{"O-01", "S-01"} {
				if got, want := x.Read(x.LaneDir(lane)+"/setup-ran"), x.LaneDir(lane)+"\n"; got != want {
					t.Errorf("%s: the rerun's lane_setup wrote %q, want %q", lane, got, want)
				}
				if !x.Exists(".obor/state/lanes/" + lane + ".setup") {
					t.Errorf("%s: no done marker after its lane_setup finished", lane)
				}
			}
			if stateOf(out, "lanes") != "done" {
				t.Errorf("lanes step %q", summary(out, "lanes"))
			}
		}},
		{"lane_setup finished last run: the rerun skips it", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"commands", "lane_setup"}, "pwd > setup-ran")
			x.init(t)
			for _, lane := range []string{"O-01", "S-01"} {
				if !x.Exists(".obor/state/lanes/" + lane + ".setup") {
					t.Fatalf("precondition: %s's lane_setup ran and left its marker", lane)
				}
				os.Remove(filepath.Join(x.LaneDir(lane), "setup-ran"))
			}
			out := x.init(t)
			for _, lane := range []string{"O-01", "S-01"} {
				if x.Exists(x.LaneDir(lane) + "/setup-ran") {
					t.Errorf("%s: the rerun ran lane_setup again", lane)
				}
			}
			if stateOf(out, "lanes") != "already" {
				t.Errorf("lanes step %q", summary(out, "lanes"))
			}
		}},
		{"a lane dir that is no worktree of the repo: FAIL", func(t *testing.T, x *initFx) {
			dir := x.LaneDir("O-01")
			x.Git(x.Root, "worktree", "remove", "--force", dir)
			os.MkdirAll(dir, 0o755)
			x.Write(dir+"/keep", "a plain directory, not a worktree\n")
			x.failing(t, "FAIL "+dir+" exists and is not a worktree of "+x.Root, "obor-init")
			if !x.Exists(dir + "/keep") {
				t.Error("the directory was touched")
			}
		}},
		{"lane_ready fails with no lane_setup to run: FAIL", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"commands", "lane_ready"}, "false")
			x.failing(t, "FAIL lane O-01: lane_ready fails (false) and commands.lane_setup is not set", "obor-init")
		}},
		{"lane_ready still fails after lane_setup: FAIL, never done", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"commands", "lane_ready"}, "echo not ready; exit 1")
			x.editConfig(t, []string{"commands", "lane_setup"}, "true")
			out, rc := x.run(t, "obor-init")
			want := "FAIL lane O-01: lane_ready still fails after lane_setup (echo not ready; exit 1): not ready"
			if rc != 1 || last(out) != want {
				t.Fatalf("exit %d, last line %q, want %q\n%s", rc, last(out), want, out)
			}
			if strings.Contains(out, "OK init") || stateOf(out, "lanes") == "done" {
				t.Errorf("a lane that is still not ready was reported done:\n%s", out)
			}
		}},
		{"lane_ready passes after lane_setup: done", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"commands", "lane_ready"}, "test -f ready")
			x.editConfig(t, []string{"commands", "lane_setup"}, "touch ready")
			out := x.init(t)
			for _, lane := range []string{"O-01", "S-01"} {
				if !x.Exists(x.LaneDir(lane) + "/ready") {
					t.Errorf("%s: the rerun's lane_setup did not run", lane)
				}
			}
			if stateOf(out, "lanes") != "done" {
				t.Errorf("lanes step %q", summary(out, "lanes"))
			}
			if out := x.init(t); stateOf(out, "lanes") != "already" {
				t.Errorf("rerun after it was ready: lanes step %q", summary(out, "lanes"))
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newInitFx(t, true)
			x.init(t)
			c.run(t, x)
		})
	}
}

// editConfig sets key to val in the project's .obor/config.json: the fix a user makes, then runs obor init again.
func (x *initFx) editConfig(t *testing.T, key []string, val any) {
	t.Helper()
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), key, val)
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
}

// T-613 #4: herdr cannot list its plugins: the events step fails with herdr's error (never "linked"), it reads the list
// once, and init stops before the panes.
func TestOborInitFailsWhenHerdrCannotListPlugins(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.srv.Fail("plugin.list", "internal", "boom")
	x.failing(t, "FAIL cannot read herdr's plugin list: herdr plugin.list: failed: boom (internal)", "obor-init")
	var lists int
	for _, r := range x.reqs() {
		if strings.HasPrefix(r, "plugin.list") {
			lists++
		}
		for _, m := range []string{"agent.start", "pane.split", "tab.create", "plugin.link", "plugin.unlink"} {
			if strings.HasPrefix(r, m) {
				t.Errorf("init sent %s after the FAIL", r)
			}
		}
	}
	if lists != 1 {
		t.Errorf("plugin.list read %d times, want once", lists)
	}
	if len(x.h.Plugins) != 0 || x.serves != 0 {
		t.Errorf("plugins %v, serve installs %d after the FAIL", x.h.Plugins, x.serves)
	}
}

// herdr down: FAIL first, before any file is written or claude is called.
func TestOborInitFailsFirstWhenHerdrIsDown(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.srv.Die()
	out, rc := x.run(t, "obor-init")
	if rc != 1 || !strings.HasPrefix(last(out), "FAIL herdr is not reachable (") ||
		!strings.HasSuffix(last(out), "): start herdr, then obor init again") {
		t.Fatalf("exit %d, last line %q\n%s", rc, last(out), out)
	}
	if x.Exists(".obor/config.json") || x.Exists("BACKLOG.md") || x.claudeCalls() != "" {
		t.Errorf("wrote or called claude before the FAIL:\n%s", out)
	}
}

// no claude on PATH: FAIL (the lead needs it), nothing written.
func TestOborInitFailsWithoutClaude(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	os.Remove(filepath.Join(x.bin, "claude"))
	x.failing(t, "FAIL claude is not on PATH (the lead runs it): install it, then obor init again", "obor-init")
	if x.Exists(".obor/config.json") || x.Exists("BACKLOG.md") || len(x.h.Plugins) != 0 {
		t.Errorf("wrote before the FAIL")
	}
}

// no codex on PATH: the reviewer is opus (the example's reviewer is a Codex type), the summary says so.
func TestOborInitReviewerOpusWithoutCodex(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, false)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if rc, _ := cfg.Role("reviewer"); rc.Type != "opus" {
		t.Errorf("reviewer type %q, want opus", rc.Type)
	}
	has(t, summary(out, "lanes"), "reviewer sol → opus", "lanes step")
	if a := x.h.Pane(x.paneOf(cfg.AgentName("reviewer", ""))); a == nil || a.Kind != "claude" {
		t.Errorf("reviewer agent: %+v", a)
	}
	has(t, out, "detected: claude on PATH;", "detected line")
}

func (x *initFx) paneOf(name string) string {
	for _, p := range x.h.Panes {
		if p.Name == name {
			return p.ID
		}
	}
	return ""
}

// A new config's prefix steers clear of another project's running agents (<prefix>-…); an explicit one is kept.
func TestOborInitPrefixUniqueAgainstRunningAgents(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.h.Agent("w9:a1", "proj-lead", "idle", "/elsewhere")
	x.h.Agent("w9:a2", "proj2-lead", "idle", "/elsewhere2")
	x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil || cfg.Data.Prefix != "proj3" {
		t.Fatalf("prefix %q (%v), want proj3", cfg.Data.Prefix, err)
	}
	y := newInitFx(t, true)
	y.h.Agent("w9:a1", "proj-lead", "idle", "/elsewhere")
	y.init(t, "--prefix", "proj")
	if cfg, _ := config.Load(y.Root); cfg.Data.Prefix != "proj" {
		t.Errorf("explicit prefix %q", cfg.Data.Prefix)
	}
}

// A failed plugin install is a FAIL that says to fix it and rerun; the rerun then installs it.
func TestOborInitPluginFailureIsRepairedByARerun(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.Write(filepath.Join(x.bin, "fail"), "")
	out, rc := x.run(t, "obor-init")
	if rc != 1 || !strings.Contains(last(out), "FAIL claude plugin install obor@") || !strings.Contains(last(out), "(exit 7)") {
		t.Fatalf("exit %d, last %q", rc, last(out))
	}
	if len(x.agents(t)) != 0 {
		t.Errorf("agents started without the plugin: %v", x.agents(t))
	}
	os.Remove(filepath.Join(x.bin, "fail"))
	out = x.init(t)
	if stateOf(out, "plugin") != "done" || stateOf(out, "lanes") != "already" {
		t.Errorf("rerun: plugin %q, lanes %q", summary(out, "plugin"), summary(out, "lanes"))
	}
}

// A pane held by the trust dialog is listed in the summary and is not fatal.
func TestOborInitListsPanesWaitingOnTheTrustDialog(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.screen = "> You are in /x\n  Do you trust the contents of this directory?\n  Trust this folder? 1. Yes  2. No\n"
	x.srv.Fail("agent.start", "agent_not_ready", "blocked during startup")
	out := x.init(t)
	if stateOf(out, "O-01") != "waiting" || stateOf(out, "reviewer") != "waiting" {
		t.Errorf("O-01 %q, reviewer %q", summary(out, "O-01"), summary(out, "reviewer"))
	}
	has(t, out, "waiting on a \"Trust this folder?\" dialog: proj-o-01 (pane ", "waiting line")
	has(t, out, "press Enter in each, then obor init again", "waiting line")
}

// Detected commands are written into the config where it has none, never over a value of the user's.
func TestOborInitWritesDetectedCommandsOnce(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.Write("go.mod", "module x\n")
	out := x.init(t)
	cfg, _ := config.Load(x.Root)
	if c := cfg.Data.Commands.M["check"]; c.Cmd != "go test ./..." {
		t.Errorf("commands.check %+v", c)
	}
	if stateOf(out, "commands") != "done" {
		t.Errorf("commands step %q", summary(out, "commands"))
	}
	has(t, out, "go.mod", "detected line")
	doc, _ := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"commands", "check"}, "make mine")
	x.Write(".obor/config.json", string(doc))
	out = x.init(t)
	cfg, _ = config.Load(x.Root)
	if c := cfg.Data.Commands.M["check"]; c.Cmd != "make mine" {
		t.Errorf("the user's check overwritten: %+v", c)
	}
	if stateOf(out, "commands") != "already" {
		t.Errorf("commands step %q", summary(out, "commands"))
	}
}

// One row per detection: a Makefile check: target, go.mod, package.json with each lockfile and script, nothing.
func TestDetectCommands(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name             string
		files            map[string]string
		check, laneSetup string
	}{
		{"makefile check", map[string]string{"Makefile": "build:\n\tgo build\ncheck: build\n\tgo test\n", "go.mod": "module x\n"}, "make check", ""},
		{"makefile without check", map[string]string{"Makefile": "build:\n\tgo build\nprecheck:\n"}, "", ""},
		{"go.mod", map[string]string{"go.mod": "module x\n"}, "go test ./...", ""},
		{"bun.lockb", map[string]string{"package.json": `{"scripts":{"check":"tsc","test":"x"}}`, "bun.lockb": ""}, "bun run check", "bun install --frozen-lockfile"},
		{"bun.lock", map[string]string{"package.json": `{"scripts":{"test":"x"}}`, "bun.lock": ""}, "bun run test", "bun install --frozen-lockfile"},
		{"pnpm", map[string]string{"package.json": `{"scripts":{"test":"x"}}`, "pnpm-lock.yaml": ""}, "pnpm run test", "pnpm install --frozen-lockfile"},
		{"npm lock", map[string]string{"package.json": `{"scripts":{"check":"x"}}`, "package-lock.json": "{}"}, "npm run check", "npm ci"},
		{"no lockfile, no script", map[string]string{"package.json": `{}`}, "", "npm install"},
		{"makefile and package.json", map[string]string{"Makefile": "check:\n", "package.json": `{"scripts":{"test":"x"}}`, "pnpm-lock.yaml": ""}, "make check", "pnpm install --frozen-lockfile"},
		{"nothing", map[string]string{"README.md": "hi"}, "", ""},
	} {
		dir := t.TempDir()
		for f, body := range c.files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		d := detectCommands(dir)
		ready := ""
		if c.laneSetup != "" {
			ready = "test -d node_modules"
		}
		if d.check != c.check || d.laneSetup != c.laneSetup || d.laneReady != ready {
			t.Errorf("%s: check %q lane_setup %q lane_ready %q, want %q %q %q", c.name, d.check, d.laneSetup, d.laneReady,
				c.check, c.laneSetup, ready)
		}
		if (len(d.from) == 0) != (c.name == "nothing" || c.name == "makefile without check") {
			t.Errorf("%s: from %v", c.name, d.from)
		}
	}
}
