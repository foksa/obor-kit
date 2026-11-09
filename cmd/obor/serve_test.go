package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
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

func TestServeReportsHerdrProtocolMismatch(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
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
	p := testkit.NewPlain(t)
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

// T-435: `obor serve dump` prints the event log of the project around cwd as JSONL, read-only (no serve runs); without a
// database it fails, printing nothing.
func TestServeDump(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
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

// A herdr on a newer protocol than obor's is served, not refused: serve's log names its version and protocol once,
// and obor status (text and JSON) carries the same note. The note follows the server serve is connected to (T-806):
// herdr coming up after serve, and herdr restarted on another protocol, change it; a restart as the same one does not
// log again.
func TestServeAcceptsNewerHerdrProtocol(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	f := herdrtest.New()
	srv := herdrtest.Serve(t, f)
	srv.Die()
	env := []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path}
	cmd, out := startServe(t, p.Root, env)
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	note := func(n int) string {
		return "herdr fake, protocol " + strconv.Itoa(n) + " (newer than obor's " + strconv.Itoa(herdr.Protocol) + ")"
	}
	waitNote := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			rc, stdout, _ := run(t, p.Root, env, "status")
			rcj, js, _ := run(t, p.Root, env, "status", "--json")
			if rc == 0 && rcj == 0 && strings.Contains(stdout, want) && strings.Contains(js, `"herdr_note": "`+want+`"`) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("obor status never carried %q in text and JSON: exit %d %q; json %q; serve output:\n%s",
					want, rc, stdout, js, out.String())
			}
		}
	}
	line := func(n int) string { return "herdr: fake protocol " + strconv.Itoa(n) + "\n" }

	// Serve started with herdr down; herdr comes up newer.
	srv.Protocol(herdr.Protocol + 1)
	srv.Revive(t)
	waitNote(note(herdr.Protocol + 1))
	if n := strings.Count(out.String(), line(herdr.Protocol+1)); n != 1 {
		t.Errorf("serve's log names the newer herdr %d times, want once:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), "update obor or herdr") {
		t.Errorf("serve refused a newer protocol:\n%s", out.String())
	}
	// The same version again: no second line.
	srv.DropConns()
	if !waitFor(func() bool { return strings.Count(out.String(), "herdr: connected") >= 2 }) {
		t.Fatalf("serve never connected again:\n%s", out.String())
	}
	if n := strings.Count(out.String(), line(herdr.Protocol+1)); n != 1 {
		t.Errorf("a reconnect to the same herdr logged it %d times in all, want 1:\n%s", n, out.String())
	}
	// Another protocol: the note changes, one more line.
	srv.Protocol(herdr.Protocol + 2)
	srv.DropConns()
	waitNote(note(herdr.Protocol + 2))
	if n := strings.Count(out.String(), line(herdr.Protocol+2)); n != 1 {
		t.Errorf("serve's log names the upgraded herdr %d times, want once:\n%s", n, out.String())
	}
}

// serve's merge loop gets the workflow workflow.kind selects (T-1029: scratch-ff, the default) and the dispatch's spec
// reader and kit; a kind serve has no implementation of fails the loop's construction.
func TestMergeLoopWorkflowWiring(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	for _, kind := range []string{"", "scratch-ff"} {
		if kind != "" {
			p.Config(testkit.Set("merge.workflow", kind))
		}
		l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})(p.Root)
		if err != nil {
			t.Fatalf("kind %q: %v", kind, err)
		}
		wf, ok := l.Workflow.(checks.ScratchFF)
		if !ok || wf.Review == nil || l.Specs == nil || l.Kit == "" {
			t.Errorf("kind %q: Workflow %T, Specs %v, Kit %q; want scratch-ff with its review git", kind, l.Workflow, l.Specs, l.Kit)
		}
	}
	p.Config(testkit.Set("merge.workflow", "x"))
	if _, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})(p.Root); err == nil ||
		!strings.Contains(err.Error(), "serve knows scratch-ff") {
		t.Errorf("kind x: %v; want refused naming the known kinds", err)
	}
}
