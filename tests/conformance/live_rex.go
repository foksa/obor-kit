//go:build conformance

// The Rex rows (`make conformance TIER=live KIND=rex`): obor's read side of the Rex adapter (internal/rex: Panes'
// snapshot and stream) on a throwaway Rex server, never the person's: the server runs from a scrubbed environment
// with XDG_RUNTIME_DIR, its socket, state dir and an empty config in a scratch dir and --restore=false; every client
// (the adapter's and the driver's) gets the same XDG_RUNTIME_DIR and `-S unix://<socket> --autostart=false`; `rex
// server stop` with the same env and --state-dir ends it, and a kill by PID if it is still up. The driver makes the
// session and blocks with the CLI itself (the adapter has no write side yet). No agent CLI runs: a block's status
// report is a printf of an OSC 7501 sequence. Rex is experimental: these rows do not gate a merge (D7), and without a
// rex CLI they SKIP.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"obor/internal/app/port"
	"obor/internal/rex"
	"obor/internal/testkit"
)

const rexPlan = "docs/plan/terminal-adapters.md step 9"

// rexBundle is where the Rex app keeps its CLI: the driver links it into its scratch PATH when no rex is on PATH (the
// adapter itself never looks there).
const rexBundle = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"

// rexLab is the throwaway server.
type rexLab struct {
	bin, root, sock, state string
	env                    []string // every rex command's and the server's
	server                 *exec.Cmd
	cl                     *rex.Client
}

func rexLive() {
	bin, err := exec.LookPath("rex")
	if err != nil {
		if _, e := os.Stat(rexBundle); e != nil {
			report("SKIP", "REX", "no rex on PATH and no Rex app bundle", rexPlan)
			return
		}
		bin = rexBundle
	}
	// /tmp, not TMPDIR: the socket's path must stay under a unix socket's 104 bytes.
	root, err := os.MkdirTemp("/tmp", "obrex-")
	if err != nil {
		report("FAIL", "REX", "no scratch dir: "+err.Error(), rexPlan)
		return
	}
	root, _ = filepath.EvalSymlinks(root)
	l := &rexLab{root: root, sock: filepath.Join(root, "s.sock"), state: filepath.Join(root, "state")}
	if err := l.setup(bin); err != nil {
		report("FAIL", "REX", "scratch setup: "+err.Error(), rexPlan)
		os.RemoveAll(root)
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		l.stop()
		os.Exit(130)
	}()
	defer signal.Stop(sig)
	defer func() {
		l.stop()
		os.RemoveAll(root)
	}()
	v, _ := output(15*time.Second, l.bin, "version")
	out.note("rex " + firstLine(v))
	if err := l.start(); err != nil {
		report("FAIL", "REX", "no scratch server: "+err.Error(), rexPlan)
		return
	}
	block, ok := rexR1(l)
	if !ok {
		return
	}
	rexR2(l, block)
}

// setup makes the scratch dirs and the environment: the scrubbed one, the scratch XDG_RUNTIME_DIR, and a scratch bin
// dir holding rex first on PATH.
func (l *rexLab) setup(bin string) error {
	for _, d := range []string{"xrun", "state", "work", "bin"} {
		if err := os.MkdirAll(filepath.Join(l.root, d), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(l.root, "init.lua"), nil, 0o600); err != nil {
		return err
	}
	l.bin = filepath.Join(l.root, "bin", "rex")
	if err := os.Symlink(bin, l.bin); err != nil {
		return err
	}
	for _, kv := range cleanEnv() {
		if strings.HasPrefix(kv, "PATH=") {
			kv = "PATH=" + filepath.Join(l.root, "bin") + ":" + strings.TrimPrefix(kv, "PATH=")
		}
		l.env = append(l.env, kv)
	}
	l.env = append(l.env, "XDG_RUNTIME_DIR="+filepath.Join(l.root, "xrun"))
	l.cl = &rex.Client{Bin: l.bin, Server: "unix://" + l.sock, NoAutostart: true, Env: l.env, Kinds: testkit.Kinds}
	return nil
}

// rex runs the CLI against the scratch server.
func (l *rexLab) rex(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.bin, append([]string{"-S", "unix://" + l.sock, "--autostart=false"}, args...)...)
	cmd.Env, cmd.Dir = l.env, filepath.Join(l.root, "work")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("rex %s: %v: %s", strings.Join(args, " "), err, firstLine(string(b)))
	}
	return string(b), nil
}

