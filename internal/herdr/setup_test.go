package herdr_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

// ResumeHint: herdr's config.toml ($HERDR_CONFIG_PATH, else ~/.config/herdr/config.toml) counts
// only `[session] resume_agents_on_restore = false` as set (a comment after it too); a missing file, another value or
// the key in another section gives the one line to add, naming the file.
func TestResumeHint(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
		set        bool
	}{
		{"no config", "", false},
		{"false under [session]", "[session]\nresume_agents_on_restore = false\n", true},
		{"false with a comment", "[ui]\nx = 1\n[session]\nresume_agents_on_restore = false # obor\n", true},
		{"true", "[session]\nresume_agents_on_restore = true\n", false},
		{"false outside [session]", "[other]\nresume_agents_on_restore = false\n", false},
		{"a later section ends [session]", "[session]\n[other]\nresume_agents_on_restore = false\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if c.body != "" {
				if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
				if got, _ := os.ReadFile(path); string(got) != c.body {
					t.Fatalf("precondition: config holds %q", got)
				}
			}
			h := &herdr.Setup{Env: func(k string) (string, bool) {
				if k == "HERDR_CONFIG_PATH" {
					return path, true
				}
				return "", false
			}}
			hint := h.ResumeHint()
			if c.set {
				if hint != "" {
					t.Fatalf("set in %s: hint %q, want none", path, hint)
				}
				return
			}
			if !strings.Contains(hint, "set resume_agents_on_restore = false under [session] in "+path) {
				t.Errorf("hint %q, want the line naming %s", hint, path)
			}
			got, err := os.ReadFile(path)
			if c.body == "" && err == nil {
				t.Errorf("herdr's config created: %q", got)
			} else if c.body != "" && (err != nil || string(got) != c.body) {
				t.Errorf("herdr's config edited: %q, %v", got, err)
			}
		})
	}
}

// ResumeHint with no HERDR_CONFIG_PATH names ~/.config/herdr/config.toml under HOME.
func TestResumeHintHomePath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	h := &herdr.Setup{Env: func(k string) (string, bool) {
		if k == "HOME" {
			return home, true
		}
		return "", false
	}}
	want := filepath.Join(home, ".config", "herdr", "config.toml")
	if hint := h.ResumeHint(); !strings.Contains(hint, " in "+want+" ") {
		t.Errorf("hint %q, want %s", hint, want)
	}
}

// (tab ids here are x-numbered: the fake names the tabs it makes t1, t2 …)
// placeKit is a herdr with a project workspace w1 (labelled proj) and a project whose agents are pj-lead (the lead)
// and pj-l-NN (lanes): the fake, the server's requests and a Setup placing on it.
type placeKit struct {
	f   *herdrtest.Fake
	srv *herdrtest.Server
	h   *herdr.Setup
	pr  port.PlaceProject
}

func newPlaceKit(t *testing.T, f *herdrtest.Fake) *placeKit {
	t.Helper()
	f.Group("w1", "proj")
	srv := herdrtest.Serve(t, f)
	return &placeKit{f: f, srv: srv, h: &herdr.Setup{Socket: srv.Client(), Env: func(string) (string, bool) { return "", false }},
		pr: port.PlaceProject{Name: "proj", Root: "/proj", MaxPanes: 3,
			Has: func(cwd string) bool { return strings.HasPrefix(cwd, "/proj") },
			KindOf: func(l string) port.PaneKind {
				switch {
				case l == "pj-lead":
					return port.KindLead
				case strings.HasPrefix(l, "pj-l-"):
					return port.KindLane
				}
				return ""
			}}}
}

// methods are the herdr methods the placements sent, in order.
func (k *placeKit) methods() []string {
	var out []string
	for _, r := range k.srv.Requests() {
		out = append(out, r.Method)
	}
	return out
}

