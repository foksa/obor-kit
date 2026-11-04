//go:build conformance

// The live tier (deep dive §6 Shape): per kind one throwaway herdr session, `herdr --session obor-conf-<kind>`, its
// server started under a clean environment and stopped (and deleted) at the end; never the default session. The
// session works in a scratch dir holding a main checkout with a minimal .obor/config.json, a lane worktree and a
// scratch kit whose bin/obor is the stub (stubmcp): each driver's real start argv runs it where it would run obor. No
// herdr plugin is linked. Agents start interactively in the session's panes (agent.start), never headless. Model calls
// only with TURN=1.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// liveKinds is each kind's live tier.
var liveKinds = map[string]func(l *lab, turn bool){"herdr": herdrLive, "claude": claudeLive, "codex": codexLive,
	"opencode": opencodeLive}

// liveIDs are each kind's live rows, for the SKIP lines when the kind cannot run.
var liveIDs = map[string][]string{
	"herdr":    {"H-L1", "H-L2", "H-L3", "H-L4", "H-L5", "H-L6", "H-L7"},
	"claude":   {"CC-L1", "CC-L2", "CC-L3", "CC-L4", "CC-L5", "CC-L6"},
	"codex":    {"CX-L1", "CX-L2", "CX-L3", "CX-L5"},
	"opencode": {"OC-L1", "OC-L2", "OC-L3", "OC-L4", "OC-L5"},
}

var (
	labsMu sync.Mutex
	labs   = map[*lab]bool{} // open labs, closed on an interrupt too
)

func live(kinds []string, turn bool) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "conformance: interrupted, stopping the throwaway sessions")
		labsMu.Lock()
		for l := range labs {
			l.close()
		}
		labsMu.Unlock()
		os.Exit(130)
	}()
	defer signal.Stop(sig)
	herdrBin, err := exec.LookPath("herdr")
	for _, kind := range kinds {
		version := ""
		if kind == "herdr" && err == nil {
			version = herdrVersion(herdrBin)
		} else if kind != "herdr" {
			if p, err := exec.LookPath(kind); err == nil {
				o, _ := output(15*time.Second, p, "--version")
				version = versionOf(o)
			}
		}
		out.begin(kind, version)
		switch {
		case err != nil:
			skipAll(kind, "herdr is not installed")
			continue
		case version == "":
			skipAll(kind, kind+" is not installed")
			continue
		}
		l, err := newLab(kind, herdrBin)
		if err != nil {
			report("FAIL", "live", kind+": the throwaway session could not be set up: "+err.Error(), "tests/conformance/live.go")
			if l != nil {
				l.close()
			}
			continue
		}
		liveKinds[kind](l, turn)
		l.close()
	}
}

func skipAll(kind, why string) {
	for _, id := range liveIDs[kind] {
		report("SKIP", id, why, "deep dive §6")
	}
}

// lab is one kind's throwaway herdr session and its scratch dir.
type lab struct {
	kind     string
	herdr    string // the herdr binary
	root     string // the scratch dir (symlinks resolved)
	kit      string // root/kit: bin/obor (the stub's wrapper), bin/obor-stub, opencode/obor.js, out/
	main     string // root/proj: the main checkout, .obor/config.json committed
	lane     string // root/proj-wt/L-01: a lane worktree on task/T-1
	session  string // obor-conf-<kind>
	sock     string
	env      []string  // the server's clean environment
	server   *exec.Cmd // the running server
	cleanups []func()
	seq      atomic.Int64
	closed   bool
}

