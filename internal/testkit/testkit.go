// Package testkit builds throwaway obor projects for the Go tests: a git repo (the main checkout) with lanes as linked
// worktrees, a committed .obor/config.json, submissions and lane rows in its store, and a fake herdr. Everything lives under a
// short temp dir removed when the test ends; the environment is cleared of the developer's OBOR_* / HERDR_* variables and git config.
package testkit

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	// every agent kind obor has a bridge for, as cmd/obor registers them
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	_ "obor/internal/agents/opencode"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/store"
)

// Main runs a package's tests isolated (Isolate) and removes the project template after them (Run). Call it from
// TestMain.
func Main(m *testing.M) {
	Isolate()
	os.Exit(Run(m))
}

// Run is m.Run followed by the removal of the project template New copies (T-1009), for a TestMain that does more than
// Main: it returns m.Run's code. It also arms New (T-1019): only a TestMain that calls it removes the template.
func Run(m *testing.M) int {
	hooked.Store(true)
	code := m.Run()
	if tmpl.base != "" {
		os.RemoveAll(tmpl.base)
	}
	if scripts.dir != "" {
		os.RemoveAll(scripts.dir)
	}
	return code
}

// hooked is Run's: this binary's TestMain removes the template. template checks it before it builds (T-1019), so the
// next package that calls New without Main/Run fails at once instead of leaving an hk-tmpl* dir in TMPDIR per run.
var hooked atomic.Bool

// tmpl is the project every New copies (T-1009): <base>/proj with its one commit on main, built once per test binary on
// first use and removed by Run. builds counts the builds (a test checks it is one).
var tmpl struct {
	once   sync.Once
	base   string
	err    error
	builds int
}

// scripts is Script's cache: one written executable per body, hardlinked into the tests' dirs (T-1025). macOS scans a
// fresh executable before its first exec (100+ ms for a small script, 200+ ms for the 24 MB obor binary), so a suite
// that writes the same launcher in each test paid that scan per test.
var scripts struct {
	once sync.Once
	dir  string
	err  error
	mu   sync.Mutex
	by   map[[32]byte]string
}

// template is the template's proj dir, built on first use: git init, add and commit of the .gitignore (a leftover done
// file), the only git processes a New costs, once per binary.
func template() (string, error) {
	if !hooked.Load() {
		return "", fmt.Errorf("testkit: New in a test binary whose TestMain does not call testkit.Main or testkit.Run, " +
			"so their cleanup would never remove the project template (T-1019); add " +
			"`func TestMain(m *testing.M) { testkit.Main(m) }` (testkit.Run(m) for a TestMain that does more)")
	}
	tmpl.once.Do(func() {
		tmpl.builds++
		tmpl.base, tmpl.err = os.MkdirTemp("", "hk-tmpl")
		if tmpl.err != nil {
			return
		}
		root := filepath.Join(tmpl.base, "proj")
		if tmpl.err = os.MkdirAll(root, 0o755); tmpl.err != nil {
			return
		}
		if tmpl.err = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".task-done.md\n"), 0o644); tmpl.err != nil {
			return
		}
		for _, args := range [][]string{{"-c", "init.defaultBranch=main", "init", "-q"}, {"add", "-A"},
			{"commit", "-q", "-m", "initial"}} {
			cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=obor test",
				"-c", "user.email=obor-test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				tmpl.err = fmt.Errorf("git %v: %v\n%s", args, err, out)
				return
			}
		}
	})
	if tmpl.err != nil {
		return "", fmt.Errorf("testkit: build the project template: %w", tmpl.err)
	}
	return filepath.Join(tmpl.base, "proj"), nil
}

// copyTree copies the tree at src to dst (which must not exist) in process: dirs, regular files (mode and mtime kept,
// so the copied index still matches the copied files' mtimes) and symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(to, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, to)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(to, b, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(to, info.ModTime(), info.ModTime())
		}
		return fmt.Errorf("copy %s: not a dir, file or symlink", path)
	})
}

// Slow marks a test that takes a second or more: the per-merge gate's fast tier (go test -short, T-957) skips it, and
// make check, the nightly and releases (no -short) run it. It is the first line of the test.
func Slow(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow: runs in make check")
	}
}