// Place carries out the decision's three answers as herdr calls: a refusal is an error and sends nothing that changes
// herdr; a new tab is a tab.create in the project's workspace under the name the answer gave (its progress line said),
// beside a person's pane left untouched; a tab is a pane.split of it. What the answers are is layout's table.
func TestPlaceCarriesOutTheDecision(t *testing.T) {
	t.Parallel()
	t.Run("a refusal: an error naming the tab, nothing sent that changes herdr", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Tab("w1:x1", 80, 24, "w1:p1")
		f.Shell("w1:p1", "pj-lead", "/proj")
		k := newPlaceKit(t, f)
		if n := len(k.srv.Requests()); n != 0 {
			t.Fatalf("precondition: %d requests before the placement", n)
		}
		_, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-01", In: "w1:x1", Project: k.pr})
		if err == nil || !strings.Contains(err.Error(), "w1:x1") || !strings.Contains(err.Error(), "--new-tab") {
			t.Fatalf("Place into the lead's tab = %v, want the refusal naming the tab and --new-tab", err)
		}
		if ms := k.methods(); slices.Contains(ms, "pane.split") || slices.Contains(ms, "tab.create") {
			t.Errorf("the refusal sent %q", ms)
		}
	})
	t.Run("a new tab: tab.create in the project's workspace, the person's pane untouched, the line said", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Tab("w1:x1", 80, 24, "w1:p1", "w1:p2", "w1:p3")
		for _, id := range []string{"w1:p1", "w1:p2", "w1:p3"} {
			f.Shell(id, "pj-l-0"+id[len(id)-1:], "/proj-wt/L-0"+id[len(id)-1:])
		}
		f.Tab("w9:x1", 80, 24, "w9:p1") // another workspace: where a careless placement would go
		f.Shell("w9:p1", "", "/elsewhere")
		k := newPlaceKit(t, f)
		var said []string
		pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-04", In: "w1:x1", Project: k.pr,
			Say: func(l string) { said = append(said, l) }})
		if err != nil {
			t.Fatalf("Place into a full tab = %v", err)
		}
		p := f.Pane(pane)
		if p == nil || p.Tab == "w1:x1" || p.Workspace != "w1" || p.Cwd != "/proj-wt/L-04" || f.TabLabels[p.Tab] != "lanes" {
			t.Errorf("new pane %s = %+v (tab labelled %q), want a pane in a new lanes tab of w1, in the lane's cwd", pane, p, f.TabLabels[p.Tab])
		}
		if ms := k.methods(); slices.Contains(ms, "pane.split") || !slices.Contains(ms, "tab.create") {
			t.Errorf("requests %q, want a tab.create and no pane.split", ms)
		}
		if len(said) == 0 || !strings.Contains(said[0], "w1:x1") {
			t.Errorf("progress lines %q, want the decision's line about w1:x1 first", said)
		}
		if q := f.Pane("w9:p1"); q == nil || q.Tab != "w9:x1" || len(f.Panes) != 5 {
			t.Errorf("the other workspace's pane %+v, %d panes in all, want it untouched and one made", q, len(f.Panes))
		}
	})
	t.Run("a tab: a split of it", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Tab("w1:x1", 80, 24, "w1:p1")
		f.Shell("w1:p1", "pj-l-01", "/proj-wt/L-01")
		k := newPlaceKit(t, f)
		pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-02", Project: k.pr})
		if p := f.Pane(pane); err != nil || p == nil || p.Tab != "w1:x1" {
			t.Fatalf("Place of a lane beside a lane = %q, %v; want a pane in w1:x1", pane, err)
		}
		if ms := k.methods(); !slices.Contains(ms, "pane.split") || slices.Contains(ms, "tab.create") {
			t.Errorf("requests %q, want a pane.split and no tab.create", ms)
		}
	})
}

