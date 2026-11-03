package setup

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
	"sort"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/testkit"
)

func TestMain(m *testing.M) {
	// obor init commits a fresh repository's files (T-629): git needs an identity in the tests' config-less environment.
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "obor test", "GIT_AUTHOR_EMAIL": "obor-test@example.invalid",
		"GIT_COMMITTER_NAME": "obor test", "GIT_COMMITTER_EMAIL": "obor-test@example.invalid"} {
		os.Setenv(k, v)
	}
	testkit.Main(m)
}

// kit is this checkout: the templates, githooks and herdr-plugin setup reads.
var kit = func() string { d, _ := filepath.Abs("../.."); return d }()

type fx struct {
	*testkit.Project
	h      *herdrtest.Fake
	srv    *herdrtest.Server // the herdr socket setup talks to
	mark   int               // reqs starts here (reset)
	env    []string          // more env for the runs ("VAR=value")
	screen string            // what pane read shows of every pane
	kit    string            // the kit setup reads ("": this checkout)
	// mcpCheck is obor mcp --check (nil: a handshake that works)
	mcpCheck func(project.Env) (string, error)
	rollout  time.Duration // a fresh Codex start's rollout search (0: 20 ms)
	// open is the files processes hold open (launch.Launcher.Open; nil: none)
	open func(pids []int) []string
	// serve is Setup.Serve (obor init's serve install; nil: none)
	serve func(context.Context, *config.Config) (string, string, error)
	// shellWait is Setup.ShellWait (0: 10 s), for tests that count on the bound
	shellWait time.Duration
}

// newFx is a project with a config (prefix pj, lanes_dir ../proj-wt, the built-in models; edits on top) and a fake
// herdr: tab w1:t1 with pane w1:p1, tab w1:t2 with w1:q1 (200×50 each),
// both shells of this project (workspace w1, cwd the root: the project's own tabs, T-632).
func newFx(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	x := bare(t)
	x.Config(edits...)
	return x
}

// bare is newFx without a config.
func bare(t *testing.T) *fx {
	t.Helper()
	h := herdrtest.New()
	h.Tab("w1:t1", 200, 50, "w1:p1")
	h.Tab("w1:t2", 200, 50, "w1:q1")
	x := &fx{Project: testkit.New(t), h: h, srv: herdrtest.Serve(t, h)}
	for _, p := range h.Panes {
		p.Workspace, p.Cwd = "w1", x.Root
	}
	// A screen applies to every pane read, whatever pane it names (the fake's per-pane Screen is empty here).
	x.srv.Before(func(r herdrtest.Request) {
		if x.screen == "" || r.Method != "pane.read" {
			return
		}
		var p struct {
			Pane string `json:"pane_id"`
		}
		if json.Unmarshal(r.Params, &p) == nil {
			x.h.Set(p.Pane, func(p *herdrtest.Pane) { p.Screen = x.screen })
		}
	})
	return x
}

// run is `obor setup <args>` in the main checkout: its output and exit code.
func (x *fx) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return x.runIn(t, x.Root, args...)
}

// runIn is `obor setup <args>` with dir as the caller's cwd: its output and exit code.
func (x *fx) runIn(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	s, out := x.newSetup(t)
	s.Dir = dir
	rc := s.Run(context.Background(), args)
	return out.String(), rc
}

// newSetup is the Setup runIn builds, with its own output buffer (nil: none), for a start `obor setup` does not run
// itself: the owner's drive agent (obor drive, StartDrive).
func (x *fx) newSetup(t *testing.T) (*Setup, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	k := kit
	if x.kit != "" {
		k = x.kit
	}
	check := x.mcpCheck
	if check == nil {
		check = func(project.Env) (string, error) { return "MCP OK (-: no tools)", nil }
	}
	rollout := x.rollout
	if rollout == 0 {
		rollout = 20 * time.Millisecond
	}
	open := x.open
	if open == nil {
		open = func([]int) []string { return nil }
	}
	env, h := x.Env(x.env...), x.srv.Client()
	return &Setup{Kit: k, Env: env, H: h, Out: &out, McpCheck: check, ShellWait: x.shellWait,
		Starter: func(cfg *config.Config) port.Starter {
			return &launch.Launcher{Cfg: cfg, Env: env, Kit: k, H: h, Out: &out, Rollout: rollout, Open: open}
		}, Serve: x.serve}, &out
}

// reqs is the requests the fake herdr socket got since reset, each as req's one line, e.g. "pane.split cwd=/w
// direction=right env={OBOR_AGENT=pj-l-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker} target_pane_id=w1:p1".
func (x *fx) reqs() []string {
	all := x.srv.Requests()
	var out []string
	for _, r := range all[min(x.mark, len(all)):] {
		out = append(out, req(r))
	}
	return out
}

// reset starts the request log over: reqs then shows only later requests.
func (x *fx) reset() { x.mark = len(x.srv.Requests()) }

// req is one request as one line: its method, then its params by name (a map's keys and a list's items sorted, so a
// line is the same however herdr's JSON orders them).
func req(r herdrtest.Request) string {
	var m map[string]any
	if len(r.Params) == 0 || json.Unmarshal(r.Params, &m) != nil || len(m) == 0 {
		return r.Method
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+param(m[k]))
	}
	return r.Method + " " + strings.Join(parts, " ")
}

// param is one request param as a log line reads it: a string bare, a list as [a b] and a map as {k=v,…}, else JSON.
func param(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		items := make([]string, 0, len(v))
		for _, x := range v {
			items = append(items, param(x))
		}
		return "[" + strings.Join(items, " ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		items := make([]string, 0, len(keys))
		for _, k := range keys {
			items = append(items, k+"="+param(v[k]))
		}
		return "{" + strings.Join(items, ",") + "}"
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprint(v)
}

// noAgents: herdr runs no agent (tab panes stay, at their shells, labels kept; agent-only panes go).
func (x *fx) noAgents() {
	var kept []*herdrtest.Pane
	for _, p := range x.h.Panes {
		if p.Tab != "" {
			p.Kind, p.Name, p.Status = "", "", ""
			kept = append(kept, p)
		}
	}
	x.h.Panes = kept
}

// running: herdr runs these agents (name, cwd), each in a pane of its own; no other.
func (x *fx) running(agents ...[2]string) {
	x.noAgents()
	for i, a := range agents {
		x.h.Agent(fmt.Sprintf("w1:x%d", i+1), a[0], "idle", a[1])
	}
}

// projectStep is the project step of `obor init` (initProject calls it too): the files it writes, its lines, or the
// error.
func (x *fx) projectStep(t *testing.T, name, prefix string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	k := kit
	if x.kit != "" {
		k = x.kit
	}
	s := &Setup{Kit: k, Dir: x.Root, Env: x.Env(), H: x.srv.Client(), Out: &out}
	if err := s.findRoot(); err != nil {
		t.Fatal(err)
	}
	err := s.initProject(name, prefix)
	return out.String(), err
}

// ok runs and wants exit 0.
func (x *fx) ok(t *testing.T, args ...string) string {
	t.Helper()
	out, rc := x.run(t, args...)
	if rc != 0 {
		t.Fatalf("setup %v: exit %d\n%s", args, rc, out)
	}
	return out
}

// failing runs and wants exit 1 and the FAIL line want last.
func (x *fx) failing(t *testing.T, want string, args ...string) string {
	t.Helper()
	out, rc := x.run(t, args...)
	if rc != 1 || last(out) != want {
		t.Fatalf("setup %v: exit %d, last line %q, want %q\n%s", args, rc, last(out), want, out)
	}
	return out
}

func last(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

func has(t *testing.T, text, want, what string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("%s: no %q in\n%s", what, want, text)
	}
}

func hasNot(t *testing.T, text, unwanted, what string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Errorf("%s: %q in\n%s", what, unwanted, text)
	}
}

