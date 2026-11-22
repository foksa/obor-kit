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
	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	"obor/internal/agents/plugin" // the plugin kinds a config names (kinds.<name>.command)
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/store"
	agykind "obor/kinds/agy/kind"
	ockind "obor/kinds/opencode/kind"
)

// OpenCode and agy are plugin kinds: a test binary has no built obor-kind-opencode or obor-kind-agy, so each kind
// answers in process (plugin.Local) with the handler its binary runs; a loaded config's kinds hold them
// (config.Config.Kinds), and Kinds are the kinds of a test that has no config. agy's start checks its model against
// AgyModels, never the host's `agy models` (no unit test runs the real agy: it needs a login, the network and a local
// socket a sandbox may refuse).
func init() {
	plugin.Local[ockind.Name] = ockind.Handle
	plugin.Local[agykind.Name] = agykind.HandleModels(func() ([]byte, error) { return []byte(AgyModels), nil })
}

// AgyModels is the model list the in-process agy kind's start checks against, in `agy models`'s shape (one slug, a tab
// and its label per line): the models a test's agy type names (models.<name>.id).
const AgyModels = "gemini-4-neon-high\tGemini 4 Neon (High)\n" +
	"gemini-4-neon-medium\tGemini 4 Neon (Medium)\n" +
	"gemini-4-neon-low\tGemini 4 Neon (Low)\n"

// Kinds are the compiled kinds and the kit's plugin kinds (OpenCode, agy) as a config naming none of its own has them,
// for a test with no project.
func Kinds() agents.Kinds {
	var ks []agents.Kind
	for name, bin := range config.KitKinds {
		ks = append(ks, agents.Plugins.Kind(name, bin, ""))
	}
	return agents.WithPlugins(ks)
}

// Main runs a package's tests isolated (Isolate) and removes the project template after them (Run). Call it from
// TestMain.
func Main(m *testing.M) {
	Isolate()
	os.Exit(Run(m))
}

// Run is m.Run followed by the removal of the project template New copies, for a TestMain that does more than
// Main: it returns m.Run's code. It also arms New: only a TestMain that calls it removes the template. It sets
// no wire deadline: a call to a serve that is running gets its method's deadline from wire/client.go's table,
// seconds, where a 0.2 s bound for the whole binary timed the app tests out under the nightly's load. A test whose call
// means to hit a serve that is not there, and must end at once, sets the short deadline itself: a ctx deadline, or
// t.Setenv("OBOR_CALL_DEADLINE", …) as cmd/obor's callDown and internal/setup do.
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

// hooked is Run's: this binary's TestMain removes the template. template checks it before it builds, so the
// next package that calls New without Main/Run fails at once instead of leaving an hk-tmpl* dir in TMPDIR per run.
var hooked atomic.Bool

// tmpl is the project every New copies: <base>/proj with its one commit on main, built once per test binary on
// first use and removed by Run. builds counts the builds (a test checks it is one).
var tmpl struct {
	once   sync.Once
	base   string
	err    error
	builds int
}

// scripts is Script's cache: one written executable per body, hardlinked into the tests' dirs. macOS scans a
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
			return copyFile(path, to, info.Mode().Perm(), info.ModTime())
		}
		return fmt.Errorf("copy %s: not a dir, file or symlink", path)
	})
}

// copyFile copies the file src to dst (which must not exist) with mode perm and mtime.
func copyFile(src, dst string, perm fs.FileMode, mtime time.Time) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, perm); err != nil {
		return err
	}
	return os.Chtimes(dst, mtime, mtime)
}