// herdr's facts the decision is asked with: a pane with an unknown cwd or one outside the project blocks its tab (it
// may be another project's) though it is labelled for a lane; the lead's pane is the lead's wherever it works; a tab
// the user names with --tab is the decision's whatever its workspace, and is split only at the panes of this project;
// a tab of another workspace is not one the default placement picks.
func TestPlaceAsksWithHerdrsFacts(t *testing.T) {
	t.Parallel()
	lane := func(t *testing.T, f *herdrtest.Fake, cwd string) *placeKit {
		t.Helper()
		f.Tab("w1:x0", 80, 24, "w1:p0") // the project's workspace holds a pane of the project
		f.Shell("w1:p0", "", "/proj")
		f.Tab("w1:x1", 80, 24, "w1:p1")
		f.Shell("w1:p1", "pj-l-01", cwd)
		return newPlaceKit(t, f)
	}
	t.Run("a lane pane in the project: its tab takes the next lane", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		k := lane(t, f, "/proj-wt/L-01")
		pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-02", Project: k.pr})
		if p := f.Pane(pane); err != nil || p == nil || p.Tab != "w1:x1" {
			t.Errorf("Place = %q, %v; want a pane in w1:x1", pane, err)
		}
	})
	for _, c := range []struct{ name, cwd string }{
		{"a lane pane with an unknown cwd: its tab is not taken", ""},
		{"a lane pane outside the project: its tab is not taken", "/elsewhere"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := herdrtest.New()
			k := lane(t, f, c.cwd)
			if p := f.Pane("w1:p1"); p.Label != "pj-l-01" || p.Cwd != c.cwd {
				t.Fatalf("precondition: the lane pane = %+v", p)
			}
			pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-02", Project: k.pr})
			if p := f.Pane(pane); err != nil || p == nil || p.Tab == "w1:x1" || p.Tab == "w1:x0" {
				t.Errorf("Place = %q (%+v), %v; want a pane in a new tab", pane, p, err)
			}
			if slices.Contains(k.methods(), "pane.split") {
				t.Errorf("requests %q, want no pane.split", k.methods())
			}
		})
	}
	t.Run("the lead's pane outside the project's cwds still keeps its tab alone", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		k := lane(t, f, "/proj-wt/L-01")
		f.Shell("w1:p1", "pj-lead", "") // the lead, its cwd unknown
		_, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-02", In: "w1:x1", Project: k.pr})
		if err == nil || !strings.Contains(err.Error(), "w1:x1") {
			t.Errorf("Place into the lead's tab = %v, want a refusal naming it", err)
		}
	})
	t.Run("a tab named with --tab in another workspace: honoured, split at the project's pane only", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		k := lane(t, f, "/proj-wt/L-01")
		f.Tab("w9:x1", 80, 24, "w9:p1", "w9:p2")
		f.Shell("w9:p1", "", "/elsewhere")
		f.Shell("w9:p2", "", "/proj-wt/L-03")
		pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-02", In: "w9:x1", Project: k.pr})
		if p := f.Pane(pane); err != nil || p == nil || p.Tab != "w9:x1" {
			t.Fatalf("Place --tab w9:x1 = %q, %v; want a pane in w9:x1", pane, err)
		}
		for _, r := range k.srv.Requests() {
			if r.Method == "pane.split" && strings.Contains(r.String(), "w9:p1") {
				t.Errorf("another project's pane was split: %s", r)
			}
		}
	})
	t.Run("a lane tab of the workspace with fewer project panes is not picked by default", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		k := lane(t, f, "/proj-wt/L-01")
		f.Tab("w9:x1", 80, 24, "w9:p1")
		f.Shell("w9:p1", "pj-l-03", "/proj-wt/L-03")
		if ws := f.Pane("w9:p1").Workspace; ws != "w9" {
			t.Fatalf("precondition: the lane tab is in %s, want w9 (w1 holds two panes of the project, w9 one)", ws)
		}
		pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj-wt/L-02", Project: k.pr})
		if p := f.Pane(pane); err != nil || p == nil || p.Workspace != "w1" || p.Tab == "w9:x1" {
			t.Errorf("Place = %q (%+v), %v; want a new tab in the project's workspace w1", pane, p, err)
		}
	})
}

// A lead placed by default never splits into a tab that is there: a tab labelled lead holding a person's shell gets a
// new tab beside it, the shell untouched.
func TestPlaceLeadByDefaultTakesANewTab(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Tab("w1:x5", 80, 24, "w1:p5")
	f.Shell("w1:p5", "", "/proj") // a person's shell in the lead's tab
	f.TabLabels = map[string]string{"w1:x5": "lead"}
	k := newPlaceKit(t, f)
	if p := f.Pane("w1:p5"); p == nil || p.Tab != "w1:x5" || p.Label != "" {
		t.Fatalf("precondition: the person's shell = %+v, want an unlabelled pane in w1:x5", p)
	}
	pane, err := k.h.Place(ctx, port.Placement{Kind: port.KindLead, Cwd: "/proj", Project: k.pr})
	if err != nil {
		t.Fatalf("lead Place = %v", err)
	}
	if p := f.Pane(pane); p == nil || p.Tab == "w1:x5" {
		t.Errorf("the lead's pane %s = %+v, want it in a new tab", pane, p)
	}
	if ms := k.methods(); !slices.Contains(ms, "tab.create") || slices.Contains(ms, "pane.split") {
		t.Errorf("requests %q, want a tab.create and no pane.split", ms)
	}
	if p := f.Pane("w1:p5"); p == nil || p.Tab != "w1:x5" || p.Label != "" {
		t.Errorf("the person's shell after the placement = %+v, want it untouched in w1:x5", p)
	}
}