// Script writes body as the executable dir/name and returns its path: a test's fake launchctl, a stub obor. It is one
// copy per body per test binary, hardlinked into dir (T-1025): macOS scans a fresh executable before its first exec
// (100+ ms for a small script), so a launcher written in each test paid that scan per test. Bodies that differ in a
// test's paths are different copies, so keep them out of the body: derive them from $0 or pass them in the environment.
func Script(t testing.TB, dir, name, body string) string {
	t.Helper()
	src := scriptCopy(t, body)
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Link(src, path); err != nil {
		// Another filesystem (a TMPDIR on another volume): its own copy, at the cost of the scan.
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// scriptCopy is Script's cached file for body, written once.
func scriptCopy(t testing.TB, body string) string {
	t.Helper()
	scripts.once.Do(func() {
		scripts.by = map[[32]byte]string{}
		scripts.dir, scripts.err = os.MkdirTemp("", "hk-bin")
	})
	if scripts.err != nil {
		t.Fatalf("testkit: script cache: %v", scripts.err)
	}
	sum := sha256.Sum256([]byte(body))
	scripts.mu.Lock()
	defer scripts.mu.Unlock()
	if path, ok := scripts.by[sum]; ok {
		return path
	}
	path := filepath.Join(scripts.dir, fmt.Sprintf("%x", sum))
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	scripts.by[sum] = path
	return path
}

// Isolate keeps the tests away from the developer's shell: no OBOR_* / HERDR_* variables (but a herdr socket that is not there), no check-slot variables the
// merge gate exports to `make check` (nor Claude's or Codex's config dirs), git without global or system config.
// CHECK_LOAD_LIMIT is 0: no heavy command waits for the machine load (T-720).
func Isolate() {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "OBOR_") || strings.HasPrefix(k, "HERDR_") || k == "CLAUDE_CONFIG_DIR" || k == "CODEX_HOME" ||
			k == "CHECK_PRIORITY" || k == "CHECK_SLOT_HELD" || k == "CHECK_SLOT_DIR" || k == "CHECK_SLOTS" {
			os.Unsetenv(k)
		}
	}
	// obor reaches herdr over its socket: no socket here unless a test serves one (herdrtest.Serve), never the
	// developer's own ($HOME/.config/herdr/herdr.sock).
	os.Setenv("HERDR_SOCKET_PATH", "/nonexistent/herdr.sock")
	// A heavy command's slot never waits for the machine load (T-720): the tests run while the machine is busy.
	os.Setenv("CHECK_LOAD_LIMIT", "0")
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// No detached `git maintenance run --auto` after a commit: it can still write .git/objects while the test's temp
	// dir is removed ("directory not empty", T-438's gate).
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "maintenance.auto")
	os.Setenv("GIT_CONFIG_VALUE_0", "false")
}

// Project is a fixture project: Root is the main checkout <Base>/proj (branch main), lanes are <Base>/proj-wt/<name>.
type Project struct {
	t    testing.TB
	Base string
	Root string

	herdr *herdrtest.Fake
	// plain: a project dir with no git (NewDir): Config writes the config uncommitted, Lane makes a plain dir and
	// notes its branch here, and Git fails the test.
	plain    bool
	branches map[string]string // lane → its branch (a plain project's lanes)
}

// Dir is a fresh temp dir, physical (symlinks resolved, as the code under test sees paths).
func Dir(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// shortDir is a fresh temp dir, physical, short enough that the project's serve socket (<root>/.obor/state/serve.sock)
// fits a unix socket address (t.TempDir's name carries the test's).
func shortDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("", "hk")
	if err == nil {
		t.Cleanup(func() { os.RemoveAll(d) })
		d, err = filepath.EvalSymlinks(d)
	}
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// New is a project with one commit on main: .gitignore (a leftover done file). No config yet.
func New(t testing.TB) *Project {
	t.Helper()
	return NewAt(t, shortDir(t))
}

// NewAt is New at base (a test that wants it under t.TempDir): the caller owns base. The project is a private copy of
// the binary's template (T-1009): no git process here.
func NewAt(t testing.TB, base string) *Project {
	t.Helper()
	p := &Project{t: t, Base: base}
	p.Root = filepath.Join(p.Base, "proj")
	src, err := template()
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, p.Root); err != nil {
		t.Fatalf("testkit: copy the project template: %v", err)
	}
	return p
}

