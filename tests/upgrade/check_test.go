// Package upgrade is the release's upgrade check (`make upgrade-check`): a project made by the previous release
// must survive the new build installed over it. The check is a test because it drives herdr's stub
// (internal/herdr/herdrtest), which only test code may import; TestUpgradeCheck is the release's run (OBOR_UPGRADE_CHECK=1,
// skipped otherwise), the other tests hold the guide's steps, the previous release and the refusal of an old one on
// fixed fixtures (the two frozen-binary runs went; `make upgrade-check` is the real check at each release).
package upgrade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
)

// The check, in three steps (stepErr names the one that failed):
//  1. In a temp HOME and OBOR_STATE_HOME, two git projects (alpha, beta) made by the previous release's obor init
//     (its kit: the previous tag's tree; herdr: its stub; claude and launchctl: stubs that record nothing real), with
//     init's files committed as an adopter commits them (the previous init leaves them uncommitted in a repo with a
//     commit). Serve runs for both on the previous binary as a child process (no launchd); each project sends its
//     other's lead a line no session takes yet (held: no inbox recorded), and alpha holds a dispatch waiting for a task
//     (--after) whose spec names its model (Model:).
//  2. The new build replaces the old: the steps of the guide (docs/UPGRADE-v<x.y>.md, guideSteps) run in each project
//     with the new obor first on PATH while the old serves run; then each serve is stopped.
//  3. Each serve starts on the new binary and answers on its socket; obor projects lists both; obor status and obor
//     setup show load; the dispatch still waits with its spec's model first; each lead, started now under its current
//     name (its SessionStart hook records its inbox), gets the line held for it; obor send works both ways.
//
// The Model: assertion needs a previous release of v0.23.0 or later (an older release's specs named a lane type):
// an older PREV is refused before the check runs (supported).

// check is one run of the upgrade check.
type check struct {
	prevBin, newBin string // the previous release's binary, the new build
	prevKit, newKit string // their kits (OBOR_PLUGIN_ROOT): the previous tag's tree, this checkout
	prev, next      string // their versions (x.y.z)
	guide           string // the new minor's guide (docs/UPGRADE-v<x.y>.md), "" for none
	log             func(format string, args ...any)
}

// stepErr is a failed step: its number, what failed, and the output that shows it.
type stepErr struct {
	step   int
	what   string
	output string
}

func (e *stepErr) Error() string {
	s := fmt.Sprintf("step %d: %s", e.step, e.what)
	if out := strings.TrimSpace(e.output); out != "" {
		s += "\n" + out
	}
	return s
}

func fail(step int, output, format string, args ...any) error {
	return &stepErr{step: step, what: fmt.Sprintf(format, args...), output: output}
}

// proj is one of the check's projects: its name, prefix, main checkout, and the serve running for it.
type proj struct {
	name, prefix, root string
	serve              *serveProc
}

// world is the check's machine: its temp dirs, the herdr stub, the environment every command runs in.
type world struct {
	t       testing.TB
	c       check
	base    string
	home    string
	state   string
	stub    string          // claude and launchctl stubs
	oborDir string          // the dir with the obor on PATH (a symlink to the binary of the phase)
	herdr   string          // the stub herdr's socket
	fake    *herdrtest.Fake // the stub herdr's state
	kit     string          // OBOR_PLUGIN_ROOT of the phase
	bin     string          // the binary of the phase
	projs   []*proj
	inboxes map[string]*inbox
}

// run is the check: nil, or the stepErr of the step that failed. Its serves are stopped when it returns, its temp
// dirs removed when t ends.
func (c check) run(t testing.TB) error {
	t.Helper()
	w := &world{t: t, c: c, inboxes: map[string]*inbox{}}
	defer func() {
		for _, p := range w.projs {
			p.serve.stop()
		}
		for _, in := range w.inboxes {
			in.close()
		}
	}()
	if err := w.setup(); err != nil {
		return err
	}
	if err := w.step1(); err != nil {
		return err
	}
	if err := w.step2(); err != nil {
		return err
	}
	return w.step3()
}

