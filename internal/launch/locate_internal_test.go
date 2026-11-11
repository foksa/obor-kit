package launch

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// The start decision as a table (decide, no I/O): serve's answer, the pane list read now, the terminal's holder of the
// name and the target, to where the start or stop goes or the one refusal line. Project /p; the config names pj-lead,
// pj-l-01, pj-advisor and pj-rev.
func TestLocate(t *testing.T) {
	t.Parallel()
	shell := func(id, label string) port.Pane { return port.Pane{ID: id, Label: label, Cwd: "/p"} }
	agent := func(id, label string) port.Pane { return port.Pane{ID: id, Label: label, Agent: "claude", Cwd: "/p"} }
	at := func(p port.Pane, cwd string) port.Pane { p.Cwd = cwd; return p }
	none := &wire.WhereReply{State: wire.WhereNone, Why: "no proof"}
	ident := func(pane, src, label string) *wire.WhereReply {
		return &wire.WhereReply{State: wire.WhereIdentified, Pane: pane, Source: src, Label: label}
	}
	const down = "obor serve is not running"
	for _, tc := range []struct {
		name   string
		agent  string
		target string
		stop   bool
		panes  []port.Pane
		serve  *wire.WhereReply // nil: serve cannot tell (down)
		holder string
		lookup error // the holder lookup's error
		want   Located
		refuse string // the refusal line ("": none)
	}{
		{name: "2 identified by its session in a relabelled pane, no name holder: runs there", agent: "pj-lead",
			panes: []port.Pane{agent("w1:p1", "notes")}, serve: ident("w1:p1", "session", "notes"),
			want: Located{Pane: "w1:p1", Runs: true, Proof: "session", Label: "notes"}},
		{name: "3 serve down, a live unlabelled-for-an-agent pane in the project: cannot tell", agent: "pj-lead",
			panes: []port.Pane{agent("w1:p1", "notes")},
			refuse: "cannot tell whether the claude in pane w1:p1 (label notes) is pj-lead: obor serve is not running. " +
				"obor serve restart, then retry; or obor restart pj-lead --pane w1:p1 to replace it"},
		{name: "4 runs elsewhere by its start row: a target elsewhere is refused", agent: "pj-l-01", target: "w1:p8",
			panes: []port.Pane{agent("w1:p3", ""), agent("w1:p8", "")}, serve: ident("w1:p3", "start", ""),
			refuse: "pj-l-01 already runs in pane w1:p3 (proof: start; label none): obor restart pj-l-01 --pane w1:p3 " +
				"restarts it there, or quit it there first"},
		{name: "4 runs elsewhere by the terminal's name alone: a target elsewhere is refused", agent: "pj-l-01",
			target: "w1:p8", panes: []port.Pane{agent("w1:p3", ""), shell("w1:p8", "")}, serve: none, holder: "w1:p3",
			refuse: "pj-l-01 already runs in pane w1:p3 (proof: name; label none): obor restart pj-l-01 --pane w1:p3 " +
				"restarts it there, or quit it there first"},
		{name: "8 the holder lookup failed: refused", agent: "pj-l-01", target: "w1:p8",
			panes: []port.Pane{shell("w1:p8", "")}, serve: none, lookup: errors.New("agent.get: connection reset"),
			refuse: "cannot tell whether a live agent holds the name pj-l-01: the terminal's lookup failed " +
				"(agent.get: connection reset): nothing touched"},
		{name: "10 runs in Q under another label, the target Q: a restart in place", agent: "pj-l-01", target: "w1:p3",
			panes: []port.Pane{agent("w1:p3", "notes")}, serve: ident("w1:p3", "session", "notes"), holder: "w1:p3",
			want: Located{Pane: "w1:p3", Runs: true, Proof: "session", Label: "notes"}},
		{name: "14 two live panes labelled the agent: ambiguous", agent: "pj-l-01",
			panes: []port.Pane{agent("w1:p3", "pj-l-01"), agent("w1:p1", "pj-l-01")}, serve: none,
			refuse: "not identified: panes w1:p1 and w1:p3 run pj-l-01 (close or relabel one)"},
		{name: "14 serve identifies Q, the terminal names another live pane: ambiguous", agent: "pj-l-01",
			panes: []port.Pane{agent("w1:p3", "pj-l-01"), agent("w1:p1", "")}, serve: ident("w1:p3", "label", "pj-l-01"),
			holder: "w1:p1", refuse: "not identified: panes w1:p1 and w1:p3 run pj-l-01 (close or relabel one)"},
		{name: "14 serve says ambiguous: its line", agent: "pj-l-01",
			panes: []port.Pane{agent("w1:p3", "pj-l-01"), agent("w1:p1", "pj-l-01")},
			serve: &wire.WhereReply{State: wire.WhereAmbiguous, Panes: []string{"w1:p1", "w1:p3"},
				Why: "not identified: panes w1:p1 and w1:p3 run pj-l-01 (close or relabel one)"},
			refuse: "not identified: panes w1:p1 and w1:p3 run pj-l-01 (close or relabel one)"},
		{name: "15 serve's answer names a pane that runs no agent now: free, its home", agent: "pj-l-01",
			panes: []port.Pane{shell("w1:p3", "pj-l-01")}, serve: ident("w1:p3", "session", "pj-l-01"),
			want: Located{Pane: "w1:p3", Label: "pj-l-01"}},
		{name: "17 the agent's label on a live pane outside the project: refused", agent: "pj-lead",
			panes: []port.Pane{at(agent("w1:p1", "pj-lead"), "/other")}, serve: none,
			refuse: "pj-lead runs in pane w1:p1 with cwd /other, outside /p (another project's agent of that name? the " +
				"config's prefix names every agent: obor setup rename changes it)"},
		{name: "17 the agent's label and the terminal's name on a live pane outside the project: refused", agent: "pj-lead",
			panes: []port.Pane{at(agent("w1:p1", "pj-lead"), "/other")}, serve: none, holder: "w1:p1",
			refuse: "pj-lead runs in pane w1:p1 with cwd /other, outside /p (another project's agent of that name? the " +
				"config's prefix names every agent: obor setup rename changes it)"},
		{name: "17 the terminal's name alone on a live pane outside the project: refused", agent: "pj-lead",
			panes: []port.Pane{at(agent("w1:p1", ""), "/other")}, serve: none, holder: "w1:p1",
			refuse: "pj-lead runs in pane w1:p1 with cwd /other, outside /p (another project's agent of that name? the " +
				"config's prefix names every agent: obor setup rename changes it)"},
		{name: "free: its home, the pane labelled with its name at its shell", agent: "pj-l-01",
			panes: []port.Pane{shell("w1:p2", "pj-l-01"), agent("w1:p1", "notes")}, serve: none,
			want: Located{Pane: "w1:p2", Label: "pj-l-01"}},
		{name: "free: a target other than the home is refused", agent: "pj-l-01", target: "w1:p4",
			panes: []port.Pane{shell("w1:p3", "pj-l-01"), shell("w1:p4", "")}, serve: none,
			refuse: "pane w1:p3 is already labelled pj-l-01 (obor restart pj-l-01 there)"},
		{name: "free: a target serve gives to another agent is refused", agent: "pj-l-01", target: "w1:p4",
			panes:  []port.Pane{agent("w1:p4", "")},
			serve:  &wire.WhereReply{State: wire.WhereNone, Runs: map[string]string{"w1:p4": "pj-advisor"}},
			refuse: "pane w1:p4 runs pj-advisor, not pj-l-01"},
		{name: "free: a target labelled for another agent is refused", agent: "pj-l-01", target: "w1:p4",
			panes: []port.Pane{shell("w1:p4", "pj-rev")}, serve: none,
			refuse: "pane w1:p4 is labelled pj-rev, not pj-l-01"},
		{name: "serve down: a target labelled notes goes, the label names nobody", agent: "pj-l-01", target: "w1:p4",
			panes: []port.Pane{shell("w1:p4", "notes")}, serve: none,
			want: Located{Pane: "w1:p4", Label: "notes"}},
		{name: "serve down: a target labelled an old alias goes, the label names nobody", agent: "pj-l-01",
			target: "w1:p4", panes: []port.Pane{shell("w1:p4", "pj-l1")}, serve: none,
			want: Located{Pane: "w1:p4", Label: "pj-l1"}},
		{name: "serve down: a target labelled notes beside an unaccounted agent is the O1 refusal", agent: "pj-l-01",
			target: "w1:p4", panes: []port.Pane{shell("w1:p4", "notes"), agent("w1:p1", "notes")},
			refuse: "cannot tell whether the claude in pane w1:p1 (label notes) is pj-l-01: obor serve is not running. " +
				"obor serve restart, then retry; or obor restart pj-l-01 --pane w1:p1 to replace it"},
		{name: "free: the agent's label on a live pane serve gives to another agent is not its pane", agent: "pj-l-01",
			panes: []port.Pane{agent("w1:p4", "pj-l-01"), shell("w1:p2", "pj-l-01")},
			serve: &wire.WhereReply{State: wire.WhereNone, Runs: map[string]string{"w1:p4": "pj-advisor"}},
			want:  Located{Pane: "w1:p2", Label: "pj-l-01"}},
		{name: "free: no home, no target: no pane", agent: "pj-l-01", panes: []port.Pane{agent("w1:p1", "")}, serve: none,
			want: Located{}},
		{name: "serve down: every other live pane labelled for a configured agent, the start goes", agent: "pj-l-01",
			panes: []port.Pane{shell("w1:p3", "pj-l-01"), agent("w1:p1", "pj-lead"), at(agent("w1:p9", ""), "/other")},
			want:  Located{Pane: "w1:p3", Label: "pj-l-01"}},
		{name: "serve down: the agent's label on one live pane, a restart in place goes", agent: "pj-l-01",
			panes: []port.Pane{agent("w1:p3", "pj-l-01"), agent("w1:p1", "notes")},
			want:  Located{Pane: "w1:p3", Runs: true, Proof: "label", Label: "pj-l-01"}},
		{name: "serve down: the target's own agent is the one replaced", agent: "pj-l-01", target: "w1:p8",
			panes: []port.Pane{agent("w1:p8", "")}, want: Located{Pane: "w1:p8"}},
		{name: "serve down: a stop adds no agent, an unaccounted pane does not refuse it", agent: "pj-l-01", stop: true,
			panes: []port.Pane{shell("w1:p3", "pj-l-01"), agent("w1:p1", "notes")},
			want:  Located{Pane: "w1:p3", Label: "pj-l-01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			named := func(label string) bool {
				return label == "pj-lead" || label == "pj-l-01" || label == "pj-advisor" || label == "pj-rev"
			}
			in := locateIn{agent: tc.agent, target: tc.target, adds: !tc.stop, root: "/p", panes: tc.panes, serve: tc.serve,
				holder: tc.holder, holderErr: tc.lookup,
				ours:    func(p port.Pane) bool { return p.Cwd == "" || strings.HasPrefix(p.Cwd, "/p") },
				project: func(p port.Pane) bool { return strings.HasPrefix(p.Cwd, "/p") },
				other:   func(label string) bool { return label != tc.agent && named(label) }}
			if tc.serve == nil {
				in.down = down
			}
			got, err := decide(in)
			var refused port.Refused
			switch {
			case tc.refuse != "" && !errors.As(err, &refused):
				t.Fatalf("decide = %+v, %v; want the refusal %q", got, err, tc.refuse)
			case tc.refuse != "":
				if refused.Line != tc.refuse {
					t.Errorf("refusal:\n got %q\nwant %q", refused.Line, tc.refuse)
				}
			case err != nil:
				t.Fatalf("decide refused: %v; want %+v", err, tc.want)
			case !reflect.DeepEqual(got, tc.want):
				t.Errorf("decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}
