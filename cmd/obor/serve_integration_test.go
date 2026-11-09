//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/service"
	"obor/internal/testkit"
)

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
	if !strings.Contains(out.String(), "herdr: fake protocol "+strconv.Itoa(herdr.Protocol)+"\n") {
		t.Errorf("serve did not report its herdr snapshot before running:\n%s", out.String())
	}
	// T-646: serve registers the project it serves in the machine's list of obor projects by name.
	if b, err := os.ReadFile(filepath.Join(home, "projects.json")); err != nil ||
		!strings.Contains(string(b), `"proj": "`+p.Root+`"`) {
		t.Errorf("the machine's project list: %v, %q; want proj at the served root", err, b)
	}
	// T-864: obor status --all is gone (T-522: a serve holds one project): a usage error.
	if rc, _, stderr := run(t, p.Root, env, "status", "--all", "--facts"); rc != 2 ||
		!strings.Contains(stderr, "usage: obor status") {
		t.Errorf("obor status --all --facts: exit %d, %q; want 2 and the usage", rc, stderr)
	}
	// obor status --facts: the running agent
	if rc, stdout, stderr := run(t, p.Root, env, "status", "--facts"); rc != 0 ||
		!strings.Contains(stdout, "agent: pj-l-01 (working)") {
		t.Errorf("obor status --facts: exit %d, %q, %q; want the project's status", rc, stdout, stderr)
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

// T-1100: serve's own start rotates its serve.log — a log over service.MaxLog is copied to serve.log.1 (replacing an
// old one) and truncated in place — so a new build's re-exec keeps the log bounded like an install or restart does.
func TestServeStartRotatesLog(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	f := herdrtest.New()
	srv := herdrtest.Serve(t, f)
	log := service.ServeLog(p.Root)
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Create(log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("old log\n"); err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(service.MaxLog + 1); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log + ".1"); !os.IsNotExist(err) {
		t.Fatalf("precondition: no serve.log.1: %v", err)
	}
	cmd, out := startServe(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path})
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "obor serve: running") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "obor serve: running") {
		t.Fatalf("serve did not start:\n%s", out.String())
	}
	if fi, err := os.Stat(log + ".1"); err != nil || fi.Size() != service.MaxLog+1 {
		t.Errorf("serve.log.1: %v %v; want the old %d bytes", fi, err, service.MaxLog+1)
	}
	if b, err := os.ReadFile(log + ".1"); err != nil || !strings.HasPrefix(string(b), "old log\n") {
		t.Errorf("serve.log.1: %v %q; want the old log first", err, b)
	}
	if fi, err := os.Stat(log); err != nil || fi.Size() != 0 {
		t.Errorf("serve.log: %v %v; want an empty file", fi, err)
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

	if rc, stdout, _ := run(t, p.Root, env, "config", "validate"); rc == 0 || !strings.Contains(stdout, "FAIL .merge.from_a_newer_obor: unknown to this obor") {
		t.Fatalf("precondition: config validate exit %d, %q; want it to refuse the unknown key", rc, stdout)
	}
	cmd, out := startServe(t, p.Root, env)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		ss, err := wire.Ask(context.Background(), wire.SocketPath(p.Root))
		if err == nil && ss.Project != nil && len(ss.Project.Agents) == 1 &&
			ss.Project.Agents[0].Name == "pj-l-01" && ss.Project.Agents[0].Lane == "L-01" { // T-1012: a lane serve never dispatched holds no task
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never listed the lane with an unknown config key: %+v, %v; output:\n%s", ss, err, out.String())
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	if n := strings.Count(out.String(), `"merge.from_a_newer_obor"`); n != 1 ||
		!strings.Contains(out.String(), "older than the config (restart serve)") ||
		!strings.Contains(out.String(), "key was retired (obor config validate names it; delete it)") {
		t.Errorf("warnings for the key: %d, want 1 naming both causes:\n%s", n, out.String())
	}
}

// T-1076: the promotion is deleted; a config that still sets its keys (branches.promote, merge.promote_weight,
// merge.weights) is a config with unknown keys: serve warns once per key and its loop runs on (it lists the lane), no
// error; the merge loop builds its workflow from it.
func TestServeRunsWithARemovedPromotionKey(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	keys := []string{"branches.promote", "merge.promote_weight", "merge.weights"}
	p.Config(testkit.Set("roles.integrator", ""), // an integrator configured: the merge loop runs (servedGitProject's)
		testkit.Set(keys[0], "prod"), testkit.Set(keys[1], 4), testkit.Set(keys[2], map[string]any{"hard": 5}))
	p.Commit(lane)
	if _, unknown, err := config.LoadHook(p.Root); err != nil || len(unknown) != len(keys) {
		t.Fatalf("precondition: the config names the removed keys: unknown %q, %v; want %q", unknown, err, keys)
	}
	l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})(p.Root)
	if err != nil || l.Workflow == nil {
		t.Fatalf("the merge loop with a removed promotion key: %+v, %v; want it built with its workflow", l, err)
	}
	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	w := f.Agent("w1:p1", "pj-l-01", "working", lane)
	w.Workspace, w.Label = "w1", "pj-l-01"
	srv := herdrtest.Serve(t, f)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path}
	cmd, out := startServe(t, p.Root, env)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		ss, err := wire.Ask(context.Background(), wire.SocketPath(p.Root))
		if err == nil && ss.Project != nil && ss.Project.Merging && len(ss.Project.Agents) == 1 &&
			ss.Project.Agents[0].Lane == "L-01" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never ran its loop with a removed promotion key: %+v, %v; output:\n%s", ss, err, out.String())
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	// branches.promote is warned of as branches: shape 1's branches, left with promote alone (T-1124)
	for _, k := range []string{"branches", keys[1], keys[2]} {
		if n := strings.Count(out.String(), `unknown key "`+k+`" ignored`); n != 1 {
			t.Errorf("warnings for %s: %d, want 1:\n%s", k, n, out.String())
		}
	}
}

// T-781: serve's merge loop (mergeLoop, built on every pass) has its nightly from merge.nightly_at as the config is
// now: on and off, committed by serve's config writer (config_set's), count from the next build, no restart.
func TestMergeLoopNightlyFollowsTheConfig(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
	p.Git(p.Root, "config", "user.name", "Owner")
	p.Git(p.Root, "config", "user.email", "owner@example.com")
	build := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})
	at := func() string {
		t.Helper()
		l, err := build(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		if l.Nightly == nil {
			t.Fatalf("the merge loop has no nightly: off is At \"\" (T-782)")
		}
		if l.Nightly.Run == nil || l.Nightly.Last == nil {
			t.Errorf("nightly at %s without Run or Last", l.Nightly.At)
		}
		if l.Nightly.At == "" {
			return "off"
		}
		return l.Nightly.At
	}
	set := func(val string) {
		t.Helper()
		l, err := build(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		doc, refused, err := l.Config.Read(context.Background())
		if err != nil || refused != "" {
			t.Fatalf("precondition: the config is writable: %q, %v", refused, err)
		}
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := cfg.Edit(doc, "merge.nightly_at", json.RawMessage(val))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Config.Commit(context.Background(), ch.Doc, "config: merge.nightly_at"); err != nil {
			t.Fatal(err)
		}
	}
	if got := at(); got != "off" {
		t.Fatalf("precondition: merge.nightly_at unset: nightly %s, want off", got)
	}
	set(`"03:00"`)
	if got := at(); got != "03:00" {
		t.Errorf("after on: nightly %s, want 03:00", got)
	}
	set(`null`)
	if got := at(); got != "off" {
		t.Errorf("after off: nightly %s, want off", got)
	}
}
