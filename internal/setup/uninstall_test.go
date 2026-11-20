package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
)

// uninstallGit is the fake git of obor init's tests with the reads and writes obor uninstall adds on obor's refs: the
// refs under refs/obor/. A branch's ancestry is never read (serve's reply decides which are merged): rev-list fails.
type uninstallGit struct {
	*fakeGit
	mu   sync.Mutex
	refs map[string]string // refs/obor/… → sha
}

func (g *uninstallGit) Git(dir string, args ...string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case len(args) > 0 && args[0] == "rev-list":
		return "", fmt.Errorf("git %s: uninstall reads no ancestry", strings.Join(args, " "))
	case len(args) == 3 && args[0] == "for-each-ref" && args[2] == "refs/obor/":
		var lines []string
		for r, sha := range g.refs {
			lines = append(lines, r+" "+sha)
		}
		slices.Sort(lines)
		return strings.Join(lines, "\n"), nil
	case len(args) == 4 && args[0] == "update-ref" && args[1] == "-d":
		if g.refs[args[2]] != args[3] {
			return "", fmt.Errorf("ref %s is not at %s", args[2], args[3])
		}
		delete(g.refs, args[2])
		return "", nil
	}
	return g.fakeGit.Git(dir, args...)
}

// fakePruner is port.Pruner over the fake git's worktrees and a list of task branches.
type fakePruner struct {
	mu       sync.Mutex
	git      *fakeGit
	root     string
	branches []port.PruneBranch
	dirt     map[string]string // worktree dir → why it must stay
	checked  map[string]bool   // branches checked out in the main checkout
	gone     []string          // registered worktrees whose dir is gone
	removed  []string
	deleted  []string
	pruned   []string
}

func (p *fakePruner) Branches(context.Context) ([]port.PruneBranch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.branches), nil
}

func (p *fakePruner) CheckedOut(context.Context) (map[string]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checked, nil
}

func (p *fakePruner) DeleteBranch(_ context.Context, b port.PruneBranch) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleted = append(p.deleted, b.Name)
	p.branches = slices.DeleteFunc(p.branches, func(x port.PruneBranch) bool { return x.Name == b.Name })
	return nil
}

func (p *fakePruner) Trees(context.Context) ([]port.PruneTree, error) {
	p.git.mu.Lock()
	defer p.git.mu.Unlock()
	var ts []port.PruneTree
	for _, w := range p.git.repoFor(p.root).worktrees {
		ts = append(ts, port.PruneTree{Dir: w, SHA: strings.Repeat("a", 40)})
	}
	for _, g := range p.gone {
		ts = append(ts, port.PruneTree{Dir: g, SHA: strings.Repeat("b", 40), Gone: true})
	}
	return ts, nil
}

func (p *fakePruner) Dirt(_ context.Context, dir string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirt[dir], nil
}

func (p *fakePruner) RemoveTree(_ context.Context, dir string) error {
	p.mu.Lock()
	p.removed = append(p.removed, dir)
	p.mu.Unlock()
	p.git.mu.Lock()
	defer p.git.mu.Unlock()
	return p.git.repoFor(p.root).removeWorktree(dir)
}

func (p *fakePruner) PruneTrees(_ context.Context, dirs []string) map[string]error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruned = append(p.pruned, dirs...)
	p.gone = nil
	return map[string]error{}
}

// fakeDown is the Teardown of the tests: serve and the agents as a few calls it counts, the pruner above.
type fakeDown struct {
	mu          sync.Mutex
	open        string
	known       bool
	merged      []string // the task branches serve's structured prune reply lists as merged
	mergedErr   error    // serve's prune reply is not read
	agents      []string // "stop", "stop force" or "dry", one per call
	serve       []string
	agentsState string // the state the next calls answer ("": done once, then already)
	pr          *fakePruner
}

