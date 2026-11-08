//go:build integration

// The integration tier of setup_test.go (T-1021): tests whose subject is the git state setup leaves (the lane and
// role worktrees git made, init's uncommitted files, what git ignores). Real git: AGENTS.md's internal/setup rule.

package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// bash setup.test.sh case_init_creates_config_docs_and_gitignore_uncommitted
func TestInitCreatesConfigDocsAndGitignoreUncommitted(t *testing.T) {
	t.Parallel()
	x := bareGit(t)
	head := x.Git(x.Root, "rev-parse", "HEAD")
	if x.Exists(".obor/config.json") || x.Exists("docs/agents") || x.Exists("docs/tasks") {
		t.Fatal("precondition: no config, no agents docs, no tasks dir")
	}
	out, err := x.projectStep(t, "My Proj", "mp")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatalf("config loads: %v", err)
	}
	if _, fails := cfg.Validate(config.ValidateOptions{}); len(fails) > 0 {
		t.Errorf("config validates: %v", fails)
	}
	d := cfg.Data
	if d.Name != "My Proj" || d.Prefix != "mp" || d.LanesDir != "../proj-wt" || d.Lanes != 0 {
		t.Errorf("name, prefix, lanes_dir filled, no lanes: %q %q %q %v", d.Name, d.Prefix, d.LanesDir, d.Lanes)
	}
	if _, ok := cfg.Command("lane_setup"); ok || len(d.LaneLinks) != 0 {
		t.Errorf("the example's lane_setup and lane_links cleared: %v", d.LaneLinks)
	}
	if !regexp.MustCompile(`(?m)^  "lanes": 0,`).MatchString(x.Read(".obor/config.json")) {
		t.Errorf("config at indent 2:\n%s", x.Read(".obor/config.json"))
	}
	// T-470: BACKLOG.md and docs/decisions.md instead of a STATUS file (paths.status is gone).
	for _, h := range []string{"## Handoff", "## Owner decisions pending", "## Open", "## Concerns", "## Ideas"} {
		has(t, x.Read("BACKLOG.md"), "\n"+h+"\n", "BACKLOG.md section "+h)
	}
	has(t, x.Read("docs/decisions.md"), "## Decisions on record", "docs/decisions.md copied")
	if x.Exists("docs/STATUS.md") || strings.Contains(out, "STATUS") {
		t.Errorf("init wrote a STATUS file:\n%s", out)
	}
	// T-547: paths.tasks is deprecated and nothing writes specs there: init creates no tasks dir.
	if x.Exists("docs/tasks") {
		t.Error("init created docs/tasks")
	}
	has(t, out, "created .obor/config.json (name My Proj, prefix mp, no lanes)", "the config said")

	// T-204: role texts stay in the kit; the project gets short stubs (title, pointer, "This project" prompts).
	texts, _ := filepath.Glob(filepath.Join(kit, "templates", "agents", "*.md"))
	n := 0
	for _, f := range texts {
		name := filepath.Base(f)
		if name == "README.md" {
			continue
		}
		n++
		b, _ := os.ReadFile(f)
		title, _, _ := strings.Cut(string(b), "\n")
		doc := x.Read("docs/agents/" + name)
		if first, _, _ := strings.Cut(doc, "\n"); first != title+" — My Proj specifics" {
			t.Errorf("%s: the kit title, project filled: %q", name, first)
		}
		has(t, doc, "Role text: `templates/agents/"+name+"` in the obor kit, read in place", name+" pointer")
		has(t, doc, "## This project", name+": the kit's This project section")
		if len(doc) >= 1500 || len(doc) >= len(b) {
			t.Errorf("%s is a short stub: %d chars (kit %d)", name, len(doc), len(b))
		}
	}
	if n < 5 {
		t.Errorf("precondition: role texts found: %d", n)
	}
	hasNot(t, x.Read("docs/agents/worker.md"), "## Rules", "worker stub: no copy of the kit's rules")
	has(t, x.Read("docs/agents/triage.md"), "mp-triage", "prefix filled in a stub")
	if x.Exists("docs/agents/README.md") {
		t.Error("the kit README is read in place, not copied")
	}
	has(t, x.Read("docs/agents/task-spec.md"), "## Protected files this task may edit", "the task spec template copied")
	hasNot(t, x.Read("docs/agents/task-spec.md"), "Files you may touch", "no files section (T-496)")
	if x.Exists("docs/agents/roster.md") { // T-774: the rosters are gone; obor status and obor web show who runs
		t.Error("init copied docs/agents/roster.md")
	}
	filepath.Walk(x.Path("docs"), func(p string, fi os.FileInfo, _ error) error {
		if b, _ := os.ReadFile(p); !fi.IsDir() && strings.Contains(string(b), "{{") {
			t.Errorf("placeholder left in %s", p)
		}
		return nil
	})
	gi := x.Read(".gitignore")
	for _, l := range []string{".obor/state/", ".obor/drafts/", ".obor/local.json"} {
		if count(gi, l) != 1 {
			t.Errorf(".gitignore has %s once:\n%s", l, gi)
		}
	}
	if count(gi, ".obor/pause") != 0 { // T-465: the pause is serve's fact, no file
		t.Errorf(".gitignore has no .obor/pause:\n%s", gi)
	}
	// T-459: no queue lock any more (the queue is the store); the fixture's own .task-done.md line is left alone.
	if count(gi, "QUEUE.md.lock") != 0 {
		t.Errorf(".gitignore has no QUEUE.md.lock:\n%s", gi)
	}
	if got := x.Git(x.Root, "rev-parse", "HEAD"); got != head {
		t.Error("something committed")
	}
	if strings.Contains(out, "QUEUE.md") {
		t.Errorf("init does not say to commit a queue file:\n%s", out)
	}
	st := x.Git(x.Root, "status", "--porcelain")
	has(t, st, "?? .obor/", "config left untracked")
	has(t, st, "M .gitignore", ".gitignore left modified")
}

