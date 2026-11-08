//go:build integration

package main

import (
	"strings"
	"testing"
)

// RV-51 #5: agent names are machine-wide; another project's pj-lead is not this project's lead.
func TestLeadNamesakeInAnotherProjectFails(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	other := t.TempDir()
	x.f.Agent("w1:p1", "pj-lead", "working", other).Label = "pj-lead"
	out, rc := x.run(t)
	if rc != 1 || !strings.HasPrefix(lastLine(out), "FAIL pj-lead runs in pane w1:p1 with cwd "+other+", outside ") {
		t.Fatalf("namesake elsewhere: exit %d\n%s", rc, out)
	}
	if c := x.calls(); strings.Contains(c, "agent.start") || strings.Contains(c, "tab create") ||
		strings.Contains(c, "pane.send_input") || strings.Contains(c, "stop") {
		t.Errorf("namesake elsewhere: nothing started, typed or stopped:\n%s", c)
	}

	x = newLeadFx(t) // no cwd known: taken as ours, as the launcher does
	x.f.Agent("w1:p1", "pj-lead", "working", "").Label = "pj-lead"
	if out, rc := x.run(t); rc != 0 || lastLine(out) != "OK pj-lead runs (pane w1:p1)" {
		t.Errorf("no cwd known: exit %d\n%s", rc, out)
	}
}
