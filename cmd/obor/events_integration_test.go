//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// T-1039: obor events, against a real obor serve (fake herdr): the JSON lines of the log from the first record, in
// seq order, a task's carrying its spec's ref; --after N from N+1; a cursor past the end prints nothing, exit 0; the
// text form leads with the seq; --follow prints a record appended after it started and ends on TERM, exit 0; with
// serve down the same records come from the store. A session with OBOR_ROLE=tracker is the custom role: spec_set
// admitted by its verbs, dispatch refused naming the verb and its key.
func TestEventsCommand(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("roles.tracker", `{"verbs": ["spec_set"]}`))
	p.Write("docs/agents/tracker.md", "# Role: tracker\n")
	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	srv := herdrtest.Serve(t, f)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path}
	cmd, serveOut := startServe(t, p.Root, env)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(wire.SocketPath(p.Root)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never bound its socket:\n%s", serveOut.String())
		}
	}
	spec := func(task, ref string) string {
		return p.Write(".obor/drafts/"+task+".md", "# "+task+" — Ticket work\n\nDifficulty: medium\nRef: "+ref+"\n")
	}
	tracker := append(slices.Clone(env), "OBOR_ROLE=tracker")
	if rc, stdout, stderr := run(t, p.Root, tracker, "spec", "set", "T-01", spec("T-01", "TICKET-1")); rc != 0 ||
		stdout != "SET T-01 rev 1\n" {
		t.Fatalf("the tracker's spec set: exit %d, %q, %q; want SET (its verb)", rc, stdout, stderr)
	}
	want := "dispatch: the lead or the owner dispatches, not pj-tracker (role tracker): role tracker has no verb dispatch (add it to roles.tracker.verbs)"
	if rc, stdout, _ := run(t, p.Root, tracker, "dispatch", "T-01"); rc != 1 || !strings.Contains(stdout, want) {
		t.Errorf("the tracker's dispatch: exit %d, %q; want FAIL %q", rc, stdout, want)
	}
	if rc, stdout, stderr := run(t, p.Root, env, "spec", "set", "T-02", spec("T-02", "TICKET-2")); rc != 0 {
		t.Fatalf("the owner's spec set: exit %d, %q, %q", rc, stdout, stderr)
	}

	up := eventLines(t, p.Root, env)
	if len(up) < 2 {
		t.Fatalf("precondition: %d records, want the two spec sets at least", len(up))
	}
	refs := map[string]string{}
	for i, r := range up {
		if r.Seq != int64(i+1) {
			t.Fatalf("obor events --json: line %d has seq %d, want %d (in order, from the first)", i, r.Seq, i+1)
		}
		refs[r.Task] = r.Ref
	}
	if refs["T-01"] != "TICKET-1" || refs["T-02"] != "TICKET-2" {
		t.Errorf("refs by task %v; want T-01 TICKET-1, T-02 TICKET-2", refs)
	}
	last := up[len(up)-1].Seq
	if got := eventLines(t, p.Root, env, "--after", "1"); len(got) != len(up)-1 || got[0].Seq != 2 {
		t.Errorf("--after 1: %d lines from %+v; want %d from seq 2", len(got), got, len(up)-1)
	}
	if rc, stdout, stderr := run(t, p.Root, env, "events", "--after", strconv.FormatInt(last+50, 10)); rc != 0 || stdout != "" {
		t.Errorf("a cursor past the end: exit %d, %q, %q; want nothing, exit 0", rc, stdout, stderr)
	}
	if rc, stdout, _ := run(t, p.Root, env, "events", "--after", strconv.FormatInt(last-1, 10)); rc != 0 ||
		!strings.HasPrefix(stdout, strconv.FormatInt(last, 10)+" ") || strings.Count(stdout, "\n") != 1 {
		t.Errorf("the text form after %d: exit %d, %q; want one line leading with seq %d", last-1, rc, stdout, last)
	}
	for _, bad := range [][]string{{"--after"}, {"--after", "-1"}, {"--after", "x"}, {"--json", "--json"}, {"nope"}} {
		if rc, _, stderr := run(t, p.Root, env, append([]string{"events"}, bad...)...); rc != 2 ||
			!strings.Contains(stderr, "usage: obor events") {
			t.Errorf("obor events %q: exit %d, %q; want the usage, exit 2", bad, rc, stderr)
		}
	}

	// --follow from the end: the next record, as serve writes it.
	follow := exec.Command(oborBin, "events", "--follow", "--json", "--after", strconv.FormatInt(last, 10))
	follow.Dir, follow.Env = p.Root, append(os.Environ(), env...)
	pipe, err := follow.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := follow.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = follow.Process.Kill() })
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	time.Sleep(300 * time.Millisecond) // connected and waiting
	if rc, stdout, stderr := run(t, p.Root, env, "spec", "set", "T-03", spec("T-03", "TICKET-3")); rc != 0 {
		t.Fatalf("spec set T-03: exit %d, %q, %q", rc, stdout, stderr)
	}
	select {
	case line := <-lines:
		var r wire.FeedRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.Seq != last+1 || r.Task != "T-03" || r.Ref != "TICKET-3" {
			t.Errorf("--follow: %q (%v); want seq %d of T-03, ref TICKET-3", line, err, last+1)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--follow: no line for the record appended after it started")
	}
	_ = follow.Process.Signal(syscall.SIGTERM)
	if err := follow.Wait(); err != nil {
		t.Errorf("--follow on TERM: %v, want exit 0", err)
	}

	// Serve down: the store, the same records.
	up = eventLines(t, p.Root, env)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("serve on TERM: %v\n%s", err, serveOut.String())
	}
	if _, err := os.Stat(filepath.Join(p.Root, ".obor", "state", "serve.sock")); err == nil {
		t.Fatal("precondition: serve's socket left after TERM")
	}
	down := eventLines(t, p.Root, env)
	if len(down) < len(up) || !slices.EqualFunc(down[:len(up)], up, func(a, b wire.FeedRecord) bool {
		return a.Seq == b.Seq && a.Step == b.Step && a.Task == b.Task && a.Ref == b.Ref && string(a.Data) == string(b.Data)
	}) {
		t.Errorf("serve down: %d records, want serve's %d first (the same records)", len(down), len(up))
	}
}

// eventLines is obor events --json [args…] in root, each line decoded (fatal on an exit but 0 or a line that is no
// record).
func eventLines(t *testing.T, root string, env []string, args ...string) []wire.FeedRecord {
	t.Helper()
	rc, stdout, stderr := run(t, root, env, append([]string{"events", "--json"}, args...)...)
	if rc != 0 {
		t.Fatalf("obor events --json %q: exit %d, %q, %q", args, rc, stdout, stderr)
	}
	var out []wire.FeedRecord
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		var r wire.FeedRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("obor events --json: %q is no record: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}