// Script writes body as the executable dir/name and returns its path: a test's fake launchctl, a stub obor. It is one
// copy per body per test binary, hardlinked into dir: macOS scans a fresh executable before its first exec
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
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// No detached `git maintenance run --auto` after a commit: it can still write .git/objects while the test's temp
	// dir is removed ("directory not empty").
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
	branches map[string]string // lane → the branch Lane put it on (a git project's: until a git call through the project)
	// pristine: the project's tree is still the one key names, New's copy changed by setup calls alone (setup.go).
	pristine bool
	key      string
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
// the binary's template: no git process here.
func NewAt(t testing.TB, base string) *Project {
	t.Helper()
	p := &Project{t: t, Base: base, pristine: true}
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
// nothing is created and nothing is removed; base lives with the test's t.TempDir.
func Adopt(t testing.TB, base string) *Project {
	return &Project{t: t, Base: base, Root: filepath.Join(base, "proj")}
}

// NewDir is a plain project dir <Base>/proj with no git: for tests whose git side is a fake (internal/app's
// FakeWorkflow). Config writes .obor/config.json there; Lane makes a plain lane dir; Git fails the test.
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
		"HOME":            filepath.Join(p.Base, "home"),
		"OBOR_STATE_HOME": filepath.Join(p.Base, "state-home"),
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

// GitEnv is Git with more environment (GIT_COMMITTER_DATE=…). It ends the project's setup run: the cache's trees
// and the lanes' branches Lane noted are what setup calls alone made.
func (p *Project) GitEnv(env []string, dir string, args ...string) string {
	p.t.Helper()
	p.pristine = false
	if !p.plain {
		p.branches = nil
	}
	return p.git(env, dir, args...)
}

// git is GitEnv inside a setup call.
func (p *Project) git(env []string, dir string, args ...string) string {
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
// branch is "". In a plain project (NewDir) it is a plain dir and its branch a note. A setup call (setup.go).
func (p *Project) Lane(name, branch string) string {
	p.t.Helper()
	dir := p.LaneDir(name)
	if p.plain {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			p.t.Fatal(err)
		}
		p.branches[name] = branch
		p.enablePlugin(dir)
		return dir
	}
	p.cached("lane\x00"+name+"\x00"+branch, func() {
		switch {
		case branch == "":
			p.git(nil, p.Root, "worktree", "add", "-q", "--detach", dir, "main")
		case exec.Command("git", "-C", p.Root, "show-ref", "--verify", "-q", "refs/heads/"+branch).Run() == nil:
			p.git(nil, p.Root, "worktree", "add", "-q", dir, branch)
		default:
			p.git(nil, p.Root, "worktree", "add", "-q", "-b", branch, dir, "main")
		}
		p.enablePlugin(dir)
	})
	if p.branches == nil {
		p.branches = map[string]string{}
	}
	p.branches[name] = branch
	return dir
}

// LaneDir is <Base>/proj-wt/<name>.
func (p *Project) LaneDir(name string) string { return filepath.Join(p.Base, "proj-wt", name) }

// Config is fx_config: the default config in shape 2 (project pj, lanes: the highest L-NN under proj-wt; the
// kit's OpenCode plugin kind, as obor init writes it; agents.models: each of config.UpgradeModels; agents.difficulty:
// fixtureDifficulty), changed by edit, written to .obor/config.json and
// committed on main (projects commit it; a plain project's stays uncommitted). An edit names shape 2's paths: the map
// is written as it is. A setup call (setup.go).
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
		"version": 2,
		"project": map[string]any{"name": "proj", "agent_prefix": "pj", "task_id_pattern": "T-[0-9]+[a-z]?"},
		"lanes":   map[string]any{"count": lanes, "dir": "../proj-wt", "task_branch": "task/{id}", "links": []any{}},
		"agents": map[string]any{"kinds": map[string]any{"opencode": map[string]any{"plugin": config.KitKinds["opencode"]}},
			"models": fixtureModels(), "difficulty": fixtureDifficulty()},
		"rules":    map[string]any{"protected_files": []any{}},
		"merge":    map[string]any{"target_branch": "main"},
		"commands": map[string]any{},
		"reviews":  map[string]any{"dir": "docs/reviews"},
	}
	for _, e := range edit {
		e(c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		p.t.Fatal(err)
	}
	text := config.Pretty(json.RawMessage(b))
	if p.plain {
		p.write(".obor/config.json", text)
		return
	}
	p.cached("config\x00"+text, func() {
		p.write(".obor/config.json", text)
		p.git(nil, p.Root, "add", ".obor/config.json")
		if exec.Command("git", "-C", p.Root, "diff", "--cached", "--quiet", "--", ".obor/config.json").Run() != nil {
			p.git(nil, p.Root, "commit", "-q", "-m", "obor config", "--", ".obor/config.json")
		}
	})
}

