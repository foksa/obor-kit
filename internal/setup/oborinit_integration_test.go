//go:build integration

// The integration tier of obor init: init makes the repository (git init, the "obor init" commit), installs
// core.hooksPath and adds the lane worktrees with git, so these run on a git project (newGitInitFx).

package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
)

// obor init writes setup.confirmed: false when the config has no such key, and a rerun keeps the owner's true.
func TestOborInitRecordsTheSetupAnswer(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Agents.Confirmed == nil || *cfg.Data.Agents.Confirmed {
		t.Fatalf("a new project's setup.confirmed = %v, want false", cfg.Data.Agents.Confirmed)
	}
	if stateOf(out, "setup") != "done" || !strings.Contains(summary(out, "setup"), "the lead asks the owner") {
		t.Errorf("setup step %q", summary(out, "setup"))
	}
	// The owner answers at the first session: a rerun keeps that answer.
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"agents", "confirmed"}, true)
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	out = x.init(t)
	if cfg, err = config.Load(x.Root); err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Agents.Confirmed == nil || !*cfg.Data.Agents.Confirmed {
		t.Errorf("a rerun changed setup.confirmed to %v, want the owner's true", cfg.Data.Agents.Confirmed)
	}
	if stateOf(out, "setup") != "already" || !strings.HasSuffix(summary(out, "setup"), "confirmed by the owner") {
		t.Errorf("setup step after the answer: %q", summary(out, "setup"))
	}
}

// an existing config without the setup key (written before the table) gets confirmed: false on init: nothing
// there says the owner saw the table.
func TestOborInitAddsTheMissingSetupAnswer(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	x.init(t)
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"agents", "confirmed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Agents.Confirmed == nil || *cfg.Data.Agents.Confirmed {
		t.Errorf("setup.confirmed = %v, want false (init restores it when the key is absent)", cfg.Data.Agents.Confirmed)
	}
	if stateOf(out, "setup") != "done" {
		t.Errorf("setup step %q", summary(out, "setup"))
	}
}

// obor init's output ends with the setup table, after the OK line; no deepseek (OpenCode is not opted in).
func TestOborInitEndsWithTheSetupTable(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	out := x.init(t)
	tail := strings.Join([]string{
		"",
		"models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch its first free model):",
		"  hard   → opus:high (claude opus, effort high)",
		"  medium → sonnet (claude sonnet)",
		"  simple → sonnet (claude sonnet) | haiku (claude haiku)",
		"lanes: 2 (L-01, L-02; each runs the model its dispatch picks)",
		"roles: lead opus:high (claude opus, effort high), integrator opus (claude opus), reviewer sol:high (codex gpt-6.1-sol, effort high)",
		"confirmed: no",
		"starting point: the lead goes through it with you at its first session",
		"",
	}, "\n")
	if !strings.HasSuffix(out, tail) {
		t.Errorf("obor init's output does not end with the setup table:\n%s", out)
	}
	if i, j := strings.Index(out, "OK init "), strings.Index(out, "models and effort"); i < 0 || j < i {
		t.Errorf("the OK line does not come before the table:\n%s", out)
	}
}