func (d *fakeDown) teardown() *Teardown {
	return &Teardown{
		Open: func(context.Context, *config.Config) (string, bool) { return d.open, d.known },
		Merged: func(context.Context, *config.Config) ([]port.PruneBranch, error) {
			var out []port.PruneBranch
			for _, name := range d.merged {
				out = append(out, port.PruneBranch{Name: name, SHA: strings.Repeat("c", 40)})
			}
			return out, d.mergedErr
		},
		Agents: func(_ context.Context, _ *config.Config, force, dry bool) (string, string, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			what := "stop"
			switch {
			case dry:
				what = "dry"
			case force:
				what = "stop force"
			}
			state := "done"
			if len(d.agents) > 0 {
				state = "already"
			}
			if !dry {
				d.agents = append(d.agents, what)
			}
			return state, what, nil
		},
		Serve: func(_ context.Context, _ *config.Config, dry bool) (string, string, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			state := "done"
			if len(d.serve) > 0 {
				state = "already"
			}
			if !dry {
				d.serve = append(d.serve, "down")
			}
			return state, "serve", nil
		},
		Pruner: func(*config.Config) port.Pruner { return d.pr },
	}
}

// uninstallFx is an initialised project (obor init ran on the fake git: two lanes, the hooks, the plugin, the panes)
// to uninstall.
type uninstallFx struct {
	*initFx
	git  *uninstallGit
	down *fakeDown
}

func newUninstallFx(t *testing.T) *uninstallFx {
	t.Helper()
	x := newFakeGitInitFx(t, true)
	g := &uninstallGit{fakeGit: x.fake, refs: map[string]string{}}
	x.host = g
	x.init(t)
	u := &uninstallFx{initFx: x, git: g}
	u.down = &fakeDown{known: true, pr: &fakePruner{git: x.fake, root: x.Root, dirt: map[string]string{},
		checked: map[string]bool{}}}
	return u
}

// uninstall is `obor uninstall args` in the main checkout.
func (u *uninstallFx) uninstall(t *testing.T, args ...string) (string, int) {
	t.Helper()
	s, out := u.newSetup(t)
	s.Dir = u.Root
	s.Down = u.down.teardown()
	rc := s.Run(context.Background(), append([]string{"obor-uninstall"}, args...))
	return out.String(), rc
}

// states is the summary's step → state ("kept" for kept: <why>).
func states(out string) map[string]string {
	got := map[string]string{}
	in := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "obor uninstall ") {
			in = true
			continue
		}
		f := strings.Fields(l)
		if !in || !strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "      ") || len(f) < 2 {
			continue
		}
		got[f[0]] = strings.TrimSuffix(f[1], ":")
	}
	return got
}

func (u *uninstallFx) branch(name string) {
	u.down.pr.branches = append(u.down.pr.branches, port.PruneBranch{Name: name, SHA: strings.Repeat("c", 40)})
}

func (u *uninstallFx) worktrees() []string {
	u.fake.mu.Lock()
	defer u.fake.mu.Unlock()
	return slices.Clone(u.fake.repoFor(u.Root).worktrees)
}