// T-731: init ignores the reviews dir (paths.reviews, default docs/reviews): a broad `git add -A` stages no open
// review, which `obor review close` would refuse.
func TestInitGitignoresTheReviewsDir(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, reviews string
		fresh         bool
	}{
		{"default path", "", true},
		{"custom path", "notes/rv", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var x *fx
			if c.fresh {
				x = bareGit(t)
			} else {
				x = newGitFx(t, testkit.Set("paths.reviews", c.reviews))
			}
			want := "docs/reviews/"
			if c.reviews != "" {
				want = c.reviews + "/"
			}
			if count(x.Read(".gitignore"), strings.TrimSuffix(want, "/")) != 0 {
				t.Fatalf("precondition: %s not ignored:\n%s", want, x.Read(".gitignore"))
			}
			out, err := x.projectStep(t, "", "")
			if err != nil {
				t.Fatalf("project step: %v\n%s", err, out)
			}
			has(t, out, ".gitignore += "+want, "the reviews dir said")
			if count(x.Read(".gitignore"), want) != 1 {
				t.Errorf("%s once:\n%s", want, x.Read(".gitignore"))
			}
			if _, err := gitIn(x.Root, "check-ignore", "-q", strings.TrimSuffix(want, "/")+"/RV-1.md"); err != nil {
				t.Errorf("a review file is ignored (%v):\n%s", err, x.Read(".gitignore"))
			}
		})
	}
}

