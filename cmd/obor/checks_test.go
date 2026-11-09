package main

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/testkit"
)

// T-781: obor nightly on HH:MM | off is one config_set request to obor serve as the caller: nightly.at set to the
// time (a JSON string) or null (T-1126: the key's shape-2 name). Serve's SET is NIGHTLY ON at HH:MM / NIGHTLY OFF,
// its UNCHANGED the same "already"; its refusal (a bad time, a lane role) is FAIL, exit 1. A usage error sends nothing
// and leaves the config as it was.
func TestNightlySwitch(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	before := p.Read(".obor/config.json")
	home, serve := startFakeServe(t, p.Root, wire.Status{}, `SET nightly.at unset → "03:00"`, "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"nightly", "on"}, {"nightly", "on", "03:00", "x"}, {"nightly", "off", "03:00"},
		{"nightly", "now"}, {"nightly", "03:00"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || stdout != "FAIL "+help.Usage("nightly")+"\n" {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
	if got := p.Read(".obor/config.json"); got != before {
		t.Errorf("a usage error changed the config:\n%s", got)
	}

	if rc, stdout, _ := run(t, p.Root, env, "nightly", "on", "03:00"); rc != 0 || stdout != "NIGHTLY ON at 03:00\n" {
		t.Errorf("on: exit %d, %q; want NIGHTLY ON at 03:00", rc, stdout)
	}
	serve2 := startFakeServeAt(t, wire.SocketPath(p.Root), wire.Status{}, `SET nightly.at "03:00" → unset`, "")
	if rc, stdout, _ := run(t, p.Root, env, "nightly", "off"); rc != 0 || stdout != "NIGHTLY OFF\n" {
		t.Errorf("off: exit %d, %q; want NIGHTLY OFF", rc, stdout)
	}
	calls, _ := serve.requests()
	calls2, _ := serve2.requests()
	calls = append(calls, calls2...)
	if len(calls) != 2 {
		t.Fatalf("%d requests, want 2", len(calls))
	}
	serve.mu.Lock()
	method := serve.methods[0]
	serve.mu.Unlock()
	if method != wire.MethodConfig {
		t.Errorf("method %q, want %q", method, wire.MethodConfig)
	}
	for i, want := range []string{`"03:00"`, `null`} {
		c := calls[i]
		if len(c.Changes) != 1 || c.Changes[0].Key != "nightly.at" || string(c.Changes[0].Value) != want ||
			c.Role != "lead" || c.Agent != "pj-lead" || c.Root != p.Root || c.CallID == "" {
			t.Errorf("request %d: %+v; want nightly.at = %s as the lead", i+1, c, want)
		}
	}

	startFakeServeAt(t, wire.SocketPath(p.Root), wire.Status{}, "UNCHANGED nightly.at", "")
	if rc, stdout, _ := run(t, p.Root, env, "nightly", "off"); rc != 0 || stdout != "NIGHTLY OFF already\n" {
		t.Errorf("off when off: exit %d, %q; want NIGHTLY OFF already", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env, "nightly", "on", "03:00"); rc != 0 || stdout != "NIGHTLY ON at 03:00 already\n" {
		t.Errorf("on when on: exit %d, %q; want NIGHTLY ON at 03:00 already", rc, stdout)
	}

	for _, refusal := range []string{"config: nightly.at takes a local time HH:MM (00:00 to 23:59), or null (off)",
		"config: the owner or the lead changes the setup, not pj-l-01 (role worker)"} {
		refusing := startFakeServeAt(t, wire.SocketPath(p.Root), wire.Status{}, "", refusal)
		if rc, stdout, _ := run(t, p.Root, env, "nightly", "on", "25:00"); rc != 1 || stdout != "FAIL "+refusal+"\n" {
			t.Errorf("serve's refusal: exit %d, %q; want FAIL %s", rc, stdout, refusal)
		}
		refusing.stop()
	}
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + p.Base + "/none", callDown}, "nightly", "off"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
	if got := p.Read(".obor/config.json"); got != before {
		t.Errorf("the CLI wrote the config itself (serve is its one writer):\n%s", got)
	}
}