func count(text, line string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

// --- init -------------------------------------------------------------------------------------------------------------

// bash setup.test.sh case_init_creates_config_docs_and_gitignore_uncommitted
func TestInitCreatesConfigDocsAndGitignoreUncommitted(t *testing.T) {
	t.Parallel()
	x := bare(t)
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
		if name == "README.md" || name == "roster.md" {
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
	has(t, x.Read("docs/agents/roster.md"), "mp-l-01", "prefix filled")
	has(t, x.Read("docs/agents/roster.md"), "../proj-wt/L-01", "lanes_dir filled")
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

// bash setup.test.sh case_init_never_overwrites_and_is_idempotent
func TestInitNeverOverwritesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	x := bare(t)
	x.Write("docs/agents/lead.md", "mine\n")
	x.Write(".gitignore", x.Read(".gitignore")+".obor/state/") // no trailing newline: appended lines must not glue on
	out, err := x.projectStep(t, "", "pj")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	has(t, out, "skip docs/agents/lead.md (exists)", "existing doc said")
	if x.Read("docs/agents/lead.md") != "mine\n" {
		t.Error("existing role doc kept")
	}
	gi := x.Read(".gitignore")
	if count(gi, ".obor/local.json") != 1 || count(gi, ".obor/state/") != 1 {
		t.Errorf("no repeat, new line after the unterminated one:\n%s", gi)
	}

	x.Write("docs/agents/worker.md", "edited\n")
	x.Write(".obor/config.json", strings.Replace(x.Read(".obor/config.json"), `"name": "proj"`, `"name": "kept"`, 1))
	if !strings.Contains(x.Read(".obor/config.json"), `"name": "kept"`) {
		t.Fatal("precondition: name edited")
	}
	out, err = x.projectStep(t, "other", "zz")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	has(t, out, "skip .obor/config.json (exists)", "config skipped")
	has(t, out, "skip BACKLOG.md (exists)", "backlog skipped")
	has(t, out, "skip docs/decisions.md (exists)", "decisions skipped")
	cfg, _ := config.Load(x.Root)
	if cfg.Data.Name != "kept" || cfg.Data.Prefix != "pj" {
		t.Errorf("config not overwritten: %q %q", cfg.Data.Name, cfg.Data.Prefix)
	}
	if x.Read("docs/agents/worker.md") != "edited\n" {
		t.Error("role doc not overwritten")
	}
	if x.Read(".gitignore") != gi {
		t.Errorf("gitignore unchanged on rerun:\n%s", x.Read(".gitignore"))
	}
	hasNot(t, out, "created", "nothing created on rerun")
}

// T-459 (was bash setup.test.sh case_init_copies_queue_to_paths_queue_once): init copies no queue file; T-470: there
// is no queue, and no paths.queue.
func TestInitCopiesNoQueueFile(t *testing.T) {
	t.Parallel()
	x := bare(t)
	if _, err := os.Stat(filepath.Join(kit, "templates", "QUEUE.md")); !os.IsNotExist(err) {
		t.Fatalf("precondition: the kit's templates/QUEUE.md is deleted (%v)", err)
	}
	out, err := x.projectStep(t, "", "pj")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	hasNot(t, out, "QUEUE.md", "init says nothing about a queue file")
	if x.Exists("docs/QUEUE.md") {
		t.Error("init wrote no docs/QUEUE.md")
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatalf("config loads: %v", err)
	}
	if got := cfg.Paths().Get("queue"); got != "" {
		t.Errorf("paths.queue = %q, want none (T-470)", got)
	}
}

// bash setup.test.sh case_init_adds_task_run_to_gitignore_once, case_init_adds_lane_links_to_gitignore_once
// T-459: the queue lock line is gone (the queue is obor serve's store; there is no docs/QUEUE.md to lock).
func TestInitAddsGitignoreLinesOnce(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lane_links", []any{"reference", "public/assets"}))
	x.Write(".gitignore", x.Read(".gitignore")+"reference\n")
	for _, l := range []string{".task-run", "public/assets"} {
		if count(x.Read(".gitignore"), l) != 0 {
			t.Fatalf("precondition: %s not ignored", l)
		}
	}
	out, err := x.projectStep(t, "", "")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	for _, l := range []string{".task-run", "public/assets"} {
		has(t, out, ".gitignore += "+l, "said")
	}
	hasNot(t, out, ".gitignore += reference", "an existing line not reported")
	hasNot(t, out, ".gitignore += docs/QUEUE.md.lock", "no queue lock line")
	gi := x.Read(".gitignore")
	for _, l := range []string{".task-run", "public/assets", "reference", ".obor/state/"} {
		if count(gi, l) != 1 {
			t.Errorf("%s once:\n%s", l, gi)
		}
	}
	if count(gi, "QUEUE.md.lock") != 0 {
		t.Errorf("no queue lock line:\n%s", gi)
	}
	if again, err := x.projectStep(t, "", ""); err != nil {
		t.Fatalf("rerun: %v\n%s", err, again)
	} else {
		hasNot(t, again, ".gitignore +=", "nothing added on rerun")
	}
	if x.Read(".gitignore") != gi {
		t.Error("gitignore unchanged on rerun")
	}
	if _, err := gitIn(x.Root, "check-ignore", "-q", "public/assets"); err != nil {
		t.Error("public/assets ignored by git")
	}
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
				x = bare(t)
			} else {
				x = newFx(t, testkit.Set("paths.reviews", c.reviews))
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

// T-731: obor setup role reviewer and deep_dive add paths.reviews to an existing project's .gitignore, once: init made
// before the line was ignored leaves it out.
func TestRoleSetupGitignoresTheReviewsDir(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("roles", `{"reviewer": "opus", "deep_dive": "opus"}`))
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

// T-731: a reviews line already there, with or without its trailing slash, is left alone (the line is git's either way).
func TestRoleSetupLeavesAnExistingReviewsLineAlone(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"docs/reviews/", "docs/reviews"} {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, testkit.Set("roles", `{"reviewer": "opus"}`))
			x.Write(".gitignore", x.Read(".gitignore")+line+"\n")
			gi := x.Read(".gitignore")
			out := x.ok(t, "role", "reviewer", "--new-tab", "review")
			hasNot(t, out, ".gitignore +=", "an existing line is not added again")
			if x.Read(".gitignore") != gi {
				t.Errorf("gitignore unchanged:\n%s", x.Read(".gitignore"))
			}
		})
	}
}

// T-310: init no longer writes the .obor-plugin ignore line T-309 added: the opencode marker lives in the state dir
// (which init already ignores), so no project needs the line.
func TestInitDoesNotWriteThePluginMarkerLine(t *testing.T) {
	t.Parallel()
	x := bare(t)
	out, err := x.projectStep(t, "", "pj")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	hasNot(t, out, ".obor-plugin", "init said nothing about .obor-plugin")
	if count(x.Read(".gitignore"), ".obor-plugin") != 0 {
		t.Errorf(".gitignore names .obor-plugin:\n%s", x.Read(".gitignore"))
	}
	if count(x.Read(".gitignore"), ".obor/state/") != 1 {
		t.Errorf(".obor/state/ still ignored once:\n%s", x.Read(".gitignore"))
	}
}

// bash setup.test.sh case_init_refuses_a_bad_prefix
func TestInitRefusesABadPrefix(t *testing.T) {
	t.Parallel()
	x := bare(t)
	_, err := x.projectStep(t, "", "Bad_Prefix")
	if err == nil || err.Error() != "bad prefix Bad_Prefix (must match ^[a-z][a-z0-9-]{0,7}$)" {
		t.Fatalf("err %v", err)
	}
	if x.Exists(".obor/config.json") {
		t.Error("no config written")
	}
}

func TestDefaultPrefix(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"obor-kit": "obor", "My Proj": "mypr", "2048 game": "game", "42": "obor",
		"axis-alies": "axis"} {
		if got := defaultPrefix(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

// T-609: `obor setup`'s init subcommand is gone (obor init sets a project up): the usage line names no init, and
// the old init subcommand is unknown.
func TestSetupUsageNamesNoInit(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, "FAIL "+Usage(), "init", "--prefix", "pj")
	hasNot(t, Usage(), "init", "the usage line")
}

// --- lane -------------------------------------------------------------------------------------------------------------

// bash setup.test.sh case_lane_adds_config_entry_worktree_links_and_runs_lane_setup; T-765: the lane is the next
// L-NN and raises lanes by one.
func TestLaneAddsConfigWorktreeLinksAndRunsLaneSetup(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lane_links", []any{"ref", "assets/img", "missing"}),
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

// T-765: lanes are L-01 … L-NN in order: setup lane takes a configured lane or the next one, nothing else, and one
// name only (no type).
func TestLaneTakesOnlyTheNextLNN(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	for _, name := range []string{"O-01", "L-02", "review"} {
		x.failing(t, "FAIL lanes are L-01 … L-NN in order (lanes: 0): the next one is L-01", "lane", name)
		if x.Exists(x.LaneDir(name)) {
			t.Errorf("worktree %s made", name)
		}
	}
	x.ok(t, "lane", "L-01")
	x.failing(t, "FAIL lanes are L-01 … L-NN in order (lanes: 1): the next one is L-02", "lane", "L-03")
	x.ok(t, "lane", "L-02")
	if cfg, _ := config.Load(x.Root); cfg.Data.Lanes != 2 {
		t.Errorf("lanes = %d, want 2", cfg.Data.Lanes)
	}
	for _, args := range [][]string{{"lane"}, {"lane", "L-03", "opus"}} {
		if out, rc := x.run(t, args...); rc != 1 || !strings.Contains(last(out), "usage: obor setup lane <L-NN> |") {
			t.Errorf("%q: exit %d, %q", args, rc, last(out))
		}
	}
}

// T-765: a lane pane without --start is labelled and starts nothing; with --start it runs the default kind with no
// model (no dispatch names one), as obor restart does.
func TestPaneOnALaneStartsTheDefaultKindWithoutADispatch(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.reset()
	out := x.ok(t, "pane", "L-01", "--tab", "w1:t1")
	has(t, out, "no agent started", "a pane without --start")
	got := strings.Join(x.reqs(), "\n")
	has(t, got, "pane.split", "the pane is made")
	has(t, got, "OBOR_LANE=L-01", "the lane's env")
	if strings.Contains(got, "agent.start") {
		t.Errorf("no agent starts: %s", got)
	}
	x.reset()
	x.ok(t, "pane", "L-01", "--pane", "w1:n1", "--start")
	has(t, strings.Join(x.reqs(), "\n"), "agent.start args=["+laneArgs+"] kind=claude name=pj-l-01", "the default kind, no model")
}

// T-770: the owner's drive agent (role driver) is a lane's agent too: its Claude start is strict — obor's MCP server
// alone, the user's MCP servers out of its context on every job.
func TestDriveStartIsStrict(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.reset()
	s, out := x.newSetup(t)
	s.Dir = x.Root
	if err := s.StartDrive(context.Background(), Drive{Agent: "pj-l-01-drive", Lane: "L-01", Cwd: x.LaneDir("L-01"),
		Prompt: "drive T-1"}); err != nil {
		t.Fatalf("StartDrive: %v\n%s", err, out)
	}
	reqs := strings.Join(x.reqs(), "\n")
	has(t, reqs, "OBOR_ROLE=driver", "the drive agent's role")
	has(t, reqs, "agent.start args=["+laneArgs+" -- drive T-1] kind=claude name=pj-l-01-drive", "a strict start, obor's server alone")
}

// bash setup.test.sh case_lane_refuses_unknown_type_type_change_and_failing_setup: a failing lane_setup fails the lane.
func TestLaneRefusesAFailingSetup(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("commands.lane_setup", "echo boom; exit 3"))
	out := x.failing(t, "FAIL lane_setup exited 3 in "+x.LaneDir("L-01")+": echo boom; exit 3", "lane", "L-01")
	has(t, out, "boom", "setup output shown")
}

// T-189: lane and role names share one runtime namespace; refused before any worktree or herdr call. T-765: lane
// agents are <prefix>-l-NN and role agents <prefix>-<role>, so only a lane row's agent (a name the store recorded)
// can collide.
func TestLaneAndRoleCollisionsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		lanes int
		args  []string
		want  string
	}{
		{"a new lane whose row names a role agent", 0, []string{"lane", "L-01"},
			"FAIL lane L-01 agent pj-rev collides with a role agent"},
		{"a role agent a lane's row names", 1, []string{"role", "reviewer", "--new-tab", "review"},
			"FAIL role reviewer agent pj-rev collides with a configured lane agent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, testkit.Set("roles.reviewer", "opus"), testkit.Set("lanes", c.lanes))
			x.State("L-01", map[string]any{"lane": "L-01", "agent": "pj-rev"})
			x.failing(t, c.want, c.args...)
			if x.Exists(x.LaneDir("L-01")) {
				t.Error("worktree L-01 made")
			}
			if calls := x.reqs(); len(calls) > 0 {
				t.Errorf("herdr calls: %v", calls)
			}
		})
	}
}

