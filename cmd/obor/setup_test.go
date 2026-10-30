package main

import (
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// T-536: setup talks to herdr over its socket (HERDR_SOCKET_PATH). The run's PATH has no herdr on it: only the fake
// socket answers, and every call is its answer.
func TestSetupPaneUsesTheHerdrSocket(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	x := herdrtest.New()
	x.Tab("w1:t1", 200, 50, "w1:p1")
	srv := herdrtest.Serve(t, x)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "PATH=/usr/bin:/bin"}
	if rc, stdout, stderr := run(t, p.Root, env, "setup", "lane", "O-01", "opus"); rc != 0 {
		t.Fatalf("setup lane: exit %d\n%s%s", rc, stdout, stderr)
	}
	rc, stdout, stderr := run(t, p.Root, env, "setup", "pane", "O-01", "--tab", "w1:t1")
	if rc != 0 || !strings.Contains(stdout, "OK O-01: pj-o-01 in pane w1:n1") {
		t.Fatalf("setup pane: exit %d\n%s%s", rc, stdout, stderr)
	}
	pane := x.Pane("w1:n1")
	if pane == nil || pane.Kind != "claude" || pane.Name != "pj-o-01" {
		t.Errorf("the socket's herdr started the agent: %+v", pane)
	}
	var methods []string
	for _, req := range srv.Requests() {
		methods = append(methods, req.Method)
	}
	for _, want := range []string{"agent.list", "pane.list", "pane.layout", "pane.split", "agent.start"} {
		if !strings.Contains(strings.Join(methods, " "), want) {
			t.Errorf("no %s on the socket: %v", want, methods)
		}
	}
}
