//go:build integration

package main

import (
	"context"
	"encoding/json"
	"testing"

	"obor/internal/config"
	"obor/internal/herdr"
)

// T-781: serve's merge loop (mergeLoop, built on every pass) has its nightly from merge.nightly_at as the config is
// now: on and off, committed by serve's config writer (config_set's), count from the next build, no restart.
func TestMergeLoopNightlyFollowsTheConfig(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
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