// newLab makes the scratch dir and starts the session. A lab it returns with an error is closed by the caller.
func newLab(kind, herdrBin string) (*lab, error) {
	dir, err := os.MkdirTemp("", "obor-conf-"+kind+"-")
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	home, _ := os.UserHomeDir()
	l := &lab{kind: kind, herdr: herdrBin, root: root, kit: filepath.Join(root, "kit"), main: filepath.Join(root, "proj"),
		lane: filepath.Join(root, "proj-wt", "L-01"), session: "obor-conf-" + kind}
	l.sock = filepath.Join(home, ".config", "herdr", "sessions", l.session, "herdr.sock")
	labsMu.Lock()
	labs[l] = true
	labsMu.Unlock()
	if err := l.makeKit(); err != nil {
		return l, fmt.Errorf("scratch kit: %v", err)
	}
	if err := l.makeProject(); err != nil {
		return l, fmt.Errorf("scratch project: %v", err)
	}
	l.env = cleanEnv()
	if err := l.startServer(); err != nil {
		return l, err
	}
	return l, nil
}

// makeKit builds the stub into root/kit/bin as obor-stub behind a bin/obor wrapper (obor.js runs `bash bin/obor`, so
// it is a script; exec keeps the pid) and copies obor.js beside it, as a kit holds it.
func (l *lab) makeKit() error {
	bin := filepath.Join(l.kit, "bin")
	for _, d := range []string{bin, filepath.Join(l.kit, "out"), filepath.Join(l.kit, "opencode")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if o, err := output(3*time.Minute, "go", "build", "-o", filepath.Join(bin, "obor-stub"), "./tests/conformance/stubmcp"); err != nil {
		return fmt.Errorf("go build stubmcp: %v: %s", err, o)
	}
	wrapper := "#!/bin/bash\nexec \"$(dirname \"$0\")/obor-stub\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "obor"), []byte(wrapper), 0o755); err != nil {
		return err
	}
	js, err := os.ReadFile("opencode/obor.js")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.kit, "opencode", "obor.js"), js, 0o644)
}

// makeProject is a git repository with a minimal obor config committed on main and a lane worktree, as obor lays them
// out (<main>-wt/L-01 on task/T-1).
func (l *lab) makeProject() error {
	if err := os.MkdirAll(filepath.Join(l.main, ".obor"), 0o755); err != nil {
		return err
	}
	config := `{"version": 1, "name": "conf", "prefix": "cf", "lanes_dir": "../proj-wt", "lanes": 1,
  "branches": {"task": "task/{id}", "main": "main"}, "task_id_pattern": "T-[0-9]+", "commands": {}}
`
	if err := os.WriteFile(filepath.Join(l.main, ".obor", "config.json"), []byte(config), 0o644); err != nil {
		return err
	}
	git := func(dir string, args ...string) error {
		args = append([]string{"-C", dir, "-c", "user.name=obor-conformance", "-c", "user.email=conformance@obor.invalid",
			"-c", "commit.gpgsign=false"}, args...)
		if o, err := output(time.Minute, "git", args...); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args[7:], " "), err, firstLine(o))
		}
		return nil
	}
	for _, step := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", ".obor/config.json"},
		{"commit", "-q", "-m", "obor config"},
		{"worktree", "add", "-q", "-b", "task/T-1", l.lane},
	} {
		if err := git(l.main, step...); err != nil {
			return err
		}
	}
	return nil
}