// bash setup.test.sh case_lane_adds_config_entry_worktree_links_and_runs_lane_setup; T-765: the lane is the next
// L-NN and raises lanes by one.
func TestLaneAddsConfigWorktreeLinksAndRunsLaneSetup(t *testing.T) {
	t.Parallel()
	x := newGitFx(t, testkit.Set("lane_links", []any{"ref", "assets/img", "missing"}),
		testkit.Set("commands.lane_setup", "pwd > setup-ran"))
	x.Write("ref/a", "")
	x.Write("assets/img/b", "")
	lane := x.LaneDir("L-01")
	if pre, _ := config.Load(x.Root); x.Exists(lane) || pre.Data.Lanes != 0 {
		t.Fatal("precondition: no lane dir, no lanes")
	}
	out := x.ok(t, "lane", "L-01")
	if want := "OK lane L-01 at " + lane; last(out) != want {
		t.Errorf("last line %q, want %q", last(out), want)
	}
	has(t, out, "config lanes = 1 (uncommitted)", "the count raised")
	cfg, _ := config.Load(x.Root)
	if cfg.Data.Lanes != 1 || !cfg.HasLane("L-01") {
		t.Errorf("lanes = %d", cfg.Data.Lanes)
	}
	if !regexp.MustCompile(`(?m)^  "lanes": 1`).MatchString(x.Read(".obor/config.json")) {
		t.Error("config kept at indent 2")
	}
	if x.Git(lane, "rev-parse", "HEAD") != x.Git(x.Root, "rev-parse", "main") {
		t.Error("worktree at main")
	}
	if x.Git(lane, "branch", "--show-current") != "" {
		t.Error("worktree detached")
	}
	for _, l := range []string{"ref", "assets/img"} {
		if got, _ := os.Readlink(filepath.Join(lane, l)); got != x.Path(l) {
			t.Errorf("link %s: %q", l, got)
		}
	}
	has(t, out, "warning: lane_links missing", "a missing link source said")
	if x.Read(lane+"/setup-ran") != lane+"\n" {
		t.Error("lane_setup ran in the lane")
	}
	has(t, x.Git(x.Root, "status", "--porcelain"), "M .obor/config.json", "config edit left uncommitted")

	os.Remove(lane + "/setup-ran")
	out = x.ok(t, "lane", "L-01")
	for _, want := range []string{"skip worktree " + lane + " (exists)", "skip link ref (exists)",
		"skip config lanes (L-01 is lane 1 of 1)"} {
		has(t, out, want, "rerun")
	}
	if !x.Exists(lane + "/setup-ran") {
		t.Error("lane_setup reruns")
	}
	if cfg, _ = config.Load(x.Root); cfg.Data.Lanes != 1 {
		t.Errorf("lanes after rerun: %d", cfg.Data.Lanes)
	}
}