// Each step reports done on the first run and already on the second; config and state are kept both times.
func TestUninstallStepsAreDoneThenAlready(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.branch("task/T-1")
	u.down.merged = []string{"task/T-1"}
	u.git.refs["refs/obor/candidate/T-1"] = strings.Repeat("d", 40)
	if n := len(u.worktrees()); n < 2 {
		t.Fatalf("precondition: %d worktrees after obor init, want the lanes", n)
	}
	if hp, _ := u.fake.Git(u.Root, "config", "--local", "--get", "core.hooksPath"); strings.TrimSpace(hp) == "" {
		t.Fatal("precondition: core.hooksPath unset after obor init")
	}
	if !strings.Contains(u.Read(filepath.Join(".claude", "settings.json")), "enabledPlugins") {
		t.Fatal("precondition: the plugin is not enabled")
	}
	if !slices.ContainsFunc(project.Projects(u.Env(u.env...)), func(p project.Project) bool { return p.Root != "" }) {
		t.Fatal("precondition: obor init registered no project")
	}

	out, rc := u.uninstall(t)
	if rc != 0 {
		t.Fatalf("first run: exit %d\n%s", rc, out)
	}
	first := states(out)
	for _, step := range []string{"agents", "serve", "panes", "worktrees", "branches", "githooks", "plugin", "plugin-links",
		"projects"} {
		if first[step] != "done" {
			t.Errorf("first run: %s is %q, want done\n%s", step, first[step], out)
		}
	}
	if first["config"] != "kept" || first["state"] != "kept" {
		t.Errorf("first run: config %q, state %q, want kept both\n%s", first["config"], first["state"], out)
	}
	if got := u.worktrees(); len(got) != 0 {
		t.Errorf("worktrees left: %v", got)
	}
	if hp, err := u.fake.Git(u.Root, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Errorf("core.hooksPath still %q", hp)
	}
	if !slices.Equal(u.down.pr.deleted, []string{"task/T-1"}) {
		t.Errorf("deleted branches %v, want task/T-1", u.down.pr.deleted)
	}
	has(t, out, "deleted task/T-1 (was "+strings.Repeat("c", 7)+")", "the branch line with its tip")
	has(t, out, "deleted refs/obor/candidate/T-1 (was "+strings.Repeat("d", 7)+")", "the ref line with its sha")
	if len(u.git.refs) != 0 {
		t.Errorf("refs left: %v", u.git.refs)
	}
	has(t, u.claudeCalls(), "plugin uninstall obor@", "claude's call")
	has(t, u.claudeCalls(), "--scope project", "claude's call")
	if !u.Exists(".obor/config.json") {
		t.Error("the config is gone")
	}
	if len(project.Projects(u.Env(u.env...))) != 0 {
		t.Errorf("the project is still listed: %v", project.Projects(u.Env(u.env...)))
	}

	out, rc = u.uninstall(t)
	if rc != 0 {
		t.Fatalf("second run: exit %d\n%s", rc, out)
	}
	for step, state := range states(out) {
		want := "already"
		if step == "config" || step == "state" {
			want = "kept"
		}
		if state != want {
			t.Errorf("second run: %s is %q, want %s\n%s", step, state, want, out)
		}
	}
	if len(states(out)) < 12 {
		t.Errorf("second run lists %d steps\n%s", len(states(out)), out)
	}
}

// --dry-run lists the same steps in the same states and changes nothing.
func TestUninstallDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.branch("task/T-1")
	u.down.merged = []string{"task/T-1"}
	u.git.refs["refs/obor/candidate/T-1"] = strings.Repeat("d", 40)
	trees := u.worktrees()
	panes := len(u.h.Panes)
	projects := project.Projects(u.Env(u.env...))
	hooks, _ := u.fake.Git(u.Root, "config", "--local", "--get", "core.hooksPath")
	claude := u.claudeCalls()
	u.WriteFileCheck(t)

	dry, rc := u.uninstall(t, "--dry-run")
	if rc != 0 {
		t.Fatalf("dry run: exit %d\n%s", rc, dry)
	}
	has(t, dry, "OK uninstall proj (dry run: nothing changed)", "the dry run's last line")
	has(t, dry, "would delete task/T-1 (was ccccccc)", "the branch line")
	if got := u.worktrees(); !slices.Equal(got, trees) {
		t.Errorf("worktrees %v, want %v", got, trees)
	}
	if got := len(u.h.Panes); got != panes {
		t.Errorf("%d panes, want %d", got, panes)
	}
	if got := project.Projects(u.Env(u.env...)); !slices.Equal(got, projects) {
		t.Errorf("projects %v, want %v", got, projects)
	}
	if got, _ := u.fake.Git(u.Root, "config", "--local", "--get", "core.hooksPath"); got != hooks {
		t.Errorf("core.hooksPath %q, want %q", got, hooks)
	}
	if u.claudeCalls() != claude {
		t.Errorf("claude called: %s", u.claudeCalls())
	}
	if len(u.down.agents)+len(u.down.serve)+len(u.down.pr.deleted)+len(u.down.pr.removed) != 0 || len(u.git.refs) != 1 {
		t.Errorf("a dry run stopped or deleted: %+v refs %v", u.down, u.git.refs)
	}

	real, rc := u.uninstall(t)
	if rc != 0 {
		t.Fatalf("real run: exit %d\n%s", rc, real)
	}
	if d, r := states(dry), states(real); fmt.Sprint(d) != fmt.Sprint(r) {
		t.Errorf("dry run states %v, real run %v", d, r)
	}
}