// setup makes the temp machine (short paths: a serve socket under a project must fit a unix socket address).
func (w *world) setup() error {
	base, err := os.MkdirTemp("", "upg")
	if err != nil {
		return fail(1, "", "temp dir: %v", err)
	}
	w.t.Cleanup(func() { os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil {
		return fail(1, "", "temp dir: %v", err)
	}
	w.base = base
	w.home, w.state, w.stub, w.oborDir = filepath.Join(base, "home"), filepath.Join(base, "sh"), filepath.Join(base, "stub"),
		filepath.Join(base, "obor")
	for _, d := range []string{w.home, w.state, w.stub, w.oborDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail(1, "", "temp dir: %v", err)
		}
	}
	log := filepath.Join(w.stub, "calls")
	stubs := map[string]string{
		// claude: plugin install enables the plugin in the project, as the real one does; nothing else runs.
		"claude": "#!/bin/sh\necho \"claude $*\" >> " + log + "\ncase \"$*\" in \"plugin install \"*) mkdir -p .claude; " +
			"printf '{\"enabledPlugins\":{\"%s\":true}}' \"$3\" > .claude/settings.json ;; esac\n",
		// launchctl: obor init's serve install bootstraps a plist nothing runs (no launchd: serve is a child here);
		// print knows each label bootstrapped and not booted out.
		"launchctl": "#!/bin/sh\necho \"launchctl $*\" >> " + log + "\nd=" + w.stub + "\ncase \"$1\" in\n" +
			"  print) [ -f \"$d/loaded.${2##*/}\" ] || exit 113; printf '\\tpid = 999999\\n' ;;\n" +
			"  bootstrap) l=${3##*/}; : > \"$d/loaded.${l%.plist}\" ;;\n" +
			"  bootout) rm -f \"$d/loaded.${2##*/}\" ;;\nesac\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(w.stub, name), []byte(script), 0o755); err != nil {
			return fail(1, "", "stub %s: %v", name, err)
		}
	}
	w.fake = herdrtest.New()
	w.herdr = herdrtest.Serve(w.t, w.fake).Path
	return nil
}

// phase points obor (PATH, OBOR_PLUGIN_ROOT) at bin and kit.
func (w *world) phase(bin, kit string) error {
	link := filepath.Join(w.oborDir, "obor")
	_ = os.Remove(link)
	w.bin, w.kit = bin, kit
	return os.Symlink(bin, link)
}

// env is the environment of every command: the caller's, without its obor, herdr, Claude and Codex identity, with the
// temp HOME and state home, the stubs and the phase's obor first on PATH.
func (w *world) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "OBOR_") || strings.HasPrefix(k, "HERDR_") || strings.HasPrefix(k, "CLAUDE") ||
			k == "CODEX_HOME" || k == "HOME" || k == "PATH" || strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "CHECK_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+w.home, "OBOR_STATE_HOME="+w.state, "OBOR_PLUGIN_ROOT="+w.kit,
		"HERDR_SOCKET_PATH="+w.herdr, "PATH="+w.stub+":"+w.oborDir+":"+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=obor upgrade", "GIT_AUTHOR_EMAIL=upgrade@example.invalid",
		"GIT_COMMITTER_NAME=obor upgrade", "GIT_COMMITTER_EMAIL=upgrade@example.invalid")
}

// obor runs the phase's binary with args in dir (60 s at most): its combined output and whether it exited 0.
func (w *world) obor(dir string, args ...string) (string, bool) {
	return w.cmd(dir, w.bin, args...)
}

// cmd runs prog with args in dir in the check's environment (60 s at most): its combined output, after the command
// line, and whether it exited 0.
func (w *world) cmd(dir, prog string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir, cmd.Env = dir, w.env()
	out, err := cmd.CombinedOutput()
	text := "$ " + filepath.Base(prog) + " " + strings.Join(args, " ") + "\n" + string(out)
	if err != nil {
		text += fmt.Sprintf("(%v)\n", err)
	}
	return text, err == nil
}

