package hc_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/osnotify"
	_ "obor/internal/testkit" // every agent kind registered, as cmd/obor registers them
)

func shell(address string, labels map[string]string, cwd string) hc.Record {
	return hc.Record{Address: address, PID: 34044, Cwd: "/start", Labels: labels,
		Foreground: &hc.Foreground{PID: 34044, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: cwd}}
}

// Every connection says hello with generation 1 before its request.
func TestHelloFirst(t *testing.T) {
	core := hctest.New()
	core.Add(shell("t1", nil, "/w"))
	if _, err := core.Client().Pane(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if got := core.Requests(); !slices.Equal(got, []string{"hello", "terminal.get"}) {
		t.Errorf("requests = %q, want hello then terminal.get", got)
	}
}

// The version rule: hc 0.1.21 or newer is taken; an older one is refused with a line naming `hc update`, another
// generation with hc's own refusal; neither refusal reads as a core that is down or a missing pane.
func TestVersionRule(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		version    string
		generation int
		refusal    string // "": taken
	}{
		{"0.1.21", 1, ""}, {"0.1.22", 1, ""}, {"0.2.0", 1, ""},
		{"0.1.20", 1, "hc 0.1.20 is older than 0.1.21, the oldest obor speaks: run `hc update`"},
		{"dev", 1, `hc reports version "dev", which obor cannot read`},
		{"0.1.21", 2, "unsupported_generation"},
	} {
		core := hctest.New()
		core.SetVersion(c.version, c.generation)
		core.Add(shell("t1", nil, "/w"))
		_, err := core.Client().Pane(ctx, "t1")
		switch {
		case c.refusal == "" && err != nil:
			t.Errorf("hc %s gen %d: %v, want it taken", c.version, c.generation, err)
		case c.refusal != "" && (err == nil || !strings.Contains(err.Error(), c.refusal)):
			t.Errorf("hc %s gen %d: %v, want a refusal with %q", c.version, c.generation, err, c.refusal)
		case c.refusal != "" && (port.NotRunning(err) || port.NotFound(err)):
			t.Errorf("hc %s gen %d: refusal %v reads as NotRunning %v / NotFound %v", c.version, c.generation, err,
				port.NotRunning(err), port.NotFound(err))
		}
		if c.refusal != "" && slices.Contains(core.Requests(), "terminal.get") {
			t.Errorf("hc %s gen %d: refused, yet terminal.get was sent: %q", c.version, c.generation, core.Requests())
		}
	}
}

// Errors by data.kind: not_found is NotFound, hc's unavailable (a terminal in recovery) is neither NotFound nor
// NotRunning but hc's own answer, and a core that refuses the dial is NotRunning.
func TestErrors(t *testing.T) {
	ctx := context.Background()
	core := hctest.New()
	core.Add(shell("t1", nil, "/w"))
	cl := core.Client()
	if _, err := cl.Pane(ctx, "t1"); err != nil {
		t.Fatalf("precondition: t1 reads: %v", err)
	}
	_, err := cl.Pane(ctx, "t99")
	if !port.NotFound(err) || port.NotRunning(err) || !port.Answered(err) {
		t.Errorf("t99: %v: NotFound %v, NotRunning %v, Answered %v; want a NotFound answer", err, port.NotFound(err),
			port.NotRunning(err), port.Answered(err))
	}
	core.Fail("terminal.get", "unavailable")
	_, err = cl.Pane(ctx, "t1")
	if err == nil || port.NotFound(err) || port.NotRunning(err) || !port.Answered(err) {
		t.Errorf("unavailable: %v: NotFound %v, NotRunning %v, Answered %v; want neither, answered", err,
			port.NotFound(err), port.NotRunning(err), port.Answered(err))
	}
	core.Fail("terminal.get", "")
	core.Down()
	_, err = cl.Pane(ctx, "t1")
	if !port.NotRunning(err) || port.NotFound(err) || port.Answered(err) {
		t.Errorf("core down: %v: NotRunning %v, NotFound %v, Answered %v; want NotRunning alone", err,
			port.NotRunning(err), port.NotFound(err), port.Answered(err))
	}
	if _, err := (&hc.Client{Path: "/nonexistent/hc.sock"}).Pane(ctx, "t1"); !port.NotRunning(err) {
		t.Errorf("no socket file: %v, want NotRunning", err)
	}
}