// WriteFileCheck asserts the config the run keeps is there (a precondition of the keep assertions).
func (u *uninstallFx) WriteFileCheck(t *testing.T) {
	t.Helper()
	if !u.Exists(".obor/config.json") {
		t.Fatal("precondition: no config")
	}
}

// A task in progress refuses with nothing changed; --force goes on.
func TestUninstallRefusesOpenTasks(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.down.open = "T-7 (L-01, working)"
	out, rc := u.uninstall(t)
	if rc != 1 || !strings.Contains(out, "FAIL obor uninstall refused, nothing changed:") ||
		!strings.Contains(out, "tasks in progress: T-7 (L-01, working)") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if len(u.down.agents)+len(u.down.serve) != 0 || len(u.worktrees()) < 2 {
		t.Errorf("a refusal changed something: %+v, %d worktrees", u.down, len(u.worktrees()))
	}
	out, rc = u.uninstall(t, "--force")
	if rc != 0 || !slices.Equal(u.down.agents, []string{"stop force"}) {
		t.Fatalf("--force: exit %d, agents %v\n%s", rc, u.down.agents, out)
	}
}

// A dirty worktree refuses with nothing changed; --discard removes it too.
func TestUninstallRefusesADirtyWorktree(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	lane := u.worktrees()[0]
	u.down.pr.dirt[lane] = "dirty (2 paths, as a.go)"
	out, rc := u.uninstall(t)
	if rc != 1 || !strings.Contains(out, "worktree "+lane+": dirty (2 paths, as a.go)") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if len(u.down.agents) != 0 || len(u.worktrees()) < 2 {
		t.Errorf("a refusal changed something: %+v", u.down)
	}
	out, rc = u.uninstall(t, "--discard")
	if rc != 0 || len(u.worktrees()) != 0 {
		t.Fatalf("--discard: exit %d, worktrees %v\n%s", rc, u.worktrees(), out)
	}
}

// A task branch serve's reply does not list as merged refuses, whatever git's ancestry says (it is never read: the fake
// git fails a rev-list); --discard deletes it with its tip; a listed one never refuses.
func TestUninstallRefusesABranchServeDoesNotListAsMerged(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.branch("task/T-1")
	u.branch("task/T-2")
	u.down.merged = []string{"task/T-1"}
	out, rc := u.uninstall(t)
	if rc != 1 || !strings.Contains(out, "branch task/T-2 (ccccccc): serve's prune does not list it as merged") ||
		strings.Contains(out, "branch task/T-1") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if len(u.down.pr.deleted) != 0 || len(u.down.agents) != 0 {
		t.Errorf("a refusal changed something: %+v", u.down)
	}
	out, rc = u.uninstall(t, "--discard")
	if rc != 0 || !slices.Equal(u.down.pr.deleted, []string{"task/T-1", "task/T-2"}) {
		t.Fatalf("--discard: exit %d, deleted %v\n%s", rc, u.down.pr.deleted, out)
	}
	has(t, out, "deleted task/T-2 (was ccccccc)", "the discarded branch's line")
}

// A reply serve's prune cannot give (an older serve, a failed call) leaves every branch unknown: refused, naming why.
func TestUninstallRefusesEveryBranchWhenTheReplyIsNotRead(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.branch("task/T-1")
	u.down.mergedErr = errors.New("prune: reply is not structured")
	out, rc := u.uninstall(t)
	if rc != 1 || !strings.Contains(out, "branch task/T-1 (ccccccc): serve's prune could not say if it is merged "+
		"(prune: reply is not structured)") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if len(u.down.pr.deleted) != 0 {
		t.Errorf("deleted %v", u.down.pr.deleted)
	}
}

