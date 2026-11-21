// obor init's decisions, on a fake git (newFakeGitInitFx): the steps it reports, the config it writes, the lanes it adds,
// the panes it places, the commit it makes or refuses. What git itself ends up holding (a real init commit, real
// lane worktrees, an interrupted init's rerun) is oborinit_integration_test.go's.

package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/testkit"
)

// obor init writes setup.confirmed: false when the config has no such key, and a rerun keeps the owner's true.
func TestOborInitRecordsTheSetupAnswer(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
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
	x := newFakeGitInitFx(t, true)
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

// obor init's output on a new project ends with the OK line saying where the lead starts: no setup table, no
// merge.check/merge.compile/--no-verify prose, no "next:" (the lead shows the table and asks).
func TestOborInitSummaryHasNoTableAndEndsWithTheLeadLine(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	out := x.init(t)
	const last = "OK init proj (prefix proj): the lead starts in its own lead tab\n"
	if !strings.HasSuffix(out, last) {
		t.Errorf("the output does not end with %q:\n%s", last, out)
	}
	if i := strings.Index(out, "obor init proj:\n"); i < 0 || !strings.Contains(out[i:], "  serve ") {
		t.Errorf("the step lines are gone:\n%s", out)
	}
	// the one merge.compile line is the no-compile warning (TestOborInitWarnsOfNoCompileOnTheDefaultBranch)
	prose := strings.ReplaceAll(out, "warning: merge.compile is empty and merge.target_branch main is the default branch: "+
		config.NoCompile+"\n", "")
	if prose == out {
		t.Errorf("precondition: the no-compile warning is in the output:\n%s", out)
	}
	for _, gone := range []string{"models and effort", "\nlanes: ", "\nroles: ", "\nconfirmed:", "starting point", "merge.check is what",
		"merge.compile", "--no-verify", "change any of it", "next:", "waiting on"} {
		hasNot(t, prose, gone, "the trimmed summary")
	}
}

// obor init warns that merge.compile is empty when merge.target_branch is the repository's default branch (the branch
// init read through its git), and says nothing when the target is another branch. The warning's own rule is
// config's (TestNoCompileWarningTakesTheCallersBranch).
func TestOborInitWarnsOfNoCompileOnTheDefaultBranch(t *testing.T) {
	t.Parallel()
	const warning = "warning: merge.compile is empty and merge.target_branch "
	for _, c := range []struct {
		name, target string
		warns        bool
	}{
		{"target is the checked-out branch: warns", "main", true},
		{"target is another branch: none", "trunk", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFakeGitInitFx(t, true)
			x.Config(func(cfg map[string]any) {
				cfg["merge"] = map[string]any{"target_branch": c.target}
			})
			if got, _ := x.fake.Git(x.Root, "symbolic-ref", "-q", "--short", "HEAD"); strings.TrimSpace(got) != "main" {
				t.Fatalf("precondition: the repository is on main: %q", got)
			}
			out := x.init(t)
			if got := strings.Contains(out, warning+c.target+" is the default branch: "+config.NoCompile); got != c.warns {
				t.Errorf("warning present %v, want %v:\n%s", got, c.warns, out)
			}
		})
	}
}