// Adopt is a project already at base (Root <Base>/proj), for a test that copied a built template into its own dir
// (T-998): nothing is created and nothing is removed; base lives with the test's t.TempDir.
func Adopt(t testing.TB, base string) *Project {
	return &Project{t: t, Base: base, Root: filepath.Join(base, "proj")}
}

// NewDir is a plain project dir <Base>/proj with no git (T-996): for tests whose git side is a fake (internal/app's
// FakeScratch). Config writes .obor/config.json there; Lane makes a plain lane dir; Git fails the test.
func NewDir(t testing.TB) *Project {
	t.Helper()
	p := &Project{t: t, Base: shortDir(t), plain: true, branches: map[string]string{}}
	p.Root = filepath.Join(p.Base, "proj")
	if err := os.MkdirAll(p.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Env is the environment a command sees in this project's tests: HOME and the state home under Base, plus kv
// ("VAR=value"; an empty value leaves VAR unset). A test that runs obor against the fake herdr passes its path
// (HERDR_SOCKET_PATH=<Serve(t, Herdr()).Path>).
func (p *Project) Env(kv ...string) project.Env {
	m := map[string]string{
		"HOME":             filepath.Join(p.Base, "home"),
		"OBOR_STATE_HOME":  filepath.Join(p.Base, "state-home"),
		"CHECK_LOAD_LIMIT": "0", // no wait for the machine load (T-720), as Isolate
	}
	for _, e := range kv {
		k, v, _ := strings.Cut(e, "=")
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return MapEnv(m)
}

// MapEnv is an Env of fixed values.
func MapEnv(m map[string]string) project.Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// Git runs git -C dir args… (fixture identity, no signing); fails the test on an error. Returns stdout, trimmed.
func (p *Project) Git(dir string, args ...string) string {
	p.t.Helper()
	return p.GitEnv(nil, dir, args...)
}

// GitEnv is Git with more environment (GIT_COMMITTER_DATE=…).
func (p *Project) GitEnv(env []string, dir string, args ...string) string {
	p.t.Helper()
	if p.plain {
		p.t.Fatalf("git %v in a plain project (testkit.NewDir has no git)", args)
	}
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=obor test",
		"-c", "user.email=obor-test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = string(ee.Stderr)
		}
		p.t.Fatalf("git %v: %v\n%s", args, err, msg)
	}
	return strings.TrimSpace(string(out))
}

// Lane is fx_lane: worktree <Base>/proj-wt/<name> on branch (created from main when new), or detached at main when
// branch is "". In a plain project (NewDir) it is a plain dir and its branch a note.
func (p *Project) Lane(name, branch string) string {
	p.t.Helper()
	dir := p.LaneDir(name)
	switch {
	case p.plain:
		if err := os.MkdirAll(dir, 0o755); err != nil {
			p.t.Fatal(err)
		}
		p.branches[name] = branch
	case branch == "":
		p.Git(p.Root, "worktree", "add", "-q", "--detach", dir, "main")
	case exec.Command("git", "-C", p.Root, "show-ref", "--verify", "-q", "refs/heads/"+branch).Run() == nil:
		p.Git(p.Root, "worktree", "add", "-q", dir, branch)
	default:
		p.Git(p.Root, "worktree", "add", "-q", "-b", branch, dir, "main")
	}
	p.enablePlugin(dir)
	return dir
}

// LaneDir is <Base>/proj-wt/<name>.
func (p *Project) LaneDir(name string) string { return filepath.Join(p.Base, "proj-wt", name) }

// Config is fx_config: the default config (prefix pj, lanes: the highest L-NN under proj-wt, T-765), changed by edit,
// written to .obor/config.json and committed on main (projects commit it; a plain project's stays uncommitted).
func (p *Project) Config(edit ...func(c map[string]any)) {
	p.t.Helper()
	lanes := 0
	entries, _ := os.ReadDir(filepath.Join(p.Base, "proj-wt"))
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "L-%d", &n); err == nil && e.IsDir() && e.Name() == fmt.Sprintf("L-%02d", n) {
			lanes = max(lanes, n)
		}
	}
	c := map[string]any{
		"version":         1,
		"name":            "proj",
		"prefix":          "pj",
		"lanes_dir":       "../proj-wt",
		"branches":        map[string]any{"task": "task/{id}", "main": "main"},
		"task_id_pattern": "T-[0-9]+[a-z]?",
		"paths":           map[string]any{"reviews": "docs/reviews"},
		"commands":        map[string]any{},
		"lane_links":      []any{},
		"protected_files": []any{},
		"lanes":           lanes,
	}
	for _, e := range edit {
		e(c)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		p.t.Fatal(err)
	}
	p.Write(".obor/config.json", string(b)+"\n")
	if p.plain {
		return
	}
	p.Git(p.Root, "add", ".obor/config.json")
	if exec.Command("git", "-C", p.Root, "diff", "--cached", "--quiet", "--", ".obor/config.json").Run() != nil {
		p.Git(p.Root, "commit", "-q", "-m", "obor config", "--", ".obor/config.json")
	}
}