// cleanEnv is the server's environment: the user's identity, shell, PATH and locale, a terminal, and nothing of the
// caller's (no HERDR_*, OBOR_*, CLAUDE_*, CODEX_*, …). Panes inherit it.
func cleanEnv() []string {
	env := []string{"TERM=xterm-256color"}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "SHELL", "PATH", "LANG", "LC_ALL", "TMPDIR", "TZ"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// startServer starts `herdr --session <name> server` (a stale session of that name is stopped and deleted first) and
// waits for its socket to answer ping.
func (l *lab) startServer() error {
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(l.session, "obor-conf-") || l.sock == filepath.Join(home, ".config", "herdr", "herdr.sock") {
		return errors.New("refused: not a throwaway session")
	}
	if l.server == nil { // the first start: clear a crashed run's session
		if _, err := l.call("ping", nil, 2*time.Second); err == nil {
			_, _ = output(15*time.Second, l.herdr, "--session", l.session, "server", "stop")
			time.Sleep(time.Second)
		}
		_, _ = output(15*time.Second, l.herdr, "session", "delete", l.session)
	}
	logf, err := os.OpenFile(filepath.Join(l.root, "herdr-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(l.herdr, "--session", l.session, "server")
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = l.env, l.root, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("herdr --session %s server: %v", l.session, err)
	}
	l.server = cmd
	go func() { _ = cmd.Wait(); logf.Close() }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := l.call("ping", nil, 2*time.Second); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("herdr --session %s server did not answer ping in 20 s (log: %s)", l.session, filepath.Join(l.root, "herdr-server.log"))
}

// stopServer stops the session's server (server stop, then its process group) and waits for it.
func (l *lab) stopServer() {
	if l.server == nil {
		return
	}
	_, _ = output(15*time.Second, l.herdr, "--session", l.session, "server", "stop")
	for i := 0; i < 50 && l.server.ProcessState == nil && syscall.Kill(l.server.Process.Pid, 0) == nil; i++ {
		time.Sleep(200 * time.Millisecond)
	}
	if syscall.Kill(l.server.Process.Pid, 0) == nil {
		_ = syscall.Kill(-l.server.Process.Pid, syscall.SIGKILL)
	}
	time.Sleep(300 * time.Millisecond)
}

// close stops and deletes the session, stops what the run left running in the scratch dir, and removes it (KEEP=1
// keeps it).
func (l *lab) close() {
	if l.closed {
		return
	}
	l.closed = true
	for i := len(l.cleanups) - 1; i >= 0; i-- {
		l.cleanups[i]()
	}
	l.stopServer()
	_, _ = output(15*time.Second, l.herdr, "session", "delete", l.session)
	if left := l.stragglers(); len(left) > 0 {
		for _, p := range left {
			_ = syscall.Kill(p.pid, syscall.SIGTERM)
		}
		time.Sleep(2 * time.Second)
		for _, p := range l.stragglers() {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
		var names []string
		for _, p := range left {
			names = append(names, fmt.Sprintf("%d %s", p.pid, p.name))
		}
		out.note(l.kind + ": stopped processes the run left in its scratch dir: " + strings.Join(names, ", "))
	}
	if os.Getenv("KEEP") == "1" {
		out.note(l.kind + ": scratch dir kept (KEEP=1): " + l.root)
	} else {
		_ = os.RemoveAll(l.root)
	}
	labsMu.Lock()
	delete(labs, l)
	labsMu.Unlock()
}

// stragglers are the processes whose command line names the scratch dir (a daemon an agent started there), never
// this one: each was started by this run.
func (l *lab) stragglers() []proc {
	var left []proc
	for _, p := range processes() {
		if p.pid != os.Getpid() && strings.Contains(p.command, l.root) {
			left = append(left, p)
		}
	}
	return left
}

// apiError is herdr's error answer.
type apiError struct{ Code, Message string }

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

// errCode is err's herdr code, "" when it is none of herdr's.
func errCode(err error) string {
	var a *apiError
	if errors.As(err, &a) {
		return a.Code
	}
	return ""
}

// call sends one request on its own connection and returns its result.
func (l *lab) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	c, err := net.DialTimeout("unix", l.sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	b, _ := json.Marshal(map[string]any{"id": "conf:" + strconv.FormatInt(l.seq.Add(1), 10), "method": method, "params": params})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReaderSize(c, 1<<16).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("%s: %v", method, err)
	}
	var a struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &a); err != nil {
		return nil, fmt.Errorf("%s: unreadable answer %q", method, firstLine(string(line)))
	}
	if a.Error != nil {
		return nil, &apiError{a.Error.Code, a.Error.Message}
	}
	return a.Result, nil
}

// get decodes field path (dotted) of a result into v.
func get(raw json.RawMessage, path string, v any) bool {
	cur := raw
	for _, k := range strings.Split(path, ".") {
		var m map[string]json.RawMessage
		if json.Unmarshal(cur, &m) != nil {
			return false
		}
		var ok bool
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return json.Unmarshal(cur, v) == nil
}

// sub is an events.subscribe connection: its lines after the acknowledgement.
type sub struct {
	c     net.Conn
	lines chan []byte
}

func (l *lab) subscribe(subs ...map[string]any) (*sub, error) {
	c, err := net.DialTimeout("unix", l.sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(map[string]any{"id": "conf:sub", "method": "events.subscribe", "params": map[string]any{"subscriptions": subs}})
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, err
	}
	r := bufio.NewReaderSize(c, 1<<16)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack, err := r.ReadBytes('\n')
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("events.subscribe: %v", err)
	}
	if strings.Contains(string(ack), `"error"`) {
		c.Close()
		return nil, fmt.Errorf("events.subscribe: %s", firstLine(string(ack)))
	}
	_ = c.SetReadDeadline(time.Time{})
	s := &sub{c: c, lines: make(chan []byte, 256)}
	go func() {
		defer close(s.lines)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			s.lines <- line
		}
	}()
	return s, nil
}

