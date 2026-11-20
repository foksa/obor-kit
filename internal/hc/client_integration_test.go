//go:build integration

package hc_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/hc"
)

// realCore starts an hc core for a scratch session (never the default one): HC_SESSION, XDG_STATE_HOME,
// XDG_CONFIG_HOME and XDG_RUNTIME_DIR in a fresh dir, HC_SOCKET unset, the core started from a scrubbed env (any hc
// command that needs a core starts one with the caller's env). It returns the session's env and an hc runner; the
// core is stopped at the end. Skipped when hc is not installed.
func realCore(t *testing.T) (env map[string]string, run func(args ...string) string) {
	t.Helper()
	bin, err := exec.LookPath("hc")
	if err != nil {
		t.Skip("hc is not installed")
	}
	// /tmp, not t.TempDir(): the socket path <dir>/run/herdr-core/<session>.sock must fit a unix socket's 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "obhc-")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"state", "config", "run", "work"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env = map[string]string{"HOME": os.Getenv("HOME"), "USER": os.Getenv("USER"), "LOGNAME": os.Getenv("LOGNAME"),
		"SHELL": "/bin/zsh", "TERM": "xterm-256color", "LANG": "en_US.UTF-8", "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
		"HC_SESSION": "obor-test-" + strconv.Itoa(os.Getpid()), "XDG_STATE_HOME": filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_RUNTIME_DIR": filepath.Join(dir, "run")}
	var list []string
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	run = func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env, cmd.Dir = list, filepath.Join(dir, "work")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hc %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	sock := filepath.Join(dir, "run", "herdr-core", env["HC_SESSION"]+".sock")
	t.Cleanup(func() {
		if c, err := net.Dial("unix", sock); err == nil { // stop only a core that runs: a stop with none starts one
			c.Close()
			run("server", "stop")
		}
		os.RemoveAll(dir)
	})
	run("server", "start")
	return env, run
}

// Against a real hc: the client finds the session's socket, the snapshot and the stream show a labelled kept shell
// terminal as a pane in its group, a relabel arrives as its event, and `hc server stop` ends the stream and every
// later call as a core that is not running.
func TestRealCore(t *testing.T) {
	env, run := realCore(t)
	ctx := context.Background()
	cl := &hc.Client{Path: hc.SocketPath(func(k string) (string, bool) { v, ok := env[k]; return v, ok })}
	if want := filepath.Join(env["XDG_RUNTIME_DIR"], "herdr-core", env["HC_SESSION"]+".sock"); cl.Path != want {
		t.Fatalf("SocketPath = %s, want %s", cl.Path, want)
	}
	var made struct {
		Address string `json:"address"`
	}
	out := run("run", "--bg", "--json", "--no-parent", "--keep", "--label", hc.LabelProject+"=itest", "--label", hc.LabelAgent+"=w1")
	if err := json.Unmarshal([]byte(out), &made); err != nil || made.Address == "" {
		t.Fatalf("hc run: %q, %v", out, err)
	}
	st, err := hc.Panes{C: cl}.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var found port.Pane
	for _, p := range st.Snapshot.Panes {
		if p.ID == made.Address {
			found = p
		}
	}
	if found.Group != "itest" || found.Label != "w1" || found.Cwd == "" || st.Snapshot.Version == "" ||
		!strings.Contains(st.Snapshot.Instance, ":") {
		t.Fatalf("snapshot = %+v; want %s in group itest labelled w1 with a cwd, a version and an instance", st.Snapshot, made.Address)
	}
	run("label", made.Address, hc.LabelAgent+"=w2")
	deadline := time.After(10 * time.Second)
	for relabelled := false; !relabelled; {
		select {
		case ev, ok := <-st.C:
			if !ok {
				t.Fatalf("the stream ended: %v", st.Err())
			}
			relabelled = ev.Kind == port.EventStatus && ev.Pane == made.Address && ev.New.Label == "w2" && ev.New.Group == "itest"
		case <-deadline:
			t.Fatal("no event with the new label in 10 s")
		}
	}
	if p, err := cl.Pane(ctx, made.Address); err != nil || p.Label != "w2" {
		t.Errorf("Pane = %+v, %v; want label w2", p, err)
	}
	if _, err := cl.Screen(ctx, made.Address, 5); err != nil {
		t.Errorf("Screen: %v", err)
	}
	run("server", "stop")
	deadline = time.After(10 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-st.C:
		case <-deadline:
			t.Fatal("the stream did not end in 10 s after the stop")
		}
	}
	if err := st.Err(); !port.NotRunning(err) || port.Refreshed(err) {
		t.Errorf("stream Err after the stop = %v, want a lost connection", err)
	}
	if _, err := cl.Pane(ctx, made.Address); !port.NotRunning(err) {
		t.Errorf("Pane after the stop = %v, want NotRunning", err)
	}
}