// Set is a config edit: the value at a dotted path ("roles.lead"), creating objects on the way. v may be a JSON text
// (a string starting with { or [) or any value.
func Set(path string, v any) func(map[string]any) {
	return func(c map[string]any) {
		keys := strings.Split(path, ".")
		for _, k := range keys[:len(keys)-1] {
			next, ok := c[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				c[k] = next
			}
			c = next
		}
		c[keys[len(keys)-1]] = JSON(v)
	}
}

// JSON decodes v when it is a string holding a JSON object or array; anything else is returned as is.
func JSON(v any) any {
	s, ok := v.(string)
	if !ok || !(strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) {
		return v
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		panic("testkit.JSON: " + err.Error() + ": " + s)
	}
	return out
}

// Path is p (absolute) or Root/p.
func (p *Project) Path(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(p.Root, rel)
}

// Write writes text to Path(path), creating its directories.
func (p *Project) Write(path, text string) string {
	p.t.Helper()
	path = p.Path(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		p.t.Fatal(err)
	}
	return path
}

// Read is the content of Path(path) ("" when missing).
func (p *Project) Read(path string) string {
	b, _ := os.ReadFile(p.Path(path))
	return string(b)
}

// Session records agent's session in the project's log with its transcript file (written empty), as serve records one
// its SessionStart hook confirmed (T-813: a restart resumes that path).
func (p *Project) Session(agent, id string) {
	p.t.Helper()
	path := p.Write(filepath.Join(".obor", "state", "sessions", id+".jsonl"), "")
	p.eventAt(store.SessionTask, store.StepSession, store.Session{Agent: agent, Kind: "claude", ID: id, Transcript: path,
		Started: time.Now().Unix()}, time.Now())
}

// Exists reports whether Path(path) exists.
func (p *Project) Exists(path string) bool {
	_, err := os.Lstat(p.Path(path))
	return err == nil
}

// Commit is lane_commit: a new work.txt committed in dir ("work in <dir name>"), unless a subject is given.
func (p *Project) Commit(dir string, subject ...string) {
	p.t.Helper()
	msg := "work in " + filepath.Base(dir)
	if len(subject) > 0 {
		msg = subject[0]
	}
	p.Write(filepath.Join(dir, "work.txt"), "work\n")
	p.Git(dir, "add", "work.txt")
	p.Git(dir, "commit", "-q", "-m", msg)
}

// Dispatched records the dispatch of the task on lane's branch (task/<id>) in the project's store, as obor serve's
// event log holds it (fact dispatch, attempt 1), and the lane's row as serve projects it: the task awaits its
// task_done (finished 0; the row's agent kept, else the lane's first configured name).
func (p *Project) Dispatched(lane string) {
	p.t.Helper()
	task := p.laneTask(lane)
	row := p.rows()[lane]
	if cfg, err := config.Load(p.Root); err == nil && row.Agent == "" {
		if names := cfg.LaneAgentAliases(lane, ""); len(names) > 0 {
			row.Agent = names[0] // the lane's first name, as serve's dispatch records it
		}
	}
	p.event(task, "dispatch", map[string]any{"lane": lane, "dir": p.LaneDir(lane), "agent": row.Agent}, 0)
	row.Lane, row.Task, row.Branch, row.Finished = lane, task, "task/"+task, 0
	p.setRow(row)
}