// A branch checked out in the main checkout stays, listed; a worktree directory that is gone loses its registration.
func TestUninstallKeepsTheMainCheckoutsBranchAndPrunesMissingTrees(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.branch("task/T-3")
	u.down.pr.checked["task/T-3"] = true
	missing := filepath.Join(filepath.Dir(u.worktrees()[0]), "L-09")
	u.down.pr.gone = []string{missing}
	out, rc := u.uninstall(t)
	if rc != 0 {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	has(t, out, "kept task/T-3: checked out in the main checkout", "the kept branch")
	if len(u.down.pr.deleted) != 0 {
		t.Errorf("deleted %v", u.down.pr.deleted)
	}
	if !slices.Equal(u.down.pr.pruned, []string{missing}) {
		t.Errorf("registrations pruned %v, want only %s", u.down.pr.pruned, missing)
	}
}

// Only the project's panes close: another project's pane in the same tab, a pane with no label and a pane
// of this project's name used elsewhere stay; the pane the command runs in stays too.
func TestUninstallClosesOnlyTheProjectsPanes(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	var mine []string
	for _, p := range u.h.Panes {
		if p.Label != "" {
			mine = append(mine, p.ID)
		}
	}
	if len(mine) < 4 {
		t.Fatalf("precondition: obor init labelled %d panes, want the lanes' and the roles'", len(mine))
	}
	tab := u.h.Pane(mine[0]).Tab
	u.h.Shell("w1:other", "zz-o-01", "/elsewhere/zz", func(p *herdrtest.Pane) { p.Tab = tab }) // another project's, same tab
	u.h.Shell("w1:plain", "", u.Root, func(p *herdrtest.Pane) { p.Tab = tab })
	own := mine[len(mine)-1]
	u.env = append(u.env, "OBOR_PANE="+own)
	var before []string
	for _, p := range u.h.Panes {
		before = append(before, p.ID)
	}

	out, rc := u.uninstall(t)
	if rc != 0 {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	var left []string
	for _, p := range u.h.Panes {
		left = append(left, p.ID)
	}
	want := slices.DeleteFunc(slices.Clone(before), func(id string) bool { return slices.Contains(mine, id) && id != own })
	if !slices.Equal(left, want) {
		t.Errorf("panes left %v, want %v\n%s", left, want, out)
	}
	for _, id := range []string{"w1:other", "w1:plain", own} {
		if !slices.Contains(left, id) {
			t.Errorf("pane %s was closed", id)
		}
	}
	has(t, out, "pane "+own+" runs this command", "the spared pane")
	if got := len(before) - len(left); got != len(mine)-1 {
		t.Errorf("%d panes closed, want the project's %d but the caller's", got, len(mine)-1)
	}
}

// --purge removes the store and the other gitignored obor files; without it the store stays; the config stays either
// way.
func TestUninstallPurge(t *testing.T) {
	t.Parallel()
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprint("purge=", purge), func(t *testing.T) {
			u := newUninstallFx(t)
			u.Write(".obor/state/store.db", "rows")
			u.Write(".obor/drafts/x.md", "draft")
			u.Write(".obor/state/stopped", "marker")
			args := []string{}
			if purge {
				args = append(args, "--purge")
			}
			out, rc := u.uninstall(t, args...)
			if rc != 0 {
				t.Fatalf("exit %d\n%s", rc, out)
			}
			if !u.Exists(".obor/config.json") {
				t.Error("the config is gone")
			}
			if u.Exists(".obor/state/stopped") {
				t.Error("obor stop's marker stays: obor init would refuse")
			}
			for _, f := range []string{".obor/state/store.db", ".obor/drafts/x.md"} {
				if got := u.Exists(f); got == purge {
					t.Errorf("--purge=%v: %s exists = %v", purge, f, got)
				}
			}
			want := map[bool]string{false: "kept", true: "done"}[purge]
			if got := states(out)["state"]; got != want {
				t.Errorf("state step %q, want %q\n%s", got, want, out)
			}
			has(t, out, map[bool]string{false: "--purge removes it", true: ".obor/state"}[purge], "the state line")
		})
	}
}