// One run sets the whole project up: config (name, prefix), L-01 opus and L-02 sonnet with worktrees, the lead and the
// integrator opus and (codex on PATH) the example's Codex reviewer, the plugin installed from the kit's marketplace,
// four agents in their tabs, serve installed; the summary says each step done.
func TestOborInitSetsUpTheProject(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	if x.Exists(".obor/config.json") {
		t.Fatal("precondition: no config")
	}
	out := x.init(t)
	if !strings.HasSuffix(out, "OK init proj (prefix proj): the lead starts in its own lead tab\n") {
		t.Errorf("no OK line in\n%s", out)
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
	if strings.Contains(doc, "lane_types") || !strings.Contains(doc, `"version": 2`) ||
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
		tab(i) == tab(o) || tab(r) != tab(i) {
		t.Errorf("agents (the lanes in a new tab of the new workspace, the integrator and the reviewer in another): %v",
			panes)
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
	if len(tabs) != 2 || !strings.Contains(tabs[0], "label=lanes") || !strings.Contains(tabs[1], "label=roles") {
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
	if x.serves != 1 {
		t.Errorf("serve install called %d times", x.serves)
	}
	for _, st := range []string{"project", "lanes", "plugin", "L-01", "L-02", "integrator", "reviewer", "serve"} {
		if stateOf(out, st) != "done" {
			t.Errorf("step %s: %q, want done\n%s", st, summary(out, st), out)
		}
	}
	if stateOf(out, "commands") != "skipped" {
		t.Errorf("commands with no build files: %q", summary(out, "commands"))
	}
	has(t, out, "detected: claude, codex on PATH; no build files", "detected line")
	has(t, out, "nothing committed: review, then commit .obor/config.json, .gitignore, BACKLOG.md, docs/decisions.md, "+
		"docs/agents/", "the files to commit named")
	has(t, out, "hint: after a herdr restart obor replaces the agents herdr resumes in its panes; to save that double "+
		"start, set resume_agents_on_restore = false", "herdr hint: optional")
	if n := strings.Count(out, "resume_agents_on_restore"); n != 1 {
		t.Errorf("the herdr hint %d times (once, in the summary)", n)
	}
}

// init commits only in a directory that held nothing but .git; with anything else and no commit it FAILs before
// any write: no repository made, no config, the user's files as they were.
func TestOborInitRefusesToCommitAUsersFile(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	dir := filepath.Join(x.Base, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, rc := x.runIn(t, dir, "obor-init")
	wantFail(t, x, out, rc, dir)
	if x.Exists(filepath.Join(dir, ".git")) {
		t.Error("init made a repository before the FAIL")
	}
	if x.Read(filepath.Join(dir, "README.md")) != "mine\n" {
		t.Error("the user's file was touched")
	}
	onlyEntries(t, dir, "README.md")
}

// wantFail: exit 1 and a FAIL for an unborn main in dir last.
func wantFail(t *testing.T, x *initFx, out string, rc int, dir string) {
	t.Helper()
	want := "FAIL main has no commit yet and " + dir + " has files: commit what belongs in the project, then obor init " +
		"again"
	if rc != 1 || last(out) != want {
		t.Fatalf("exit %d, last line %q, want %q\n%s", rc, last(out), want, out)
	}
	if x.hasCommit(dir) {
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

// In a repository with no commit, a file at one of init's own paths is init's input: it goes into the commit with
// the bytes it has (BACKLOG.md, .gitignore with its lines added, a hand-written config, an agents doc). Init adds
// to .gitignore, and keeps the rest as it is.
func TestOborInitCommitsAUnbornRepositoryFilesAtItsPaths(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"BACKLOG.md", "docs/agents/worker.md", ".gitignore"} {
		t.Run(f, func(t *testing.T) {
			x := newFakeGitInitFx(t, true)
			dir := x.unborn(t, "unborn")
			path := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("the user's text\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, rc := x.runIn(t, dir, "obor-init")
			if rc != 0 {
				t.Fatalf("exit %d\n%s", rc, out)
			}
			got := x.Read(path)
			if !strings.HasPrefix(got, "the user's text\n") {
				t.Errorf("%s is now %q, want the user's text kept", f, got)
			}
			if f != ".gitignore" && got != "the user's text\n" {
				t.Errorf("%s is now %q, want the user's bytes", f, got)
			}
			if !slices.Contains(committedFiles(x, dir), f) {
				t.Errorf("the commit lacks %s", f)
			}
		})
	}
}

// committedFiles are the paths of dir's last commit.
func committedFiles(x *initFx, dir string) []string {
	return strings.Fields(x.Git(dir, "show", "--name-only", "--format=", "HEAD"))
}

// A hand-written .obor/config.json in an unborn repository goes into the commit with init's edits, and any other file
// there FAILs before any write: nothing created, the file unchanged.
func TestOborInitUnbornRepositoryConfigGoesInAndOtherFilesRefuse(t *testing.T) {
	t.Parallel()
	const mine = `{"project": {"name": "mine", "agent_prefix": "mine"}}` + "\n"
	t.Run("config", func(t *testing.T) {
		x := newFakeGitInitFx(t, true)
		dir := x.unborn(t, "unborn")
		x.Write(filepath.Join(dir, ".obor", "config.json"), mine)
		out, rc := x.runIn(t, dir, "obor-init")
		if rc != 0 {
			t.Fatalf("exit %d\n%s", rc, out)
		}
		if got := x.Git(dir, "log", "--format=%s"); got != "obor init" {
			t.Errorf("commits %q, want the one obor init", got)
		}
		if files := committedFiles(x, dir); !slices.Contains(files, ".obor/config.json") {
			t.Errorf("the commit lacks the config: %v", files)
		}
		cfg, err := config.Load(dir)
		if err != nil || cfg.Name() != "mine" {
			t.Errorf("the config is not the hand-written one: %v", err)
		}
	})
	t.Run("another file", func(t *testing.T) {
		x := newFakeGitInitFx(t, true)
		dir := x.unborn(t, "unborn")
		x.Write(filepath.Join(dir, ".obor", "config.json"), mine)
		x.Write(filepath.Join(dir, "README.md"), "mine\n")
		out, rc := x.runIn(t, dir, "obor-init")
		wantFail(t, x, out, rc, dir)
		if x.Read(filepath.Join(dir, ".obor", "config.json")) != mine || x.Read(filepath.Join(dir, "README.md")) != "mine\n" {
			t.Error("init touched a file before its FAIL")
		}
		onlyEntries(t, dir, ".obor", "README.md")
		if x.Exists(filepath.Join(dir, "BACKLOG.md")) || x.claudeCalls() != "" {
			t.Error("init wrote or called claude before its FAIL")
		}
	})
}

// interrupted is the project step alone in dir (an init that stopped before its commit): init's untouched files.
func (x *initFx) interrupted(t *testing.T, dir string) {
	t.Helper()
	s := &Setup{Kit: kit, Dir: dir, Env: x.Env(x.env...), T: &herdr.Setup{Socket: x.srv.Client(), Env: x.Env(x.env...)},
		Out: &bytes.Buffer{}, host: x.host}
	if err := s.findRoot(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.initProject("", ""); err != nil {
		t.Fatal(err)
	}
}

func TestOborInitLeavesACommittedRepositoryAlone(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
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
	x := newFakeGitInitFx(t, true)
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

// A config that counts four lanes gets all four worktrees: init leaves no configured lane missing (L-03 and L-04
// waited for a hand-run of obor setup lane). A fresh config still makes two (TestOborInitSetsUpTheProject), and a
// rerun with every lane done adds nothing.
func TestOborInitSetsUpEveryConfiguredLane(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	x.Config(testkit.Set("lanes.count", 4))
	if cfg, err := config.Load(x.Root); err != nil || cfg.LaneCount() != 4 {
		t.Fatalf("precondition: a config counting four lanes (%v)", err)
	}
	for _, lane := range []string{"L-01", "L-02", "L-03", "L-04"} {
		if _, err := os.Stat(x.LaneDir(lane)); err == nil {
			t.Fatalf("precondition: no worktree for %s yet", lane)
		}
	}
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LaneCount() != 4 {
		t.Errorf("lanes = %d, want the config's 4", cfg.LaneCount())
	}
	for _, lane := range []string{"L-01", "L-02", "L-03", "L-04"} {
		if _, err := os.Stat(filepath.Join(cfg.LanesDir(), lane, ".git")); err != nil {
			t.Errorf("no worktree for %s: %v", lane, err)
		}
	}
	if got := summary(out, "lanes"); stateOf(out, "lanes") != "done" ||
		!strings.Contains(got, "L-01, L-02, L-03, L-04") {
		t.Errorf("lanes step %q", got)
	}
	// Panes stay as they are: init places L-01's and L-02's, a later lane's pane comes at dispatch.
	for _, lane := range []string{"L-03", "L-04"} {
		if p := x.paneOf(cfg.AgentName("worker", lane)); p != "" {
			t.Errorf("%s has a pane at init: %q", lane, p)
		}
	}
	// A rerun with all four done skips them: no worktree added, nothing reported done.
	worktrees := x.Git(x.Root, "worktree", "list")
	x.reset()
	out = x.init(t)
	if stateOf(out, "lanes") != "already" {
		t.Errorf("lanes step on the rerun %q", summary(out, "lanes"))
	}
	if got := x.Git(x.Root, "worktree", "list"); got != worktrees {
		t.Errorf("the rerun changed the worktrees:\n%s\nwere:\n%s", got, worktrees)
	}
}

// init's panes follow the configured lanes: the first two, whatever named them (a list's own names, or a count's), the
// first in a tab of its own and the second beside it; zero or one lane places what it has, and the role panes are
// placed in every case. Lanes past the first two place their pane at dispatch. Init reaches its serve step in each.
func TestOborInitPlacesTheConfiguredLanes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		edit func(map[string]any)
		want []string // the lane panes init places, in order
	}{
		{"a list: L-02 and L-05", testkit.Set("lanes.list", `[{"name":"L-02"},{"name":"L-05"}]`),
			[]string{"L-02", "L-05"}},
		{"a list without L-02: L-01 and L-03", testkit.Set("lanes.list", `[{"name":"L-01"},{"name":"L-03"}]`),
			[]string{"L-01", "L-03"}},
		{"a list of one: L-02", testkit.Set("lanes.list", `[{"name":"L-02"}]`), []string{"L-02"}},
		{"an empty list: no lane pane", testkit.Set("lanes.list", `[]`), nil},
		{"a count of four: the first two", testkit.Set("lanes.count", 4), []string{"L-01", "L-02"}},
	} {
		x := newFakeGitInitFx(t, true)
		x.Config(c.edit)
		out := x.init(t)
		if x.serves != 1 || stateOf(out, "serve") != "done" {
			t.Errorf("%s: serve step %q, %d installs\n%s", c.name, summary(out, "serve"), x.serves, out)
		}
		cfg, err := config.Load(x.Root)
		if err != nil {
			t.Fatal(err)
		}
		labels := map[string]bool{} // every labelled pane
		for _, p := range x.h.Panes {
			if p.Label != "" {
				labels[p.Label] = true
			}
		}
		if len(labels) != len(c.want)+2 { // the lane panes, the integrator and the reviewer
			t.Errorf("%s: labelled panes %v, want %v, the integrator and the reviewer", c.name, labels, c.want)
		}
		want := map[string]bool{}
		for _, lane := range c.want {
			want[lane] = true
			agent := cfg.AgentName("worker", lane)
			if x.paneOf(agent) == "" || stateOf(out, lane) != "done" {
				t.Errorf("%s: %s: pane %q, step %q", c.name, lane, x.paneOf(agent), summary(out, lane))
			}
		}
		for _, l := range cfg.Lanes() { // past the first two: its pane comes at dispatch
			if !want[l.Name] && x.paneOf(cfg.AgentName("worker", l.Name)) != "" {
				t.Errorf("%s: %s has a pane at init", c.name, l.Name)
			}
		}
		for _, role := range []string{"integrator", "reviewer"} {
			if x.paneOf(cfg.AgentName(role, "")) == "" || stateOf(out, role) != "done" {
				t.Errorf("%s: %s: pane %q, step %q", c.name, role, x.paneOf(cfg.AgentName(role, "")), summary(out, role))
			}
		}
		if len(c.want) == 0 {
			continue
		}
		// The lane panes share a tab; the integrator and the reviewer share another.
		tab := func(agent string) string {
			if p := x.h.Pane(x.paneOf(agent)); p != nil {
				return p.Tab
			}
			return ""
		}
		first := tab(cfg.AgentName("worker", c.want[0]))
		for _, lane := range c.want[1:] {
			if got := tab(cfg.AgentName("worker", lane)); got != first {
				t.Errorf("%s: %s in tab %q, %s in %q", c.name, lane, got, c.want[0], first)
			}
		}
		roles := tab(cfg.AgentName("integrator", ""))
		if roles == "" || roles == first {
			t.Errorf("%s: integrator in tab %q, the lanes in %q", c.name, roles, first)
		}
		if got := tab(cfg.AgentName("reviewer", "")); got != roles {
			t.Errorf("%s: reviewer in tab %q, the integrator in %q", c.name, got, roles)
		}
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
			x.failing(t, "FAIL lane L-01: lane_ready fails (false) and lanes.setup is not set", "obor-init")
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
			x := newFakeGitInitFx(t, true)
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

// no codex on PATH: the reviewer is opus (the example's reviewer is a Codex type), the summary says so.
func TestOborInitReviewerOpusWithoutCodex(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, false)
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
	x := newFakeGitInitFx(t, true)
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
	x := newFakeGitInitFx(t, true)
	x.h.Agent("w9:a1", "proj-lead", "idle", "/elsewhere").Label = "proj-lead"
	x.h.Agent("w9:a2", "proj2-lead", "idle", "/elsewhere2").Label = "proj2-lead"
	x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil || cfg.Data.Project.AgentPrefix != "proj3" {
		t.Fatalf("prefix %q (%v), want proj3", cfg.Data.Project.AgentPrefix, err)
	}
	y := newFakeGitInitFx(t, true)
	y.h.Agent("w9:a1", "proj-lead", "idle", "/elsewhere").Label = "proj-lead"
	y.init(t, "--prefix", "proj")
	if cfg, _ := config.Load(y.Root); cfg.Data.Project.AgentPrefix != "proj" {
		t.Errorf("explicit prefix %q", cfg.Data.Project.AgentPrefix)
	}
}

// A failed plugin install is a FAIL that says to fix it and rerun; the rerun then installs it.
func TestOborInitPluginFailureIsRepairedByARerun(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	x.Write(filepath.Join(x.bin, "fail"), "")
	out, rc := x.run(t, "obor-init")
	if rc != 1 || !strings.Contains(last(out), "FAIL claude plugin install obor@") || !strings.Contains(last(out), "(exit 7)") {
		t.Fatalf("exit %d, last %q", rc, last(out))
	}
	if len(x.agents(t)) != 0 {
		t.Errorf("agents started without the plugin: %v", x.agents(t))
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if x.Exists(filepath.Join(cfg.LanesDir(), "L-01")) {
		t.Error("a worktree was made before the plugin was installed")
	}
	os.Remove(filepath.Join(x.bin, "fail"))
	out = x.init(t)
	if stateOf(out, "plugin") != "done" || stateOf(out, "lanes") != "done" {
		t.Errorf("rerun: plugin %q, lanes %q", summary(out, "plugin"), summary(out, "lanes"))
	}
}

// Detected commands are written into the config where it has none, never over a value of the user's.
func TestOborInitWritesDetectedCommandsOnce(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	x.Write("go.mod", "module x\n")
	out := x.init(t)
	cfg, _ := config.Load(x.Root)
	if c := cfg.Data.Merge.Check; c.Cmd != "go test ./..." {
		t.Errorf("merge.check %+v", c)
	}
	if stateOf(out, "commands") != "done" {
		t.Errorf("commands step %q", summary(out, "commands"))
	}
	hasNot(t, out, "merge.check is what", "the check prose is the lead's")
	hasNot(t, out, "--no-verify", "the hooks prose is the lead's")
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

// package.json detection leaves merge.compile unset, so a moved candidate re-runs merge.check, and suggests a
// typecheck script for compile instead; a config that sets compile keeps it and gets no suggestion.
func TestOborInitSuggestsTypecheckForCompile(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	// the fake git's worktrees hold no package.json: the detected install would fail there, so the config brings its own.
	x.Config(testkit.Set("lanes.setup", "true"), testkit.Set("lanes.ready", "true"))
	x.Write("package.json", `{"scripts":{"typecheck":"tsc --noEmit","test":"vitest run"}}`)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Data.Merge.Compile; c.Cmd != "" {
		t.Errorf("merge.compile = %+v, want it unset", c)
	}
	if c := cfg.Data.Merge.Check; c.Cmd != "npm run typecheck && npm run test" {
		t.Errorf("merge.check = %+v", c)
	}
	hasNot(t, out, "set merge.compile to", "the compile suggestion is the lead's")
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"merge", "compile"}, "tsc --noEmit")
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	out = x.init(t)
	hasNot(t, out, "set merge.compile to", "no suggestion with compile set")
	cfg, _ = config.Load(x.Root)
	if c := cfg.Data.Merge.Compile; c.Cmd != "tsc --noEmit" {
		t.Errorf("the user's compile overwritten: %+v", c)
	}
}

// on a rerun L-01's tab holds a pane with an unknown cwd (maybe another project's): init does not pass that
// tab for L-02, which goes to a new tab of the project's workspace; nothing is split.
func TestOborInitRerunPutsS01InANewTabBesideAMixedO01Tab(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
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
	has(t, calls, "label=lanes workspace_id="+ows, "a new tab in the project's workspace")
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
	x := newFakeGitInitFx(t, false)
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

// A config init finds without project.agent_prefix (the hc trial's `{"version": 2, "project": {"terminal": "hc"}}`)
// gets one, derived as for a new config, so the lead has a name; --prefix wins; a config with a prefix keeps it.
func TestOborInitFillsTheMissingPrefixOfAFoundConfig(t *testing.T) {
	t.Parallel()
	const found = `{"version": 2, "project": {"terminal": "hc"}}` + "\n"
	for _, tc := range []struct {
		name, config string
		args         []string
		want         string
	}{
		{"derived from the project's name, unique against the running agents", found, nil, ""},
		{"--prefix wins", found, []string{"--prefix", "abc"}, "abc"},
		{"a prefix the config has is kept", `{"version": 2, "project": {"name": "mine", "agent_prefix": "keep"}}` + "\n",
			[]string{"--prefix", "abc"}, "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := newFakeGitInitFx(t, true)
			want := tc.want
			if want == "" {
				base := filepath.Base(x.Root)
				x.h.Agent("w9:a1", defaultPrefix(base)+"-lead", "idle", "/elsewhere").Label = defaultPrefix(base) + "-lead"
				want = defaultPrefix(base) + "2"
			}
			x.Write(".obor/config.json", tc.config)
			out := x.init(t, tc.args...)
			cfg, err := config.Load(x.Root)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Prefix() != want {
				t.Errorf("prefix %q, want %q", cfg.Prefix(), want)
			}
			if got := cfg.AgentName("lead", ""); got != want+"-lead" {
				t.Errorf("the lead's name %q, want %s-lead", got, want)
			}
			if _, fails := cfg.Validate(config.ValidateOptions{}); len(fails) > 0 {
				t.Errorf("config validate fails: %v", fails)
			}
			if got := summary(out, "project"); !strings.Contains(got, "(prefix "+want+")") {
				t.Errorf("project step %q does not name the prefix %s", got, want)
			}
			wrote := strings.Contains(out, "config project.agent_prefix = "+want)
			if wrote != (tc.want != "keep") {
				t.Errorf("the prefix write line present = %v in\n%s", wrote, out)
			}
			if tc.want == "keep" && cfg.Name() != "mine" {
				t.Errorf("project.name %q, want the config's mine", cfg.Name())
			}
			if tc.want != "keep" && cfg.Data.Project.Terminal != "hc" {
				t.Errorf("project.terminal %q, want the owner's hc", cfg.Data.Project.Terminal)
			}
		})
	}
}
