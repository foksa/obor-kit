package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"obor/internal/testkit"
)

// startServe starts obor serve (the binary) in dir with env; its output is the buffer.
func startServe(t *testing.T, dir string, env []string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(oborBin, "serve")
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Env = dir, &out, &out, append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, &out
}

// T-522: obor serve serves the project it runs in: its socket is <root>/.obor/state/serve.sock and its lock the
// project's serve.flock. A CLI in a lane worktree reaches it (obor status: serve's agent of the lane, from the fake
// herdr); a second serve of the project is refused by the lock, also from a lane; serve outside an obor project fails;
// on TERM the lock and the socket are gone.
func TestServeBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(lane)
	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}, {ID: "w2", Label: "elsewhere"}}
	w := f.Agent("w1:p1", "pj-l-01", "working", lane)
	w.Workspace, w.Label = "w1", "pj-l-01" // labelled by its launch: serve's identity of it
	f.Agent("w2:p1", "x", "idle", lane).Workspace = "w2"
	srv := herdrtest.Serve(t, f)
	home := t.TempDir()
	env := []string{"OBOR_STATE_HOME=" + home, "HERDR_SOCKET_PATH=" + srv.Path}
	sock, flock := filepath.Join(p.Root, ".obor", "state", "serve.sock"), filepath.Join(p.Root, ".obor", "state", "serve.flock")
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("precondition: no serve socket before serve starts")
	}

	cmd, out := startServe(t, p.Root, env)
	var status string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		rc, stdout, _ := run(t, lane, env, "status")
		if rc == 0 && strings.Contains(stdout, "agent: pj-l-01 (working)") { // the lanes are read just after the snapshot
			status = stdout
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("obor status in the lane never showed serve's agent: exit %d %q; serve output:\n%s", rc, stdout, out.String())
		}
	}
	if want := fmt.Sprintf("serve: pane pid %d since ", cmd.Process.Pid); !strings.HasPrefix(status, want) ||
		strings.Contains(status, "not loaded") {
		t.Errorf("status from the lane:\n%s\nwant %q… with the project loaded", status, want)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("serve socket %s: %v; want a unix socket in the project", sock, err)
	}
	if b, _ := os.ReadFile(flock); string(b) != strconv.Itoa(cmd.Process.Pid)+"\n" {
		t.Errorf("serve.flock holds %q; want serve's pid %d", b, cmd.Process.Pid)
	}
	if !strings.Contains(out.String(), "obor serve: running (project "+p.Root+", socket "+sock+")") {
		t.Errorf("serve's start line names no project and socket:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "herdr: fake, protocol "+strconv.Itoa(herdr.Protocol)) {
		t.Errorf("serve did not report its herdr snapshot before running:\n%s", out.String())
	}
	// T-646: serve registers the project it serves in the machine's list of obor projects by name.
	if b, err := os.ReadFile(filepath.Join(home, "projects.json")); err != nil ||
		!strings.Contains(string(b), `"proj": "`+p.Root+`"`) {
		t.Errorf("the machine's project list: %v, %q; want proj at the served root", err, b)
	}
	// obor status --facts: --all is a no-op for one release (T-522)
	if rc, stdout, stderr := run(t, p.Root, env, "status", "--all", "--facts"); rc != 0 ||
		!strings.Contains(stdout, "agent: pj-l-01 (working)") {
		t.Errorf("obor status --all --facts: exit %d, %q, %q; want the project's status", rc, stdout, stderr)
	}
	// T-774: obor serve status is gone; obor status --facts replaces it
	if rc, _, stderr := run(t, p.Root, env, "serve", "status"); rc != 2 || !strings.Contains(stderr, "usage: obor serve") {
		t.Errorf("obor serve status: exit %d, %q; want 2 and the usage", rc, stderr)
	}
	if strings.Contains(status, "w2:p1") {
		t.Errorf("status lists a pane of another workspace:\n%s", status)
	}

	for _, dir := range []string{p.Root, lane} {
		rc, _, stderr := run(t, dir, env, "serve")
		if want := "FAIL obor serve already runs for " + p.Root + " (pid " + strconv.Itoa(cmd.Process.Pid); rc != 1 || !strings.Contains(stderr, want) {
			t.Errorf("second obor serve in %s: exit %d, %q; want 1 and %q", dir, rc, stderr, want)
		}
	}
	if rc, _, stderr := run(t, p.Root, env, "serve", "nope"); rc != 2 || !strings.Contains(stderr, "usage: obor serve") {
		t.Errorf("obor serve nope: exit %d, %q", rc, stderr)
	}
	if rc, _, stderr := run(t, p.Root, env, "serve", "forget"); rc != 2 || !strings.Contains(stderr, "usage: obor serve") {
		t.Errorf("obor serve forget (gone): exit %d, %q", rc, stderr)
	}
	if rc, _, stderr := run(t, p.Base, env, "serve"); rc != 1 || !strings.Contains(stderr, "FAIL obor serve: "+p.Base+" is not in an obor project") {
		t.Errorf("obor serve outside a project: exit %d, %q", rc, stderr)
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("TERM: %v, want exit 0; output:\n%s", err, out.String())
	}
	for _, f := range []string{sock, flock} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s left after TERM", f)
		}
	}
	if rc, stdout, _ := run(t, lane, env, "status"); rc != 0 || !strings.Contains(stdout, "serve: down") {
		t.Errorf("obor status after TERM: exit %d, %q; want serve down", rc, stdout)
	}
	if rc, _, _ := run(t, p.Root, env, "has", "serve", "status"); rc != 1 { // T-774: the alias is gone
		t.Errorf("obor has serve status: exit %d, want 1", rc)
	}
}