// start runs the server and waits for its socket to answer.
func (l *rexLab) start() error {
	l.server = exec.Command(l.bin, "server", "run", "--listen", "unix://"+l.sock, "--state-dir", l.state,
		"--config", filepath.Join(l.root, "init.lua"), "--restore=false")
	l.server.Env, l.server.Dir = l.env, filepath.Join(l.root, "work")
	logf, err := os.Create(filepath.Join(l.root, "server.log"))
	if err != nil {
		return err
	}
	l.server.Stdout, l.server.Stderr = logf, logf
	if err := l.server.Start(); err != nil {
		return err
	}
	go func() { _ = l.server.Wait(); logf.Close() }()
	for range 50 {
		if _, err := l.rex("api", "call", "server.status", "{}"); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the server did not answer within 5 s")
}

// stop stops the server with the same env and state dir, then by PID if it is still up.
func (l *rexLab) stop() {
	if l.server == nil || l.server.Process == nil {
		return
	}
	if l.server.ProcessState == nil {
		cmd := exec.Command(l.bin, "server", "stop", "-S", "unix://"+l.sock, "--state-dir", l.state)
		cmd.Env = l.env
		_ = cmd.Run()
		for range 30 {
			if l.server.ProcessState != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if l.server.ProcessState == nil {
			_ = l.server.Process.Kill()
		}
	}
}

// rexR1: a session's block is a pane in the adapter's snapshot: its session's label the group, its label the label,
// its program's pid the run, its cwd the cwd, no agent at its shell; the instance is server.status's instance_id.
func rexR1(l *rexLab) (string, bool) {
	where := "internal/rex/panes.go (term.pane, Panes.Snapshot)"
	work := filepath.Join(l.root, "work")
	b, err := l.rex("new", "conf", "-c", work, "--json")
	var made struct {
		SessionID string `json:"session_id"`
		Windows   []struct {
			Blocks []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(b), &made)
	}
	if err != nil || len(made.Windows) != 1 || len(made.Windows[0].Blocks) != 1 {
		report("FAIL", "REX-R1", fmt.Sprintf("rex new: %v (%s)", err, firstLine(b)), where)
		return "", false
	}
	block := made.Windows[0].Blocks[0]
	if _, err := l.rex("api", "call", "session.set_block_label", fmt.Sprintf(`{"session_id":%q,"block_id":%q,"label":"w1"}`, made.SessionID, block)); err != nil {
		report("FAIL", "REX-R1", err.Error(), where)
		return "", false
	}
	var st struct {
		InstanceID string `json:"instance_id"`
	}
	sb, _ := l.rex("api", "call", "server.status", "{}")
	_ = json.Unmarshal([]byte(sb), &st)
	var p port.Pane
	var snap port.Snapshot
	deadline := time.Now().Add(10 * time.Second)
	for { // the shell's cwd shows once login has started it
		snap, err = (rex.Panes{C: l.cl}).Snapshot(context.Background())
		if err == nil && len(snap.Panes) == 1 {
			p = snap.Panes[0]
		}
		if p.Cwd != "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	var bad []string
	if err != nil {
		bad = append(bad, "Snapshot: "+err.Error())
	}
	if p.ID != block || p.Terminal != block || p.Group != "conf" || p.Label != "w1" || !same(p.Cwd, work) {
		bad = append(bad, fmt.Sprintf("pane %+v, want %s labelled w1 in group conf at %s", p, block, work))
	}
	if _, e := strconv.Atoi(p.Run); e != nil {
		bad = append(bad, fmt.Sprintf("run %q is no pid", p.Run))
	}
	if p.Status != "" || p.Agent != "" {
		bad = append(bad, fmt.Sprintf("status %q agent %q at the shell, want none", p.Status, p.Agent))
	}
	if snap.Instance == "" || snap.Instance != st.InstanceID || snap.Version == "" || !snap.PaneRuns {
		bad = append(bad, fmt.Sprintf("snapshot instance %q (server.status %q), version %q, PaneRuns %v", snap.Instance, st.InstanceID, snap.Version, snap.PaneRuns))
	}
	if snap.Note != "" {
		report("WARN", "REX-R1", snap.Note, "internal/rex/contract.go (APIRevision)")
	}
	if len(bad) > 0 {
		report("FAIL", "REX-R1", strings.Join(bad, "; "), where)
		return "", false
	}
	report("PASS", "REX-R1", fmt.Sprintf("a block is a pane (group, label, run %s, cwd), no agent at its shell; instance %s, Rex %s",
		p.Run, snap.Instance, snap.Version), where)
	return made.SessionID + " " + block, true
}

// rexR2: the stream, from the adapter's Subscribe: a relabel (session_view_changed only), a program at the shell
// (unknown), a status report while its reporter holds the foreground and none once it exited (its record stays: the
// owner rule), a new block (created) and its close (closed), and the server's stop (the stream ends as a refresh; a new
// subscribe finds no server).
func rexR2(l *rexLab, ids string) {
	where := "internal/rex/stream.go (Panes.Subscribe)"
	sid, block, _ := strings.Cut(ids, " ")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Now()
	s, err := (rex.Panes{C: l.cl}).Subscribe(ctx)
	if err != nil {
		report("FAIL", "REX-R2", "Subscribe: "+err.Error(), where)
		return
	}
	defer s.Close()
	out.note(fmt.Sprintf("rex: Subscribe (stream, sync, snapshot of one block) took %s", time.Since(t0).Round(time.Millisecond)))
	if len(s.Snapshot.Panes) != 1 || s.Snapshot.Panes[0].ID != block {
		report("FAIL", "REX-R2", fmt.Sprintf("the subscribe's snapshot %+v, want the one block", s.Snapshot.Panes), where)
		return
	}
	// await reads events until one meets ok (its pane's), within d.
	await := func(d time.Duration, ok func(port.Event) bool) (port.Event, error) {
		timeout := time.After(d)
		for {
			select {
			case e, open := <-s.C:
				if !open {
					return port.Event{}, fmt.Errorf("the stream ended: %v", s.Err())
				}
				if ok(e) {
					return e, nil
				}
			case <-timeout:
				return port.Event{}, fmt.Errorf("no such event within %s", d)
			}
		}
	}
	step := func(name string, act func() error, d time.Duration, ok func(port.Event) bool) bool {
		if err := act(); err != nil {
			report("FAIL", "REX-R2", name+": "+err.Error(), where)
			return false
		}
		if _, err := await(d, ok); err != nil {
			report("FAIL", "REX-R2", name+": "+err.Error(), where)
			return false
		}
		return true
	}
	call := func(method, params string) func() error {
		return func() error { _, err := l.rex("api", "call", method, params); return err }
	}
	send := func(text string) func() error {
		return func() error { _, err := l.rex("send", "-b", block, text); return err }
	}
	status := func(st, agent string) func(port.Event) bool {
		return func(e port.Event) bool {
			return e.Kind == port.EventStatus && e.Pane == block && e.New.Status == st && e.New.Agent == agent
		}
	}
	// A reporter that ends its turn (done) and exits: Rex removes a record itself only while it says working, so a
	// done one stays behind its exited owner (docs/research/rex-probes.md R3).
	report7501 := `sh -c 'printf "\033]7501;state=working:app=claude-code\007"; sleep 1; printf "\033]7501;state=done:app=claude-code\007"; sleep 1'` + "\r"
	var w3 string
	if !step("relabel", call("session.set_block_label", fmt.Sprintf(`{"session_id":%q,"block_id":%q,"label":"w2"}`, sid, block)), 10*time.Second,
		func(e port.Event) bool { return e.Kind == port.EventStatus && e.Pane == block && e.New.Label == "w2" }) ||
		!step("a program at the shell", send("sleep 2\r"), 10*time.Second, status("unknown", "")) ||
		!step("back at the shell", func() error { return nil }, 10*time.Second, status("", "")) ||
		!step("a status report by the foreground", send(report7501), 10*time.Second, status("working", "claude")) ||
		!step("its turn's end", func() error { return nil }, 10*time.Second, status("done", "claude")) ||
		!step("its reporter exited, its record left", func() error { return nil }, 10*time.Second, status("", "")) ||
		!step("a new block", func() error {
			b, err := l.rex("run", "-s", sid, "--focus=false", "--label", "w3", "-c", filepath.Join(l.root, "work"), "--json", "--", "/bin/zsh", "-l")
			var r struct {
				Blocks []string `json:"block_ids"`
			}
			if err == nil && json.Unmarshal([]byte(b), &r) == nil && len(r.Blocks) == 1 {
				w3 = r.Blocks[0]
			}
			return err
		}, 10*time.Second, func(e port.Event) bool {
			return e.Kind == port.EventCreated && e.New.Label == "w3" && e.New.Group == "conf"
		}) ||
		!step("its close", func() error { return call("block.close", fmt.Sprintf(`{"session_id":%q,"block_id":%q}`, sid, w3))() }, 10*time.Second,
			func(e port.Event) bool { return e.Kind == port.EventClosed && e.Pane == w3 }) {
		return
	}
	// The record the reporter left is still there: the pane says no agent by the owner rule, not by its removal.
	var ps struct {
		Records []struct {
			State string `json:"state"`
		} `json:"records"`
	}
	b, err := l.rex("api", "call", "com.superlogical.terminal.program_status", fmt.Sprintf(`{"session_id":%q,"block_id":%q,"args":{}}`, sid, block))
	if err != nil || json.Unmarshal([]byte(b), &ps) != nil || len(ps.Records) != 1 || ps.Records[0].State != "done" {
		report("WARN", "REX-R2", fmt.Sprintf("the exited reporter's record is not kept (%v, %s): the owner rule was not what cleared the pane", err, firstLine(b)), where)
	}
	report("PASS", "REX-R2", "relabel, a program (unknown), reports by the foreground (working, done: claude) and no agent once it exited with its done record left, a block created and closed", where)

	l.stop()
	if _, err := await(10*time.Second, func(port.Event) bool { return false }); err == nil || !port.Refreshed(s.Err()) {
		report("FAIL", "REX-R3", fmt.Sprintf("after the server's stop: %v (Err %v), want the stream ended as a refresh", err, s.Err()), where)
		return
	}
	_, err = (rex.Panes{C: l.cl}).Subscribe(ctx)
	if !port.NotRunning(err) {
		report("FAIL", "REX-R3", fmt.Sprintf("a subscribe with the server stopped: %v, want NotRunning", err), where)
		return
	}
	report("PASS", "REX-R3", "the server's stop ends the stream as a refresh ("+firstLine(s.Err().Error())+"); a new subscribe is NotRunning", where)
}