// --- pane -------------------------------------------------------------------------------------------------------------

const settings = `--settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`

// laneArgs is a lane agent's start args before its first prompt (T-770): obor's MCP server, then the strict flag that
// hides every other one, then the settings.
var laneArgs = "--mcp-config " + claude.MCPConfig(kit) + " --strict-mcp-config " + settings

// bash setup.test.sh case_pane_splits_the_tab_and_starts_the_agent_in_order
func TestPaneSplitsTheTabAndStartsTheAgentInOrder(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	lane := x.LaneDir("L-01")
	out := x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	if last(out) != "OK L-01: pj-l-01 in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	want := []string{"agent.list", "pane.list", "pane.layout pane_id=w1:p1",
		"pane.split cwd=" + lane + " direction=right env={OBOR_AGENT=pj-l-01,OBOR_LANE=L-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker} target_pane_id=w1:p1",
		"pane.rename label=pj-l-01 pane_id=w1:n1", "agent.get target=pj-l-01", "agent.get target=w1:n1", "agent.get target=w1:n1",
		"agent.start args=[" + laneArgs + "] kind=claude name=pj-l-01 pane_id=w1:n1"}
	if got := x.reqs(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// STATUS 2026-10-13: hk-int got a 1-line pane: the largest pane is split (not the last), right when wide, else down.
func TestPaneSplitsTheLargestPane(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.ok(t, "lane", "L-02")
	x.h.Panes = nil
	x.h.Tab("w1:t1", 100, 50, "w1:p1")
	x.h.Tab("w1:t1", 100, 2, "w1:p2")
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	has(t, strings.Join(x.reqs(), "\n"), "direction=down env={OBOR_AGENT=pj-l-01,OBOR_LANE=L-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker} target_pane_id=w1:p1", "the big pane, not the last one; tall → down")

	x.h.Panes = nil
	x.h.Tab("w1:t1", 50, 20, "w1:p1")
	x.h.Tab("w1:t1", 150, 20, "w1:p2")
	x.ok(t, "pane", "--start", "L-02", "--tab", "w1:t1")
	has(t, strings.Join(x.reqs(), "\n"), "direction=right env={OBOR_AGENT=pj-l-02,OBOR_LANE=L-02,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker} target_pane_id=w1:p2", "wide → right")
}

// T-557: panes of the same area go to the last one, as before; 50×20 and 100×10 are both 1000 cells.
func TestPaneSplitsTheLastOfTiedPanes(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.h.Panes = nil
	x.h.Tab("w1:t1", 50, 20, "w1:p1")
	x.h.Tab("w1:t1", 100, 10, "w1:p2")
	p1, p2 := x.h.Pane("w1:p1"), x.h.Pane("w1:p2")
	if p1.W*p1.H != p2.W*p2.H {
		t.Fatalf("precondition: areas %d and %d differ", p1.W*p1.H, p2.W*p2.H)
	}
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	has(t, strings.Join(x.reqs(), "\n"), "direction=right env={OBOR_AGENT=pj-l-01,OBOR_LANE=L-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker} target_pane_id=w1:p2", "equal areas → the last pane, not the first; wide → right")
}

// bash setup.test.sh case_pane_new_tab_creates_a_tab_with_cwd_and_env
func TestPaneNewTabCreatesATabWithCwdAndEnv(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
	lane := x.LaneDir("L-01")
	out := x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
	if last(out) != "OK L-01: pj-l-01 in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	want := []string{"agent.list", "pane.list",
		"tab.create cwd=" + lane + " env={OBOR_AGENT=pj-l-01,OBOR_LANE=L-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker} focus=false label=workers workspace_id=w1",
		"pane.rename label=pj-l-01 pane_id=w1:n1", "agent.get target=pj-l-01", "agent.get target=w1:n1", "agent.get target=w1:n1",
		"agent.start args=[-s workspace-write -a never -m gpt-6-luna -c check_for_update_on_startup=false -c mcp_servers.obor.command=\"" + kit + "/bin/obor\" -c mcp_servers.obor.args=[\"mcp\"] -c mcp_servers.obor.env_vars=[\"OBOR_ROLE\",\"OBOR_AGENT\",\"OBOR_LANE\",\"HERDR_PANE_ID\",\"OBOR_STATE_HOME\"] -c mcp_servers.obor.env={OBOR_NO_SELF_HOST=\"1\"} -c mcp_servers.obor.default_tools_approval_mode=\"approve\"] kind=codex name=pj-l-01 pane_id=w1:n1",
		"pane.process_info pane_id=w1:n1"} // the fresh Codex start's pid: the rollout it holds is its session (T-588)
	if got := x.reqs(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// T-708: setup role labels the pane and starts no agent; --start starts one.
func TestSetupRoleStartsNoAgentWithoutStart(t *testing.T) {
	t.Parallel()
	count := func(x *fx) int { return strings.Count(strings.Join(x.reqs(), "\n"), "agent.start") }
	x := newFx(t, testkit.Set("roles", `{"reviewer": "opus"}`))
	out := x.ok(t, "role", "reviewer", "--new-tab", "review")
	has(t, strings.Join(x.reqs(), "\n"), "pane.rename label=pj-rev", "the pane is labelled")
	has(t, out, "no agent started (started on first use; --start starts it now)", "the no-agent line")
	if n := count(x); n != 0 {
		t.Errorf("role without --start: %d agent.start, want 0", n)
	}
	y := newFx(t, testkit.Set("roles", `{"reviewer": "opus"}`))
	y.ok(t, "role", "reviewer", "--new-tab", "review", "--start")
	if n := count(y); n != 1 {
		t.Errorf("role with --start: %d agent.start, want 1", n)
	}
}

// A new tab goes to the project's workspace (w1: its panes work in the root), not herdr's active one (T-137) and not
// the caller's when that holds no pane of the project (T-632).
func TestNewTabInTheProjectsWorkspace(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("roles", `{"reviewer": "opus"}`))
	x.ok(t, "role", "reviewer", "--new-tab", "review", "--start")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+x.Root+" env={OBOR_AGENT=pj-rev,OBOR_OPENCODE_MODEL=,OBOR_ROLE=reviewer} focus=false label=review workspace_id=w1", "without HERDR_WORKSPACE_ID: the project's")

	x.noAgents()
	x.h.Pane("w1:n1").Label = ""
	x.reset()
	x.env = []string{"HERDR_WORKSPACE_ID=w6"}
	out := x.ok(t, "role", "reviewer", "--new-tab", "review", "--start")
	if last(out) != "OK reviewer: pj-rev in pane w1:n2" {
		t.Errorf("last line %q", last(out))
	}
	calls = strings.Join(x.reqs(), "\n")
	has(t, calls, "workspace_id=w1", "the project's workspace")
	hasNot(t, calls, "workspace_id=w6", "not the caller's, no pane of the project there")
}

// bash setup.test.sh case_pane_refuses_a_full_tab
func TestPaneRefusesAFullTab(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("limits.max_panes_per_tab", 3))
	x.ok(t, "lane", "L-01")
	x.h.Tab("w1:t1", 200, 50, "w1:p2")
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	has(t, strings.Join(x.reqs(), "\n"), "pane.split ", "precondition: 2 of 3 panes is not full")

	x.noAgents()
	x.h.Pane("w1:n1").Label = ""
	x.reset()
	x.failing(t, "FAIL tab w1:t1 is full (3 of 3 panes; --new-tab <label>)", "pane", "--start", "L-01", "--tab", "w1:t1")
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "pane.split", "no split")
	hasNot(t, calls, "agent.start", "no agent start")
}