// next is the next event line within d (nil: none came).
func (s *sub) next(d time.Duration) []byte {
	select {
	case line := <-s.lines:
		return line
	case <-time.After(d):
		return nil
	}
}

func (s *sub) close() { s.c.Close() }

// event is an event line's name and pane ("" when it names none).
func event(line []byte) (name, pane string) {
	var e struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(line, &e)
	var d struct {
		PaneID string `json:"pane_id"`
		Pane   struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	_ = json.Unmarshal(e.Data, &d)
	if d.PaneID == "" {
		d.PaneID = d.Pane.PaneID
	}
	return e.Event, d.PaneID
}

// workspace creates a workspace in cwd and returns its root pane.
func (l *lab) workspace(label, cwd string) (string, error) {
	r, err := l.call("workspace.create", map[string]any{"label": label, "cwd": cwd, "focus": false}, 10*time.Second)
	if err != nil {
		return "", err
	}
	var pane string
	if !get(r, "root_pane.pane_id", &pane) {
		return "", fmt.Errorf("workspace.create: no root_pane.pane_id in %s", firstLine(string(r)))
	}
	return pane, l.waitPrompt(pane)
}

// split opens a pane next to pane in cwd.
func (l *lab) split(pane, cwd string) (string, error) {
	r, err := l.call("pane.split", map[string]any{"target_pane_id": pane, "direction": "right", "cwd": cwd, "focus": false}, 10*time.Second)
	if err != nil {
		return "", err
	}
	var id string
	if !get(r, "pane.pane_id", &id) {
		return "", fmt.Errorf("pane.split: no pane.pane_id in %s", firstLine(string(r)))
	}
	return id, l.waitPrompt(id)
}

// waitPrompt waits for a new pane's shell to take input (its startup files read): a marker command answers.
func (l *lab) waitPrompt(pane string) error {
	_, err := l.shell(pane, "true", 20*time.Second)
	return err
}

// typeLine is pane.send_input: text then Enter.
func (l *lab) typeLine(pane, text string) error {
	_, err := l.call("pane.send_input", map[string]any{"pane_id": pane, "text": text, "keys": []string{"Enter"}}, 10*time.Second)
	return err
}

// keys is pane.send_keys.
func (l *lab) keys(pane string, keys ...string) error {
	_, err := l.call("pane.send_keys", map[string]any{"pane_id": pane, "keys": keys}, 10*time.Second)
	return err
}

// screen is the pane's recent text, lines unwrapped, without ANSI codes.
func (l *lab) screen(pane string) string {
	r, err := l.call("pane.read", map[string]any{"pane_id": pane, "source": "recent_unwrapped", "lines": 200, "strip_ansi": true}, 10*time.Second)
	if err != nil {
		return ""
	}
	var text string
	get(r, "read.text", &text)
	return text
}

// visible is the pane's visible screen.
func (l *lab) visible(pane string) string {
	r, err := l.call("pane.read", map[string]any{"pane_id": pane, "source": "visible", "strip_ansi": true}, 10*time.Second)
	if err != nil {
		return ""
	}
	var text string
	get(r, "read.text", &text)
	return text
}

// waitOutput is pane.wait_for_output on the recent unwrapped text.
func (l *lab) waitOutput(pane, value string, regex bool, d time.Duration) error {
	typ := "substring"
	if regex {
		typ = "regex"
	}
	_, err := l.call("pane.wait_for_output", map[string]any{"pane_id": pane, "source": "recent_unwrapped",
		"match": map[string]any{"type": typ, "value": value}, "timeout_ms": d.Milliseconds()}, d+5*time.Second)
	return err
}

// shell types cmd at pane's shell and returns its exit status, read from a marker line the shell prints after it (the
// typed line holds the marker as $((40+2)), so only the output matches).
func (l *lab) shell(pane, cmd string, d time.Duration) (int, error) {
	n := strconv.FormatInt(l.seq.Add(1), 10)
	if err := l.typeLine(pane, cmd+`; echo "OC$((40+2))_`+n+`=$?"`); err != nil {
		return -1, err
	}
	return l.marker(pane, n, d)
}

// status types the marker alone: the exit status of the command that last ran at pane's shell (an agent that exited).
func (l *lab) status(pane string, d time.Duration) (int, error) {
	n := strconv.FormatInt(l.seq.Add(1), 10)
	if err := l.typeLine(pane, `echo "OC$((40+2))_`+n+`=$?"`); err != nil {
		return -1, err
	}
	return l.marker(pane, n, d)
}

func (l *lab) marker(pane, n string, d time.Duration) (int, error) {
	re := `OC42_` + n + `=[0-9]+`
	if err := l.waitOutput(pane, re, true, d); err != nil {
		return -1, fmt.Errorf("no marker after the command (%v); screen: %s", err, tail(l.screen(pane), 3))
	}
	m := regexp.MustCompile(`OC42_`+n+`=([0-9]+)`).FindAllStringSubmatch(l.screen(pane), -1)
	if m == nil {
		return -1, errors.New("marker matched but not read back")
	}
	return strconv.Atoi(m[len(m)-1][1])
}

// tail is the last n non-empty lines of s, joined by " / " and cut to 300 bytes.
func tail(s string, n int) string {
	t := lastLines(s, n)
	if len(t) > 300 {
		t = t[len(t)-300:]
	}
	return strconv.Quote(t)
}

// fg is the pane's shell pid and its foreground processes.
func (l *lab) fg(pane string) (shell int, procs []proc) {
	r, err := l.call("pane.process_info", map[string]any{"pane_id": pane}, 10*time.Second)
	if err != nil {
		return 0, nil
	}
	var info struct {
		Shell int `json:"shell_pid"`
		Procs []struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		} `json:"foreground_processes"`
	}
	get(r, "process_info", &info)
	for _, p := range info.Procs {
		procs = append(procs, proc{pid: p.PID, name: p.Name})
	}
	return info.Shell, procs
}