// One run sets the whole project up: config (name, prefix), L-01 opus and L-02 sonnet with worktrees, the lead and the
// integrator opus and (codex on PATH) the example's Codex reviewer, the plugin installed from the kit's marketplace,
// no herdr plugin linked, four agents in their tabs, serve installed; the summary says each step done, then
// next: obor lead.
func TestOborInitSetsUpTheProject(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	if x.Exists(".obor/config.json") || len(x.h.Plugins) != 0 {
		t.Fatal("precondition: no config, no herdr plugin")
	}
	out := x.init(t)
	const ok = "OK init proj (prefix proj); next: first task for the lead: set up the stack and `commands.check` " +
		"(lane_setup, lane_ready); until then every gate stops on \"missing command\""
	if !strings.Contains(out, ok+"\n") {
		t.Errorf("no OK line %q in\n%s", ok, out)
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Project.Name != "proj" || cfg.Data.Project.AgentPrefix != "proj" {
		t.Errorf("name %q prefix %q", cfg.Data.Project.Name, cfg.Data.Project.AgentPrefix)
	}
	if cfg.LaneCount() != 2 {
		t.Errorf("lanes = %d, want 2 (T-765)", cfg.LaneCount())
	}
	// init writes config shape 2: no lane types, a lanes count, roles as model refs.
	doc := x.Read(".obor/config.json")
	if strings.Contains(doc, "lane_types") || config.ShapeOf([]byte(doc)) != config.ShapeVersion ||
		!regexp.MustCompile(`(?m)^    "count": 2,?$`).MatchString(doc) ||
		!regexp.MustCompile(`"integrator": "opus"`).MatchString(doc) || !strings.Contains(doc, `"lead": "opus:high"`) {
		t.Errorf("config.json is not the new shape:\n%s", doc)
	}
	for _, lane := range []string{"L-01", "L-02"} {
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
	for _, wt := range []string{"review", "deep-dive"} { // the reviewer runs in the main checkout
		if x.Exists(filepath.Join(cfg.LanesDir(), wt)) {
			t.Errorf("init made a %s worktree", wt)
		}
	}
	// init registers the project in the machine's list of obor projects by name.
	if ps := project.Projects(x.Env()); len(ps) != 1 || ps[0] != (project.Project{Name: "proj", Root: x.Root}) {
		t.Errorf("the machine's project list %+v, want proj at its root\n%s", ps, out)
	}
	if stateOf(out, "projects") != "done" {
		t.Errorf("projects step %q, want done", summary(out, "projects"))
	}
	mkt, err := kitMarketplace(kit)
	if err != nil {
		t.Fatal(err)
	}
	want := "plugin marketplace add " + kit + "\nplugin install obor@" + mkt + " --scope project\n"
	if got := x.claudeCalls(); got != want {
		t.Errorf("claude calls:\n%s\nwant:\n%s", got, want)
	}
	if len(x.h.Plugins) != 0 {
		t.Errorf("herdr plugins: %+v, want none (T-1117: serve restarts the panes)", x.h.Plugins)
	}
	for _, q := range x.reqs() {
		if strings.HasPrefix(q, "plugin.link") {
			t.Errorf("init linked a herdr plugin: %s", q)
		}
	}
	// The fake numbers a new tab's pane n<k> and the tab t<k>.
	// the worker lanes' panes are labelled and hold no agent (dispatch starts one); role panes run theirs.
	panes := map[string]string{}
	for _, p := range x.h.Panes {
		if p.Label != "" {
			panes[p.Label] = p.ID + " " + p.Tab
		}
		if (p.Label == "proj-l-01" || p.Label == "proj-l-02") && (p.Name != "" || p.Kind != "") {
			t.Errorf("lane pane %s holds an agent: %+v", p.Label, p)
		}
		if (p.Label == "proj-int" || p.Label == "proj-rev") && (p.Name != "" || p.Kind != "") {
			t.Errorf("role pane %s holds an agent: %+v", p.Label, p)
		}
	}
	// init starts no agent at all; it labels every pane it places.
	for _, q := range x.reqs() {
		if strings.HasPrefix(q, "agent.start") {
			t.Errorf("init started an agent: %s", q)
		}
	}
	has(t, out, "pane wn1:n4 labelled proj-int, no agent (started on first use)", "integrator line")
	has(t, out, "labelled proj-rev, no agent (started on first use)", "reviewer line")
	o, s := panes["proj-l-01"], panes["proj-l-02"]
	i, r := panes[cfg.AgentName("integrator", "")], panes[cfg.AgentName("reviewer", "")]
	tab := func(v string) string { return append(strings.Fields(v), "", "")[1] }
	// No pane of the project anywhere: one workspace of its own, wn1, made first.
	if len(panes) != 4 || !strings.HasPrefix(o, "wn1:n") || !strings.HasPrefix(r, "wn1:n") || tab(s) != tab(o) ||
		tab(i) != tab(o) || tab(r) == tab(o) {
		t.Errorf("agents (L-01 in a new tab of the new workspace, L-02 and the integrator beside it, the reviewer in a "+
			"new tab): %v", panes)
	}
	if n := strings.Count(strings.Join(x.reqs(), "\n"), "workspace.create cwd="+x.Root+" focus=false label=proj"); n != 1 {
		t.Errorf("workspace.create for the root %d times, want once", n)
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
	// obor has no monitor role: no monitor pane, no "system" tab, and nothing typed at a shell.
	for _, p := range x.h.Panes {
		if p.Label == "proj-monitor" {
			t.Errorf("init made a monitor pane: %+v", p)
		}
	}
	if reqs := strings.Join(x.reqs(), "\n"); strings.Contains(reqs, "text=obor monitor") || strings.Contains(reqs, "label=system") {
		t.Errorf("init touched a monitor pane:\n%s", reqs)
	}
	if n := strings.Count(strings.Join(x.reqs(), "\n"), "plugin.list"); n != 1 {
		t.Errorf("plugin.list read %d times, want once for the old plugin's removal (T-1117)", n)
	}
	if x.serves != 1 {
		t.Errorf("serve install called %d times", x.serves)
	}
	for _, st := range []string{"project", "lanes", "plugin", "L-01", "L-02", "integrator", "reviewer", "serve"} {
		if stateOf(out, st) != "done" {
			t.Errorf("step %s: %q, want done\n%s", st, summary(out, st), out)
		}
	}
	if summary(out, "events") != "" {
		t.Errorf("events step %q, want none (T-1117: it only removes the old plugin, without a step)", summary(out, "events"))
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

// obor init in a plain directory (no repository) makes one on branch main, commits its own files as "obor init"
// and branches the lanes from that commit; a directory the user already made a repository of, with no commit, gets the
// same first commit; a user's untracked file is a FAIL, never committed; a repository with a commit is left as it is.
func TestOborInitInAnEmptyDirectory(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	dir := filepath.Join(x.Base, "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, rc := x.runIn(t, dir, "obor-init")
	if rc != 0 {
		t.Fatalf("obor init in an empty dir: exit %d\n%s", rc, out)
	}
	if got := x.Git(dir, "branch", "--show-current"); got != "main" {
		t.Errorf("branch %q, want main", got)
	}
	if got := x.Git(dir, "log", "--format=%s"); got != "obor init" {
		t.Errorf("commits %q, want the one obor_init commit", got)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"L-01", "L-02"} {
		if _, err := os.Stat(filepath.Join(cfg.LanesDir(), lane, ".git")); err != nil {
			t.Errorf("no worktree for %s: %v", lane, err)
		}
	}
	has(t, out, "git init -b main in "+dir, "the git init line")
	has(t, out, "committed obor init: ", "the commit line")
	has(t, out, ".obor/config.json", "the commit's config")
	has(t, out, ".gitignore", "the commit's .gitignore")
	has(t, out, "docs/agents/", "the commit's agents dir")
	hasNot(t, out, "nothing committed", "no uncommitted note after init committed")
}

// init commits only in a directory that held nothing but .git; with anything else and no commit it FAILs before
// any write: no repository made, no config, the user's files as they were.
func TestOborInitRefusesToCommitAUsersFile(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	dir := filepath.Join(x.Base, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, rc := x.runIn(t, dir, "obor-init")
	wantFail(t, out, rc, dir)
	if x.Exists(filepath.Join(dir, ".git")) {
		t.Error("init made a repository before the FAIL")
	}
	if x.Read(filepath.Join(dir, "README.md")) != "mine\n" {
		t.Error("the user's file was touched")
	}
	onlyEntries(t, dir, "README.md")
}

// wantFail: exit 1 and a FAIL for an unborn main in dir last.
func wantFail(t *testing.T, out string, rc int, dir string) {
	t.Helper()
	want := "FAIL main has no commit yet and " + dir + " has files: commit what belongs in the project, then obor init " +
		"again"
	if rc != 1 || last(out) != want {
		t.Fatalf("exit %d, last line %q, want %q\n%s", rc, last(out), want, out)
	}
	if _, err := exec.Command("git", "-C", dir, "rev-parse", "-q", "--verify", "HEAD").Output(); err == nil {
		t.Error("init made a commit")
	}
}

// onlyEntries: dir's top level is exactly names (.git aside).
func onlyEntries(t *testing.T, dir string, names ...string) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		if e.Name() != ".git" {
			got = append(got, e.Name())
		}
	}
	if strings.Join(got, " ") != strings.Join(names, " ") {
		t.Errorf("%s holds %v, want %v (init wrote before its FAIL)", dir, got, names)
	}
}

// unborn is a new repository on main with no commit, dir under the fixture's base.
func (x *initFx) unborn(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(x.Base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	x.Git(dir, "init", "-q", "-b", "main")
	return dir
}

// a user's own untracked file in an unborn repository, even one named as init's (BACKLOG.md,
// .gitignore, which init would append to), is a FAIL before any write: the file unchanged, nothing created.
func TestOborInitRefusesAnUnbornRepositoryWithAUsersFile(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"BACKLOG.md", ".gitignore", "docs/agents/worker.md"} {
		t.Run(f, func(t *testing.T) {
			x := newGitInitFx(t, true)
			dir := x.unborn(t, "unborn")
			path := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("the user's text\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, rc := x.runIn(t, dir, "obor-init")
			wantFail(t, out, rc, dir)
			if got := x.Read(path); got != "the user's text\n" {
				t.Errorf("%s now %q", f, got)
			}
			onlyEntries(t, dir, strings.SplitN(f, "/", 2)[0])
		})
	}
}

// interrupted is the project step alone in dir (an init that stopped before its commit): init's untouched files.
func (x *initFx) interrupted(t *testing.T, dir string) {
	t.Helper()
	s := &Setup{Kit: kit, Dir: dir, Env: x.Env(x.env...), T: &herdr.Setup{Socket: x.srv.Client(), Env: x.Env(x.env...)},
		Out: &bytes.Buffer{}}
	if err := s.findRoot(); err != nil {
		t.Fatal(err)
	}
	if err := s.initProject("", ""); err != nil {
		t.Fatal(err)
	}
}

// a rerun after an interrupted init (init's own files, no commit) commits them when each is init's text; one
// the user edited is a FAIL, left as it is.
func TestOborInitRerunAfterAnInterruptedInit(t *testing.T) {
	t.Parallel()
	t.Run("untouched", func(t *testing.T) {
		x := newGitInitFx(t, true)
		dir := x.unborn(t, "fresh")
		x.interrupted(t, dir)
		if !x.Exists(filepath.Join(dir, ".obor", "config.json")) || !x.Exists(filepath.Join(dir, "BACKLOG.md")) {
			t.Fatal("the interrupted init wrote no files")
		}
		out, rc := x.runIn(t, dir, "obor-init")
		if rc != 0 {
			t.Fatalf("rerun: exit %d\n%s", rc, out)
		}
		if got := x.Git(dir, "log", "--format=%s"); got != "obor init" {
			t.Errorf("commits %q, want the one obor init commit", got)
		}
		files := x.Git(dir, "show", "--name-only", "--format=", "HEAD")
		for _, f := range []string{".obor/config.json", ".gitignore", "BACKLOG.md", "docs/decisions.md"} {
			if !strings.Contains("\n"+files+"\n", "\n"+f+"\n") {
				t.Errorf("the commit lacks %s:\n%s", f, files)
			}
		}
	})
	t.Run("edited", func(t *testing.T) {
		x := newGitInitFx(t, true)
		dir := x.unborn(t, "fresh")
		x.interrupted(t, dir)
		path := filepath.Join(dir, "BACKLOG.md")
		edited := x.Read(path) + "- my own item\n"
		x.Write(path, edited)
		out, rc := x.runIn(t, dir, "obor-init")
		wantFail(t, out, rc, dir)
		if x.Read(path) != edited {
			t.Error("the edited BACKLOG.md was touched")
		}
	})
}

func TestOborInitLeavesACommittedRepositoryAlone(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	before := x.Git(x.Root, "rev-parse", "HEAD")
	out := x.init(t)
	if after := x.Git(x.Root, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved %s → %s", before, after)
	}
	has(t, out, "nothing committed: review, then commit", "the uncommitted note")
}

// obor init's rerun is the repair path for a lane, and the last run's outcome decides: a failed lane_setup
// worktree is gone gets it back.
func TestOborInitRerunKeepsTheLanes(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	x.init(t)
	agents, wt := x.agents(t), x.Git(x.Root, "worktree", "list")
	x.reset()
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil || cfg.LaneCount() != 2 {
		t.Fatalf("lanes after the rerun: %d (%v)", cfg.LaneCount(), err)
	}
	if stateOf(out, "lanes") != "already" || !strings.Contains(summary(out, "lanes"), "L-01, L-02") {
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
	x.Git(x.Root, "worktree", "remove", "--force", filepath.Join(cfg.LanesDir(), "L-01"))
	out = x.init(t)
	if _, err := os.Stat(filepath.Join(cfg.LanesDir(), "L-01", ".git")); err != nil || stateOf(out, "lanes") != "done" {
		t.Errorf("worktree not recreated (%v), lanes %q", err, summary(out, "lanes"))
	}
	if cfg, _ := config.Load(x.Root); cfg.LaneCount() != 2 {
		t.Errorf("lanes after the recreation %d", cfg.LaneCount())
	}
}

// obor init's rerun is the repair path for a lane, and the last run's outcome decides: a failed lane_setup
// runs again, a finished one is skipped, and a lane dir that is no worktree of the repo is a FAIL (never a skip).
// Slow: seven cases, each two or three real obor init runs (its commit, its lanes' worktree adds: the code under test
// runs git, so no fake), ≈0.35 s a case.
func TestOborInitLaneSetupOnARerun(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		run  func(t *testing.T, x *initFx)
	}{
		{"lane_setup failed last run: the rerun runs it", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"lanes", "setup"}, "exit 3")
			x.failing(t, "FAIL lane_setup exited 3 in "+x.LaneDir("L-01")+": exit 3", "obor-init")
			for _, lane := range []string{"L-01", "L-02"} {
				if x.Exists(".obor/state/lanes/" + lane + ".setup") {
					t.Errorf("%s: a failed lane_setup left the done marker", lane)
				}
			}
			x.editConfig(t, []string{"lanes", "setup"}, "pwd > setup-ran")
			out := x.init(t)
			for _, lane := range []string{"L-01", "L-02"} {
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
		{"lane_setup changed since it finished: the rerun runs it", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"lanes", "setup"}, "pwd > setup-ran")
			x.init(t)
			for _, lane := range []string{"L-01", "L-02"} {
				if !x.Exists(".obor/state/lanes/" + lane + ".setup") {
					t.Fatalf("precondition: %s's lane_setup ran and left its marker", lane)
				}
				os.Remove(filepath.Join(x.LaneDir(lane), "setup-ran"))
			}
			x.editConfig(t, []string{"lanes", "setup"}, "pwd > setup-ran-again")
			out := x.init(t)
			for _, lane := range []string{"L-01", "L-02"} {
				if !x.Exists(x.LaneDir(lane) + "/setup-ran-again") {
					t.Errorf("%s: the rerun did not run the edited lane_setup", lane)
				}
			}
			if stateOf(out, "lanes") != "done" {
				t.Errorf("lanes step %q", summary(out, "lanes"))
			}
		}},
		{"lane_setup finished last run: the rerun skips it", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"lanes", "setup"}, "pwd > setup-ran")
			x.init(t)
			for _, lane := range []string{"L-01", "L-02"} {
				if !x.Exists(".obor/state/lanes/" + lane + ".setup") {
					t.Fatalf("precondition: %s's lane_setup ran and left its marker", lane)
				}
				os.Remove(filepath.Join(x.LaneDir(lane), "setup-ran"))
			}
			out := x.init(t)
			for _, lane := range []string{"L-01", "L-02"} {
				if x.Exists(x.LaneDir(lane) + "/setup-ran") {
					t.Errorf("%s: the rerun ran lane_setup again", lane)
				}
			}
			if stateOf(out, "lanes") != "already" {
				t.Errorf("lanes step %q", summary(out, "lanes"))
			}
		}},
		{"a lane dir that is no worktree of the repo: FAIL", func(t *testing.T, x *initFx) {
			dir := x.LaneDir("L-01")
			x.Git(x.Root, "worktree", "remove", "--force", dir)
			os.MkdirAll(dir, 0o755)
			x.Write(dir+"/keep", "a plain directory, not a worktree\n")
			x.failing(t, "FAIL "+dir+" exists and is not a worktree of "+x.Root, "obor-init")
			if !x.Exists(dir + "/keep") {
				t.Error("the directory was touched")
			}
		}},
		{"lane_ready fails with no lane_setup to run: FAIL", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"lanes", "ready"}, "false")
			x.failing(t, "FAIL lane L-01: lane_ready fails (false) and commands.lane_setup is not set", "obor-init")
		}},
		{"lane_ready still fails after lane_setup: FAIL, never done", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"lanes", "ready"}, "echo not ready; exit 1")
			x.editConfig(t, []string{"lanes", "setup"}, "true")
			out, rc := x.run(t, "obor-init")
			want := "FAIL lane L-01: lane_ready still fails after lane_setup (echo not ready; exit 1): not ready"
			if rc != 1 || last(out) != want {
				t.Fatalf("exit %d, last line %q, want %q\n%s", rc, last(out), want, out)
			}
			if strings.Contains(out, "OK init") || stateOf(out, "lanes") == "done" {
				t.Errorf("a lane that is still not ready was reported done:\n%s", out)
			}
		}},
		{"lane_ready passes after lane_setup: done", func(t *testing.T, x *initFx) {
			x.editConfig(t, []string{"lanes", "ready"}, "test -f ready")
			x.editConfig(t, []string{"lanes", "setup"}, "touch ready")
			out := x.init(t)
			for _, lane := range []string{"L-01", "L-02"} {
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
			x := newGitInitFx(t, true)
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

// herdr cannot list its plugins. The removal of the obor.events plugin an older obor linked is best
// effort: init reads the list once, says a WARN and sets the project up all the same, linking and unlinking nothing.
func TestOborInitWarnsWhenHerdrCannotListPlugins(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	old := x.Path("old-kit/herdr-plugin")
	x.h.Plugins = []herdrtest.Plugin{{ID: "obor.events", Root: old}}
	x.srv.Fail("plugin.list", "internal", "boom")
	out := x.init(t)
	has(t, out, "WARN events: cannot read herdr's plugin list: herdr plugin.list: failed: boom (internal)", "the list error said")
	var lists int
	for _, r := range x.reqs() {
		if strings.HasPrefix(r, "plugin.list") {
			lists++
		}
		for _, m := range []string{"plugin.link", "plugin.unlink"} {
			if strings.HasPrefix(r, m) {
				t.Errorf("init sent %s although it could not list herdr's plugins", r)
			}
		}
	}
	if lists != 1 {
		t.Errorf("plugin.list read %d times, want once", lists)
	}
	if len(x.h.Plugins) != 1 || x.h.Plugins[0].ID != "obor.events" {
		t.Errorf("plugins %v after the WARN, want the old link left", x.h.Plugins)
	}
	if x.serves != 1 || len(x.agents(t)) != 0 {
		t.Errorf("the project is set up (serve installs %d, agents %v) despite the WARN", x.serves, x.agents(t))
	}
	has(t, out, "hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false", "the herdr hint")
}

// no codex on PATH: the reviewer is opus (the example's reviewer is a Codex type), the summary says so.
func TestOborInitReviewerOpusWithoutCodex(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, false)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if rc, _ := cfg.Role("reviewer"); rc.Type != "opus" {
		t.Errorf("reviewer type %q, want opus", rc.Type)
	}
	has(t, summary(out, "lanes"), "reviewer sol:high → opus", "lanes step")
	// the reviewer's pane is labelled and holds no agent; its type decides what the first review starts.
	if a := x.h.Pane(x.paneOf(cfg.AgentName("reviewer", ""))); a == nil || a.Kind != "" || a.Name != "" {
		t.Errorf("reviewer pane: %+v, want labelled with no agent", a)
	}
	has(t, out, "detected: claude on PATH;", "detected line")
}

func (x *initFx) paneOf(name string) string {
	for _, p := range x.h.Panes {
		if p.Label == name {
			return p.ID
		}
	}
	return ""
}

// the template names the lead's model (kept by init); a hand-written config with no roles.lead gets opus
// (obor lead refuses an untyped lead); a type the user set stays, and a rerun changes nothing.
func TestOborInitSetsTheLeadsType(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if rc, _ := cfg.Role("lead"); rc.Type != "opus" || rc.Effort != "high" {
		t.Errorf("lead %+v, want the template's opus:high", rc)
	}
	has(t, summary(out, "lanes"), "lead opus:high", "lanes step")

	x.editConfig(t, []string{"agents", "roles", "lead"}, nil) // a hand-written config that omits the lead
	if cfg, err = config.Load(x.Root); err != nil {
		t.Fatal(err)
	}
	if rc, _ := cfg.Role("lead"); rc.Type != "" {
		t.Fatalf("precondition: the lead has no model, got %+v", rc)
	}
	out = x.init(t)
	if cfg, err = config.Load(x.Root); err != nil {
		t.Fatal(err)
	}
	if rc, _ := cfg.Role("lead"); rc.Type != "opus" {
		t.Errorf("lead type %q, want opus", rc.Type)
	}
	has(t, summary(out, "lanes"), "lead unset → opus", "lanes step")

	before := x.Read(".obor/config.json")
	x.init(t)
	if x.Read(".obor/config.json") != before {
		t.Errorf("config changed on a rerun")
	}

	x.editConfig(t, []string{"agents", "roles", "lead"}, "sonnet")
	out = x.init(t)
	if cfg, err = config.Load(x.Root); err != nil {
		t.Fatal(err)
	}
	if lead, ok := cfg.Data.Agents.Roles.M["lead"]; !ok || lead.Model != "sonnet" {
		t.Errorf("a kept lead model: %+v", lead)
	}
	if got := summary(out, "lanes"); !strings.Contains(got, "lead sonnet") || strings.Contains(got, "lead sonnet →") {
		t.Errorf("lanes step %q", got)
	}
}

// A new config's prefix steers clear of another project's running agents (<prefix>-…); an explicit one is kept.
func TestOborInitPrefixUniqueAgainstRunningAgents(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	x.h.Agent("w9:a1", "proj-lead", "idle", "/elsewhere")
	x.h.Agent("w9:a2", "proj2-lead", "idle", "/elsewhere2")
	x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil || cfg.Data.Project.AgentPrefix != "proj3" {
		t.Fatalf("prefix %q (%v), want proj3", cfg.Data.Project.AgentPrefix, err)
	}
	y := newGitInitFx(t, true)
	y.h.Agent("w9:a1", "proj-lead", "idle", "/elsewhere")
	y.init(t, "--prefix", "proj")
	if cfg, _ := config.Load(y.Root); cfg.Data.Project.AgentPrefix != "proj" {
		t.Errorf("explicit prefix %q", cfg.Data.Project.AgentPrefix)
	}
}

// A failed plugin install is a FAIL that says to fix it and rerun; the rerun then installs it.
func TestOborInitPluginFailureIsRepairedByARerun(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
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

// Detected commands are written into the config where it has none, never over a value of the user's.
func TestOborInitWritesDetectedCommandsOnce(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	x.Write("go.mod", "module x\n")
	out := x.init(t)
	cfg, _ := config.Load(x.Root)
	if c := cfg.Data.Merge.Check; c.Cmd != "go test ./..." {
		t.Errorf("commands.check %+v", c)
	}
	if stateOf(out, "commands") != "done" {
		t.Errorf("commands step %q", summary(out, "commands"))
	}
	if !strings.Contains(out, "OK init proj (prefix proj); next: obor lead\n") {
		t.Errorf("a detected stack's next line is not the OK line:\n%s", out)
	}
	// the summary says what commands.check is for, so an adopter keeps it to a few minutes.
	has(t, out, "commands.check is what every merge must pass, keep it to a few minutes; slower tests go in "+
		"check_milestone (the nightly), and obor nightly off if CI on main already does that", "the check line")
	// obor's commits pass --no-verify, so the summary says an adopter's hooks never run on them.
	has(t, out, "obor commits with --no-verify: your pre-commit and commit-msg hooks do not run on them; put "+
		"formatting and secret scans in commands.check (the merge check)", "the hooks line")
	has(t, out, "go.mod", "detected line")
	doc, _ := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"merge", "check"}, "make mine")
	x.Write(".obor/config.json", string(doc))
	out = x.init(t)
	cfg, _ = config.Load(x.Root)
	if c := cfg.Data.Merge.Check; c.Cmd != "make mine" {
		t.Errorf("the user's check overwritten: %+v", c)
	}
	if stateOf(out, "commands") != "already" {
		t.Errorf("commands step %q", summary(out, "commands"))
	}
}

// on a rerun L-01's tab holds a pane with an unknown cwd (maybe another project's): init does not pass that
// tab for L-02, which goes to a new tab of the project's workspace; nothing is split.
func TestOborInitRerunPutsS01InANewTabBesideAMixedO01Tab(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	x.init(t)
	o, s := x.paneOf("proj-l-01"), x.paneOf("proj-l-02")
	if o == "" || s == "" {
		t.Fatalf("precondition: L-01 %q and L-02 %q placed", o, s)
	}
	otab := x.h.Pane(o).Tab
	ows, _, _ := strings.Cut(o, ":") // the fake's pane ids name their workspace
	if x.h.Pane(s).Tab != otab || ows == "" {
		t.Fatalf("precondition: L-02 beside L-01 in %s of a workspace (%q)", otab, ows)
	}
	x.h.Remove(s)
	x.h.Tab(otab, 200, 50, ows+":zz") // a pane with an unknown cwd in L-01's tab
	if x.h.Pane(ows+":zz").Cwd != "" || x.paneOf("proj-l-02") != "" {
		t.Fatal("precondition: an unknown-cwd pane in L-01's tab, L-02 gone")
	}
	x.reset()
	out := x.init(t)
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "pane.split", "the mixed tab is not split")
	has(t, calls, "label=workers workspace_id="+ows, "a new tab in the project's workspace")
	ns := x.paneOf("proj-l-02")
	if ns == "" || x.h.Pane(ns).Tab == otab {
		t.Errorf("L-02 in pane %q, want one in a new tab (not %s)", ns, otab)
	}
	if stateOf(out, "L-02") != "done" || stateOf(out, "L-01") != "already" {
		t.Errorf("L-01 %q, L-02 %q; want already, done", summary(out, "L-01"), summary(out, "L-02"))
	}
}

