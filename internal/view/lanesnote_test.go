package view

import (
	"context"
	"slices"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/testkit"
)

// T-879: serve's line on a lane table older than its tick follows the project's own notes (obor status prints them,
// the page shows them); a fresh table adds none.
func TestBuildLanesNote(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	own := []string{"unknown config key x"}
	for _, c := range []struct {
		lanes string
		want  []string
	}{
		{"", own},
		{"lane table read 2m ago (last scan: git: index.lock exists)",
			[]string{"unknown config key x", "lane table read 2m ago (last scan: git: index.lock exists)"}},
	} {
		ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Root: p.Root, Lanes: c.lanes}}
		v := Build(Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), Now: time.Now(),
			Notes: slices.Clone(own)})
		if !slices.Equal(v.Notes, c.want) {
			t.Errorf("lanes %q: notes %q, want %q", c.lanes, v.Notes, c.want)
		}
	}
}