// PaneControl's reads: Pane and Panes carry no Group and leave a finished terminal out (NotFound for Pane), Screen and
// Recent are terminal.read's text cut to its last lines, ProcessInfo is the shell's pid and the foreground's leader
// (its argv0, not its process title).
func TestReads(t *testing.T) {
	ctx := context.Background()
	core := hctest.New()
	core.Add(shell("t1", map[string]string{hc.LabelProject: "cap", hc.LabelAgent: "w1"}, "/w/sub"))
	core.Add(hc.Record{Address: "t2", State: "finished", Reason: "exited", Cwd: "/w"})
	core.SetScreen("t1", "line a\nline b\nprompt %")
	cl := core.Client()

	want := port.Pane{ID: "t1", Terminal: "t1", Run: "34044", Label: "w1", Cwd: "/w/sub"}
	if p, err := cl.Pane(ctx, "t1"); err != nil || p != want {
		t.Errorf("Pane(t1) = %+v, %v; want %+v", p, err, want)
	}
	if ps, err := cl.Panes(ctx); err != nil || !slices.Equal(ps, []port.Pane{want}) {
		t.Errorf("Panes = %+v, %v; want only t1, no group", ps, err)
	}
	if _, err := cl.Pane(ctx, "t2"); !port.NotFound(err) {
		t.Errorf("Pane(finished t2) = %v, want NotFound", err)
	}
	if s, err := cl.Screen(ctx, "t1", 2); err != nil || s != "line b\nprompt %" {
		t.Errorf("Screen(t1, 2) = %q, %v", s, err)
	}
	if s, err := cl.Recent(ctx, "t1", 0); err != nil || s != "line a\nline b\nprompt %" {
		t.Errorf("Recent(t1, 0) = %q, %v", s, err)
	}
	if _, err := cl.Screen(ctx, "t9", 2); !port.NotFound(err) {
		t.Errorf("Screen(t9) = %v, want NotFound", err)
	}
	info, err := cl.ProcessInfo(ctx, "t1")
	if err != nil || info.ShellPID != 34044 || !info.AtShell() || info.Foreground() != "zsh" {
		t.Errorf("ProcessInfo(t1) at its shell = %+v, %v; want shell 34044 at the foreground", info, err)
	}
	core.Update("t1", func(r *hc.Record) {
		r.Foreground = &hc.Foreground{PID: 71307, Name: "2.1.329", Argv: []string{"claude", "--model", "haiku"}, Cwd: "/w"}
	})
	info, err = cl.ProcessInfo(ctx, "t1")
	if err != nil || info.AtShell() || info.Foreground() != "claude" || info.FgPGID != 71307 {
		t.Errorf("ProcessInfo(t1) with Claude = %+v, %v; want not at the shell, foreground claude 71307", info, err)
	}
	if _, err := cl.ProcessInfo(ctx, "t2"); !port.NotFound(err) {
		t.Errorf("ProcessInfo(finished t2) = %v, want NotFound", err)
	}
}

// Snapshot: the version, the instance (the core id, whatever its holders), the live terminals with their group and
// run (PaneRuns), and a note only when hc lacks a method obor sends.
func TestSnapshot(t *testing.T) {
	ctx := context.Background()
	core := hctest.New()
	core.SetHolders("bbb", "aaa")
	core.Add(shell("t1", map[string]string{hc.LabelProject: "cap", hc.LabelAgent: "w1"}, "/w"))
	core.Add(shell("t2", nil, "/x"))
	core.Add(hc.Record{Address: "t3", State: "finished", Labels: map[string]string{hc.LabelProject: "cap"}})
	snap, err := hc.Panes{C: core.Client()}.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []port.Pane{{ID: "t1", Terminal: "t1", Run: "34044", Group: "cap", Label: "w1", Cwd: "/w"},
		{ID: "t2", Terminal: "t2", Run: "34044", Cwd: "/x"}}
	if snap.Version != "0.1.21" || snap.Instance != "6bdfe8023479182e4e1a5ff565c060e4" || !snap.PaneRuns || snap.Note != "" ||
		!slices.Equal(snap.Panes, want) {
		t.Errorf("snapshot = %+v\nwant version 0.1.21, the core id as instance, pane runs, no note, panes %+v", snap, want)
	}
	core.SetMethods(slices.DeleteFunc(slices.Clone(hctest.Methods), func(m string) bool { return m == "terminal.read" })...)
	snap, err = hc.Panes{C: core.Client()}.Snapshot(ctx)
	if err != nil || snap.Note != "hc 0.1.21 lacks terminal.read: obor's calls to them fail" {
		t.Errorf("snapshot without terminal.read: note %q, %v", snap.Note, err)
	}
	if note, err := (hc.Panes{C: core.Client()}).Note(ctx); err != nil || note != snap.Note {
		t.Errorf("Note without terminal.read: %q, %v; want the snapshot's note %q", note, err, snap.Note)
	}
}

// A notification that does not show (osnotify's reason) is a NotShown naming hc and the reason, which the caller logs
// and goes on from.
func TestNotify(t *testing.T) {
	core := hctest.New().Client()
	fail := func(context.Context, string, string) error { return errors.New("execution error: not allowed") }
	err := (hc.Panes{C: core, Notifier: osnotify.Notifier{GOOS: "darwin", Post: fail}}).Notify(context.Background(), "t", "b")
	var ns *port.NotShown
	if !errors.As(err, &ns) {
		t.Fatalf("Notify = %v, want a NotShown", err)
	}
	if ns.Reason != "hc: osascript: execution error: not allowed" {
		t.Errorf("reason = %q, want hc and osascript's error", ns.Reason)
	}
}

// SocketPath finds the core's socket as hc does: HC_SOCKET, else the runtime dir's, else the session's state dir.
func TestSocketPath(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HC_SOCKET": "/s.sock", "XDG_RUNTIME_DIR": "/run"}, "/s.sock"},
		{map[string]string{"XDG_RUNTIME_DIR": "/run", "HC_SESSION": "p1"}, "/run/herdr-core/p1.sock"},
		{map[string]string{"XDG_STATE_HOME": "/st", "HC_SESSION": "p1"}, "/st/herdr-core/p1/p1.sock"},
		{map[string]string{"HOME": "/h"}, "/h/.local/state/herdr-core/default/default.sock"},
	} {
		env := func(k string) (string, bool) { v, ok := c.env[k]; return v, ok }
		if got := hc.SocketPath(env); got != c.want {
			t.Errorf("SocketPath(%v) = %s, want %s", c.env, got, c.want)
		}
	}
}