// R-28 §2: the new pane is labelled the agent name before the start; a label another pane holds, or --pane labelled
// for another agent → FAIL before any split or start; a failing rename → FAIL, no start.
// bash setup.test.sh case_pane_label_set_and_refused
func TestPaneLabelSetAndRefused(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	out := x.ok(t, "pane", "--start", "L-01", "--pane", "w1:p1")
	has(t, out, "labelled pane w1:p1 pj-l-01", "said")
	if x.h.Pane("w1:p1").Label != "pj-l-01" {
		t.Error("label set in herdr")
	}
	has(t, strings.Join(x.reqs(), "\n"), "pane.rename label=pj-l-01 pane_id=w1:p1\nagent.get target=pj-l-01\nagent.get target=w1:p1\nagent.get target=w1:p1\nagent.start args=["+laneArgs+"] kind=claude name=pj-l-01 pane_id=w1:p1",
		"rename, the name-held check, then the start")

	x.noAgents()
	if out := x.ok(t, "pane", "--start", "L-01", "--pane", "w1:p1"); last(out) != "OK L-01: pj-l-01 in pane w1:p1" {
		t.Errorf("own label on --pane → fine: %q", last(out))
	}

	x.noAgents()
	x.reset()
	x.failing(t, "FAIL pane w1:p1 is already labelled pj-l-01 (obor restart pj-l-01, or herdr pane rename w1:p1 --clear)",
		"pane", "--start", "L-01", "--tab", "w1:t1")
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "pane.split", "held label → no split")
	hasNot(t, calls, "agent.start", "held label → no start")

	x.h.Pane("w1:p1").Label = ""
	x.h.Pane("w1:q1").Label = "advisor"
	x.reset()
	x.failing(t, "FAIL pane w1:q1 is labelled advisor, not pj-l-01 (herdr pane rename w1:q1 --clear to reuse it)",
		"pane", "--start", "L-01", "--pane", "w1:q1")
	hasNot(t, strings.Join(x.reqs(), "\n"), "agent.start", "foreign label on --pane → no start")

	x.srv.Fail("pane.rename", "pane_not_found", "gone")
	x.reset()
	x.failing(t, "FAIL herdr pane rename w1:p1 pj-l-01 failed", "pane", "--start", "L-01", "--pane", "w1:p1")
	hasNot(t, strings.Join(x.reqs(), "\n"), "agent.start", "rename failed → no start")
}

// bash setup.test.sh case_pane_skips_a_lane_whose_agent_runs
func TestPaneSkipsALaneWhoseAgentRuns(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.running([2]string{"someone", x.LaneDir("L-01")})
	out := x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	if last(out) != "OK L-01: someone already running" {
		t.Errorf("last line %q", last(out))
	}
	if got := x.reqs(); strings.Join(got, ",") != "agent.list" {
		t.Errorf("no split, no start: %v", got)
	}
}

// A failed start reads the pane (T-256: only then, the one launch.Start): a trust dialog or a Codex self-update names
// its fix; herdr's agent_not_ready alone says the agent waits; any other failure is herdr's.
// bash setup.test.sh case_pane_trust_dialog_fails_with_hint, case_pane_codex_self_update_fails_with_hint
func TestPaneNotReadyHints(t *testing.T) {
	t.Parallel()
	notReady := herdrtest.Failure{Code: "agent_not_ready", Message: "blocked during startup"}
	exited := herdrtest.Failure{Code: "agent_exited", Message: "the agent exited"}
	for _, c := range []struct {
		screen string
		fail   herdrtest.Failure
		want   string
	}{
		{"> You are in /x\n  Do you trust the contents of this directory?\n  Trust this folder? 1. Yes  2. No\n", notReady,
			"FAIL agent not ready: a 'Trust this folder?' dialog in pane w1:n1: answer it there (Enter = trust; once per repo)"},
		{"Updating Codex via npm install -g @openai/codex\nUpdated to 0.161.0. Restart Codex.\n$ \n", exited,
			"FAIL agent not ready: codex updated itself and exited in pane w1:n1: run herdr agent start pj-l-01 --kind codex --pane w1:n1 again"},
		{"", notReady, "FAIL agent not ready: pj-l-01 waits at a startup dialog in pane w1:n1: answer it there"},
		{"$ \n", exited, "FAIL herdr agent start pj-l-01 failed (pane w1:n1)"},
	} {
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
		x.screen = c.screen
		x.srv.Fail("agent.start", c.fail.Code, c.fail.Message)
		out := x.failing(t, c.want, "pane", "--start", "L-01", "--tab", "w1:t1")
		has(t, strings.Join(x.reqs(), "\n"), "kind=codex name=pj-l-01 pane_id=w1:n1", "precondition: agent start ran")
		has(t, out, "("+c.fail.Code+")", "herdr's error printed")
	}
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
	x.screen = "Trust this folder?"
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	hasNot(t, strings.Join(x.reqs(), "\n"), "pane.read", "a start herdr took: no screen read")
}

// bash setup.test.sh case_pane_refuses_an_unknown_lane_and_ambiguous_placement
func TestPaneRefusesUnknownLaneAndAmbiguousPlacement(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, "FAIL unknown lane L-09 (lanes: 0; obor setup lane L-09 first)", "pane", "L-09", "--tab", "w1:t1")
	x.ok(t, "lane", "L-01")
	x.failing(t, "FAIL pass at most one of --tab, --new-tab, --pane", "pane", "--start", "L-01", "--tab", "w1:t1", "--new-tab", "x")
	x.ok(t, "lane", "L-02")
	x.ok(t, "pane", "--start", "L-02") // not in a herdr pane: the project's tab with most panes, the first of equals (T-632)
	has(t, strings.Join(x.reqs(), "\n"), "target_pane_id=w1:p1", "no caller: the project's first tab")
	x.reset()
	x.env = []string{"HERDR_PANE_ID=w1:q1"}
	x.ok(t, "pane", "--start", "L-01")
	has(t, strings.Join(x.reqs(), "\n"), "target_pane_id=w1:q1", "no --tab: the caller's tab")
}

// --- role -------------------------------------------------------------------------------------------------------------

// The reviewer and the deep dive run in the main checkout (T-718; before it, T-179: a detached worktree each): setup
// role makes no worktree, and an old one (roles.<role>.worktree, else review / deep-dive) is named with its removal
// line, never removed (T-765: roles.<role>.worktree is gone, so review / deep-dive).
func TestReviewerAndDeepDiveRunInTheRoot(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		role, wt, agent, line, env string
		old                        bool // the old worktree is there
		edit                       func(map[string]any)
	}{
		{"reviewer", "review", "pj-rev", "OK reviewer: pj-rev in pane w1:n1", "{OBOR_AGENT=pj-rev,OBOR_OPENCODE_MODEL=,OBOR_ROLE=reviewer}",
			false, testkit.Set("roles", `{"reviewer": "luna"}`)},
		{"reviewer", "review", "pj-rev", "OK reviewer: pj-rev in pane w1:n1", "{OBOR_AGENT=pj-rev,OBOR_OPENCODE_MODEL=,OBOR_ROLE=reviewer}",
			true, testkit.Set("roles", `{"reviewer": "luna"}`)},
		{"deep_dive", "deep-dive", "pj-deep-dive", "OK deep_dive: pj-deep-dive in pane w1:n1", "{OBOR_AGENT=pj-deep-dive,OBOR_OPENCODE_MODEL=,OBOR_ROLE=deep_dive}",
			true, testkit.Set("roles", `{"deep_dive": "luna"}`)},
	} {
		x := newFx(t, c.edit)
		wt := x.LaneDir(c.wt)
		if c.old {
			x.Git(x.Root, "worktree", "add", "-q", "--detach", wt, "main")
		}
		if x.Exists(wt) != c.old {
			t.Fatalf("precondition: %s worktree there = %v, want %v", c.wt, x.Exists(wt), c.old)
		}
		out := x.ok(t, "role", c.role, "--tab", "w1:t1", "--start")
		if last(out) != c.line {
			t.Errorf("%s: last line %q", c.role, last(out))
		}
		if x.Exists(wt) != c.old {
			t.Errorf("%s: worktree %s there = %v after setup role, want %v (made or removed)", c.role, c.wt, x.Exists(wt), c.old)
		}
		hint := "worktree " + wt + " is unused (the role runs in the main checkout): git -C " + x.Root + " worktree remove " + wt
		if got := strings.Contains(out, hint); got != c.old {
			t.Errorf("%s: removal hint shown = %v, want %v:\n%s", c.role, got, c.old, out)
		}
		has(t, strings.Join(x.reqs(), "\n"), "pane.split cwd="+x.Root+" direction=right env="+c.env+" target_pane_id=w1:p1", c.role)
		has(t, strings.Join(x.reqs(), "\n"), "agent.start args=[-s workspace-write -a never -m gpt-6-luna ", c.role+" start")
		has(t, strings.Join(x.reqs(), "\n"), "kind=codex name="+c.agent+" pane_id=w1:n1", c.role+" start: the agent")
		if cfg, _ := config.Load(x.Root); cfg.Data.Lanes != 0 {
			t.Errorf("%s: added to the lanes", c.role)
		}
	}
}

