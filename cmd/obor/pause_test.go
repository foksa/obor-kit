package main

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-465: obor pause [--for <dur>] "<why>" | --clear is one pause request to obor serve as the caller (the lead, the
// owner with no role), serve's reply printed; a usage error or a bad duration sends nothing and writes no file; those,
// serve down or its refusal are FAIL on stdout, exit 1 (T-520).
func TestPauseRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "PAUSED until cleared", "")
	env := func(role string) []string {
		return []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=" + role, "OBOR_AGENT=pj-lead"}
	}
	for _, args := range [][]string{{"pause"}, {"pause", "--stat"}, {"pause", "--status"}, {"pause", "--for", "2h"},
		{"pause", "--for", "2h", "--stat"}, {"pause", "--for 2h"}, {"pause", ""}, {"pause", "a", "b"}} {
		if rc, stdout, _ := run(t, p.Root, env("lead"), args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor pause") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	for _, d := range []string{"tomorrow", "2days", "0s", "-1h"} {
		if rc, stdout, _ := run(t, p.Root, env("lead"), "pause", "--for", d, "later"); rc != 1 ||
			stdout != "FAIL bad duration: "+d+" (usage: obor pause [--model <name>] [--for <dur>] \"<why>\" | obor pause [--model <name>] --clear)\n" {
			t.Errorf("--for %s: exit %d, %q; want 1, bad duration", d, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}
	if p.Exists(".obor/pause") {
		t.Error("a .obor/pause file written")
	}

	before := time.Now()
	if rc, stdout, _ := run(t, p.Root, env("lead"), "pause", "a -- in the middle"); rc != 0 || stdout != "PAUSED until cleared\n" {
		t.Errorf("pause: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env(""), "pause", "--for", "90m", "-x is fine")
	run(t, p.Root, env("lead"), "pause", "--clear")
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	serve.mu.Lock()
	methods := append([]string(nil), serve.methods...)
	serve.mu.Unlock()
	for i, m := range methods {
		if m != wire.MethodPause {
			t.Errorf("request %d: method %q, want %q", i+1, m, wire.MethodPause)
		}
	}
	if c := calls[0]; c.Note != "a -- in the middle" || c.Until != 0 || c.Clear || c.Role != "lead" || c.Agent != "pj-lead" ||
		c.Root != p.Root || c.CallID == "" {
		t.Errorf("pause: %+v", c)
	}
	end := before.Add(90 * time.Minute).Unix()
	if c := calls[1]; c.Note != "-x is fine" || c.Until < end || c.Until > end+60 || c.Role != "" {
		t.Errorf("pause --for 90m as the owner: %+v (until want ~%d)", c, end)
	}
	if c := calls[2]; !c.Clear || c.Note != "" || c.Until != 0 {
		t.Errorf("pause --clear: %+v", c)
	}

	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "", "pause: the lead or the owner pauses, not pj-l1 (role worker)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "pause", "x"); rc != 1 ||
		stdout != "FAIL pause: the lead or the owner pauses, not pj-l1 (role worker)\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
	refusing.stop()
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + p.Base + "/none", callDown}, "pause", "x"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
}