// CloseProject closes the labelled panes the project owns and no other: another project's pane in the same tab, a pane
// with no label and the caller's own (Ours says no) stay; with Dry nothing closes.
func TestCloseProjectClosesOnlyWhatIsOurs(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Shell("w1:a", "pj-o-01", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t1" })
	f.Shell("w1:b", "pj-o-02", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t1" })
	f.Shell("w1:c", "zz-o-01", "/zz", func(p *herdrtest.Pane) { p.Tab = "w1:t1" }) // another project's, same tab
	f.Shell("w1:d", "", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t1" })        // no label
	f.Shell("w1:e", "pj-lead", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t2" }) // the caller's own
	srv := herdrtest.ServeMem(t, f)
	srv.ClosePanes = true
	h := &herdr.Setup{Socket: srv.Client()}
	ours := func(p port.Pane) bool { return strings.HasPrefix(p.Label, "pj-") && p.ID != "w1:e" }
	ids := func() string {
		var out []string
		for _, p := range f.Panes {
			out = append(out, p.ID)
		}
		return strings.Join(out, " ")
	}

	lines, err := h.CloseProject(context.Background(), port.CloseProject{Name: "pj", Ours: ours, Dry: true})
	if err != nil || len(lines) != 2 || ids() != "w1:a w1:b w1:c w1:d w1:e" {
		t.Fatalf("dry run: %v, %v, panes %s", lines, err, ids())
	}
	lines, err = h.CloseProject(context.Background(), port.CloseProject{Name: "pj", Ours: ours})
	if err != nil || len(lines) != 2 {
		t.Fatalf("close: %v, %v", lines, err)
	}
	if got := ids(); got != "w1:c w1:d w1:e" {
		t.Errorf("panes left %q, want another project's, the unlabelled and the caller's", got)
	}
	if again, err := h.CloseProject(context.Background(), port.CloseProject{Name: "pj", Ours: ours}); err != nil || len(again) != 0 {
		t.Errorf("second run: %v, %v, want nothing", again, err)
	}
}

// TakeLead refuses a pane that runs an agent (a person's agent typed obor lead) and one this herdr does not have,
// touching nothing; a qualifying pane whose workspace already carries the project's name keeps that label.
func TestTakeLeadRefusesAnAgentsPane(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Tab("w1:tc", 80, 24, "w1:p1")
	f.Agent("w1:p1", "", "idle", "/proj")
	f.Tab("w2:tc", 80, 24, "w2:p1")
	f.Shell("w2:p1", "", "/proj")
	f.Group("w2", "proj")
	srv := herdrtest.Serve(t, f)
	h := &herdr.Setup{Socket: srv.Client(), Env: func(string) (string, bool) { return "", false }}
	pr := port.PlaceProject{Name: "proj", Root: "/proj", MaxPanes: 4,
		Has: func(cwd string) bool { return strings.HasPrefix(cwd, "/proj") }, KindOf: func(string) port.PaneKind { return "" }}
	for pane, why := range map[string]string{"w1:p1": "pane w1:p1 runs an agent (claude)", "w9:p9": "pane w9:p9 is not a pane of this herdr"} {
		got, err := h.TakeLead(ctx, port.LeadPane{Pane: pane, Agent: "pj-lead", Project: pr})
		if err != nil || got.Taken || got.Line != why {
			t.Errorf("TakeLead(%s) = %+v, %v; want not taken: %s", pane, got, err, why)
		}
	}
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Method, ".rename") {
			t.Errorf("a refusal sent %s", r)
		}
	}
	got, err := h.TakeLead(ctx, port.LeadPane{Pane: "w2:p1", Agent: "pj-lead", Project: pr})
	if err != nil || !got.Taken || got.Line != "pane w2:p1 labelled pj-lead, tab w2:tc labelled lead" {
		t.Errorf("TakeLead(w2:p1) = %+v, %v; want taken, no workspace rename", got, err)
	}
	if f.Pane("w2:p1").Label != "pj-lead" || f.TabLabels["w2:tc"] != "lead" {
		t.Errorf("pane %+v, tab %q: want pj-lead and lead", f.Pane("w2:p1").Label, f.TabLabels["w2:tc"])
	}
}