// step1: the two projects on the previous release, their held lines and alpha's waiting dispatch.
func (w *world) step1() error {
	if err := w.phase(w.c.prevBin, w.c.prevKit); err != nil {
		return fail(1, "", "the previous binary on PATH: %v", err)
	}
	for _, p := range []*proj{{name: "alpha", prefix: "al"}, {name: "beta", prefix: "be"}} {
		p.root = filepath.Join(w.base, p.name)
		for _, args := range [][]string{{"-c", "init.defaultBranch=main", "init", "-q", p.root},
			{"-C", p.root, "commit", "-q", "--allow-empty", "-m", "initial"}} {
			if out, ok := w.cmd(w.base, "git", args...); !ok {
				return fail(1, out, "git for %s", p.name)
			}
		}
		if out, ok := w.obor(p.root, "init", "--name", p.name, "--prefix", p.prefix); !ok {
			return fail(1, out, "v%s obor init of %s", w.c.prev, p.name)
		}
		// The previous release's init leaves its files uncommitted in a repository that had a commit: an adopter
		// commits them.
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "obor init"}} {
			if out, ok := w.cmd(p.root, "git", args...); !ok {
				return fail(1, out, "commit obor init's files in %s", p.name)
			}
		}
		w.projs = append(w.projs, p)
	}
	for _, p := range w.projs {
		s, err := w.startServe(p)
		if err != nil {
			return fail(1, s.output(), "v%s serve of %s: %v", w.c.prev, p.name, err)
		}
		p.serve = s
	}
	alpha, beta := w.projs[0], w.projs[1]
	for _, s := range []struct{ from, to *proj }{{alpha, beta}, {beta, alpha}} {
		if out, ok := w.obor(s.from.root, "send", s.to.name, heldLine(s.from)); !ok {
			return fail(1, out, "v%s obor send %s (held: no session of its lead yet)", w.c.prev, s.to.name)
		}
	}
	for id, text := range map[string]string{"T-1": spec("T-1", ""), "T-2": spec("T-2", waitingModel)} {
		f := filepath.Join(w.base, id+".md")
		if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
			return fail(1, "", "spec %s: %v", id, err)
		}
		if out, ok := w.obor(alpha.root, "spec", "set", id, f); !ok {
			return fail(1, out, "v%s obor spec set %s", w.c.prev, id)
		}
	}
	out, ok := w.obor(alpha.root, "dispatch", "T-2", "--after", "T-1")
	if !ok || !strings.Contains(out, "WAITING") {
		return fail(1, out, "v%s obor dispatch T-2 --after T-1: want WAITING", w.c.prev)
	}
	w.c.log("step 1: v%s projects alpha and beta, serves up, held lines, T-2 waiting:\n%s", w.c.prev, out)
	return nil
}

// waitingModel is the model the waiting dispatch's spec names (Model:): of its level (simple: sonnet, haiku), not the
// level's default, so a model taken from the level alone shows.
const waitingModel = "haiku"

func heldLine(from *proj) string { return "held line from " + from.name }

// spec is a task's spec text; model "" for none.
func spec(id, model string) string {
	s := "# " + id + ": upgrade check task\nDifficulty: simple\n"
	if model != "" {
		s += "Model: " + model + "\n"
	}
	return s + "\n## Goal\nNothing: the upgrade check's task.\n"
}

// step2: the new build replaces the old: the guide's steps in each project (the old serves still run, as they do
// while the owner re-runs the installer), then each serve stopped as launchd stops it.
func (w *world) step2() error {
	blocks, err := guideSteps(w.c.guide, w.c.prev, w.c.next)
	if err != nil {
		return fail(2, "", "%v", err)
	}
	if err := w.phase(w.c.newBin, w.c.newKit); err != nil {
		return fail(2, "", "the new binary on PATH: %v", err)
	}
	for _, p := range w.projs {
		for _, b := range blocks {
			out, ok := w.cmd(p.root, "/bin/sh", "-ec", b.text)
			if !ok {
				return fail(2, out, "the v%s steps of the guide in %s", b.version, p.name)
			}
			w.c.log("step 2: v%s steps in %s:\n%s", b.version, p.name, out)
		}
	}
	for _, p := range w.projs {
		if err := p.serve.stop(); err != nil {
			return fail(2, p.serve.output(), "stop the v%s serve of %s: %v", w.c.prev, p.name, err)
		}
	}
	return nil
}

