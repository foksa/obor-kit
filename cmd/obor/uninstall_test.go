package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// The branches uninstall deletes are the structured reply's merged list, with their tips: no line of text is read for
// them (the reply here carries branch-looking lines that are no data), and a reply that is not structured is an error.
func TestMergedBranchesReadsTheStructuredReply(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	reply := `{"merged":[{"name":"task/T-1","sha":"` + sha + `"},{"name":"task/T-22","sha":"` + sha + `"}],` +
		`"unmerged":[{"name":"task/T-3","sha":"` + sha + `"}]}`
	got, err := mergedBranches(reply)
	if err != nil || len(got) != 2 || got[0] != (port.PruneBranch{Name: "task/T-1", SHA: sha}) || got[1].Name != "task/T-22" {
		t.Errorf("merged %v, %v, want task/T-1 and task/T-22 with their tips (not the unmerged task/T-3)", got, err)
	}
	if got, err := mergedBranches(`{"merged":[],"unmerged":[]}`); err != nil || len(got) != 0 {
		t.Errorf("an empty reply: %v, %v", got, err)
	}
	text := "would delete task/T-1 (was abc1234)\nPRUNE would delete 1 branches, 0 worktrees"
	if got, err := mergedBranches(text); err == nil || len(got) != 0 || !strings.Contains(err.Error(), "not structured") {
		t.Errorf("a text reply: %v, %v, want an error and no branch", got, err)
	}
}

// serve running in a pane is asked to exit and confirmed down; a second call finds no serve and no agent: already.
func TestServeDownEndsAPaneServeThenIsAlready(t *testing.T) {
	_, s, _, _, f := sleepFx(t)
	paneServe(t, s, f, true)
	state, line, err := serveDown(context.Background(), s, false)
	if err != nil || state != "done" || !strings.Contains(line, "EXITING") {
		t.Fatalf("first run: %q, %q, %v", state, line, err)
	}
	if strings.Join(f.calls, "|") != "exit" {
		t.Errorf("calls %q, want the exit only (a pause would outlive the uninstall)", f.calls)
	}
	f.up = false
	state, line, err = serveDown(context.Background(), s, false)
	if err != nil || state != "already" {
		t.Errorf("second run: %q, %q, %v, want already", state, line, err)
	}
}

// A dry run says what it would do and sends nothing.
func TestServeDownDryRunSendsNothing(t *testing.T) {
	_, s, _, _, f := sleepFx(t)
	paneServe(t, s, f, true)
	state, line, err := serveDown(context.Background(), s, true)
	if err != nil || state != "done" || !strings.Contains(line, "would stop serve") || len(f.calls) != 0 {
		t.Errorf("dry run: %q, %q, %v, calls %q", state, line, err, f.calls)
	}
}

// obor.web goes with the machine's last serve agent only: another project's serve plist keeps it.
func TestServeDownRemovesTheWebAgentWithTheLastServe(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd's agents: darwin only")
	}
	for _, other := range []bool{false, true} {
		_, s, _, _, f := sleepFx(t)
		f.up = false
		fakeLaunchctl(t)
		svc := serviceOf(s.Cfg, s.Env)
		agents := filepath.Dir(svc.PlistPath())
		web := webOf(s.Cfg, s.Env)
		if err := os.MkdirAll(agents, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string][]byte{svc.PlistPath(): svc.Plist(), web.PlistPath(): web.Plist()}
		if other {
			files[filepath.Join(agents, "obor.serve.other.plist")] = []byte("<plist/>")
		}
		for p, b := range files {
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		state, line, err := serveDown(context.Background(), s, false)
		if err != nil || state != "done" {
			t.Fatalf("other serve %v: %q, %q, %v", other, state, line, err)
		}
		if _, err := os.Stat(svc.PlistPath()); !os.IsNotExist(err) {
			t.Errorf("other serve %v: the project's agent stays: %v", other, err)
		}
		_, err = os.Stat(web.PlistPath())
		if gone := os.IsNotExist(err); gone == other {
			t.Errorf("other serve %v: obor.web gone = %v, want gone only with the last serve", other, gone)
		}
	}
}
