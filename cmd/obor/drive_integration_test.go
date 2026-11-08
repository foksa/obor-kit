//go:build integration

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
)

// obor land (T-751) is one socket request after two checks of its own: the task is a driving one in serve's status
// (by id, or by the drive's name in its stored spec), and its lane worktree is clean. A dirty worktree is refused with
// its paths and nothing is sent (land commits nothing); no summary: the branch's commit subjects since main; none:
// refused. Serve's reply is printed.
func TestLandRequest(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
	p.Spec("T-01", "# T-01 — Owner drive: web page\n")
	lane := p.LaneDir("L-01")
	st := wire.Status{Connected: true, Project: &wire.ProjectStatus{Root: p.Root, Merging: true,
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", State: wire.TaskDriving}, {Task: "T-02", Lane: "L-02", State: "working"}}}}
	home, serve := startFakeServe(t, p.Root, st, "LANDED T-01: submitted (gate, review and merge follow)", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	lands := func() []sentCall {
		calls, _ := serve.requests()
		return calls
	}

	p.Write(filepath.Join(lane, "page.html"), "<p>draft</p>\n")
	rc, stdout, _ := run(t, p.Root, env, "land", "T-01")
	if rc != 1 || !strings.Contains(stdout, "FAIL lane L-01 has uncommitted changes") || !strings.Contains(stdout, "page.html") {
		t.Errorf("land with a dirty worktree: rc %d, %q; want FAIL naming page.html", rc, stdout)
	}
	if n := len(lands()); n != 0 {
		t.Fatalf("a refused land sent %d requests", n)
	}

	p.Git(lane, "add", "-A")
	p.Git(lane, "commit", "-qm", "page draft")
	p.Write(filepath.Join(lane, "page.html"), "<p>final</p>\n")
	p.Git(lane, "commit", "-qam", "page final")
	rc, stdout, _ = run(t, p.Root, env, "land", "T-01")
	calls := lands()
	if rc != 0 || !strings.Contains(stdout, "LANDED T-01") || len(calls) != 1 {
		t.Fatalf("land: rc %d, %q, %d calls", rc, stdout, len(calls))
	}
	if c := calls[0]; c.Task != "T-01" || c.Report != "page draft\npage final" || c.CallID == "" {
		t.Errorf("land's call %+v: want T-01 with the branch's commit subjects as its report", c)
	}

	rc, stdout, _ = run(t, p.Root, env, "land", "web page", "the new page")
	calls = lands()
	if rc != 0 || len(calls) != 2 || calls[1].Task != "T-01" || calls[1].Report != "the new page" {
		t.Errorf("land by name with a summary: rc %d, %q, %+v", rc, stdout, calls)
	}

	for _, args := range [][]string{{"land", "T-02"}, {"land", "nothing"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL no open drive "+args[1]) {
			t.Errorf("%q: rc %d, %q; want FAIL no open drive", args, rc, stdout)
		}
	}
	for _, args := range [][]string{{"land"}, {"land", "T-01", "a", "b"}, {"land", "--x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL usage: obor land") {
			t.Errorf("%q: rc %d, %q; want usage", args, rc, stdout)
		}
	}
	if n := len(lands()); n != 2 {
		t.Errorf("refusals sent requests: %d calls", n)
	}
}
