package app

import (
	"testing"

	"obor/internal/agents"
)

// stubKind is a plugin kind of the given name (only Name is called).
type stubKind struct {
	agents.Kind
	name string
}

func (k stubKind) Name() string { return k.name }

// The watchdogs read their kinds through Options.Kinds, the value serve's last pass sets: a plugin kind a config edit
// adds counts at the next lookup, with no refresh of the app's own (no second snapshot to disagree with the terminal
// adapter's).
func TestProjKindsFollowOptionsKinds(t *testing.T) {
	h := newHarness(t)
	cur := agents.Kinds{}
	h.a.opt.Kinds = func() agents.Kinds { return cur }
	p := reloadProj(t, h)
	if p.kind("hermes") != nil {
		t.Fatal("precondition: the plugin kind is unknown before the pass")
	}
	cur = agents.WithPlugins([]agents.Kind{stubKind{name: "hermes"}})
	if p.kind("hermes") == nil {
		t.Error("a plugin kind the last pass added is not found by the watchdog's lookup")
	}
}
