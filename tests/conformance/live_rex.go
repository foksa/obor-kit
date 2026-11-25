//go:build conformance

// The Rex rows (`make conformance TIER=live KIND=rex`): obor's read side of the Rex adapter (internal/rex: Panes'
// snapshot and stream) on a throwaway Rex server, never the person's: the server runs from a scrubbed environment
// with XDG_RUNTIME_DIR, its socket, state dir and an empty config in a scratch dir and --restore=false; every client
// (the adapter's and the driver's) gets the same XDG_RUNTIME_DIR and `-S unix://<socket> --autostart=false`; `rex
// server stop` with the same env and --state-dir ends it, and a kill by PID if it is still up. The driver makes the
// session and blocks of the read rows with the CLI itself; the contract rows (REX-C) drive the adapter's write side and
// placement. No agent CLI runs here (TERMINAL=rex KIND=<kind> starts each kind: live_rexterm.go): a block's status
// report is a printf of an OSC 7501 sequence. Rex is experimental: these rows do not gate a merge (D7), and without a
// rex CLI they SKIP.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
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
	"obor/internal/osnotify"
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
	leak                   []string // NAME=VALUE the server alone gets besides env: an identity its blocks must not keep
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
	l.server.Env, l.server.Dir = append(append([]string{}, l.env...), l.leak...), filepath.Join(l.root, "work")
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

	rexR4(l, sid, block)
	rexContract(l, block)

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

// rexBurst is the delivery row's reports: n status reports (msg base64 "m<i>"), then an idle one whose msg is "end".
// One `cat` of them holds the block's foreground for the whole burst, so each report's owner is the foreground (a
// script forking per report would hand the foreground to its children, and the owner rule would rightly hide those
// reports).
func rexBurst(n int) []byte {
	var b strings.Builder
	osc := func(state, msg string) {
		fmt.Fprintf(&b, "\033]7501;state=%s:app=claude-code:msg=%s\007", state, base64.StdEncoding.EncodeToString([]byte(msg)))
	}
	for i := 1; i <= n; i++ {
		osc("working", fmt.Sprintf("m%d", i))
	}
	osc("idle", "end")
	return []byte(b.String())
}

// rexR4: the stream's delivery (internal/rex/contract.go), on the transport the adapter reads: `rex events --all
// --json` run as stream.go runs it, its output not read until the block's record says a burst of status reports ended;
// then every report's event is there, in order. (Read through the adapter's panes, a report counts only while its
// owner holds the foreground, which a short-lived reporter never does in Rex's eyes: the owner rule, not delivery.) The
// block is back at its shell when the row ends.
func rexR4(l *rexLab, sid, block string) {
	where := "internal/rex/contract.go (the stream's delivery), internal/rex/stream.go (Client.events)"
	const n = 1000
	defer poll(15*time.Second, func() bool { p, err := l.cl.Pane(context.Background(), block); return err == nil && p.Status == "" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := exec.CommandContext(ctx, l.bin, "-S", "unix://"+l.sock, "--autostart=false", "events", "--all", "--json")
	ev.Env = l.env
	stdout, err := ev.StdoutPipe()
	if err == nil {
		err = ev.Start()
	}
	if err != nil {
		report("FAIL", "REX-R4", "rex events: "+err.Error(), where)
		return
	}
	defer func() { _ = ev.Process.Kill(); _ = ev.Wait() }()
	time.Sleep(time.Second) // the stream subscribes before the burst
	if err := os.WriteFile(filepath.Join(l.root, "work", "burst.txt"), rexBurst(n), 0o600); err != nil {
		report("FAIL", "REX-R4", err.Error(), where)
		return
	}
	if _, err := l.rex("send", "-b", block, fmt.Sprintf("cat %s\r", shq(filepath.Join(l.root, "work", "burst.txt")))); err != nil {
		report("FAIL", "REX-R4", err.Error(), where)
		return
	}
	ended := poll(60*time.Second, func() bool { // the stream's output is not read meanwhile
		b, err := l.rex("api", "call", rex.Terminal+".program_status", fmt.Sprintf(`{"session_id":%q,"block_id":%q,"args":{}}`, sid, block))
		return err == nil && strings.Contains(b, `"state":"idle"`) && strings.Contains(b, `"msg":"end"`)
	})
	if !ended {
		report("FAIL", "REX-R4", "the burst did not end within 60 s", where)
		return
	}
	got, end := []int{}, false
	go func() { time.Sleep(30 * time.Second); cancel() }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 1<<20)
	for !end && sc.Scan() {
		var line struct {
			Payload struct {
				Event struct {
					Type    string `json:"type"`
					Payload struct {
						Block   string `json:"block_id"`
						Name    string `json:"name"`
						Payload struct {
							Record struct {
								Msg string `json:"msg"`
							} `json:"record"`
						} `json:"payload"`
					} `json:"payload"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		e := line.Payload.Event
		if e.Type != rex.EventBlock || e.Payload.Block != block || e.Payload.Name != rex.EventStatusChanged {
			continue
		}
		msg := e.Payload.Payload.Record.Msg
		if msg == "end" {
			end = true
		} else if i, err := strconv.Atoi(strings.TrimPrefix(msg, "m")); err == nil && strings.HasPrefix(msg, "m") {
			got = append(got, i)
		}
	}
	inOrder := len(got) == n
	for i, v := range got {
		inOrder = inOrder && v == i+1
	}
	if !end || !inOrder {
		report("FAIL", "REX-R4", fmt.Sprintf("%d of %d reports read (in order %v), the end report read %v", len(got), n, inOrder, end), where)
		return
	}
	report("PASS", "REX-R4", fmt.Sprintf("%d status reports, rex events read only after the burst ended: every one, in order", n), where)
}

// rexContract: the terminal contract (testkit.Contract) against the Rex adapter on the scratch server, one REX-C row
// each, on block (a shell in session conf, working in the scratch work dir), the guard in front of its Prompt as the
// terminal is built. A row that starts an agent has none here (the server-only run starts no CLI): each kind's workflow
// runs those rows with its live agent (RX-C, live_rexterm.go).
func rexContract(l *rexLab, block string) {
	where := "internal/testkit/terminal.go, on internal/rex (control.go, place.go)"
	cl := l.cl
	panes := rex.Panes{C: cl, Notifier: osnotify.Notifier{Post: func(context.Context, string, string) error { return nil }}}
	t := testkit.Terminal{Control: port.GuardPrompts(cl), Panes: panes, Setup: cl, Group: "conf", Cwd: filepath.Join(l.root, "work")}
	for _, row := range testkit.Contract {
		id := "REX-C " + row.Name
		if row.Agent {
			report("SKIP", id, "starts an agent CLI: TERMINAL=rex KIND=<kind> runs it with the kind's live agent (RX-C)", where)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := row.Run(ctx, t, block)
		cancel()
		if err != nil {
			report("FAIL", id, err.Error(), where)
			continue
		}
		report("PASS", id, "the Rex adapter keeps the row on a scratch server", where)
	}
}
