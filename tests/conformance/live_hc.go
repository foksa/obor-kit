//go:build conformance

// The hc probes (docs/plan/herdr-core.md step 0, P1-P11; facts in docs/research/hc-probes.md), run by
// `make conformance TIER=live KIND=hc [TURN=1]`. hc is not in the default kinds: it runs only when named. Each probe
// gets a throwaway session, `HC_SESSION=obor-probe-<n>` with scratch XDG_STATE_HOME, XDG_CONFIG_HOME and
// XDG_RUNTIME_DIR, HC_SOCKET unset and the core started from a scrubbed environment, and stops it at the end. The default
// session is only read, over its own socket (a dial never starts a core), first and last (P11). Lines say what hc
// does: PASS where the plan's assumption holds, WARN where it does not or a risk is confirmed, FAIL for a no-go
// (the default session touched, a send at blocked not refused, Claude not reporting blocked).
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const hcPlan = "docs/plan/herdr-core.md"

// hcLab is one probe's throwaway hc session.
type hcLab struct {
	hc, root, session, sock string
	env                     []string // the scrubbed environment every hc command and the core get
}

// hcLabs are the sessions this run started, stopped at the end or on an interrupt.
var hcLabs []*hcLab

func hcLive(turn bool) {
	hc, err := exec.LookPath("hc")
	if err != nil {
		report("SKIP", "HC", "hc is not installed", hcPlan+" step 0")
		return
	}
	v, _ := output(15*time.Second, hc, "--version")
	out.note("hc " + versionOf(v))
	// /tmp, not TMPDIR: the core's socket is <root>/<n>/run/herdr-core/obor-probe-<n>.sock, past a unix socket's
	// 104 bytes under macOS's TMPDIR.
	root, err := os.MkdirTemp("/tmp", "obp-")
	if err != nil {
		report("FAIL", "HC", "no scratch dir: "+err.Error(), hcPlan)
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		hcStopAll()
		os.Exit(130)
	}()
	defer signal.Stop(sig)
	defer func() {
		hcStopAll()
		os.RemoveAll(root)
	}()
	first := hcDefaultSnapshot()
	hcP11Schema(hc, root)
	hcP4(hc, root)
	hcP2(hc, root)
	hcP5(hc, root)
	hcP6(hc, root)
	hcP8(hc, root)
	if !turn {
		report("SKIP", "HC-P1", "an agent in an hc terminal needs TURN=1 (one model call)", hcPlan+" P1")
	} else if _, err := exec.LookPath("claude"); err != nil {
		report("SKIP", "HC-P1", "claude is not installed", hcPlan+" P1")
	} else {
		hcP1Claude(hc, root)
	}
	report("SKIP", "HC-P1", "codex and opencode run by hand (dialogs and their own background servers): "+
		"docs/research/hc-probes.md P1", hcPlan+" P1")
	report("SKIP", "HC-P7", "needs a person at herdr-app: the owner's steps are in docs/research/hc-probes.md P7", hcPlan+" P7")
	report("SKIP", "HC-P10", "an idle-CPU sample takes 15 minutes or more: run by hand, docs/research/hc-probes.md P10", hcPlan+" P10")
	hcStopAll()
	hcP11Default(first, hcDefaultSnapshot())
}

// newHCLab makes the scratch dirs of probe n and starts its core from the scrubbed environment plus extra.
func newHCLab(hc, root, n string, extra ...string) (*hcLab, error) {
	dir := filepath.Join(root, n)
	l := &hcLab{hc: hc, root: dir, session: "obor-probe-" + n}
	l.sock = filepath.Join(dir, "run", "herdr-core", l.session+".sock")
	for _, d := range []string{"state", "config", "run", "work"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return nil, err
		}
	}
	l.env = append(cleanEnv(), "HC_SESSION="+l.session, "XDG_STATE_HOME="+filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_RUNTIME_DIR="+filepath.Join(dir, "run"))
	hcLabs = append(hcLabs, l)
	if _, err := l.run(append(l.env, extra...), "server", "start"); err != nil {
		return nil, err
	}
	return l, nil
}

// run runs hc with env in the lab's work dir.
func (l *hcLab) run(env []string, args ...string) (string, error) {
	cmd := exec.Command(l.hc, args...)
	cmd.Env, cmd.Dir = env, filepath.Join(l.root, "work")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("hc %s: %v: %s", strings.Join(args, " "), err, firstLine(string(b)))
	}
	return string(b), nil
}

// stop stops the session's core when one answers (an hc command with no core running may start one).
func (l *hcLab) stop() {
	if h, err := hcDial(l.sock); err == nil {
		h.c.Close()
		_, _ = l.run(l.env, "server", "stop")
	}
}

func hcStopAll() {
	for _, l := range hcLabs {
		l.stop()
	}
	hcLabs = nil
}

// hcError is hc's error answer: its message and stable data.kind.
type hcError struct{ Kind, Message string }