// atShell: only the pane's shell is in its foreground.
func (l *lab) atShell(pane string) bool {
	shell, procs := l.fg(pane)
	return shell != 0 && len(procs) == 1 && procs[0].pid == shell
}

// waitShell waits until the pane's foreground is its shell again.
func (l *lab) waitShell(pane string, d time.Duration) bool {
	return poll(d, func() bool { return l.atShell(pane) })
}

// poll calls cond every 200 ms until it holds or d passes.
func poll(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// agentInfo is the part of herdr's agent answer the checks read.
type agentInfo struct {
	Name          string `json:"name"`
	Agent         string `json:"agent"`
	Status        string `json:"agent_status"`
	LaunchPending bool   `json:"launch_pending"`
	Ready         bool   `json:"interactive_ready"`
	Pane          string `json:"pane_id"`
}

func (l *lab) agent(target string) (agentInfo, error) {
	r, err := l.call("agent.get", map[string]any{"target": target}, 10*time.Second)
	var a agentInfo
	if err == nil {
		get(r, "agent", &a)
	}
	return a, err
}

// started is one agent.start: its answer, how long herdr took, and what the pane showed then.
type started struct {
	name    string // the agent name the start asked for (unique per start: herdr keeps an exited agent's name on its pane)
	info    agentInfo
	err     error
	took    time.Duration
	timeout time.Duration
}

// start is agent.start of kind in pane with the driver's args, herdr's timeout_ms set, as name-<n>.
func (l *lab) start(name, kind, pane string, args []string, timeout time.Duration) started {
	name = fmt.Sprintf("%s-%d", name, l.seq.Add(1))
	// A pane whose agent just exited reads busy until herdr sees its shell again (agent_pane_busy): retried.
	var r json.RawMessage
	var err error
	t0 := time.Now()
	poll(30*time.Second, func() bool {
		t0 = time.Now()
		r, err = l.call("agent.start", map[string]any{"name": name, "kind": kind, "pane_id": pane, "args": args,
			"timeout_ms": timeout.Milliseconds()}, timeout+15*time.Second)
		return errCode(err) != "agent_pane_busy"
	})
	s := started{name: name, err: err, took: time.Since(t0), timeout: timeout}
	if err == nil {
		get(r, "agent", &s.info)
	}
	return s
}

// reportStart is H-L1 for a kind's agent.start: when herdr answered (before the agent was ready, launch_pending, as
// the driver's comment says, or once it was), with what, within obor's 10 s client timeout.
func reportStart(kind string, s started, readyAfter time.Duration) {
	where := at("internal/herdr/socket.go", "func (s *Socket) Start") + ", " + at("internal/launch/start.go", "func (l *Launcher) started") + ", F3.4"
	fact := fmt.Sprintf("%s: agent.start (timeout_ms %d) answered after %.1fs", kind, s.timeout.Milliseconds(), s.took.Seconds())
	fact += " with " + answer(s)
	if readyAfter > 0 {
		fact += fmt.Sprintf("; the agent took input after %.1fs", readyAfter.Seconds())
	}
	const client = 10 * time.Second // internal/herdr/socket.go: the per-request default agent.start runs under
	switch {
	case s.took > client:
		report("FAIL", "H-L1", fact+": past obor's 10 s client timeout, so Socket.Start reports an error for a start herdr carried out", where)
	case s.err == nil && !s.info.LaunchPending && s.info.Status != "" && s.info.Status != "unknown":
		report("WARN", "H-L1", fact+": herdr answers once the agent is up, not at once (the driver's comment says launch_pending; its poll is redundant)", where)
	default:
		report("PASS", "H-L1", fact, where)
	}
}

// proc is one process.
type proc struct {
	pid, ppid int
	name      string // the executable's base name
	command   string // the command line
}

// processes is every process (ps).
func processes() map[int]proc {
	o, err := output(10*time.Second, "ps", "-A", "-o", "pid=,ppid=,command=")
	m := map[int]proc{}
	if err != nil {
		return m
	}
	for _, line := range strings.Split(o, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		m[pid] = proc{pid: pid, ppid: ppid, name: filepath.Base(f[2]), command: strings.Join(f[2:], " ")}
	}
	return m
}

// ancestors is pid's parent chain, nearest first (as ps sees it now).
func ancestors(pid int) []proc {
	ps := processes()
	var chain []proc
	for p, ok := ps[ps[pid].ppid]; ok && len(chain) < 32; p, ok = ps[p.ppid] {
		chain = append(chain, p)
		if p.pid <= 1 {
			break
		}
	}
	return chain
}

// chain is ancestors as "name(pid) < name(pid) …", for a fact.
func chain(ps []proc, n int) string {
	var parts []string
	for _, p := range ps[:min(n, len(ps))] {
		parts = append(parts, fmt.Sprintf("%s(%d)", p.name, p.pid))
	}
	return strings.Join(parts, " < ")
}

// record is one stub record (stubmcp.Record).
type record struct {
	Mode  string            `json:"mode"`
	Name  string            `json:"name"`
	PID   int               `json:"pid"`
	PPID  int               `json:"ppid"`
	Cwd   string            `json:"cwd"`
	Argv  []string          `json:"argv"`
	Env   map[string]string `json:"env"`
	Stdin string            `json:"stdin"`
	file  string
}

// records are the stub records of mode (and name, "*" any) written since the last fresh.
func (l *lab) records(mode, name string) []record {
	pattern := mode + "-*.json"
	if name != "*" {
		pattern = mode + "-" + name + "-*.json"
		if name == "" {
			pattern = mode + "-[0-9]*.json"
		}
	}
	files, _ := filepath.Glob(filepath.Join(l.kit, "out", pattern))
	sort.Strings(files)
	var rs []record
	for _, f := range files {
		b, err := os.ReadFile(f)
		var r record
		if err != nil || json.Unmarshal(b, &r) != nil {
			continue
		}
		r.file = f
		rs = append(rs, r)
	}
	return rs
}

// waitRecords waits up to d for a record of mode and name.
func (l *lab) waitRecords(mode, name string, d time.Duration) []record {
	var rs []record
	poll(d, func() bool { rs = l.records(mode, name); return len(rs) > 0 })
	return rs
}

// fresh moves the records so far aside (out-<n>), so the next start's records stand alone.
func (l *lab) fresh() {
	dir := filepath.Join(l.kit, "out")
	_ = os.Rename(dir, fmt.Sprintf("%s-%d", dir, l.seq.Add(1)))
	_ = os.MkdirAll(dir, 0o755)
}

// same: a and b name the same file or directory.
func same(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// quit runs the kind's quit recipe (agents.Stop: Quit with Enter, a second Enter when the popup took the first) and
// returns the agent's exit status, as the shell prints it.
func (l *lab) quit(pane, quit string, secondEnter bool) (int, error) {
	if err := l.typeLine(pane, quit); err != nil {
		return -1, err
	}
	if secondEnter && !l.waitShell(pane, 2*time.Second) {
		_ = l.keys(pane, "enter")
	}
	if !l.waitShell(pane, 20*time.Second) {
		return -1, fmt.Errorf("still running 20 s after %s; screen: %s", quit, tail(l.visible(pane), 4))
	}
	return l.status(pane, 10*time.Second)
}

// stopAgent ends whatever runs in pane (Ctrl+C twice, then the shell's own exit is not used): for a check that failed
// half way.
func (l *lab) stopAgent(pane string) {
	for i := 0; i < 3 && !l.atShell(pane); i++ {
		_ = l.keys(pane, "ctrl+c")
		time.Sleep(time.Second)
	}
}

// envNames is the sorted names of env that match re.
func envNames(env map[string]string, re string) []string {
	r := regexp.MustCompile(re)
	var out []string
	for k := range env {
		if r.MatchString(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// shq is s single-quoted for the pane's shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// exports is an `export K=V …` line for the pane's shell, keys sorted.
func exports(env map[string]string) string {
	var parts []string
	for _, k := range names(env) {
		parts = append(parts, k+"="+shq(env[k]))
	}
	return "export " + strings.Join(parts, " ")
}

// readyAfter waits up to 30 s for the agent s started to show one of statuses with its launch no longer pending (herdr
// answers agent.start before it types the command, the pane's old status still showing): how long after t0 (0: it did
// not, or the start failed).
func (l *lab) readyAfter(s started, t0 time.Time, statuses ...string) time.Duration {
	if s.err != nil {
		return 0
	}
	if poll(30*time.Second, func() bool {
		a, err := l.agent(s.name)
		return err == nil && !a.LaunchPending && slices.Contains(statuses, a.Status)
	}) {
		return time.Since(t0)
	}
	return 0
}