func TestServeReportsHerdrProtocolMismatch(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	f := herdrtest.New()
	srv := herdrtest.Serve(t, f)
	srv.Protocol(herdr.Protocol - 1)
	cmd, out := startServe(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path})
	want := "herdr fake speaks protocol " + strconv.Itoa(herdr.Protocol-1) +
		", obor speaks " + strconv.Itoa(herdr.Protocol) + ": update obor or herdr so they match"
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), want) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), want) {
		t.Fatalf("serve did not print the mismatched snapshot before dispatch:\n%s", out.String())
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// T-522: a project whose serve socket path is over the unix limit fails at serve start, naming the path and the
// limit; nothing is left behind.
func TestServeSocketPathTooLong(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	deep := filepath.Join(p.Base, strings.Repeat("d", 60))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(deep, "proj")
	if err := os.Rename(p.Root, long); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(long, ".obor", "state", "serve.sock")
	if len(sock) <= herdr.MaxSocketPath {
		t.Fatalf("precondition: %s is %d bytes, not over %d", sock, len(sock), herdr.MaxSocketPath)
	}
	rc, _, stderr := run(t, long, nil, "serve")
	if rc != 1 || !strings.Contains(stderr, "FAIL obor serve: ") || !strings.Contains(stderr, sock) ||
		!strings.Contains(stderr, strconv.Itoa(herdr.MaxSocketPath)) {
		t.Errorf("obor serve with a long socket path: exit %d, %q; want 1 naming %s and %d", rc, stderr, sock, herdr.MaxSocketPath)
	}
	if _, err := os.Stat(filepath.Join(long, ".obor", "state", "serve.flock")); err == nil {
		t.Error("serve.flock left by the refused start")
	}
}

// T-336, T-437: the lane table holds a lane's task open while its branch is not merged: working until its task_done is
// in the event log, done after; it names the lane's worker and type.
func TestLaneTable(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(lane)
	p.State("L-01", map[string]any{"lane": "L-01", "task": "T-01", "branch": "task/T-01", "agent": "pj-l-01",
		"dispatched_at": time.Now().Unix() - 100})
	for _, c := range []struct {
		name, state string
		prep        func()
	}{
		{"dispatched", "working", func() { p.Dispatched("L-01") }},
		{"task_done in", "done", func() { p.Submit("L-01") }},
		{"dispatched anew", "working", func() { p.Dispatched("L-01") }},
	} {
		c.prep()
		ls, err := checks.Lanes{}.Lanes(p.Root)
		if err != nil || len(ls) != 1 {
			t.Fatalf("%s: lanes %+v, %v", c.name, ls, err)
		}
		if l := ls[0]; l.Task != "T-01" || l.Agent != "pj-l-01" || l.State != c.state {
			t.Errorf("%s: %+v; want T-01 of pj-l-01, %s", c.name, l, c.state)
		}
	}
}

// T-377: a config key this binary does not know (the config is newer than the running serve) does not stop serve:
// it loads the project, reads its lanes, warns once for the key however many times the config is read, while
// `obor config validate` still refuses it.
func TestServeToleratesUnknownConfigKey(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	p.Config(testkit.Set("merge.from_a_newer_obor", []string{"x"}))
	p.Commit(lane)
	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	w := f.Agent("w1:p1", "pj-l-01", "working", lane)
	w.Workspace, w.Label = "w1", "pj-l-01" // labelled by its launch: serve's identity of it
	srv := herdrtest.Serve(t, f)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path}

	if rc, _, stderr := run(t, p.Root, env, "config", "validate"); rc == 0 || !strings.Contains(stderr, "unknown field") {
		t.Fatalf("precondition: config validate exit %d, %q; want it to refuse the unknown key", rc, stderr)
	}
	cmd, out := startServe(t, p.Root, env)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		ss, err := wire.Ask(context.Background(), wire.SocketPath(p.Root)) // the CLI's own config read is strict
		if err == nil && ss.Project != nil && len(ss.Project.Agents) == 1 &&
			ss.Project.Agents[0].Name == "pj-l-01" && ss.Project.Agents[0].Task == "T-01" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never listed the lane with an unknown config key: %+v, %v; output:\n%s", ss, err, out.String())
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	if n := strings.Count(out.String(), `"merge.from_a_newer_obor"`); n != 1 || !strings.Contains(out.String(), "restart serve") {
		t.Errorf("warnings for the key: %d, want 1 naming the restart:\n%s", n, out.String())
	}
}

// T-435: `obor serve dump` prints the event log of the project around cwd as JSONL, read-only (no serve runs); without a
// database it fails, printing nothing.
func TestServeDump(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	var out bytes.Buffer
	if code := serveDump(&out, p.Root); code != 1 || out.Len() != 0 {
		t.Fatalf("dump without a database = %d, %q; want 1 and nothing", code, out.String())
	}
	db, err := store.OpenRW(event.DBFile(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err == nil {
		_, err = log.Append(event.Record{Task: "T-1", Attempt: 1, Step: "submitted", Kind: event.KindFact,
			Data: []byte(`{"lane":"L-01"}`)}, time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"seq":1,"ts":"2026-10-27T09:00:00Z","task":"T-1","attempt":1,"step":"submitted","kind":"fact","data":{"lane":"L-01"}}` + "\n"
	for _, cwd := range []string{p.Root, filepath.Join(p.Root, ".obor")} {
		out.Reset()
		if code := serveDump(&out, cwd); code != 0 || out.String() != want {
			t.Errorf("dump in %s = %d, %q; want 0, %q", cwd, code, out.String(), want)
		}
	}
}