func (e *hcError) Error() string { return e.Kind + ": " + e.Message }

func hcKind(err error) string {
	var e *hcError
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// hcConn is one JSON-RPC connection to a core, past its hello.
type hcConn struct {
	c  net.Conn
	r  *bufio.Reader
	id int
}

func hcDial(sock string) (*hcConn, error) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	h := &hcConn{c: c, r: bufio.NewReaderSize(c, 1<<16)}
	if _, err := h.call("hello", map[string]any{"generation": 1}, 10*time.Second); err != nil {
		c.Close()
		return nil, err
	}
	return h, nil
}

func (h *hcConn) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	h.id++
	_ = h.c.SetDeadline(time.Now().Add(timeout))
	defer h.c.SetDeadline(time.Time{})
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": h.id, "method": method, "params": params})
	if _, err := h.c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := h.r.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("%s: %v", method, err)
		}
		var a struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
				Data    struct {
					Kind string `json:"kind"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &a) != nil || a.ID != h.id {
			continue
		}
		if a.Error != nil {
			return nil, &hcError{a.Error.Data.Kind, a.Error.Message}
		}
		return a.Result, nil
	}
}

// call sends one request on its own connection.
func (l *hcLab) call(method string, params any) (json.RawMessage, error) {
	h, err := hcDial(l.sock)
	if err != nil {
		return nil, err
	}
	defer h.c.Close()
	return h.call(method, params, 90*time.Second)
}

// timed is call and how long it took.
func (l *hcLab) timed(method string, params any) (json.RawMessage, time.Duration, error) {
	t0 := time.Now()
	r, err := l.call(method, params)
	return r, time.Since(t0), err
}

// hcTerm is the part of a terminal record the probes read.
type hcTerm struct {
	Address  string            `json:"address"`
	State    string            `json:"state"`
	Reason   *string           `json:"reason"`
	PID      int               `json:"pid"`
	Parent   *string           `json:"parent"`
	Keep     bool              `json:"keep"`
	Reported bool              `json:"reported"`
	Turn     int               `json:"turn"`
	Labels   map[string]string `json:"labels"`
	Cwd      string            `json:"cwd"`
	Output   string            `json:"output"`
	Status   *struct {
		State string  `json:"state"`
		Kind  *string `json:"kind"`
		Msg   string  `json:"msg"`
		App   string  `json:"app"`
	} `json:"status"`
	Foreground *struct {
		PID  int    `json:"pid"`
		Name string `json:"name"`
		Cwd  string `json:"cwd"`
	} `json:"foreground"`
}

func (t hcTerm) state() string {
	if t.Status == nil {
		return "null"
	}
	s := t.Status.State
	if t.Status.Kind != nil {
		s += "/" + *t.Status.Kind
	}
	return s
}

func (l *hcLab) term(params map[string]any) (hcTerm, error) {
	var t hcTerm
	r, err := l.call("terminal.run", params)
	if err == nil {
		err = json.Unmarshal(r, &t)
	}
	return t, err
}

func (l *hcLab) get(addr string) hcTerm {
	var t hcTerm
	if r, err := l.call("terminal.get", map[string]any{"terminal": addr}); err == nil {
		_ = json.Unmarshal(r, &t)
	}
	return t
}

// shell starts a kept login-shell terminal in the work dir and waits for its prompt.
func (l *hcLab) shell(labels map[string]string) (hcTerm, error) {
	params := map[string]any{"keep": true, "no_parent": true, "cwd": filepath.Join(l.root, "work")}
	if labels != nil {
		params["labels"] = labels
	}
	t, err := l.term(params)
	if err != nil {
		return t, err
	}
	if !poll(10*time.Second, func() bool { return strings.TrimSpace(l.screen(t.Address)) != "" }) {
		return t, errors.New("no prompt in 10s")
	}
	return t, nil
}

// screen is the visible screen of a terminal.
func (l *hcLab) screen(addr string) string {
	var s string
	if r, err := l.call("terminal.read", map[string]any{"terminal": addr, "lines": 0}); err == nil {
		get(r, "text", &s)
	}
	return s
}

func (l *hcLab) send(addr, text string) (time.Duration, error) {
	_, d, err := l.timed("terminal.send", map[string]any{"terminal": addr, "text": text})
	return d, err
}

func (l *hcLab) keys(addr string, keys ...string) (time.Duration, error) {
	_, d, err := l.timed("terminal.keys", map[string]any{"terminal": addr, "keys": keys})
	return d, err
}

// line is a shell command's marker: typed at addr, true once its output line shows.
func (l *hcLab) line(addr, cmd, want string) bool {
	if _, err := l.send(addr, cmd); err != nil {
		return false
	}
	return poll(10*time.Second, func() bool { return hcHasLine(l.screen(addr), want) })
}

func hcHasLine(screen, want string) bool {
	for _, s := range strings.Split(screen, "\n") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// hcSub is an event stream: its notifications after the snapshot.
type hcSub struct {
	h     *hcConn
	seq   int
	lines chan []byte
}

func (l *hcLab) subscribe(params map[string]any) (*hcSub, error) {
	h, err := hcDial(l.sock)
	if err != nil {
		return nil, err
	}
	r, err := h.call("events.subscribe", params, 10*time.Second)
	if err != nil {
		h.c.Close()
		return nil, err
	}
	s := &hcSub{h: h, lines: make(chan []byte, 1<<14)}
	get(r, "seq", &s.seq)
	go func() {
		defer close(s.lines)
		for {
			b, err := h.r.ReadBytes('\n')
			if err != nil {
				return
			}
			s.lines <- b
		}
	}()
	return s, nil
}

func (s *hcSub) close() { s.h.c.Close() }

// hcEvent is one notification: an event's type, terminal and data, or another method (heartbeat, missed).
type hcEvent struct {
	Method string
	Seq    int
	Type   string
	Term   string
	Data   json.RawMessage
}

func hcEventOf(b []byte) hcEvent {
	var m struct {
		Method string `json:"method"`
		Params struct {
			Seq      int             `json:"seq"`
			Type     string          `json:"type"`
			Terminal string          `json:"terminal"`
			Data     json.RawMessage `json:"data"`
		} `json:"params"`
	}
	_ = json.Unmarshal(b, &m)
	return hcEvent{m.Method, m.Params.Seq, m.Params.Type, m.Params.Terminal, m.Params.Data}
}

// events collects notifications for d; closed is true when the stream ended in that time.
func (s *hcSub) events(d time.Duration) (evs []hcEvent, closed bool) {
	deadline := time.After(d)
	for {
		select {
		case b, ok := <-s.lines:
			if !ok {
				return evs, true
			}
			evs = append(evs, hcEventOf(b))
		case <-deadline:
			return evs, false
		}
	}
}

func hcTypes(evs []hcEvent, term string) []string {
	var out []string
	for _, e := range evs {
		if e.Method == "event" && (term == "" || e.Term == term) && e.Type != "foreground" {
			out = append(out, e.Type)
		}
	}
	return out
}

// hcSnapshot is what P11 compares of the default session: its core and live terminals (read over its socket, only
// when it answers) and the hashes of its state files.
type hcSnapshot struct {
	running   bool
	core, pid string
	live      []string // address:pid
	files     map[string]string
	config    bool // ~/.config/herdr-core exists
}

func hcDefaultSnapshot() hcSnapshot {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "state", "herdr-core", "default")
	s := hcSnapshot{files: map[string]string{}}
	for _, f := range []string{"core.json", "core.lock", "docs.json"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			sum := sha256.Sum256(b)
			s.files[f] = hex.EncodeToString(sum[:8])
		}
	}
	_, err := os.Stat(filepath.Join(home, ".config", "herdr-core"))
	s.config = err == nil
	sock := filepath.Join(dir, "default.sock")
	if b, err := os.ReadFile(filepath.Join(dir, "core.lock")); err == nil && strings.TrimSpace(string(b)) != "" {
		sock = strings.TrimSpace(string(b))
	}
	h, err := hcDial(sock)
	if err != nil {
		return s
	}
	defer h.c.Close()
	s.running = true
	if r, err := h.call("server.status", map[string]any{}, 10*time.Second); err == nil {
		var pid int
		get(r, "core.id", &s.core)
		get(r, "pid", &pid)
		s.pid = strconv.Itoa(pid)
	}
	if r, err := h.call("terminal.list", map[string]any{}, 10*time.Second); err == nil {
		var ts []hcTerm
		get(r, "terminals", &ts)
		for _, t := range ts {
			if t.State == "live" {
				s.live = append(s.live, t.Address+":"+strconv.Itoa(t.PID))
			}
		}
		sort.Strings(s.live)
	}
	return s
}

func (s hcSnapshot) String() string {
	if !s.running {
		return fmt.Sprintf("no default core answering; files %v; config dir %v", s.files, s.config)
	}
	return fmt.Sprintf("core %s pid %s, live %v; files %v; config dir %v", s.core, s.pid, s.live, s.files, s.config)
}

// hcP11Default: the default session after every probe equals the one before them.
func hcP11Default(before, after hcSnapshot) {
	where := hcPlan + " P11"
	if fmt.Sprint(before) != fmt.Sprint(after) {
		report("FAIL", "HC-P11", "the default session changed during the probes: before "+before.String()+"; after "+after.String(), where)
		return
	}
	report("PASS", "HC-P11", "the default session is unchanged after the probes: "+after.String(), where)
}

// hcP11Schema: whether `hc schema` needs, or starts, a core.
func hcP11Schema(hc, root string) {
	where := hcPlan + " P11"
	l := &hcLab{hc: hc, root: filepath.Join(root, "11"), session: "obor-probe-11"}
	l.sock = filepath.Join(l.root, "run", "herdr-core", l.session+".sock")
	for _, d := range []string{"state", "config", "run", "work"} {
		_ = os.MkdirAll(filepath.Join(l.root, d), 0o700)
	}
	l.env = append(cleanEnv(), "HC_SESSION="+l.session, "XDG_STATE_HOME="+filepath.Join(l.root, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(l.root, "config"), "XDG_RUNTIME_DIR="+filepath.Join(l.root, "run"))
	hcLabs = append(hcLabs, l)
	s, err := l.run(l.env, "schema")
	if err != nil || !strings.Contains(s, `"openrpc"`) {
		report("FAIL", "HC-P11", "hc schema with no core running printed no schema: "+orOK(err), where)
		return
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := l.call("hello", map[string]any{"generation": 1}); err == nil {
		report("WARN", "HC-P11", "hc schema with no core running prints the schema but starts a core for the session "+
			"(with the caller's environment): the static tier must run it from a scrubbed env and a scratch session", where)
	} else {
		report("PASS", "HC-P11", "hc schema with no core running prints the schema and starts no core", where)
	}
	l.stop()
}

// hcP4: a terminal's env against the core's; empty values; the core's env carried from its starter, and machine.exec
// as its read.
func hcP4(hc, root string) {
	where := hcPlan + " P4"
	l, err := newHCLab(hc, root, "4")
	if err != nil {
		report("FAIL", "HC-P4", "no core: "+err.Error(), where)
		return
	}
	defer l.stop()
	envOf := func(params map[string]any) map[string]string {
		params["argv"], params["wait"], params["no_parent"] = []string{"/usr/bin/env"}, true, true
		t, err := l.term(params)
		if err != nil {
			return nil
		}
		m := map[string]string{}
		for _, kv := range strings.Split(t.Output, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
				m[k] = v
			}
		}
		return m
	}
	env := envOf(map[string]any{})
	core := l.coreEnv()
	var added []string
	for k := range env {
		if _, ok := core[k]; !ok {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	report("PASS", "HC-P4", fmt.Sprintf("a terminal of a scrubbed core gets the core's env plus %s; HERDR_/OBOR_ names: %v",
		strings.Join(added, " "), envNames(env, `^(HERDR|OBOR)_`)), where)
	e := envOf(map[string]any{"env": map[string]string{"OBOR_PROBE": "", "LOGNAME": "", "TERM_PROGRAM": "herdr"}})
	_, set := e["OBOR_PROBE"]
	report("PASS", "HC-P4", fmt.Sprintf(`env {"NAME": ""} sets it empty, never unsets: OBOR_PROBE set %v %q, LOGNAME %q; `+
		`TERM_PROGRAM=herdr is overridden by the terminal identity: %q`, set, e["OBOR_PROBE"], e["LOGNAME"], e["TERM_PROGRAM"]), where)
	l.stop()
	if _, err := l.run(append(l.env, "HERDR_PANE_ID=w99:p99", "OBOR_ROLE=worker", "OBOR_PANE=w99:p99"), "server", "start"); err != nil {
		report("FAIL", "HC-P4", "no core from a shell with HERDR_/OBOR_ names: "+err.Error(), where)
		return
	}
	leaked := envNames(envOf(map[string]any{}), `^(HERDR|OBOR)_`)
	read := envNames(l.coreEnv(), `^(HERDR|OBOR)_`)
	status := "WARN"
	if len(leaked) == 0 {
		status = "PASS"
	}
	report(status, "HC-P4", fmt.Sprintf("a core started from a shell carrying HERDR_PANE_ID, OBOR_ROLE, OBOR_PANE gives every "+
		"new terminal %v; machine.exec of env reads the core's env: %v", leaked, read), where+", risk 2")
	if _, err := l.run(l.env, "server", "restart", "--fresh"); err == nil {
		time.Sleep(2 * time.Second)
		report("PASS", "HC-P4", fmt.Sprintf("hc server restart --fresh from a scrubbed shell replaces it: HERDR_/OBOR_ names "+
			"after: %v", envNames(l.coreEnv(), `^(HERDR|OBOR)_`)), where)
	}
}

// coreEnv is the core's environment as machine.exec of env sees it.
func (l *hcLab) coreEnv() map[string]string {
	m := map[string]string{}
	r, err := l.call("machine.exec", map[string]any{"argv": []string{"/usr/bin/env"}})
	if err != nil {
		return m
	}
	var b []byte
	get(r, "stdout", &b) // base64 in the answer; encoding/json decodes it into []byte
	for _, kv := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// hcP2: terminal.send and terminal.keys at a shell (P2's shell rows, P3), and the shell's pid and foreground (P9).
func hcP2(hc, root string) {
	where := hcPlan + " P2"
	l, err := newHCLab(hc, root, "2")
	if err != nil {
		report("FAIL", "HC-P2", "no core: "+err.Error(), where)
		return
	}
	defer l.stop()
	t, err := l.shell(nil)
	if err != nil {
		report("FAIL", "HC-P2", "no shell terminal: "+err.Error(), where)
		return
	}
	a := t.Address
	d, err := l.send(a, "echo P2-ONE")
	ok := poll(5*time.Second, func() bool { return hcHasLine(l.screen(a), "P2-ONE") })
	report(passIf(err == nil && ok), "HC-P2", fmt.Sprintf("a send to a quiet shell with nobody attached returns in %s and runs: %v",
		d.Round(time.Millisecond), ok && err == nil), where)
	_, _ = l.send(a, "clear")
	_, _ = l.keys(a, "e", "c", "h", "o", "space", "L", "E", "F", "T")
	time.Sleep(500 * time.Millisecond)
	_, _ = l.send(a, "echo X")
	time.Sleep(time.Second)
	scr := l.screen(a)
	switch {
	case hcHasLine(scr, "leftecho X"):
		report("WARN", "HC-P2", "a send after unsent input appends to it (no clear): keys `echo LEFT` then send `echo X` "+
			"ran `echo leftecho X`; keys take uppercase letters as lowercase key names", where)
	case hcHasLine(scr, "X"):
		report("PASS", "HC-P2", "a send after unsent input clears it first", where)
	default:
		report("WARN", "HC-P2", "a send after unsent input: neither appended nor cleared, screen "+tail(scr, 4), where)
	}
	_, _ = l.send(a, "clear")
	_, _ = l.send(a, "echo P2-A\necho P2-B")
	ok = poll(5*time.Second, func() bool { s := l.screen(a); return hcHasLine(s, "P2-A") && hcHasLine(s, "P2-B") })
	report(map[bool]string{true: "PASS", false: "WARN"}[ok], "HC-P2", fmt.Sprintf("a two-line send at a zsh prompt runs both "+
		"lines: %v", ok), where)
	for _, n := range []int{4000, 16000} {
		ok = l.line(a, "echo "+strings.Repeat("x", n)+" | wc -c", strconv.Itoa(n+1))
		report(passIf(ok), "HC-P2", fmt.Sprintf("a %d-byte command line at a shell arrives whole: %v", n, ok), where+
			", "+at("internal/launch/launch.go", "func shellQuote"))
	}
	var ds []string
	for _, k := range []string{"esc", "ctrl+c", "enter"} {
		d, _ := l.keys(a, k)
		ds = append(ds, k+" "+d.Round(time.Millisecond).String())
	}
	report("PASS", "HC-P3", "terminal.keys at a quiet shell, one call each: "+strings.Join(ds, ", "), hcPlan+" P3")
	_, _ = l.send(a, "while :; do date; sleep 0.2; done")
	time.Sleep(time.Second)
	ds = nil
	for _, k := range []string{"esc", "ctrl+c", "enter"} {
		d, _ := l.keys(a, k)
		ds = append(ds, k+" "+d.Round(time.Millisecond).String())
	}
	report("PASS", "HC-P3", "terminal.keys at a shell printing every 200 ms: "+strings.Join(ds, ", ")+" (no 3 s quiet wait "+
		"for a program that reports no status)", hcPlan+" P3")
	// foreground is refreshed about twice a second: wait for the loop's end to show
	atShell := poll(5*time.Second, func() bool { t = l.get(a); return t.Foreground != nil && t.Foreground.PID == t.PID })
	report(passIf(atShell), "HC-P9", fmt.Sprintf("at the shell: pid %d, foreground %s", t.PID, fgOf(t)), hcPlan+" P9")
	_, _ = l.send(a, "sleep 30")
	time.Sleep(time.Second)
	t = l.get(a)
	report("PASS", "HC-P9", fmt.Sprintf("running `sleep 30`: pid %d (the shell), foreground %s", t.PID, fgOf(t)), hcPlan+" P9")
	_, _ = l.keys(a, "ctrl+c")
}

func fgOf(t hcTerm) string {
	if t.Foreground == nil {
		return "null"
	}
	return fmt.Sprintf("%s %d", t.Foreground.Name, t.Foreground.PID)
}

func passIf(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

// hcP5: a lost holder, a planned restart, a stop and start: what the stream and the records show, and what names one
// run of the core.
func hcP5(hc, root string) {
	where := hcPlan + " P5"
	l, err := newHCLab(hc, root, "5")
	if err != nil {
		report("FAIL", "HC-P5", "no core: "+err.Error(), where)
		return
	}
	defer l.stop()
	labels := map[string]string{"obor.project": "probe", "obor.agent": "w1"}
	t, err := l.shell(labels)
	if err != nil {
		report("FAIL", "HC-P5", "no shell terminal: "+err.Error(), where)
		return
	}
	_ = os.MkdirAll(filepath.Join(l.root, "work", "sub"), 0o700)
	_, _ = l.send(t.Address, "cd sub")
	plain, _ := l.term(map[string]any{"argv": []string{"/bin/sleep", "600"}, "no_parent": true})
	time.Sleep(time.Second)
	st := l.status()
	s, err := l.subscribe(map[string]any{})
	if err != nil || len(st.holders) == 0 {
		report("FAIL", "HC-P5", "no stream or no holder", where)
		return
	}
	_ = syscall.Kill(st.holders[0], syscall.SIGKILL)
	evs, closed := s.events(6 * time.Second)
	after := l.get(t.Address)
	kept := after.Address == t.Address && fmt.Sprint(after.Labels) == fmt.Sprint(labels) && strings.HasSuffix(after.Cwd, "/sub") &&
		after.PID != t.PID
	report(passIf(kept), "HC-P5", fmt.Sprintf("holder killed: kept %s events %v (stream open: %v), now pid %d cwd %s labels %v; "+
		"plain %s events %v, reason %s", t.Address, hcTypes(evs, t.Address), !closed, after.PID, after.Cwd, after.Labels,
		plain.Address, hcTypes(evs, plain.Address), deref(l.get(plain.Address).Reason)), where)
	s.close()
	before := l.status()
	s, _ = l.subscribe(map[string]any{})
	if _, err := l.run(l.env, "server", "restart"); err != nil {
		report("FAIL", "HC-P5", err.Error(), where)
		return
	}
	_, closed = s.events(5 * time.Second)
	time.Sleep(2 * time.Second)
	now := l.status()
	survived := l.get(t.Address).PID == after.PID
	s2, err := l.subscribe(map[string]any{})
	seq := -1
	if err == nil {
		seq = s2.seq
		s2.close()
	}
	report(passIf(survived && closed), "HC-P5", fmt.Sprintf("hc server restart: programs keep running %v; the stream ends "+
		"(EOF) %v; a new subscribe starts at seq %d; core.id %s→%s, core pid %d→%d, holder instances %v→%v", survived, closed,
		seq, before.core, now.core, before.pid, now.pid, before.instances, now.instances), where)
	s, _ = l.subscribe(map[string]any{})
	l.stop()
	evs, closed = s.events(3 * time.Second)
	if _, err := l.run(l.env, "server", "start"); err != nil {
		report("FAIL", "HC-P5", err.Error(), where)
		return
	}
	var back hcTerm
	poll(10*time.Second, func() bool { back = l.get(t.Address); return back.PID != 0 })
	again := l.status()
	var stopped []string
	for _, e := range evs {
		if e.Method == "event" && e.Type != "foreground" {
			stopped = append(stopped, e.Type+" "+e.Term)
		}
	}
	report(passIf(back.State == "live" && back.Keep && back.PID != 0), "HC-P5", fmt.Sprintf("hc server stop then start: the "+
		"stream got %v and ended %v; the kept terminal is back live as a shell (pid %d, labels %v, cwd %s); core.id %s, "+
		"holder instances %v→%v", stopped, closed, back.PID, back.Labels, back.Cwd, again.core, now.instances, again.instances), where)
}

func deref(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

type hcStatus struct {
	core      string
	pid       int
	holders   []int
	instances []string
}

func (l *hcLab) status() hcStatus {
	var s hcStatus
	r, err := l.call("server.status", map[string]any{})
	if err != nil {
		return s
	}
	var hs []struct {
		PID      int    `json:"pid"`
		Instance string `json:"instance"`
	}
	get(r, "core.id", &s.core)
	get(r, "pid", &s.pid)
	get(r, "holders", &hs)
	for _, h := range hs {
		s.holders, s.instances = append(s.holders, h.PID), append(s.instances, h.Instance)
	}
	return s
}

// hcP6: which label keys hc takes, what a label-filtered stream sends as a terminal starts and stops matching, and
// what a slow subscriber gets.
func hcP6(hc, root string) {
	where := hcPlan + " P6"
	l, err := newHCLab(hc, root, "6")
	if err != nil {
		report("FAIL", "HC-P6", "no core: "+err.Error(), where)
		return
	}
	defer l.stop()
	var took, refused []string
	for _, k := range []string{"obor.project", "obor/project", "obor_project", "obor project", "Obor.Project", "a=b", ""} {
		_, err := l.term(map[string]any{"argv": []string{"/bin/sleep", "60"}, "no_parent": true, "labels": map[string]string{k: "v=1 x/y"}})
		if err != nil {
			refused = append(refused, strconv.Quote(k)+" ("+hcKind(err)+")")
		} else {
			took = append(took, strconv.Quote(k))
		}
	}
	report("PASS", "HC-P6", "label keys taken: "+strings.Join(took, " ")+"; refused: "+strings.Join(refused, " ")+
		" (values are free text)", where)
	t, err := l.shell(nil)
	if err != nil {
		report("FAIL", "HC-P6", "no shell terminal: "+err.Error(), where)
		return
	}
	s, err := l.subscribe(map[string]any{"labels": map[string]string{"obor.project": "probe"}})
	if err != nil {
		report("FAIL", "HC-P6", err.Error(), where)
		return
	}
	var steps []string
	for _, lb := range []map[string]string{{"obor.project": "probe"}, {"obor.agent": "w1"}, {"obor.project": ""},
		{"obor.agent": "w2"}, {"obor.project": "probe"}} {
		_, _ = l.call("terminal.label", map[string]any{"terminal": t.Address, "labels": lb})
		evs, _ := s.events(700 * time.Millisecond)
		var got []string
		for _, e := range evs {
			if e.Type == "labels" {
				var d map[string]json.RawMessage
				_ = json.Unmarshal(e.Data, &d)
				g := "labels"
				if _, ok := d["record"]; ok {
					g += "+record"
				}
				got = append(got, g)
			}
		}
		steps = append(steps, fmt.Sprintf("%v→%v", lb, got))
	}
	s.close()
	report("PASS", "HC-P6", "a stream filtered on obor.project=probe, labels set in turn: "+strings.Join(steps, "; "), where)
	// The slow subscriber reads the socket itself through a small buffer, so the core sees it fall behind.
	r, err := hcDial(l.sock)
	if err != nil {
		return
	}
	r.r = bufio.NewReaderSize(r.c, 4096)
	if _, err := r.call("events.subscribe", map[string]any{}, 10*time.Second); err != nil {
		r.c.Close()
		return
	}
	slow := map[string]int{}
	var stopping atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			b, err := r.r.ReadBytes('\n')
			if err != nil {
				if !stopping.Load() {
					slow["EOF"]++
				}
				return
			}
			slow[hcEventOf(b).Method]++
			time.Sleep(20 * time.Millisecond)
		}
	}()
	h, err := hcDial(l.sock)
	if err == nil {
		for i := 0; i < 3000; i++ {
			_, _ = h.call("terminal.label", map[string]any{"terminal": t.Address, "labels": map[string]string{"n": strconv.Itoa(i)}}, 10*time.Second)
		}
		h.c.Close()
	}
	time.Sleep(3 * time.Second)
	stopping.Store(true)
	r.c.Close()
	<-done
	report("PASS", "HC-P6", fmt.Sprintf("a subscriber reading one line per 20 ms through a 4 KB buffer while 3000 label "+
		"events arrive: %v (an EOF entry: the core closed it)", slow), where)
}

// hcP8: who a run's parent is, and what reaches a parent shell.
func hcP8(hc, root string) {
	where := hcPlan + " P8"
	l, err := newHCLab(hc, root, "8")
	if err != nil {
		report("FAIL", "HC-P8", "no core: "+err.Error(), where)
		return
	}
	defer l.stop()
	c, err := l.term(map[string]any{"argv": []string{"/bin/sh", "-c", "echo child; sleep 1"}})
	report(passIf(err == nil && c.Parent == nil), "HC-P8", fmt.Sprintf("terminal.run with no caller and no no_parent: parent %s",
		deref(c.Parent)), where)
	t, err := l.shell(nil)
	if err != nil {
		report("FAIL", "HC-P8", "no shell terminal: "+err.Error(), where)
		return
	}
	_, _ = l.send(t.Address, "clear")
	_, _ = l.send(t.Address, "hc run --bg -- sh -c 'echo child-c; sleep 1'")
	time.Sleep(6 * time.Second)
	scr := l.screen(t.Address)
	var child hcTerm
	if r, err := l.call("terminal.list", map[string]any{"all": true}); err == nil {
		var ts []hcTerm
		get(r, "terminals", &ts)
		for _, x := range ts {
			if x.Parent != nil && *x.Parent == t.Address {
				child = x
			}
		}
	}
	report(passIf(child.Address != ""), "HC-P8", fmt.Sprintf("`hc run --bg` typed in shell %s: child %s parent %s; typed into "+
		"the shell after the child ended: %v", t.Address, child.Address, deref(child.Parent), strings.Contains(scr, "<hc")), where)
	_, _ = l.send(t.Address, "hc run --bg -- sleep 300")
	time.Sleep(3 * time.Second)
	_, _ = l.send(t.Address, "exit")
	time.Sleep(4 * time.Second)
	gone := l.get(t.Address)
	var kid hcTerm
	if r, err := l.call("terminal.list", map[string]any{"all": true}); err == nil {
		var ts []hcTerm
		get(r, "terminals", &ts)
		for _, x := range ts {
			if x.Parent != nil && *x.Parent == t.Address && x.Address != child.Address {
				kid = x
			}
		}
	}
	report("WARN", "HC-P8", fmt.Sprintf("`exit` typed at a kept shell: the terminal is %s (%s), keep does not bring it back; its "+
		"running child %s is %s (%s)", gone.State, deref(gone.Reason), kid.Address, kid.State, deref(kid.Reason)), where)
}

// hcP1Claude: Claude in an hc terminal: the trust dialog, a send at blocked, one turn's status words, a permission
// prompt, the exit (TURN=1).
func hcP1Claude(hc, root string) {
	where := hcPlan + " P1"
	l, err := newHCLab(hc, root, "1")
	if err != nil {
		report("FAIL", "HC-P1", "no core: "+err.Error(), where)
		return
	}
	defer l.stop()
	proj := filepath.Join(l.root, "work", "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil || exec.Command("git", "-C", proj, "init", "-q").Run() != nil {
		report("FAIL", "HC-P1", "no scratch project", where)
		return
	}
	t, err := l.term(map[string]any{"keep": true, "no_parent": true, "cwd": proj})
	if err != nil || !poll(10*time.Second, func() bool { return strings.TrimSpace(l.screen(t.Address)) != "" }) {
		report("FAIL", "HC-P1", "no shell terminal", where)
		return
	}
	a := t.Address
	s, err := l.subscribe(map[string]any{"terminal": a})
	if err != nil {
		report("FAIL", "HC-P1", err.Error(), where)
		return
	}
	defer s.close()
	_, _ = l.send(a, "claude --model haiku --permission-mode manual")
	var cur hcTerm
	waitState := func(d time.Duration, want ...string) bool {
		return poll(d, func() bool { cur = l.get(a); return slices.Contains(want, cur.state()) })
	}
	if !waitState(30*time.Second, "blocked/permission") {
		report("FAIL", "HC-P1", "Claude at its trust dialog reports "+cur.state()+", not blocked/permission; screen "+
			tail(l.screen(a), 6), where+", no-go")
		return
	}
	report("PASS", "HC-P1", fmt.Sprintf("Claude's trust dialog: blocked/permission %q, app %q, reported %v", cur.Status.Msg,
		cur.Status.App, cur.Reported), where)
	hcRefused(l, a, where)
	_, _ = l.keys(a, "down")
	_, _ = l.keys(a, "enter")
	if !waitState(30*time.Second, "idle") {
		report("FAIL", "HC-P1", "Claude after the trust dialog: "+cur.state(), where)
		return
	}
	_, _ = s.events(500 * time.Millisecond)
	d, _ := l.send(a, "Reply with exactly OK")
	waitState(60*time.Second, "done", "idle")
	evs, _ := s.events(2 * time.Second)
	report("PASS", "HC-P1", fmt.Sprintf("one turn: the send returned in %s; status words %v", d.Round(time.Millisecond),
		hcWords(evs)), where)
	_, _ = l.send(a, "Run this exact shell command with your Bash tool: touch probe-file.txt . Then reply OK.")
	if !waitState(60*time.Second, "blocked/permission") {
		report("FAIL", "HC-P1", "Claude at a permission prompt reports "+cur.state()+", not blocked/permission", where+", no-go")
		return
	}
	report("PASS", "HC-P1", fmt.Sprintf("Claude's permission prompt: blocked/permission %q", cur.Status.Msg), where)
	hcRefused(l, a, where)
	_, _ = l.keys(a, "esc")
	waitState(30*time.Second, "idle", "done")
	_, _ = l.send(a, "/exit")
	poll(15*time.Second, func() bool { cur = l.get(a); return cur.Foreground != nil && cur.Foreground.PID == cur.PID })
	report(passIf(cur.Status == nil), "HC-P1", fmt.Sprintf("Claude exited to the shell: status %s, reported %v", cur.state(),
		cur.Reported), where)
}

// hcRefused: a send at blocked is refused with kind blocked and types nothing (the status stays the same prompt).
func hcRefused(l *hcLab, a, where string) {
	before := l.get(a)
	_, err := l.send(a, "4")
	time.Sleep(2 * time.Second)
	after := l.get(a)
	same := after.state() == before.state() && after.Status != nil && before.Status != nil && after.Status.Msg == before.Status.Msg
	report(passIf(hcKind(err) == "blocked" && same), "HC-P2", fmt.Sprintf("a send at blocked: refused with kind %q, the "+
		"prompt still up after it: %v", hcKind(err), same), where+", no-go")
}

// hcWords are the status words of a stream's status events, repeats folded.
func hcWords(evs []hcEvent) []string {
	var out []string
	for _, e := range evs {
		if e.Type != "status" {
			continue
		}
		var d struct {
			Status *struct {
				State string `json:"state"`
			} `json:"status"`
		}
		_ = json.Unmarshal(e.Data, &d)
		w := "null"
		if d.Status != nil {
			w = d.Status.State
		}
		if len(out) == 0 || out[len(out)-1] != w {
			out = append(out, w)
		}
	}
	return out
}
