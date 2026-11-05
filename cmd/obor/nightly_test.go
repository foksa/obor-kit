package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/herdr"
	"obor/internal/testkit"
)

// T-781: obor nightly on HH:MM | off is one config_set request to obor serve as the caller: merge.nightly_at set to the
// time (a JSON string) or null. Serve's SET is NIGHTLY ON at HH:MM / NIGHTLY OFF, its UNCHANGED the same "already";
// its refusal (a bad time, a lane role) is FAIL, exit 1. A usage error sends nothing and leaves the config as it was.
func TestNightlySwitch(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	before := p.Read(".obor/config.json")
	home, serve := startFakeServe(t, p.Root, wire.Status{}, `SET merge.nightly_at unset → "03:00"`, "")
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
	serve2 := startFakeServeAt(t, wire.SocketPath(p.Root), wire.Status{}, `SET merge.nightly_at "03:00" → unset`, "")
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
		if len(c.Changes) != 1 || c.Changes[0].Key != "merge.nightly_at" || string(c.Changes[0].Value) != want ||
			c.Role != "lead" || c.Agent != "pj-lead" || c.Root != p.Root || c.CallID == "" {
			t.Errorf("request %d: %+v; want merge.nightly_at = %s as the lead", i+1, c, want)
		}
	}

	startFakeServeAt(t, wire.SocketPath(p.Root), wire.Status{}, "UNCHANGED merge.nightly_at", "")
	if rc, stdout, _ := run(t, p.Root, env, "nightly", "off"); rc != 0 || stdout != "NIGHTLY OFF already\n" {
		t.Errorf("off when off: exit %d, %q; want NIGHTLY OFF already", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env, "nightly", "on", "03:00"); rc != 0 || stdout != "NIGHTLY ON at 03:00 already\n" {
		t.Errorf("on when on: exit %d, %q; want NIGHTLY ON at 03:00 already", rc, stdout)
	}

	for _, refusal := range []string{"config: merge.nightly_at takes a local time HH:MM (00:00 to 23:59), or null (off)",
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

// T-781: serve's merge loop (mergeLoop, built on every pass) has its nightly from merge.nightly_at as the config is
// now: on and off, committed by serve's config writer (config_set's), count from the next build, no restart.
func TestMergeLoopNightlyFollowsTheConfig(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	p.Git(p.Root, "config", "user.name", "Owner")
	p.Git(p.Root, "config", "user.email", "owner@example.com")
	build := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}})
	at := func() string {
		t.Helper()
		l, err := build(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		if l.Nightly == nil {
			t.Fatalf("the merge loop has no nightly: off is At \"\" (T-782)")
		}
		if l.Nightly.Run == nil || l.Nightly.Last == nil {
			t.Errorf("nightly at %s without Run or Last", l.Nightly.At)
		}
		if l.Nightly.At == "" {
			return "off"
		}
		return l.Nightly.At
	}
	set := func(val string) {
		t.Helper()
		l, err := build(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		doc, refused, err := l.Config.Read(context.Background())
		if err != nil || refused != "" {
			t.Fatalf("precondition: the config is writable: %q, %v", refused, err)
		}
		cfg, err := config.LoadDaemon(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := cfg.Edit(doc, "merge.nightly_at", json.RawMessage(val))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Config.Commit(context.Background(), ch.Doc, "config: merge.nightly_at"); err != nil {
			t.Fatal(err)
		}
	}
	if got := at(); got != "off" {
		t.Fatalf("precondition: merge.nightly_at unset: nightly %s, want off", got)
	}
	set(`"03:00"`)
	if got := at(); got != "03:00" {
		t.Errorf("after on: nightly %s, want 03:00", got)
	}
	set(`null`)
	if got := at(); got != "off" {
		t.Errorf("after off: nightly %s, want off", got)
	}
}