// bash setup.test.sh case_role_integrator_starts_in_the_root_and_needs_a_type
func TestRoleIntegratorStartsInTheRootAndNeedsAType(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.h.Tab("w1:t1", 200, 50, "w1:p3")
	x.failing(t, `FAIL roles.integrator is not set (a model ref, e.g. "opus:high")`, "role", "integrator", "--pane", "w1:p3", "--start")
	x.Config(testkit.Set("roles", `{"integrator": "opus"}`))
	x.reset()
	out := x.ok(t, "role", "integrator", "--pane", "w1:p3", "--start")
	if last(out) != "OK integrator: pj-int in pane w1:p3" {
		t.Errorf("last line %q", last(out))
	}
	want := []string{"agent.list", "pane.list", "pane.process_info pane_id=w1:p3", "<env line>",
		"pane.read lines=30 pane_id=w1:p3 source=visible strip_ansi=true", "pane.rename label=pj-int pane_id=w1:p3",
		"agent.get target=pj-int", "agent.get target=w1:p3", "agent.get target=w1:p3",
		"agent.start args=[--permission-mode auto --model opus " + settings + "] kind=claude name=pj-int pane_id=w1:p3"}
	got := x.reqs()
	for i, r := range got {
		if strings.HasPrefix(r, "pane.send_input") {
			got[i] = "<env line>"
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(got, "\n"))
	}
}

// envLines is the text typed at a shell since reset (pane.send_input), one per line.
func (x *fx) envLines() []string {
	var out []string
	for _, r := range x.reqs() {
		if _, text, ok := strings.Cut(r, " text="); ok && strings.HasPrefix(r, "pane.send_input") {
			out = append(out, text)
		}
	}
	return out
}

// A reused pane (--pane) gets the env line a restart types, so a shell that holds the env of the agent that ran there
// before (a stale OBOR_AGENT, OBOR_LANE) starts the new agent with its own: cd, exports and unsets, before the start.
func TestSetupPaneTypesTheEnvAtAReusedPane(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("roles", `{"integrator": "opus"}`))
	x.ok(t, "lane", "L-01")
	x.reset()
	x.ok(t, "role", "integrator", "--pane", "w1:p1", "--start")
	lines := x.envLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "cd "+x.Root+" && export OBOR_AGENT=pj-int OBOR_ROLE=integrator && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL && echo obor-env-") {
		t.Fatalf("typed %q, want one env line: cd the root, export pj-int and integrator, unset OBOR_LANE and OBOR_OPENCODE_MODEL, then the marks", lines)
	}
	calls := strings.Join(x.reqs(), "\n")
	if strings.Index(calls, "pane.send_input") > strings.Index(calls, "agent.start") || !strings.Contains(calls, "agent.start") {
		t.Errorf("the env line comes before the start:\n%s", calls)
	}

	x.noAgents()
	x.h.Pane("w1:p1").Label = ""
	x.reset()
	x.ok(t, "pane", "--start", "L-01", "--pane", "w1:p1")
	lines = x.envLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "cd "+x.LaneDir("L-01")+" && export OBOR_AGENT=pj-l-01 OBOR_ROLE=worker OBOR_LANE=L-01 && unset OBOR_OPENCODE_MODEL && echo obor-env-") {
		t.Errorf("typed %q, want a lane's env: its worktree, pj-l-01, worker, L-01", lines)
	}

	// a new pane gets its env from herdr at creation: nothing typed
	x.noAgents()
	x.h.Pane("w1:p1").Label = ""
	x.reset()
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t2")
	if lines := x.envLines(); len(lines) != 0 {
		t.Errorf("a new pane got a typed env line: %q", lines)
	}
}

// T-601 (RV-64 #3): setup role --pane types the env line through the same setEnv as restart. A cd that fails echoes the
// line's failure word, not its success word, so the agent is not started with the shell's old cwd and OBOR_* env.
func TestSetupPaneStopsWhenTheEnvLineFailed(t *testing.T) {
	t.Parallel()
	t.Run("cd fails: the failure mark, no start, FAIL", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, testkit.Set("roles", `{"integrator": "opus"}`))
		x.h.Tab("w1:t1", 200, 50, "w1:p3")
		pane := x.h.Pane("w1:p3")
		pane.OnRun = "fail"
		x.failing(t, "FAIL the env line in w1:p3 failed (nothing started; check the pane)", "role", "integrator", "--pane", "w1:p3", "--start")
		lines := x.envLines()
		if len(lines) != 1 {
			t.Fatalf("one env line typed, got %q", lines)
		}
		okWord, badWord := envWords(t, lines[0])
		if strings.Contains(pane.Screen, okWord) || !strings.Contains(pane.Screen, badWord) {
			t.Errorf("the screen shows the failure mark only, want %q:\n%s", badWord, pane.Screen)
		}
		hasNot(t, strings.Join(x.reqs(), "\n"), "agent.start", "no start after a failed env line")
	})
	t.Run("the line runs: the success mark, the agent starts", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, testkit.Set("roles", `{"integrator": "opus"}`))
		x.h.Tab("w1:t1", 200, 50, "w1:p3")
		pane := x.h.Pane("w1:p3")
		if out := x.ok(t, "role", "integrator", "--pane", "w1:p3", "--start"); last(out) != "OK integrator: pj-int in pane w1:p3" {
			t.Errorf("last line %q", last(out))
		}
		lines := x.envLines()
		if len(lines) != 1 {
			t.Fatalf("one env line typed, got %q", lines)
		}
		okWord, badWord := envWords(t, lines[0])
		if !strings.Contains(pane.Screen, okWord) || strings.Contains(pane.Screen, badWord) {
			t.Errorf("the screen shows the success mark only, want %q:\n%s", okWord, pane.Screen)
		}
		has(t, strings.Join(x.reqs(), "\n"), "agent.start args=[--permission-mode auto --model opus "+settings+"] kind=claude name=pj-int pane_id=w1:p3",
			"the agent starts after the env line succeeded")
	})
}

// envMarksRE is the env line's two words as typed (T-601): its success word, then its failure word.
var envMarksRE = regexp.MustCompile(` && echo obor-env-"([0-9a-z]+)" \|\| echo obor-env-"([0-9a-z]+)"$`)

// envWords is the success and failure words the typed env line echoes (T-601), quotes removed.
func envWords(t *testing.T, line string) (ok, bad string) {
	t.Helper()
	m := envMarksRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("the env line has no success and failure marks: %q", line)
	}
	return "obor-env-" + m[1], "obor-env-" + m[2]
}

// bash setup.test.sh case_role_lead_new_tab_gets_role_and_agent_env
func TestRoleLeadNewTabGetsRoleAndAgentEnv(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("roles", `{"lead": "opus"}`))
	if out := x.ok(t, "role", "lead", "--new-tab", "lead", "--start"); last(out) != "OK lead: pj-lead in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+x.Root+" env={OBOR_AGENT=pj-lead,OBOR_OPENCODE_MODEL=,OBOR_ROLE=lead} focus=false label=lead", "env")
	has(t, calls, "agent.start args=[--permission-mode auto --model opus /obor:lead] kind=claude name=pj-lead pane_id=w1:n1", "the agent named as OBOR_AGENT, no Remote Control default (T-280)")
}

// T-280: a lead of a type without settings keeps Remote Control; a worker does not. (Setup role lead, the one lead.)
func TestLeadKeepsRemoteControl(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("roles", `{"lead": "opus"}`))
	if out := x.ok(t, "role", "lead", "--new-tab", "lead", "--start"); last(out) != "OK lead: pj-lead in pane w1:n1" {
		t.Fatalf("setup role lead: %q", out)
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "agent.start args=[--permission-mode auto --model opus /obor:lead] kind=claude name=pj-lead pane_id=w1:n1", "lead: type args, no --settings")
	hasNot(t, calls, "remoteControlAtStartup", "lead")
}

// bash setup.test.sh case_role_triage_runs_in_the_root_with_agent_env_only_and_creates_bugs_md
func TestRoleTriageRunsInTheRootWithAgentEnvOnlyAndCreatesBugsMd(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, `FAIL roles.triage is not set (a model ref, e.g. "opus:high")`, "role", "triage", "--new-tab", "triage", "--start")
	if x.Exists("docs/BUGS.md") {
		t.Fatal("precondition: no BUGS.md after the FAIL")
	}
	x.Config(testkit.Set("roles", `{"triage": "opus"}`))
	if out := x.ok(t, "role", "triage", "--new-tab", "triage", "--start"); last(out) != "OK triage: pj-triage in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+x.Root+" env={OBOR_AGENT=pj-triage,OBOR_OPENCODE_MODEL=} focus=false label=triage", "agent env only")
	hasNot(t, calls, "OBOR_ROLE", "triage is not a lead, worker or integrator")
	has(t, x.Read("docs/BUGS.md"), "Severity", "BUGS.md from the template")
	x.Write("docs/BUGS.md", "mine\n")
	x.noAgents()
	x.h.Pane("w1:n1").Label = ""
	x.ok(t, "role", "triage", "--new-tab", "triage", "--start")
	if x.Read("docs/BUGS.md") != "mine\n" {
		t.Error("BUGS.md not overwritten")
	}
}

// --- githooks ---------------------------------------------------------------------------------------------------------