// obor init writes `difficulty` from the kinds it detected (Claude), so no level lists deepseek (OpenCode)
// on a machine that never opted in; an existing `difficulty` (a user's opt-in) is kept on a rerun.
func TestOborInitDifficultyListsOnlyDetectedKinds(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, false)
	x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Data.Agents.Difficulty) == 0 {
		t.Fatalf("difficulty not written:\n%s", x.Read(".obor/config.json"))
	}
	if _, ok := cfg.Model("deepseek"); !ok {
		t.Fatal("precondition: deepseek is a lane type of the default config")
	}
	for level, list := range cfg.Data.Agents.Difficulty {
		for _, ref := range list {
			if typ, _ := config.ParseModelRef(ref); typ == "deepseek" {
				t.Errorf("level %s lists %s without OpenCode opted in", level, ref)
			}
		}
	}
	if got := cfg.Data.Agents.Difficulty["simple"]; strings.Join(got, ",") != "sonnet,haiku" {
		t.Errorf("simple = %v, want sonnet,haiku", got)
	}

	// Opted in: the user lists deepseek; a rerun keeps it.
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"agents", "difficulty"},
		map[string][]string{"simple": {"deepseek", "sonnet"}})
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	x.reset()
	x.init(t)
	cfg, err = config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Data.Agents.Difficulty["simple"]; strings.Join(got, ",") != "deepseek,sonnet" {
		t.Errorf("an existing difficulty was changed: simple = %v", got)
	}
	if _, set := cfg.Data.Agents.Difficulty["hard"]; set {
		t.Errorf("rerun added levels to an existing difficulty: %v", cfg.Data.Agents.Difficulty)
	}
}
