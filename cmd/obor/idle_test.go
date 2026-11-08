package main

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-803: obor idle ["<why>"] is one request to obor serve as the caller: with a text it acknowledges the idle state,
// with none it asks for it; serve's reply is printed. A usage error sends nothing, serve's refusal is FAIL.
func TestIdleRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "IDLE: nothing to dispatch", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead"}
	for _, args := range [][]string{{"idle", "a", "b"}, {"idle", "  "}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor idle") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}

	if rc, stdout, _ := run(t, p.Root, env, "idle"); rc != 0 || stdout != "IDLE: nothing to dispatch\n" {
		t.Errorf("obor idle: exit %d, %q", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env, "idle", "nothing to dispatch"); rc != 0 ||
		stdout != "IDLE: nothing to dispatch\n" {
		t.Errorf("obor idle <why>: exit %d, %q", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 2 {
		t.Fatalf("%d requests, want 2", len(calls))
	}
	for i, want := range []sentCall{{}, {Note: "nothing to dispatch"}} {
		if c := calls[i]; c.Note != want.Note || c.Role != "lead" || c.Root != p.Root || c.CallID == "" {
			t.Errorf("request %d: %+v, want note %q", i+1, c, want.Note)
		}
	}
	if calls[0].CallID == calls[1].CallID {
		t.Error("the two requests share a call id")
	}

	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "",
		"idle: the lead or the owner answers it, not pj-l-01 (role worker)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "idle", "why"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL idle: the lead or the owner answers it") {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
	refusing.stop()
}

// T-803: obor status shows the idle state: unacknowledged with when it began, or the acknowledgement's why.
func TestStatusShowsIdle(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	at := time.Date(2026, 11, 4, 9, 5, 0, 0, time.UTC)
	ss := wire.Status{Project: &wire.ProjectStatus{Name: "proj", Root: p.Root, Merging: true,
		Idle: &wire.IdleStatus{Since: at.Unix(), Last: "T-1 merged abc1234"}}}
	_, text := statusOf(t, p, ss, nil, at.Add(time.Hour))
	matchLine(t, text, `^idle, not acknowledged since `+at.Local().Format("Jan 2 15:04")+`$`, "the unacknowledged idle state")

	ss.Project.Idle = &wire.IdleStatus{Since: at.Unix(), Last: "T-1 merged abc1234", Acked: true, Why: "nothing to dispatch"}
	_, text = statusOf(t, p, ss, nil, at.Add(time.Hour))
	matchLine(t, text, `^idle: nothing to dispatch$`, "the acknowledged idle state")
	if strings.Contains(text, "not acknowledged") {
		t.Errorf("an acknowledged idle state shows as unacknowledged:\n%s", text)
	}
}