// fixtureModels is the fixture config's agents.models: each of config.UpgradeModels, name → {kind, id}.
func fixtureModels() map[string]any {
	out := map[string]any{}
	for _, e := range config.UpgradeModels {
		out[e.Name] = map[string]any{"kind": e.Kind, "id": e.ID}
	}
	return out
}

// fixtureDifficulty is the fixture config's agents.difficulty: hard opus at effort high, medium sonnet then deepseek,
// simple deepseek, sonnet, haiku.
func fixtureDifficulty() map[string]any {
	return map[string]any{"hard": []any{"opus:high"}, "medium": []any{"sonnet", "deepseek"},
		"simple": []any{"deepseek", "sonnet", "haiku"}}
}

// Set is a config edit: the value at a dotted path ("agents.roles.lead"), creating objects on the way. v may be a JSON text
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

// AddModels is a config edit: each member of entries (a JSON object text, name → {kind, id}) added to
// agents.models beside the fixture's own.
func AddModels(entries string) func(map[string]any) {
	return func(c map[string]any) {
		add, ok := JSON(entries).(map[string]any)
		if !ok {
			panic("testkit.AddModels: not a JSON object: " + entries)
		}
		for name, e := range add {
			Set("agents.models."+name, e)(c)
		}
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

// Write writes text to Path(path), creating its directories. It ends the project's setup run (setup.go).
func (p *Project) Write(path, text string) string {
	p.t.Helper()
	p.pristine = false
	return p.write(path, text)
}

// write is Write inside a setup call.
func (p *Project) write(path, text string) string {
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
// its SessionStart hook confirmed (a restart resumes that path).
func (p *Project) Session(agent, id string) {
	p.t.Helper()
	path := p.Write(filepath.Join(".obor", "state", "sessions", id+".jsonl"), "")
	p.eventAt(store.SessionTask, store.StepSession, store.Session{Agent: agent, Kind: "claude", ID: id, Transcript: path,
		Started: time.Now().Unix()}, time.Now())
}

// SessionOf records agent's session id of kind with its transcript file (a path, not written), as serve records one
// its SessionStart hook confirmed.
func (p *Project) SessionOf(agent, kind, id, transcript string) {
	p.t.Helper()
	p.eventAt(store.SessionTask, store.StepSession, store.Session{Agent: agent, Kind: kind, ID: id, Transcript: transcript,
		Started: time.Now().Unix()}, time.Now())
}

// Exists reports whether Path(path) exists.
func (p *Project) Exists(path string) bool {
	_, err := os.Lstat(p.Path(path))
	return err == nil
}

// Commit is lane_commit: a new work.txt committed in dir ("work in <dir name>"), unless a subject is given. A setup
// call when dir is in the project (setup.go).
func (p *Project) Commit(dir string, subject ...string) {
	p.t.Helper()
	msg := "work in " + filepath.Base(dir)
	if len(subject) > 0 {
		msg = subject[0]
	}
	step := ""
	if rel, err := filepath.Rel(p.Base, p.Path(dir)); err == nil && (rel == "proj" || strings.HasPrefix(rel, "proj"+string(filepath.Separator)) ||
		strings.HasPrefix(rel, "proj-wt"+string(filepath.Separator))) {
		step = "commit\x00" + rel + "\x00" + msg
	}
	p.cached(step, func() {
		p.write(filepath.Join(dir, "work.txt"), "work\n")
		p.git(nil, dir, "add", "work.txt")
		p.git(nil, dir, "commit", "-q", "-m", msg)
	})
}

// Dispatched records the dispatch of the task on lane's branch (task/<id>) in the project's store, as obor serve's
// event log holds it (fact dispatch, attempt 1), and the lane's row as serve projects it: the task awaits its
// task_done (finished 0) and its dispatch names the lane's derived agent (no row stores it).
func (p *Project) Dispatched(lane string) {
	p.t.Helper()
	task := p.laneTask(lane)
	row := p.rows()[lane]
	agent := ""
	if cfg, err := config.Load(p.Root); err == nil {
		agent = cfg.AgentName("worker", lane) // as serve's dispatch records it
	}
	p.event(task, "dispatch", map[string]any{"lane": lane, "dir": p.LaneDir(lane), "agent": agent}, 0)
	row.Lane, row.Task, row.Branch, row.Finished = lane, task, "task/"+task, 0
	p.setRow(row)
}

// Submit is fx_done: the dispatch of the task on lane's branch and its task_done in the project's store,
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

// laneTask is the task id lane's branch names (task/<id>; else the branch): the branch Lane put it on while no git
// call through the project has run since, else the lane's HEAD.
func (p *Project) laneTask(lane string) string {
	p.t.Helper()
	if b, ok := p.branches[lane]; p.plain || (ok && b != "") {
		return strings.TrimPrefix(b, "task/")
	}
	b := strings.TrimSpace(p.Git(p.LaneDir(lane), "symbolic-ref", "--short", "HEAD"))
	return strings.TrimPrefix(b, "task/")
}

// MergedAt appends fact merged of task (attempt 1) to the project's event log at epoch at, as obor serve records a
// merge: its head sha and whether its gate ran the full check.
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
	p.pristine = false
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

// State writes lane's row in the store's lanes table (what obor serve projects from its log): v as JSON (a
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
		DispatchedAt                   int64 `json:"dispatched_at"`
		Finished                       int64
		Blocked, Landed                bool
	}
	if err := json.Unmarshal([]byte(text), &j); err != nil {
		p.t.Fatal(err)
	}
	row := store.LaneRow{Lane: lane, Task: j.Task, Branch: j.Branch, Base: j.Base, Head: j.Head,
		DispatchedAt: j.DispatchedAt, Finished: j.Finished, Type: j.Type, Blocked: j.Blocked, Landed: j.Landed}
	p.setRow(row)
}

// Landed sets lane's row landed at the lane's HEAD: its task merged and the lane's HEAD the head its submission
// pinned, as obor serve projects the submit result and the merged fact; the one free rule (port.FreeState)
// then reads the lane free (dirty-free with uncommitted changes), whatever the main checkout's history.
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

// Spec stores text as task's spec in the project's store, as obor spec set does through serve: the next rev.
func (p *Project) Spec(task, text string) {
	p.t.Helper()
	p.pristine = false
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
	p.pristine = false
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
	if row.Task != "" || row.Branch != "" || row.Base != "" {
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
func (p *Project) Age(path string, secs int64) {
	p.t.Helper()
	p.pristine = false
	Age(p.t, p.Path(path), secs)
}

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

// enablePlugin gives lane dir the project settings a real lane has (obor's plugin enabled, as obor init links
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

// HookKind is a kind with no inbox whose hook transport acts on the hooks' answers (agents.Caps.HookAnswers), as
// agy's is to: a lead of it takes its lines through its hooks. Inert otherwise (no transcript, no headless form, no
// delivery). Not registered here: a test package that runs such a lead registers it (agents.Register) and names it in
// its config with HookModel.
type HookKind struct{}

// HookKindName is HookKind's name.
const HookKindName = "hookkind"

// HookModel is the config edit adding the model hooky of HookKind (a role's type: agents.roles.<role> "hooky").
func HookModel() func(map[string]any) {
	return Set("agents.models.hooky", map[string]any{"kind": HookKindName, "id": "hooky"})
}

func (HookKind) Name() string                                       { return HookKindName }
func (HookKind) NotReady(string) string                             { return "" }
func (HookKind) Efforts() []string                                  { return nil }
func (HookKind) Vars() []string                                     { return nil }
func (HookKind) Stop() agents.Stop                                  { return agents.Stop{} }
func (HookKind) Headless() agents.Headless                          { return agents.Headless{} }
func (HookKind) Install(string, func(string) string) agents.Install { return agents.Install{} }
func (HookKind) Transcript(func(string) string, string) string      { return "" }
func (HookKind) LastTurn([]byte) (agents.Turn, bool)                { return agents.Turn{}, false }
func (HookKind) Context([]byte) (agents.Context, bool)              { return agents.Context{}, false }
func (HookKind) Session([]byte) (agents.Session, bool)              { return agents.Session{}, false }
func (HookKind) Reset(string, string, time.Time) (time.Time, bool) {
	return time.Time{}, false
}
func (HookKind) Deliver(string, []byte) (agents.Delivery, bool) { return agents.Delivery{}, false }
func (HookKind) InboxRecord(func(string) string) []byte         { return nil }
func (HookKind) Caps() agents.Caps {
	return agents.Caps{Hooks: agents.HooksPlugin, HookAnswers: true, Status: agents.StatusHooks}
}