// Submit is fx_done since T-437: the dispatch of the task on lane's branch and its task_done in the project's store,
// now: facts dispatch and submitted, the report text (the four sections when none), as obor serve records them, and
// the lane's row finished then.
func (p *Project) Submit(lane string, text ...string) { p.t.Helper(); p.SubmitAgo(lane, 0, text...) }

// SubmitAgo is Submit secs seconds ago.
func (p *Project) SubmitAgo(lane string, secs int64, text ...string) {
	p.t.Helper()
	report := "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	if len(text) > 0 {
		report = text[0]
	}
	p.Dispatched(lane)
	p.event(p.laneTask(lane), "submitted", map[string]any{"lane": lane, "report": report}, secs)
	row := p.rows()[lane]
	row.Finished = time.Now().Add(-time.Duration(secs) * time.Second).Unix()
	p.setRow(row)
}

// laneTask is the task id lane's branch names (task/<id>; else the branch).
func (p *Project) laneTask(lane string) string {
	p.t.Helper()
	if p.plain {
		return strings.TrimPrefix(p.branches[lane], "task/")
	}
	b := strings.TrimSpace(p.Git(p.LaneDir(lane), "symbolic-ref", "--short", "HEAD"))
	return strings.TrimPrefix(b, "task/")
}

// MergedAt appends fact merged of task (attempt 1) to the project's event log at epoch at, as obor serve records a
// merge: its head sha and whether its gate ran the full check (T-525).
func (p *Project) MergedAt(task, sha string, full bool, at int64) {
	p.t.Helper()
	p.eventAt(task, "merged", map[string]any{"head": sha, "full": full}, time.Unix(at, 0))
}

// event appends fact step of task (attempt 1) with data to the project's event log, secs seconds ago.
func (p *Project) event(task, step string, data any, secs int64) {
	p.t.Helper()
	p.eventAt(task, step, data, time.Now().Add(-time.Duration(secs)*time.Second))
}