// tests/githooks.test.sh case_setup_githooks_sets_chains_removes (T-620)
func TestGithooksSetsChainsRemoves(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	hooks := filepath.Join(kit, "githooks")
	chain := filepath.Join(hooks, "chain")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+hooks+"\n" {
		t.Errorf("set: %q", out)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != hooks {
		t.Errorf("hooksPath %q", got)
	}
	if got, err := gitIn(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != "" || err == nil {
		t.Errorf("no chain for a repo without its own hooks: %q (%v)", got, err)
	}
	has(t, x.ok(t, "githooks"), "OK githooks: core.hooksPath already", "rerun")
	x.ok(t, "githooks", "--remove")
	if got, _ := gitIn(x.Root, "config", "--local", "--get", "core.hooksPath"); got != "" {
		t.Errorf("removed: %q", got)
	}
	// The project's own hooksPath is chained, not refused (T-620): recorded, then obor's full set (T-623).
	x.Git(x.Root, "config", "core.hooksPath", ".githooks")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+chain+", chaining .githooks\n" {
		t.Errorf("own hooksPath: %q", out)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != ".githooks" {
		t.Errorf("obor.hooksChain %q", got)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != chain {
		t.Errorf("chained hooksPath %q", got)
	}
	has(t, x.ok(t, "githooks"), "OK githooks: core.hooksPath already", "rerun chained")
	x.ok(t, "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != ".githooks" {
		t.Errorf("restored hooksPath %q", got)
	}
	if got, err := gitIn(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != "" || err == nil {
		t.Errorf("chain unset: %q (%v)", got, err)
	}
	// An absolute chainsPath is recorded as it is.
	x.Git(x.Root, "config", "core.hooksPath", "/elsewhere")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+chain+", chaining /elsewhere\n" {
		t.Errorf("absolute hooksPath: %q", out)
	}
	x.ok(t, "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != "/elsewhere" {
		t.Errorf("absolute restored: %q", got)
	}
	// Not obor's: --remove leaves it alone.
	x.failing(t, "FAIL core.hooksPath is /elsewhere (not obor's "+hooks+"): left alone", "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != "/elsewhere" {
		t.Errorf("someone else's hooksPath kept: %q", got)
	}
}

// T-623: core.hooksPath is githooks/ (pre-commit, pre-push and the runner) unless there is something to chain: a
// recorded obor.hooksChain, or a hook in .git/hooks the small set lacks. Then it is githooks/chain/ (every hook git
// runs). A rerun moves between the two as that changes, and --remove works from either.
func TestGithooksPicksTheHooksDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(kit, "githooks")
	chain := filepath.Join(dir, "chain")
	hook := func(x *fx, name string, mode os.FileMode) {
		os.MkdirAll(filepath.Join(x.Root, ".git", "hooks"), 0o755)
		os.Chmod(x.Write(filepath.Join(".git", "hooks", name), "#!/bin/sh\n"), mode)
	}
	for _, c := range []struct {
		name    string
		setup   func(t *testing.T, x *fx)
		want    string
		removed string // core.hooksPath after --remove ("" unset)
	}{
		{"no chain, no .git/hooks hook: githooks/", func(*testing.T, *fx) {}, dir, ""},
		{"a chain recorded: chain/", func(t *testing.T, x *fx) {
			x.Git(x.Root, "config", "obor.hooksChain", ".githooks")
		}, chain, ".githooks"},
		{"a .git/hooks/post-merge: chain/", func(t *testing.T, x *fx) { hook(x, "post-merge", 0o755) }, chain, ""},
		{"only .git/hooks/pre-commit: githooks/", func(t *testing.T, x *fx) { hook(x, "pre-commit", 0o755) }, dir, ""},
		{"a .git/hooks/post-merge.sample: githooks/", func(t *testing.T, x *fx) {
			hook(x, "post-merge.sample", 0o755)
		}, dir, ""},
		{"a non-executable .git/hooks/post-merge: githooks/", func(t *testing.T, x *fx) {
			hook(x, "post-merge", 0o644)
		}, dir, ""},
		{"the chain removed: back to githooks/", func(t *testing.T, x *fx) {
			x.Git(x.Root, "config", "core.hooksPath", ".githooks")
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+chain, "a chain moves to the full set")
			x.Git(x.Root, "config", "--local", "--unset", "obor.hooksChain")
		}, dir, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t)
			c.setup(t, x)
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+c.want, "hooksPath")
			if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != c.want {
				t.Errorf("core.hooksPath = %q, want %q", got, c.want)
			}
			has(t, x.ok(t, "githooks"), "already "+c.want, "rerun")
			x.ok(t, "githooks", "--remove")
			if got, _ := gitIn(x.Root, "config", "--local", "--get", "core.hooksPath"); got != c.removed {
				t.Errorf("--remove: core.hooksPath = %q, want %q", got, c.removed)
			}
		})
	}
}

// --- herdr-plugin (R-28 §3) -------------------------------------------------------------------------------------------

// bash setup.test.sh case_herdr_plugin_refreshes_symlink_and_reports_link; not linked → the kit's herdr-plugin linked
// directly (T-250, T-256).
func TestHerdrPluginLinksTheKitAndHints(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := x.Path("herdr-config.toml")
	x.env = []string{"HERDR_CONFIG_PATH=" + conf}
	mine := filepath.Join(kit, "herdr-plugin")
	x.h.Plugins = []herdrtest.Plugin{{ID: "obor.events", Root: mine}}
	out := x.ok(t, "herdr-plugin")
	has(t, out, "herdr-plugin: obor.events linked ("+mine+")", "linked")
	hasNot(t, strings.Join(x.reqs(), "\n"), "plugin.link", "linked → no link")
	has(t, out, "hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false under [session] in "+conf,
		"no setting → the resume line")
	if last(out) != "OK herdr-plugin" {
		t.Errorf("last line %q", last(out))
	}
	x.Write(conf, "[session]\nresume_agents_on_restore = true\n")
	has(t, x.ok(t, "herdr-plugin"), "set resume_agents_on_restore = false", "true → the resume line")
	text := "[ui]\nx = 1\n[session]\nresume_agents_on_restore = false # obor\n"
	x.Write(conf, text)
	out = x.ok(t, "herdr-plugin")
	has(t, out, "obor.events linked", "precondition: still linked")
	hasNot(t, out, "hint:", "false → no hint at all")
	if x.Read(conf) != text {
		t.Error("herdr's config never edited")
	}
	x.Write(conf, "[other]\nresume_agents_on_restore = false\n")
	has(t, x.ok(t, "herdr-plugin"), "set resume_agents_on_restore", "false outside [session] → the line")
	os.Remove(conf)

	x.srv.Fail("plugin.list", "invalid_request", "boom")
	x.failing(t, "FAIL cannot read herdr's plugin list: herdr plugin.list: failed: boom (invalid_request)", "herdr-plugin")
	if len(x.h.Plugins) != 1 || x.h.Plugins[0].Root != mine {
		t.Errorf("a plugin list error changed herdr's plugins: %v", x.h.Plugins)
	}
	x.srv.Clear("plugin.list")

	x.h.Plugins = nil
	x.reset()
	out = x.ok(t, "herdr-plugin")
	has(t, strings.Join(x.reqs(), "\n"), "plugin.link path="+mine, "not linked → the kit's herdr-plugin linked")
	has(t, out, "herdr-plugin: obor.events linked to "+mine+" (user-global: obor restarts its panes after a herdr session restore)", "said")
	has(t, out, "(user-global: affects every herdr pane, not only obor's; those come back as shells)", "the resume line, linked or not")
	if x.Exists(filepath.Join(x.Base, "state-home", "herdr-plugin")) {
		t.Error("no ~/.obor/herdr-plugin symlink")
	}

	x.h.Plugins = nil
	x.srv.Fail("plugin.link", "plugin_invalid", "boom")
	out = x.failing(t, "FAIL herdr-plugin", "herdr-plugin")
	has(t, out, "WARN herdr-plugin: herdr plugin.link: failed: boom (plugin_invalid)", "a failed link said")
	x.srv.Clear("plugin.link")

	x.kit = x.Path("no-kit")
	x.reset()
	out = x.failing(t, "FAIL herdr-plugin", "herdr-plugin")
	has(t, out, "WARN herdr-plugin: no herdr plugin in "+x.Path("no-kit/herdr-plugin"), "a kit without the plugin said")
	if calls := x.reqs(); len(calls) > 0 {
		t.Errorf("no herdr call: %v", calls)
	}
}

// RV-18 #1 + STATUS "herdr resolves the symlink": herdr runs another kit's plugin (a kit at another path) →
// herdr-plugin re-links it to this kit (init only says so); this kit's (by any path to it) → nothing to re-link.
// bash setup.test.sh case_herdr_plugin_other_kit_relink_command
func TestHerdrPluginRelinksAnotherKitsPlugin(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	old, mine := x.Path("old-kit/herdr-plugin"), filepath.Join(kit, "herdr-plugin")
	os.MkdirAll(old, 0o755)
	x.h.Plugins = []herdrtest.Plugin{{ID: "obor.events", Root: old}}
	x.reset()
	out := x.ok(t, "herdr-plugin")
	if got := strings.Join(x.reqs(), "\n"); !strings.Contains(got, "plugin.unlink plugin_id=obor.events\nplugin.link path="+mine) {
		t.Errorf("unlink, then link the kit's herdr-plugin:\n%s", got)
	}
	has(t, out, "herdr-plugin: obor.events re-linked to "+mine+" (was "+old+";", "said")
	if len(x.h.Plugins) != 1 || x.h.Plugins[0].Root != mine {
		t.Errorf("herdr now runs this kit: %v", x.h.Plugins)
	}

	alias := x.Path("kit-alias")
	if err := os.Symlink(kit, alias); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{mine, filepath.Join(alias, "herdr-plugin")} {
		x.h.Plugins = []herdrtest.Plugin{{ID: "obor.events", Root: root}}
		x.reset()
		out = x.ok(t, "herdr-plugin")
		has(t, out, "obor.events linked ("+mine+")", root+" is this kit")
		hasNot(t, strings.Join(x.reqs(), "\n"), "plugin.link", root+": no re-link")
	}
}