// T-853: the reviewer and the deep dive run in their role worktree <lanes_dir>/<role> (T-718 had them in the main
// checkout): setup role adds it detached when missing, keeps one that is there, refuses a dir that is no worktree of
// the repo, and leaves a leftover of the old names (review, deep-dive) alone. No lane is added.
func TestReviewerAndDeepDiveRunInTheirRoleWorktree(t *testing.T) {
	testkit.Slow(t)
	t.Parallel()
	for _, c := range []struct {
		role, old, agent, line, env string
		there                       bool // the role worktree is there before setup role
		edit                        func(map[string]any)
	}{
		{"reviewer", "review", "pj-rev", "OK reviewer: pj-rev in pane w1:n1", "{OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=reviewer,OPENCODE_CONFIG=}",
			false, testkit.Set("roles", `{"reviewer": "luna"}`)},
		{"reviewer", "review", "pj-rev", "OK reviewer: pj-rev in pane w1:n1", "{OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=reviewer,OPENCODE_CONFIG=}",
			true, testkit.Set("roles", `{"reviewer": "luna"}`)},
		{"deep_dive", "deep-dive", "pj-deep-dive", "OK deep_dive: pj-deep-dive in pane w1:n1", "{OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=deep_dive,OPENCODE_CONFIG=}",
			false, testkit.Set("roles", `{"deep_dive": "luna"}`)},
	} {
		x := newGitFx(t, c.edit)
		wt, old := x.LaneDir(c.role), x.LaneDir(c.old)
		x.Git(x.Root, "worktree", "add", "-q", "--detach", old, "main") // a leftover of before T-718
		if c.there {
			x.Git(x.Root, "worktree", "add", "-q", "--detach", wt, "main")
		}
		if x.Exists(wt) != c.there {
			t.Fatalf("precondition: %s there = %v, want %v", wt, x.Exists(wt), c.there)
		}
		out := x.ok(t, "role", c.role, "--tab", "w1:t1", "--start")
		if last(out) != c.line {
			t.Errorf("%s: last line %q", c.role, last(out))
		}
		if top := strings.TrimSpace(x.Git(wt, "rev-parse", "--show-toplevel")); project.Physical(top) != project.Physical(wt) {
			t.Errorf("%s: %s is no worktree after setup role (toplevel %q)", c.role, wt, top)
		}
		if b := strings.TrimSpace(x.Git(wt, "rev-parse", "--abbrev-ref", "HEAD")); b != "HEAD" {
			t.Errorf("%s: its worktree is on %q, want detached", c.role, b)
		}
		if !x.Exists(old) {
			t.Errorf("%s: the leftover %s was removed", c.role, old)
		}
		has(t, strings.Join(x.reqs(), "\n"), "pane.split cwd="+wt+" direction=right env="+c.env+" target_pane_id=w1:p1", c.role)
		// Codex writes its cwd only: the start adds the main checkout's reviews dir, made when missing.
		reviews := filepath.Join(x.Root, "docs", "reviews")
		has(t, strings.Join(x.reqs(), "\n"), "agent.start args=[-s workspace-write -a never --no-daemon -m gpt-6-luna --add-dir "+reviews+" ", c.role+" start")
		has(t, strings.Join(x.reqs(), "\n"), "kind=codex name="+c.agent+" pane_id=w1:n1", c.role+" start: the agent")
		if !x.Exists(reviews) {
			t.Errorf("%s: no %s made for its start", c.role, reviews)
		}
		if cfg, _ := config.Load(x.Root); cfg.Data.Lanes != 0 {
			t.Errorf("%s: added to the lanes", c.role)
		}
	}
	// Another role's Codex start adds no dir.
	y := newGitFx(t, testkit.Set("roles", `{"integrator": "luna"}`))
	y.ok(t, "role", "integrator", "--tab", "w1:t1", "--start")
	if calls := strings.Join(y.reqs(), "\n"); !strings.Contains(calls, "agent.start args=[-s workspace-write") || strings.Contains(calls, "--add-dir") {
		t.Errorf("the integrator's codex start:\n%s", calls)
	}

	x := newGitFx(t, testkit.Set("roles", `{"reviewer": "luna"}`))
	x.Write(x.LaneDir("reviewer")+"/notes.txt", "not a worktree\n")
	x.failing(t, "FAIL "+x.LaneDir("reviewer")+" exists and is not a worktree of "+x.Root, "role", "reviewer", "--tab", "w1:t1", "--start")
}

// bash setup.test.sh case_role_integrator_starts_in_the_root_and_needs_a_type; T-853: in its role worktree
// <lanes_dir>/integrator (not the merge scratch, where gates run), added detached when missing.
func TestRoleIntegratorStartsInItsWorktreeAndNeedsAType(t *testing.T) {
	testkit.Slow(t)
	t.Parallel()
	x := newGitFx(t)
	x.h.Tab("w1:t1", 200, 50, "w1:p3")
	x.failing(t, `FAIL roles.integrator is not set (a model ref, e.g. "opus:high")`, "role", "integrator", "--pane", "w1:p3", "--start")
	x.Config(testkit.Set("roles", `{"integrator": "opus"}`))
	x.reset()
	out := x.ok(t, "role", "integrator", "--pane", "w1:p3", "--start")
	if last(out) != "OK integrator: pj-int in pane w1:p3" {
		t.Errorf("last line %q", last(out))
	}
	want := []string{"agent.list", "pane.list", "pane.process_info pane_id=w1:p3", "<env line>",
		"<env wait>", "pane.rename label=pj-int pane_id=w1:p3",
		"agent.get target=w1:p3",
		"agent.start args=[--permission-mode auto --model opus " + settings + " --session-id NEW] kind=claude name=pj-int pane_id=w1:p3"}
	got := x.reqs()
	for i, r := range got {
		switch {
		case strings.HasPrefix(r, "pane.send_input"):
			got[i] = "<env line>"
		case strings.HasPrefix(r, "pane.wait_for_output"):
			got[i] = "<env wait>"
		}
	}
	if named(strings.Join(got, "\n")) != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(got, "\n"))
	}
	wt := x.LaneDir("integrator")
	if top := strings.TrimSpace(x.Git(wt, "rev-parse", "--show-toplevel")); project.Physical(top) != project.Physical(wt) {
		t.Errorf("the integrator's worktree %s is no worktree after setup role (toplevel %q)", wt, top)
	}
	if lines := x.envLines(); len(lines) != 1 || !strings.HasPrefix(lines[0], "cd "+wt+" && ") {
		t.Errorf("typed %q, want the env line to cd the integrator's worktree", lines)
	}
	if x.Exists(x.LaneDir("merge")) {
		t.Errorf("setup role made the merge scratch: it is serve's")
	}

}

