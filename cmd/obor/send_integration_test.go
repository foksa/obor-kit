//go:build integration

package main

import (
	"os"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// TestSendRequest's path outside an obor project: FAIL, exit 1, nothing sent. project.Find finds no .git above the
// path and asks git (rev-parse), so the case runs here.
func TestSendRequestOutsideAProject(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	_, own := startFakeServe(t, p.Root, wire.Status{}, "SENT (to the own project's serve)", "")
	home, err := os.MkdirTemp("", "hs") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	want := "FAIL " + p.Base + " is not in an obor project (no .obor/config.json)"
	if rc, stdout, _ := run(t, p.Root, env, "send", p.Base, "hi"); rc != 1 || strings.TrimRight(stdout, "\n") != want {
		t.Errorf("obor send %s: exit %d, %q; want 1, %q", p.Base, rc, stdout, want)
	}
	if calls, _ := own.requests(); len(calls) != 0 {
		t.Fatalf("a refused send sent %+v", calls)
	}
}
