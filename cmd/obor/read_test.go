package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/herdr/herdrtest"
	"obor/internal/rex"
	"obor/internal/testkit"
)

// readFixture is a project with one lane and the lane agent's config entry, read-only for every kind's fake.
func readFixture(t *testing.T) (lane, agent string, cfg *config.Config) {
	t.Helper()
	p := testkit.NewPlain(t)
	lane = p.PlainLane("L-01", "task/T-01")
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	agent = cfg.LaneAgent("L-01")
	if agent == "" {
		t.Fatal("precondition: no agent name for L-01")
	}
	return lane, agent, cfg
}

// obor read prints the agent's pane as each terminal adapter reads it, and hands it --lines: the herdr fake sees the
// read's line count, hc's and Rex's trim their text to it.
func TestReadPrintsTheAgentsPane(t *testing.T) {
	t.Parallel()
	lane, agent, cfg := readFixture(t)
	ctx := context.Background()

	t.Run("herdr", func(t *testing.T) {
		f := herdrtest.New()
		f.Agent("w1:p1", agent, "blocked", lane, func(p *herdrtest.Pane) {
			p.Label, p.Screen = agent, "one\ntwo"
		})
		srv := herdrtest.ServeMem(t, f)
		var buf bytes.Buffer
		if rc := readOn(ctx, &buf, cfg, srv.Client(), agent, 2); rc != 0 {
			t.Fatalf("exit %d, %q", rc, buf.String())
		}
		if buf.String() != "one\ntwo\n" {
			t.Errorf("printed %q, want the pane's screen", buf.String())
		}
		var read bool
		for _, r := range srv.Requests() {
			if r.Method == "pane.read" {
				read = true
				if !strings.Contains(string(r.Params), `"lines":2`) ||
					!strings.Contains(string(r.Params), `"source":"recent_unwrapped"`) {
					t.Errorf("pane.read params %s, want the recent output's last 2 lines", r.Params)
				}
			}
		}
		if !read {
			t.Fatal("precondition: no pane.read request")
		}
	})

	t.Run("hc", func(t *testing.T) {
		const addr = "t1"
		core := hctest.New()
		core.Add(hc.Record{Address: addr, Cwd: lane, Labels: map[string]string{hc.LabelAgent: agent}})
		core.SetScreen(addr, "one\ntwo\nthree")
		var buf bytes.Buffer
		if rc := readOn(ctx, &buf, cfg, core.Client(), agent, 2); rc != 0 {
			t.Fatalf("exit %d, %q", rc, buf.String())
		}
		if buf.String() != "two\nthree\n" {
			t.Errorf("printed %q, want the screen's last 2 lines", buf.String())
		}
	})

	t.Run("rex", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "rex")
		script := `#!/bin/sh
case "$3" in
session.list) printf '%s\n' '{"sessions":[{"session_id":"s1","label":"work"}]}' ;;
session.list_blocks) printf '{"blocks":[{"block_id":"block:1","label":"%s","creator_name":"com.superlogical.terminal"}]}\n' "$REX_AGENT" ;;
com.superlogical.terminal.process) printf '{"child":{"pid":10,"name":"claude"},"foreground":{"pid":20,"name":"zsh","cwd":"%s"}}\n' "$REX_ROOT" ;;
com.superlogical.terminal.program_status) printf '%s\n' '{"records":[]}' ;;
target.resolve) printf '%s\n' '{"results":[{"status":"resolved","target":{"session_id":"s1","window_id":"w1"}}]}' ;;
com.superlogical.terminal.format) printf '%s\n' '{"content":"one\ntwo\nthree"}' ;;
*) printf 'unexpected method %s\n' "$3" >&2; exit 1 ;;
esac
`
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cl := &rex.Client{Bin: bin, Env: []string{"PATH=/usr/bin:/bin", "REX_ROOT=" + lane, "REX_AGENT=" + agent}}
		var buf bytes.Buffer
		if rc := readOn(ctx, &buf, cfg, cl, agent, 2); rc != 0 {
			t.Fatalf("exit %d, %q", rc, buf.String())
		}
		if buf.String() != "two\nthree\n" {
			t.Errorf("printed %q, want the screen's last 2 lines", buf.String())
		}
	})
}