// gitIn is git -C dir args…: its trimmed stdout and error (testkit's Git fails the test instead).
func gitIn(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// --- agents (T-306, DD-v0.13 B1) -------------------------------------------------------------------------------------

// agents (alias opencode-plugin) links <XDG_CONFIG_HOME>/opencode/plugins/obor.js to the kit's opencode/obor.js, idempotently;
// re-links another kit's copy; leaves a foreign file or link and the rest of the dir alone; --remove deletes the link.
func TestOpencodePluginLinksAndRemoves(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	dir := filepath.Join(conf, "opencode", "plugins")
	link := filepath.Join(dir, "obor.js")
	mine := filepath.Join(kit, "opencode", "obor.js")
	target := func() string { s, _ := os.Readlink(link); return s }

	if x.Exists(dir) {
		t.Fatal("precondition: no plugins dir")
	}
	out := x.ok(t, "opencode-plugin") // the alias of agents
	if target() != mine {
		t.Fatalf("link %s → %q, want %s\n%s", link, target(), mine, out)
	}
	has(t, out, "OK agents opencode: "+link+" → "+mine, "said")
	x.Write(filepath.Join(dir, "herdr-agent-state.js"), "// herdr's\n")
	has(t, x.ok(t, "agents"), "OK agents opencode: "+link+" already links to "+mine, "rerun: idempotent")
	if target() != mine {
		t.Error("rerun keeps the link")
	}

	os.Remove(link)
	other := filepath.Join(x.Base, "old-kit", "opencode", "obor.js")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	has(t, x.ok(t, "agents"), "agents opencode: re-linked "+link+" to "+mine+" (was "+other+")", "another kit's copy → re-linked")
	if target() != mine {
		t.Error("re-linked to this kit")
	}

	x.ok(t, "agents", "--remove")
	if _, err := os.Lstat(link); err == nil {
		t.Error("--remove deletes the link")
	}
	if x.Read(filepath.Join(dir, "herdr-agent-state.js")) != "// herdr's\n" {
		t.Error("other files in the plugins dir untouched")
	}
	has(t, x.ok(t, "agents", "--remove"), "not linked", "remove again: idempotent")

	if err := os.Symlink("/elsewhere/mine.js", link); err != nil {
		t.Fatal(err)
	}
	x.failing(t, "FAIL agents opencode: "+link+" links to /elsewhere/mine.js (not an obor kit's opencode/obor.js): left alone", "agents")
	x.failing(t, "FAIL agents opencode: "+link+" links to /elsewhere/mine.js (not an obor kit's opencode/obor.js): left alone", "agents", "--remove")
	os.Remove(link)
	x.Write(link, "// a user's own obor.js\n")
	x.failing(t, "FAIL agents opencode: "+link+" is not a link: left alone", "agents")
	if x.Read(link) != "// a user's own obor.js\n" {
		t.Error("a user's file is never replaced")
	}
	x.failing(t, "FAIL "+Usage(), "agents", "--bogus")
}

// Without XDG_CONFIG_HOME the plugins dir is ~/.config/opencode/plugins.
func TestOpencodePluginDefaultsToHomeConfig(t *testing.T) {
	t.Parallel()
	x := bare(t)
	home := filepath.Join(x.Base, "home") // the fixture's HOME; no XDG_CONFIG_HOME
	x.ok(t, "agents")
	if s, _ := os.Readlink(filepath.Join(home, ".config", "opencode", "plugins", "obor.js")); s != filepath.Join(kit, "opencode", "obor.js") {
		t.Errorf("link → %q", s)
	}
}

// --- obor MCP registration (T-375) ------------------------------------------------------------------------------------

// agents also sets mcp.servers.obor in opencode.json (OpenCode 2): created when absent, idempotent, a stale
// entry replaced, every other key (and the other mcp servers) kept; --remove deletes only mcp.servers.obor; a .jsonc
// config with comments is never edited.
func TestOpencodePluginWritesTheMCPEntry(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	path := filepath.Join(conf, "opencode", "opencode.json")
	want := `"obor": {
        "command": [
          "` + filepath.Join(kit, "bin", "obor") + `",
          "mcp"
        ],
        "environment": {
          "OBOR_NO_SELF_HOST": "1"
        },
        "type": "local"
      }`

	if x.Exists(path) {
		t.Fatal("precondition: no opencode.json")
	}
	has(t, x.ok(t, "agents"), "OK agents opencode: mcp.servers.obor set in "+path, "created")
	has(t, x.Read(path), want, "type local, command <kit>/bin/obor mcp, under mcp.servers")
	has(t, x.Read(path), `"servers"`, "the v2 shape")
	before := x.Read(path)
	has(t, x.ok(t, "agents"), "mcp.servers.obor already set in "+path, "rerun: idempotent")
	if x.Read(path) != before {
		t.Error("an equal entry is not rewritten")
	}

	x.Write(path, `{"theme":"dark","mcp":{"keep":1,"servers":{"other":{"type":"remote","url":"http://x"},"obor":{"type":"local","command":["/old/obor","mcp"]}}}}`)
	has(t, x.ok(t, "agents"), "mcp.servers.obor set in "+path, "another kit's path: replaced")
	got := x.Read(path)
	for _, s := range []string{want, `"theme": "dark"`, `"keep": 1`, `"other"`, `"url": "http://x"`} {
		has(t, got, s, "the stale entry replaced, the rest kept")
	}
	if strings.Contains(got, "/old/obor") {
		t.Errorf("stale entry left: %s", got)
	}

	has(t, x.ok(t, "agents", "--remove"), "mcp.servers.obor removed from "+path, "remove")
	got = x.Read(path)
	if strings.Contains(got, "obor") || !strings.Contains(got, `"other"`) || !strings.Contains(got, `"keep"`) || !strings.Contains(got, `"theme"`) {
		t.Errorf("--remove deletes only mcp.servers.obor: %s", got)
	}
	has(t, x.ok(t, "agents", "--remove"), "no mcp.servers.obor in "+path, "remove again: idempotent")

	os.Remove(path)
	x.Write(path+"c", "// comments\n{}\n")
	if out, rc := x.run(t, "agents"); rc != 1 || !strings.Contains(last(out), "opencode.jsonc is not edited") {
		t.Errorf("a .jsonc config with comments is left alone: exit %d\n%s", rc, out)
	}
}

// An opencode.jsonc that is plain JSON is read and edited like opencode.json: the user's current file (only $schema
// and mcp.servers.obor) is registered and left byte for byte; an empty one gets the entry.
func TestOpencodeJSONCPlainJSON(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	path := filepath.Join(conf, "opencode", "opencode.jsonc")
	cur := `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "obor": {
        "type": "local",
        "command": ["` + filepath.Join(kit, "bin", "obor") + `", "mcp"],
        "environment": {"OBOR_NO_SELF_HOST": "1"}
      }
    }
  }
}
`
	x.Write(path, cur)
	out, _ := x.run(t, "mcp")
	has(t, out, "mcp opencode: registered ("+path+")", "the current file is registered")
	if strings.Contains(out, "WARN mcp opencode") {
		t.Errorf("registered file warned:\n%s", out)
	}
	has(t, x.ok(t, "agents"), "mcp.servers.obor already set in "+path, "already set")
	if x.Read(path) != cur {
		t.Errorf("the registered file is unchanged: %q", x.Read(path))
	}

	x.Write(path, "")
	if x.Exists(filepath.Join(conf, "opencode", "opencode.json")) {
		t.Fatal("precondition: no opencode.json")
	}
	has(t, x.ok(t, "agents"), "mcp.servers.obor set in "+path, "empty .jsonc: entry written")
	has(t, x.Read(path), `"servers"`, "written into the .jsonc")
	has(t, x.ok(t, "mcp"), "mcp opencode: registered ("+path+")", "and then registered")
}

// `obor setup mcp` on a .jsonc config (comments): the opencode WARN is followed by the same paste block, the file is
// left alone, and the doctor still fails.
func TestSetupMCPDoctorJSONCPrintsPasteHint(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	path := filepath.Join(conf, "opencode", "opencode.jsonc")
	x.Write(path, "// comments\n{}\n")
	warn := "WARN mcp opencode: not registered: " + path + " is not read (comments) (obor setup agents)"
	paste := `paste into ` + path + ` under "mcp.servers":
  "obor": {
    "command": [
      "` + filepath.Join(kit, "bin", "obor") + `",
      "mcp"
    ],
    "environment": {
      "OBOR_NO_SELF_HOST": "1"
    },
    "type": "local"
  }`

	out, rc := x.run(t, "mcp")
	if rc != 1 {
		t.Errorf("exit %d, want 1\n%s", rc, out)
	}
	has(t, out, warn+"\n"+paste, "the WARN, then the paste block right after it")
	if got := x.Read(path); got != "// comments\n{}\n" {
		t.Errorf("the jsonc config is not written: %q", got)
	}
}

// A .jsonc config (comments) is not edited: the WARN line is followed by the obor entry to paste under "mcp.servers", and
// the FAIL stays the last line.
func TestOpencodePluginJSONCPrintsPasteHint(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	path := filepath.Join(conf, "opencode", "opencode.jsonc")
	x.Write(path, "// comments\n{}\n")
	paste := `paste into ` + path + ` under "mcp.servers":
  "obor": {
    "command": [
      "` + filepath.Join(kit, "bin", "obor") + `",
      "mcp"
    ],
    "environment": {
      "OBOR_NO_SELF_HOST": "1"
    },
    "type": "local"
  }`

	out := x.failing(t, "FAIL agents opencode: "+path+" is not edited (comments would be lost)", "agents")
	has(t, out, "WARN agents opencode: "+path+" is not edited", "the WARN line")
	has(t, out, paste, "the obor entry to paste, indented, under mcp.servers")
	if got := x.Read(path); got != "// comments\n{}\n" {
		t.Errorf("the jsonc config is not written: %q", got)
	}
}

// A config that is not JSON is left alone with a FAIL naming the file.
func TestOpencodePluginLeavesBrokenConfigAlone(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	path := filepath.Join(conf, "opencode", "opencode.json")
	x.Write(path, "{ not json")
	out, rc := x.run(t, "agents")
	if rc != 1 || !strings.Contains(last(out), path) || x.Read(path) != "{ not json" {
		t.Errorf("exit %d, last %q, file %q", rc, last(out), x.Read(path))
	}
}

// `obor setup mcp` says per kind whether the server is registered, then runs the handshake; OpenCode unregistered is a
// WARN and a FAIL, registered all OK.
func TestSetupMCPDoctor(t *testing.T) {
	t.Parallel()
	x := bare(t)
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	out, rc := x.run(t, "mcp")
	if rc != 1 {
		t.Errorf("opencode unregistered: exit %d\n%s", rc, out)
	}
	has(t, out, "mcp claude: registered", "claude: the kit's plugin.json")
	has(t, out, "mcp codex: registered per start", "codex: launch args")
	has(t, out, "env OBOR_NO_SELF_HOST=1", "codex: the pin")
	if strings.Contains(out, "without the OBOR_NO_SELF_HOST pin") {
		t.Errorf("a pinned claude manifest warns about the pin:\n%s", out)
	}
	has(t, out, `env_vars=["OBOR_ROLE","OBOR_AGENT","OBOR_LANE","HERDR_PANE_ID","OBOR_STATE_HOME"]`, "codex: forwarded variables")
	has(t, out, "WARN mcp opencode: not registered", "opencode: not yet")

	x.ok(t, "agents")
	out = x.ok(t, "mcp")
	has(t, out, "mcp opencode: registered", "opencode: after obor setup agents")
	has(t, out, "MCP OK (", "the handshake line of obor mcp --check")
	has(t, last(out), "OK mcp", "result")

	x.mcpCheck = func(project.Env) (string, error) { return "", errors.New("the server answered nothing") }
	out, rc = x.run(t, "mcp")
	has(t, out, "WARN mcp check: the server answered nothing", "a broken handshake")
	if rc != 1 || last(out) != "FAIL mcp" {
		t.Errorf("broken handshake: exit %d, last %q", rc, last(out))
	}
}

// The doctor says when an OpenCode entry or the Claude manifest lacks the pin, and still accepts it (a WARN, no FAIL).
func TestSetupMCPDoctorUnpinned(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	path := filepath.Join(conf, "opencode", "opencode.json")
	x.Write(path, `{"mcp":{"servers":{"obor":{"type":"local","command":["`+kit+`/bin/obor","mcp"]}}}}`)
	out, rc := x.run(t, "mcp")
	has(t, out, "WARN mcp opencode: registered without the OBOR_NO_SELF_HOST pin", "opencode: unpinned entry")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("an unpinned entry is accepted: exit %d, last %q", rc, last(out))
	}
	x.ok(t, "agents")
	has(t, x.Read(path), `"OBOR_NO_SELF_HOST": "1"`, "obor setup agents writes the pin")
	out = x.ok(t, "mcp")
	if strings.Contains(out, "without the") {
		t.Errorf("a rewritten entry is pinned:\n%s", out)
	}
}