// step3: each serve started on the new binary, and what must hold there.
func (w *world) step3() error {
	for _, p := range w.projs {
		s, err := w.startServe(p)
		p.serve = s
		if err != nil {
			return fail(3, s.output(), "serve of %s on the new binary is not up: %v", p.name, err)
		}
	}
	for _, p := range w.projs {
		if _, err := w.ask(p); err != nil {
			return fail(3, p.serve.output(), "%v", err)
		}
	}
	for _, p := range w.projs {
		for _, args := range [][]string{{"status"}, {"setup", "show"}} {
			out, ok := w.obor(p.root, args...)
			if !ok || hasFail(out) {
				return fail(3, out+"\nserve:\n"+p.serve.output(), "obor %s in %s does not load", strings.Join(args, " "), p.name)
			}
		}
	}
	out, ok := w.obor(w.projs[0].root, "projects")
	for _, p := range w.projs {
		if !ok || !regexp.MustCompile(`(?m)^\s*`+p.name+`\b`).MatchString(out) {
			return fail(3, out, "obor projects does not list %s", p.name)
		}
	}
	// The dispatch still waits for T-1, its spec's model first (serve's own status: obor status shows no models
	// of a dispatch waiting for tasks).
	st, err := w.ask(w.projs[0])
	if err != nil {
		return fail(3, w.projs[0].serve.output(), "%v", err)
	}
	if why := waits(st, "T-2", "T-1", waitingModel); why != "" {
		b, _ := json.MarshalIndent(st.Project.Tasks, "", "  ")
		return fail(3, string(b), "T-2: %s", why)
	}
	// Each lead starts under its current name: the line held for it reaches it. Both start before either is awaited:
	// serve delivers a held lead line on its next pass (its tick, 30 s), not when the session is recorded.
	for _, p := range w.projs {
		if err := w.startLead(p); err != nil {
			return fail(3, "", "%v", err)
		}
	}
	for _, p := range w.projs {
		in := w.inboxes[p.name]
		if err := in.await(heldLine(w.other(p)), lineWait); err != nil {
			return fail(3, p.serve.output(), "the line held for %s's lead (%s) is not delivered: %v", p.name, in.agent, err)
		}
	}
	for _, p := range w.projs {
		to := w.other(p)
		text := "after the upgrade from " + p.name
		out, ok := w.obor(p.root, "send", to.name, text)
		if !ok {
			return fail(3, out, "obor send %s from %s", to.name, p.name)
		}
		if err := w.inboxes[to.name].await(text, lineWait); err != nil {
			return fail(3, out+"\nserve:\n"+to.serve.output(), "obor send %s from %s is not delivered: %v", to.name, p.name, err)
		}
	}
	w.c.log("step 3: both serves on the new binary, projects, status, setup show, held lines, T-2 waiting, send both ways")
	return nil
}

func (w *world) other(p *proj) *proj {
	if p == w.projs[0] {
		return w.projs[1]
	}
	return w.projs[0]
}

func hasFail(out string) bool { return regexp.MustCompile(`(?m)^FAIL\b`).MatchString(out) }

// waits is why task is not waiting for after with model first among its models in st ("": it is).
func waits(st wire.Status, task, after, model string) string {
	for _, t := range st.Project.Tasks {
		if t.Task != task {
			continue
		}
		switch {
		case t.State != "waiting":
			return "its state is " + t.State + ", not waiting"
		case !slices.Equal(t.After, []string{after}):
			return fmt.Sprintf("it waits for %v, not %s", t.After, after)
		case len(t.Types) == 0 || t.Types[0] != model:
			return fmt.Sprintf("its models are %v: its spec's Model: %s is not the first", t.Types, model)
		}
		return ""
	}
	return "not among serve's tasks"
}

// ask is the status of p's serve over its socket (10 s at most).
func (w *world) ask(p *proj) (wire.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := wire.Ask(ctx, wire.SocketPath(p.root))
	if err == nil && st.Project == nil {
		err = errors.New("no project loaded")
	}
	if err != nil {
		return st, fmt.Errorf("serve of %s does not answer on its socket: %v", p.name, err)
	}
	return st, nil
}

// lineWait bounds the wait for a line to reach a lead: serve's tick (30 s) and a margin.
const lineWait = 75 * time.Second