// The command's wiring: obor read <agent> builds the project's terminal and prints its pane's screen; --lines reaches
// the read.
func TestReadCommand(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "task/T-01")
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	agent := cfg.LaneAgent("L-01")
	f := p.Herdr()
	f.Agent("w1:p1", agent, "blocked", lane, func(p *herdrtest.Pane) {
		p.Label, p.Screen = agent, "the pane's screen"
	})
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path}
	rc, stdout, stderr := run(t, p.Root, env, "read", agent, "--lines", "5")
	if rc != 0 || stdout != "the pane's screen\n" || stderr != "" {
		t.Fatalf("obor read: exit %d, %q, %q; want the pane's screen", rc, stdout, stderr)
	}
	for _, r := range srv.Requests() {
		if r.Method == "pane.read" && !strings.Contains(string(r.Params), `"lines":5`) {
			t.Errorf("pane.read params %s, want --lines 5", r.Params)
		}
	}
}

// An agent the project does not know and one it knows but has no labelled pane for are different FAILs; nothing is
// printed between them.
func TestReadRefusesUnknownAgentAndNoPane(t *testing.T) {
	t.Parallel()
	_, agent, cfg := readFixture(t)
	core := hctest.New()
	for _, c := range []struct{ name, agent, want string }{
		{"unknown agent", "pj-nobody", "FAIL unknown agent pj-nobody (no lane, role or recorded session)\n"},
		{"no pane", agent, "FAIL no pane labelled " + agent + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if rc := readOn(context.Background(), &buf, cfg, core.Client(), c.agent, 60); rc != 1 {
				t.Fatalf("exit %d, %q; want 1", rc, buf.String())
			}
			if buf.String() != c.want {
				t.Errorf("printed %q, want %q", buf.String(), c.want)
			}
		})
	}
}

// A terminal that cannot list its panes is unreachable, named as such rather than as a missing pane.
func TestReadRefusesAnUnreachableTerminal(t *testing.T) {
	t.Parallel()
	_, agent, cfg := readFixture(t)
	core := hctest.New()
	core.Down()
	var buf bytes.Buffer
	if rc := readOn(context.Background(), &buf, cfg, core.Client(), agent, 60); rc != 1 {
		t.Fatalf("exit %d, %q; want 1", rc, buf.String())
	}
	if got := buf.String(); !strings.HasPrefix(got, "FAIL terminal unreachable: ") {
		t.Errorf("printed %q, want a terminal-unreachable FAIL", got)
	}
}

// --lines is the pane lines, 60 unless named; a missing positive number or an extra agent is the usage.
func TestReadArgs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args  []string
		agent string
		lines int
		ok    bool
	}{
		{nil, "", 0, false},
		{[]string{"pj-l-01"}, "pj-l-01", 60, true},
		{[]string{"pj-l-01", "--lines", "15"}, "pj-l-01", 15, true},
		{[]string{"--lines", "15", "pj-l-01"}, "pj-l-01", 15, true},
		{[]string{"pj-l-01", "--lines"}, "", 0, false},
		{[]string{"pj-l-01", "--lines", "0"}, "", 0, false},
		{[]string{"pj-l-01", "--lines", "many"}, "", 0, false},
		{[]string{"pj-l-01", "pj-reviewer"}, "", 0, false},
		{[]string{"--bogus"}, "", 0, false},
	} {
		agent, lines, ok := readArgs(c.args)
		if agent != c.agent || lines != c.lines || ok != c.ok {
			t.Errorf("readArgs(%q) = %q, %d, %v; want %q, %d, %v", c.args, agent, lines, ok, c.agent, c.lines, c.ok)
		}
	}
}