// The plugin manifest's mcpServers: the obor server is `bash ${CLAUDE_PLUGIN_ROOT}/bin/obor mcp`, nothing else.
func TestKitManifestMCPServers(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(kit, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		McpServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	h := got.McpServers["obor"]
	if len(got.McpServers) != 1 || h.Command != "bash" || strings.Join(h.Args, " ") != "${CLAUDE_PLUGIN_ROOT}/bin/obor mcp" ||
		len(h.Env) != 1 || h.Env["OBOR_NO_SELF_HOST"] != "1" {
		t.Errorf("plugin.json mcpServers: %s", b)
	}
	// DD-v0.13 B1: the bridge's entry is the shipped one.
	k, _ := agents.Lookup("claude")
	m := k.Install(kit, nil).MCP
	if state, path, err := registration(m); state != entryPinned || err != nil || path != filepath.Join(kit, ".claude-plugin", "plugin.json") {
		t.Errorf("registration of the kit's manifest = %d, %s, %v", state, path, err)
	}
	m.Path = filepath.Join(kit, "nope.json")
	if state, _, err := registration(m); state != entryNone || err != nil {
		t.Errorf("registration of a missing manifest = %d, %v", state, err)
	}
}

// T-582 (RV-62 #3), T-588: obor setup pane and setup role start Codex through launch's one start path, so the rollout
// the fresh start's process holds open (the fake herdr's agent: pid 2) is recorded as the agent's session: serve down
// here, so it is said and nothing is recorded (no .sid file: T-599).
func TestCodexSetupStartRecordsItsSession(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, agent, wt string
		args            []string
		edit            func(map[string]any)
	}{
		{"setup pane", "pj-l-01", "L-01", []string{"pane", "--start", "L-01", "--tab", "w1:t1"}, func(map[string]any) {}},
		{"setup role", "pj-rev", "review", []string{"role", "reviewer", "--tab", "w1:t1", "--start"},
			testkit.Set("roles", `{"reviewer": "luna"}`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, c.edit)
			x.rollout = 10 * time.Second
			if c.args[0] == "pane" {
				x.ok(t, "lane", c.wt)
				x.State(c.wt, map[string]any{"task": "T-1", "type": "luna"})
			}
			cwd := x.LaneDir(c.wt)
			rollout := filepath.Join(x.Base, "home", ".codex", "sessions", "2026", "10", "30", "rollout-cx-new.jsonl")
			x.open = func(pids []int) []string {
				if slices.Contains(pids, 2) {
					return []string{rollout}
				}
				return nil
			}
			x.srv.Before(func(r herdrtest.Request) {
				if r.Method != "agent.start" {
					return
				}
				at := time.Now().UTC()
				x.Write(rollout,
					`{"timestamp":"`+at.Format(time.RFC3339Nano)+`","type":"session_meta","payload":{"id":"cx-new","timestamp":"`+
						at.Format(time.RFC3339Nano)+`","cwd":"`+cwd+`"}}`+"\n")
			})
			out := x.ok(t, c.args...)
			has(t, strings.Join(x.reqs(), "\n"), "kind=codex name="+c.agent, "precondition: a codex start")
			has(t, out, "session cx-new (", "the new session found")
			has(t, out, "session cx-new of "+c.agent+" not recorded", "serve down: said, nothing recorded")
			if _, err := os.Stat(filepath.Join(x.Env().Get("OBOR_STATE_HOME"), "agents", c.agent+".sid")); err == nil {
				t.Errorf("a .sid file written\n%s", out)
			}
		})
	}
}

// A githooks path of another obor kit (a checkout whose plugin.json names obor, or one that is gone) is re-pointed at
// this kit's; anyone else's hooks dir is the project's own: chained, not refused (T-620), so obor's full set runs
// (T-623).
func TestGithooksRepointsAnotherOborKitsHooksAndChainsOthers(t *testing.T) {
	x := newFx(t)
	mine := filepath.Join(kit, "githooks")
	chain := filepath.Join(mine, "chain")
	// an obor kit's githooks (its plugin.json names obor): re-pointed
	old := filepath.Join(x.Base, "obor-kit-stable")
	x.Write(old+"/.claude-plugin/plugin.json", `{"name": "obor"}`)
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks")
	out := x.ok(t, "githooks")
	has(t, out, "core.hooksPath = "+mine, "output")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("core.hooksPath = %s, want %s", got, mine)
	}
	// an obor kit's chain set is re-pointed too (T-623)
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks/chain")
	has(t, x.ok(t, "githooks"), "core.hooksPath = "+mine, "a kit's chain set re-pointed")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("a kit's chain set: core.hooksPath = %s, want %s", got, mine)
	}
	// a kit that is gone, named like one: re-pointed
	x.Git(x.Root, "config", "--local", "core.hooksPath", "/nowhere/obor-kit/githooks")
	x.ok(t, "githooks")
	// anyone else's hooks dir is the project's own: chained, not refused
	for _, other := range []string{"/nowhere/husky/githooks", "/usr/local/hooks", x.Base + "/githooks"} {
		x.Git(x.Root, "config", "--local", "core.hooksPath", other)
		has(t, x.ok(t, "githooks"), "chaining "+other, "chained hooks "+other)
		if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != other {
			t.Errorf("obor.hooksChain = %s, want %s", got, other)
		}
		if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != chain {
			t.Errorf("core.hooksPath %s changed to %s, want %s", other, got, chain)
		}
	}
}