// A core.hooksPath that is not obor's stays; a plugin enabled in a tracked settings file stays.
func TestUninstallLeavesWhatIsNotObors(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	if _, err := u.fake.Git(u.Root, "config", "--local", "core.hooksPath", "/the/projects/own/hooks"); err != nil {
		t.Fatal(err)
	}
	out, rc := u.uninstall(t)
	if rc != 0 {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if got := states(out)["githooks"]; got != "kept" {
		t.Errorf("githooks %q, want kept\n%s", got, out)
	}
	if hp, _ := u.fake.Git(u.Root, "config", "--local", "--get", "core.hooksPath"); strings.TrimSpace(hp) != "/the/projects/own/hooks" {
		t.Errorf("core.hooksPath %q", hp)
	}
}

// A worker's uninstall refuses; the usage line prints for a bad flag.
func TestUninstallRefusesAWorkerAndABadFlag(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	out, rc := u.uninstall(t, "--bogus")
	if rc != 1 || !strings.Contains(out, UninstallUsage) {
		t.Errorf("exit %d\n%s", rc, out)
	}
	u.env = append(u.env, "OBOR_ROLE=worker")
	out, rc = u.uninstall(t)
	if rc != 1 || !strings.Contains(out, "not a worker's") {
		t.Errorf("a worker's uninstall: exit %d\n%s", rc, out)
	}
	if len(u.down.agents) != 0 {
		t.Error("a worker's uninstall stopped agents")
	}
}

// changed is what a refused or dry uninstall must leave alone, as a comparable string: the worktrees, the panes, the
// hooks, the project list, the store file, and what serve and the agents were asked.
func (u *uninstallFx) changed() string {
	hooks, _ := u.fake.Git(u.Root, "config", "--local", "--get", "core.hooksPath")
	return fmt.Sprint(u.worktrees(), len(u.h.Panes), hooks, project.Projects(u.Env(u.env...)), u.Exists(".obor/state/store.db"),
		u.down.agents, u.down.serve, u.down.pr.deleted, u.down.pr.removed, u.git.refs)
}

const serveDownFail = "FAIL serve is not running, so open tasks cannot be checked: obor start, then obor uninstall " +
	"(or --force to go ahead)"

// Serve not answering leaves the open tasks unknown: refused with the way out, nothing changed, --dry-run and --purge
// included; --force goes on.
func TestUninstallRefusesWhenServeCannotSayWhatIsOpen(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.Write(".obor/state/store.db", "rows")
	u.down.known = false
	u.down.open = "T-7 (L-01, working)" // what a serve that answered would say: never read while it does not
	before := u.changed()
	if len(u.worktrees()) < 2 || !u.Exists(".obor/state/store.db") {
		t.Fatalf("precondition: nothing set up to lose: %s", before)
	}
	for _, args := range [][]string{nil, {"--dry-run"}, {"--purge"}, {"--discard"}, {"--dry-run", "--purge"}} {
		out, rc := u.uninstall(t, args...)
		if rc != 1 || !strings.Contains(out, serveDownFail) || strings.Contains(out, "obor uninstall:") {
			t.Fatalf("%v: exit %d\n%s", args, rc, out)
		}
		if got := u.changed(); got != before {
			t.Fatalf("%v changed something:\nbefore %s\nafter  %s", args, before, got)
		}
	}

	out, rc := u.uninstall(t, "--force")
	if rc != 0 || !slices.Equal(u.down.agents, []string{"stop force"}) || len(u.worktrees()) != 0 {
		t.Fatalf("--force: exit %d, agents %v, worktrees %v\n%s", rc, u.down.agents, u.worktrees(), out)
	}
	has(t, out, "OK uninstall proj;", "the run's last line")
}

// With serve down a branch is never known merged: refused without --discard (serve's reply is not asked for), so --force
// alone keeps the work.
func TestUninstallForceWithServeDownKeepsUnknownBranches(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	u.branch("task/T-1")
	u.down.known = false
	u.down.merged = []string{"task/T-1"} // not asked: serve does not answer
	out, rc := u.uninstall(t, "--force")
	if rc != 1 || !strings.Contains(out, "branch task/T-1 (ccccccc): serve's prune could not say if it is merged "+
		"(serve is not running)") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if len(u.down.pr.deleted) != 0 || len(u.worktrees()) < 2 {
		t.Errorf("a refusal changed something: %+v", u.down)
	}
	if _, rc = u.uninstall(t, "--force", "--discard"); rc != 0 || !slices.Equal(u.down.pr.deleted, []string{"task/T-1"}) {
		t.Errorf("--force --discard: exit %d, deleted %v", rc, u.down.pr.deleted)
	}
}

// A project already uninstalled has nothing to lose: with serve down every step is already, and --purge, which would
// remove the store, is refused unless --force.
func TestUninstallOfAnUninstalledProjectNeedsNoServe(t *testing.T) {
	t.Parallel()
	u := newUninstallFx(t)
	if out, rc := u.uninstall(t); rc != 0 {
		t.Fatalf("first run: exit %d\n%s", rc, out)
	}
	u.down.known = false
	for _, args := range [][]string{nil, {"--dry-run"}} {
		out, rc := u.uninstall(t, args...)
		if rc != 0 {
			t.Fatalf("%v: exit %d\n%s", args, rc, out)
		}
		for step, state := range states(out) {
			want := "already"
			if step == "config" || step == "state" {
				want = "kept"
			}
			if state != want {
				t.Errorf("%v: %s is %q, want %s\n%s", args, step, state, want, out)
			}
		}
	}
	u.Write(".obor/state/store.db", "rows")
	before := u.changed()
	out, rc := u.uninstall(t, "--purge")
	if rc != 1 || !strings.Contains(out, serveDownFail) || u.changed() != before {
		t.Fatalf("--purge: exit %d\n%s", rc, out)
	}
	if out, rc = u.uninstall(t, "--purge", "--force"); rc != 0 || u.Exists(".obor/state/store.db") {
		t.Errorf("--purge --force: exit %d, store exists %v\n%s", rc, u.Exists(".obor/state/store.db"), out)
	}
}

// setTerminal is project.terminal of the kept config.
func (u *uninstallFx) setTerminal(t *testing.T, kind string) {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(u.Read(".obor/config.json")), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["project"].(map[string]any)["terminal"] = kind
	b, _ := json.Marshal(cfg)
	u.Write(".obor/config.json", string(b))
}

// The caller's pane is the terminal kind's own: on an hc project a shell with only HC_TERMINAL_ID is spared (the dry run
// says so, a real run leaves it open); on a herdr project that variable names nobody.
func TestUninstallSparesTheCallersPaneByTheKindsOwnVariable(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind, env string
		spared    bool
	}{
		{"hc", "HC_TERMINAL_ID=", true},
		{"hc", "OBOR_PANE=", true},
		{"herdr", "HERDR_PANE_ID=", true},
		{"herdr", "HC_TERMINAL_ID=", false},
	} {
		t.Run(c.kind+" "+c.env, func(t *testing.T) {
			u := newUninstallFx(t)
			u.setTerminal(t, c.kind)
			var own string
			for _, p := range u.h.Panes {
				if p.Label != "" {
					own = p.ID
				}
			}
			if own == "" {
				t.Fatal("precondition: obor init labelled no pane")
			}
			u.env = append(u.env, c.env+own)
			out, rc := u.uninstall(t, "--dry-run")
			if rc != 0 {
				t.Fatalf("dry run: exit %d\n%s", rc, out)
			}
			if got := strings.Contains(out, "pane "+own+" runs this command"); got != c.spared {
				t.Errorf("dry run says spared = %v, want %v\n%s", got, c.spared, out)
			}
			if _, rc := u.uninstall(t); rc != 0 {
				t.Fatalf("exit %d", rc)
			}
			if got := u.h.Pane(own) != nil; got != c.spared {
				t.Errorf("caller's pane open = %v, want %v", got, c.spared)
			}
		})
	}
}