// startLead is p's lead session starting now: an inbox socket listening, and the new binary's SessionStart hook run as
// the lead's Claude session runs it (OBOR_ROLE lead, OBOR_AGENT its current name, the session's messaging socket).
func (w *world) startLead(p *proj) error {
	cfg, err := config.Load(p.root)
	if err != nil {
		return fmt.Errorf("the new config of %s: %v", p.name, err)
	}
	agent := cfg.AgentName("lead", "")
	in, err := listen(filepath.Join(w.base, p.prefix+".in"), agent)
	if err != nil {
		return err
	}
	w.inboxes[p.name] = in
	event := fmt.Sprintf(`{"session_id":"upgrade-%s","cwd":%q,"hook_event_name":"SessionStart","source":"startup"}`, p.name, p.root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.bin, "hook", "session-start")
	cmd.Dir, cmd.Stdin = p.root, strings.NewReader(event)
	cmd.Env = append(w.env(), "OBOR_ROLE=lead", "OBOR_AGENT="+agent, "CLAUDE_CODE_MESSAGING_SOCKET="+in.path,
		"CLAUDE_CODE_MESSAGING_TOKEN=upgrade-token")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("the SessionStart hook of %s (%s): %v\n%s", agent, p.name, err, out)
	}
	return nil
}

// inbox is a lead session's messaging socket: every line posted to it.
type inbox struct {
	agent, path string
	ln          net.Listener
	mu          sync.Mutex
	got         []string
}

func listen(path, agent string) (*inbox, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("inbox socket of %s: %v", agent, err)
	}
	in := &inbox{agent: agent, path: path, ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					in.mu.Lock()
					in.got = append(in.got, sc.Text())
					in.mu.Unlock()
				}
			}()
		}
	}()
	return in, nil
}

func (in *inbox) close() { in.ln.Close() }

// await: a line with text arrives within d.
func (in *inbox) await(text string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		in.mu.Lock()
		got := strings.Join(in.got, "\n")
		in.mu.Unlock()
		if strings.Contains(got, text) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no line with %q within %s (got: %q)", text, d, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// serveProc is a serve running as a child process, its output kept.
type serveProc struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  strings.Builder
	done chan struct{}
}

func (s *serveProc) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(b)
}

func (s *serveProc) output() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// startServe runs the phase's serve for p and waits until it answers (its running line), 60 s at most.
func (w *world) startServe(p *proj) (*serveProc, error) {
	s := &serveProc{done: make(chan struct{})}
	s.cmd = exec.Command(w.bin, "serve")
	s.cmd.Dir, s.cmd.Env, s.cmd.Stdout, s.cmd.Stderr = p.root, w.env(), s, s
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := s.cmd.Start(); err != nil {
		close(s.done)
		return s, err
	}
	go func() { _ = s.cmd.Wait(); close(s.done) }()
	deadline := time.After(60 * time.Second)
	for {
		if strings.Contains(s.output(), "obor serve: running") {
			return s, nil
		}
		select {
		case <-s.done:
			return s, fmt.Errorf("serve exited (%v)", s.cmd.ProcessState)
		case <-deadline:
			return s, errors.New("no running line within 60 s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// stop ends serve as launchd does (SIGTERM; SIGKILL after 20 s) and waits for it.
func (s *serveProc) stop() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-s.done:
		return nil
	case <-time.After(20 * time.Second):
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		<-s.done
		return errors.New("serve did not end within 20 s of SIGTERM (killed)")
	}
}

// guideBlock is one fenced sh block of the guide: the version heading it is under, and its text.
type guideBlock struct {
	version, text string
}

var (
	headingRe = regexp.MustCompile(`^#+\s*v([0-9]+\.[0-9]+\.[0-9]+)\b`)
	versionRe = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)$`)
)

// guideSteps is what the upgrade from prev to next runs: the fenced sh blocks (```sh) under the guide's version
// headings above prev up to next, in the guide's order. A next whose minor is not prev's needs its minor's x.y.0
// heading with a block (its steps, possibly none: a comment), else an error.
func guideSteps(guide, prev, next string) ([]guideBlock, error) {
	p, ok1 := parseVersion(prev)
	n, ok2 := parseVersion(next)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("versions %q → %q: not x.y.z", prev, next)
	}
	var blocks []guideBlock
	var cur string
	var in bool
	var text strings.Builder
	minorBlock := false
	for _, line := range strings.Split(guide, "\n") {
		if in {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				in = false
				if v, ok := parseVersion(cur); ok && less(p, v) && !less(n, v) {
					blocks = append(blocks, guideBlock{cur, text.String()})
					if v[2] == 0 && v[0] == n[0] && v[1] == n[1] {
						minorBlock = true
					}
				}
				continue
			}
			text.WriteString(line + "\n")
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			continue
		}
		if t := strings.TrimSpace(line); t == "```sh" {
			in = true
			text.Reset()
		}
	}
	if (p[0] != n[0] || p[1] != n[1]) && !minorBlock {
		return nil, fmt.Errorf("v%d.%d.0 changes the minor, and the guide has no fenced sh block of its steps under its "+
			"v%d.%d.0 heading (docs/UPGRADE-v%d.%d.md; a comment when there are none)", n[0], n[1], n[0], n[1], n[0], n[1])
	}
	return blocks, nil
}