// eventAt appends fact step of task (attempt 1) with data to the project's event log at at.
func (p *Project) eventAt(task, step string, data any, at time.Time) {
	p.t.Helper()
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		p.t.Fatal(err)
	}
	defer db.Close()
	b, _ := json.Marshal(data)
	ts := at.UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, 'fact', '', ?)`, ts, task, step,
		string(b)); err != nil {
		p.t.Fatal(err)
	}
}

// State writes lane's row in the store's lanes table (what obor serve projects from its log; T-439): v as JSON (a
// JSON text string as is) with the row's keys lane, task, branch, agent (null: ""), base, head, dispatched_at,
// finished, type, blocked, landed; other keys are ignored. The row's lane is lane.
func (p *Project) State(lane string, v any) {
	p.t.Helper()
	text, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			p.t.Fatal(err)
		}
		text = string(b)
	}
	var j struct {
		Task, Branch, Base, Head, Type string
		Agent                          *string
		DispatchedAt                   int64 `json:"dispatched_at"`
		Finished                       int64
		Blocked, Landed                bool
	}
	if err := json.Unmarshal([]byte(text), &j); err != nil {
		p.t.Fatal(err)
	}
	row := store.LaneRow{Lane: lane, Task: j.Task, Branch: j.Branch, Base: j.Base, Head: j.Head,
		DispatchedAt: j.DispatchedAt, Finished: j.Finished, Type: j.Type, Blocked: j.Blocked, Landed: j.Landed}
	if j.Agent != nil {
		row.Agent = *j.Agent
	}
	p.setRow(row)
}

// Landed sets lane's row landed (T-1012) at the lane's HEAD: its task merged and the lane's HEAD the head its submission
// pinned (T-1015), as obor serve projects the submit result and the merged fact; the one free rule (port.FreeState)
// then reads the lane free (dirty-free with uncommitted changes) while its HEAD stays there, whatever the main
// checkout's history.
func (p *Project) Landed(lane string) {
	p.t.Helper()
	row, ok := p.rows()[lane]
	if !ok {
		p.t.Fatalf("Landed: lane %s has no row (Dispatched first)", lane)
	}
	row.Landed, row.Head = true, p.Git(p.LaneDir(lane), "rev-parse", "HEAD")
	p.setRow(row)
}

// ClearState deletes lane's row (a lane with no dispatched task).
func (p *Project) ClearState(lane string) {
	p.t.Helper()
	p.setRow(store.LaneRow{Lane: lane})
}

// Row is lane's row in the lanes table (ok false: none).
func (p *Project) Row(lane string) (store.LaneRow, bool) {
	p.t.Helper()
	row, ok := p.rows()[lane]
	return row, ok
}

// rows is the lanes table by lane.
func (p *Project) rows() map[string]store.LaneRow {
	p.t.Helper()
	rows, err := store.ReadLanesAt(p.Root)
	if err != nil {
		p.t.Fatal(err)
	}
	return rows
}

// Spec stores text as task's spec in the project's store, as obor spec set does through serve (T-440): the next rev.
func (p *Project) Spec(task, text string) {
	p.t.Helper()
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		p.t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := store.SetSpec(db, task, text, "", "lead", time.Now()); err != nil {
		p.t.Fatal(err)
	}
}

// setRow replaces row.Lane's row with row (a row with no task: deleted).
func (p *Project) setRow(row store.LaneRow) {
	p.t.Helper()
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		p.t.Fatal(err)
	}
	defer db.Close()
	all, err := store.ReadLanes(db)
	if err != nil {
		p.t.Fatal(err)
	}
	rows := []store.LaneRow{}
	for _, r := range all {
		if r.Lane != row.Lane {
			rows = append(rows, r)
		}
	}
	if row.Task != "" || row.Agent != "" || row.Branch != "" || row.Base != "" {
		rows = append(rows, row)
	}
	if err := store.WriteLanes(db, rows); err != nil {
		p.t.Fatal(err)
	}
}

// Age sets the mtime of path to secs seconds ago.
func Age(t testing.TB, path string, secs int64) {
	t.Helper()
	when := time.Now().Add(-time.Duration(secs) * time.Second)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// Age sets the mtime of Path(path) to secs seconds ago.
func (p *Project) Age(path string, secs int64) { p.t.Helper(); Age(p.t, p.Path(path), secs) }

// Mtime is the mtime of Path(path) in epoch seconds.
func (p *Project) Mtime(path string) int64 {
	p.t.Helper()
	fi, err := os.Stat(p.Path(path))
	if err != nil {
		p.t.Fatal(err)
	}
	return fi.ModTime().Unix()
}

// DeadPID is the pid of a process that has exited (and was reaped).
func DeadPID(t testing.TB) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// Sleeper starts a process that lives until the test ends (or stop is called); returns its pid.
func Sleeper(t testing.TB, args ...string) (pid int, stop func()) {
	t.Helper()
	if len(args) == 0 {
		args = []string{"sleep", "60"}
	}
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	stop = func() {
		if !done {
			done = true
			cmd.Process.Kill()
			cmd.Wait()
		}
	}
	t.Cleanup(stop)
	return cmd.Process.Pid, stop
}

// Herdr is the project's fake herdr (herdrtest), created on first use: script its panes, then serve it
// (herdrtest.Serve) and point the code under test at the socket with HERDR_SOCKET_PATH.
func (p *Project) Herdr() *herdrtest.Fake {
	if p.herdr == nil {
		p.herdr = herdrtest.New()
	}
	return p.herdr
}

// enablePlugin gives lane dir the project settings a real lane has (T-705: obor's plugin enabled, as obor init links
// them), excluded from git so the lane stays clean.
func (p *Project) enablePlugin(dir string) {
	p.t.Helper()
	settings := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"enabledPlugins":{"obor@obor-kit":true}}`+"\n"), 0o644); err != nil {
		p.t.Fatal(err)
	}
	if p.plain {
		return // no git to exclude it from
	}
	exclude := filepath.Join(p.Root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		p.t.Fatal(err)
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		p.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("/.claude/\n.claude/\n"); err != nil {
		p.t.Fatal(err)
	}
}
