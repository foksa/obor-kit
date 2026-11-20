package app

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
	"obor/internal/project"
	"obor/internal/setup"
)

// serve's PlaceLane on hc in a project whose herdr-app window has no space yet: the pane it returns is the terminal
// herdr-app started with the space, already in the lane's cwd (the app's folder is the root), so serve's selection
// (paneIn) takes it, the worker's start types in it, and the tab holds that one terminal and no stray shell.
func TestPlaceLaneOnHCReturnsAReadyPaneServeStartsIn(t *testing.T) {
	h := newHarness(t)
	h.write(h.root+"/.obor/config.json", `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":2}}`)
	if err := os.Mkdir(h.root+"/.git", 0o755); err != nil { // the project's root as setup finds it, with no git process
		t.Fatal(err)
	}
	cfg, err := config.Load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Lanes()[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	laneDir := project.Physical(cfg.Lanes()[0].Dir) // setup resolves the project's root

	core, win := hctest.New(), herdrapptest.New()
	win.StartTerminals(core.AppTerminals(false))
	cl := core.Client()
	cl.App = win.Client()
	if len(win.Spaces()) != 0 || len(core.Live()) != 0 {
		t.Fatalf("precondition: the window has %d space(s) and the core %d terminal(s), want none", len(win.Spaces()), len(core.Live()))
	}

	var out bytes.Buffer
	s := &setup.Setup{Dir: h.root, Env: project.OSEnv, T: cl, Out: &out}
	pane, err := s.PlaceLane(context.Background(), "L-01")
	if err != nil {
		t.Fatalf("PlaceLane = %v\n%s", err, out.String())
	}

	live := core.Live()
	if len(live) != 1 || live[0].Address != pane || live[0].Cwd != laneDir {
		t.Fatalf("terminals %+v after PlaceLane = %q; want the one pane, in the lane %s", live, pane, laneDir)
	}
	if spaces := win.Spaces(); len(spaces) != 1 || len(spaces[0].Tabs) != 1 || core.Labels(pane)[herdrapp.LabelTab] != spaces[0].Tabs[0].ID {
		t.Errorf("spaces %+v, the pane's labels %v; want one space with one tab holding the pane", spaces, core.Labels(pane))
	}

	// serve's snapshot of the core, and its selection of the pane to start the worker in
	snap, err := hc.Panes{C: cl}.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.snap = snap
	h.connect()
	if got := placed(h, laneDir, "pj-l-01"); got != pane {
		t.Errorf("serve selects %q for pj-l-01 in %s, want the placed pane %s (snapshot %+v)", got, laneDir, pane, snap.Panes)
	}

	// the worker's start types in that pane; the tab still holds that terminal alone
	core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
	if _, err := cl.Start(context.Background(), port.AgentStart{Kind: "claude", Program: "claude", Pane: pane, Label: "pj-l-01"}); err != nil {
		t.Fatalf("Start in %s = %v", pane, err)
	}
	if sent := core.Sent(pane); len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "claude") {
		t.Errorf("typed in %s: %q, want the worker's start there", pane, sent)
	}
	if live := core.Live(); len(live) != 1 || !slices.Equal([]string{live[0].Address}, []string{pane}) {
		t.Errorf("terminals %+v after the start, want the one pane alone in its tab", live)
	}
}