// T-731, T-843: obor setup role reviewer and deep_dive add paths.reviews to an existing project's .gitignore, once: an
// init made before the line was ignored leaves it out (init's own case is TestInitGitignoresTheReviewsDir).
func TestRoleSetupGitignoresTheReviewsDir(t *testing.T) {
	t.Parallel()
	x := newGitFx(t, testkit.Set("roles", `{"reviewer": "opus", "deep_dive": "opus"}`))
	if count(x.Read(".gitignore"), "docs/reviews") != 0 {
		t.Fatalf("precondition: the reviews dir not ignored:\n%s", x.Read(".gitignore"))
	}
	out := x.ok(t, "role", "reviewer", "--new-tab", "review")
	has(t, out, ".gitignore += docs/reviews/", "the reviewer's setup said")
	if count(x.Read(".gitignore"), "docs/reviews/") != 1 {
		t.Errorf("docs/reviews/ once:\n%s", x.Read(".gitignore"))
	}
	if _, err := gitIn(x.Root, "check-ignore", "-q", "docs/reviews/RV-1.md"); err != nil {
		t.Errorf("a review file is ignored (%v):\n%s", err, x.Read(".gitignore"))
	}
	gi := x.Read(".gitignore")
	out = x.ok(t, "role", "deep_dive", "--new-tab", "deep-dive")
	hasNot(t, out, ".gitignore +=", "the deep dive adds nothing")
	if x.Read(".gitignore") != gi {
		t.Errorf("gitignore unchanged:\n%s", x.Read(".gitignore"))
	}
}

// T-731, T-843: a reviews line already there, with or without its trailing slash, is left alone (the line is git's
// either way): setup role reviewer adds no second line.
func TestRoleSetupLeavesAnExistingReviewsLineAlone(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"docs/reviews/", "docs/reviews"} {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			x := newGitFx(t, testkit.Set("roles", `{"reviewer": "opus"}`))
			x.Write(".gitignore", x.Read(".gitignore")+line+"\n")
			gi := x.Read(".gitignore")
			if count(gi, line) != 1 {
				t.Fatalf("precondition: %s there once:\n%s", line, gi)
			}
			out := x.ok(t, "role", "reviewer", "--new-tab", "review")
			hasNot(t, out, ".gitignore +=", "an existing line is not added again")
			if x.Read(".gitignore") != gi {
				t.Errorf("gitignore unchanged:\n%s", x.Read(".gitignore"))
			}
			if _, err := gitIn(x.Root, "check-ignore", "-q", "docs/reviews/RV-1.md"); err != nil {
				t.Errorf("a review file is ignored (%v):\n%s", err, x.Read(".gitignore"))
			}
		})
	}
}

// gitIn is git -C dir args…: its trimmed stdout and error (testkit's Git fails the test instead).
func gitIn(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