func parseVersion(v string) ([3]int, bool) {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// previous is the newest x.y.z of the catalog (bin/checksums.txt) below next.
func previous(catalog, next string) (string, bool) {
	n, ok := parseVersion(next)
	if !ok {
		return "", false
	}
	best, found := [3]int{}, false
	for _, line := range strings.Split(catalog, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "dist/")
		rest, ok := strings.CutPrefix(name, "obor-")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "-") // <version>-<os>-<arch>: a dev build (0.6.4-dev-…) is no release
		if len(parts) != 3 {
			continue
		}
		v, ok := parseVersion(parts[0])
		if ok && less(v, n) && (!found || less(best, v)) {
			best, found = v, true
		}
	}
	return fmt.Sprintf("%d.%d.%d", best[0], best[1], best[2]), found
}

// release is a released binary for this platform, as the kit's shim gets it: the cache under the state home
// (${OBOR_STATE_HOME:-$HOME/.obor}/bin/obor-<v>-<os>-<arch>), else fetched from bin/release-url with curl into the cache;
// either way its sha256 must be the catalog's line.
func release(kit, v string, fetch bool) (string, error) {
	asset := "obor-" + v + "-" + runtime.GOOS + "-" + runtime.GOARCH
	catalog, err := os.ReadFile(filepath.Join(kit, "bin", "checksums.txt"))
	if err != nil {
		return "", err
	}
	want := ""
	for _, line := range strings.Split(string(catalog), "\n") {
		if f := strings.Fields(line); len(f) == 2 && (f[1] == asset || f[1] == "dist/"+asset) {
			want = f[0]
			break
		}
	}
	if want == "" {
		return "", fmt.Errorf("no checksum for %s in bin/checksums.txt", asset)
	}
	state := os.Getenv("OBOR_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".obor")
	}
	path := filepath.Join(state, "bin", asset)
	if _, err := os.Stat(path); err != nil {
		if !fetch {
			return "", fmt.Errorf("%s is not cached (%s)", asset, path)
		}
		if err := download(kit, v, asset, path); err != nil {
			return "", err
		}
	}
	got, err := sha(path)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", fmt.Errorf("checksum mismatch for %s: %s is %s, bin/checksums.txt says %s", asset, path, got, want)
	}
	return path, nil
}

// download fetches asset (bin/release-url's template) to path through a temp file, mode 0755.
func download(kit, v, asset, path string) error {
	tmpl, err := os.ReadFile(filepath.Join(kit, "bin", "release-url"))
	if err != nil {
		return err
	}
	url := strings.NewReplacer("%VERSION%", v, "%OS%", runtime.GOOS, "%ARCH%", runtime.GOARCH).Replace(strings.TrimSpace(string(tmpl)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	defer os.Remove(tmp)
	if out, err := exec.Command("curl", "-fsSL", url, "-o", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("cannot fetch %s from %s: %v\n%s", asset, url, err, out)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sha(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

// tagTree is the kit's tree at tag v<v> extracted into a temp dir (git archive): the previous release's kit.
func tagTree(t testing.TB, kit, v string) (string, error) {
	dir := t.TempDir()
	archive := exec.Command("git", "-C", kit, "archive", "--format=tar", "v"+v)
	untar := exec.Command("tar", "-x", "-C", dir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return "", err
	}
	untar.Stdin = pipe
	var stderr strings.Builder
	archive.Stderr = &stderr
	if err := untar.Start(); err != nil {
		return "", err
	}
	if err := archive.Run(); err != nil {
		return "", fmt.Errorf("git archive v%s: %v %s", v, err, stderr.String())
	}
	if err := untar.Wait(); err != nil {
		return "", fmt.Errorf("untar v%s: %v", v, err)
	}
	return dir, nil
}
