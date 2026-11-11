package app

import (
	"encoding/json"
	"reflect"
	"testing"
	"testing/synctest"

	"obor/internal/app/wire"
)

// where is serve's identity table as the socket's where request answers it, one row per state, each asked through the
// in-memory socket in a synctest bubble: the agent's pane and proof, and the agent each named pane runs.
func TestWhere(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(h *harness)
		agent string
		panes []string
		want  wire.WhereReply
	}{
		// The fixture of TestIdentSessionBeatsLabel: the lead's recorded session is w1:p1's, the pane is labelled
		// pj-advisor. The lead is identified by its session there, under the other label.
		{"session beats label", func(h *harness) {
			h.withLead()
			h.pane("w1:p1").Label = "pj-advisor"
			h.sids(map[string]string{"pj-lead": "s1"})
		}, "pj-lead", []string{"w1:p1"},
			wire.WhereReply{State: wire.WhereIdentified, Pane: "w1:p1", Source: "session", Label: "pj-advisor",
				Runs: map[string]string{"w1:p1": "pj-lead"}}},
		// The same fixture asked for the label's agent: no proof, and the pane its label names runs the lead.
		{"a named pane runs another agent", func(h *harness) {
			h.withLead()
			h.pane("w1:p1").Label = "pj-advisor"
			h.sids(map[string]string{"pj-lead": "s1"})
		}, "pj-advisor", []string{"w1:p1", "w1:p2", "w9:p9"},
			wire.WhereReply{State: wire.WhereNone, Why: "no proof",
				Runs: map[string]string{"w1:p1": "pj-lead", "w1:p2": "", "w9:p9": ""}}},
		// serve's own start of pj-l-01 in w1:p1 (terminal t1); the pane shows no session yet.
		{"start row", func(h *harness) {
			h.pane("w1:p1").Session = ""
			h.started("pj-l-01", "w1:p1", "t1", "s1")
		}, "pj-l-01", nil,
			wire.WhereReply{State: wire.WhereIdentified, Pane: "w1:p1", Source: "start", Label: "pj-l-01"}},
		// No session, no start: the pane's label is the only proof.
		{"label only", func(h *harness) {
			h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
		}, "pj-l-01", []string{"w1:p1"},
			wire.WhereReply{State: wire.WhereIdentified, Pane: "w1:p1", Source: "label", Label: "pj-l-01",
				Runs: map[string]string{"w1:p1": "pj-l-01"}}},
		// Two live panes labelled pj-l-01, no stronger proof: neither runs it.
		{"ambiguous", func(h *harness) {
			h.pane("w1:p1").Session = ""
			h.snap.Panes = append(h.snap.Panes, h.twin())
		}, "pj-l-01", []string{"w1:p1", "w1:p3"},
			wire.WhereReply{State: wire.WhereAmbiguous, Panes: []string{"w1:p1", "w1:p3"},
				Why:  "not identified: panes w1:p1 and w1:p3 run pj-l-01 (close or relabel one)",
				Runs: map[string]string{"w1:p1": "", "w1:p3": ""}}},
		// The terminal's stream ended after the label fixture was identified: the table is not the answer.
		{"not connected", func(h *harness) {
			h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
		}, "pj-l-01", []string{"w1:p1"},
			wire.WhereReply{State: wire.WhereNotConnected}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				h := identInBubble(t)
				tc.setup(h)
				h.connect()
				if tc.want.State == wire.WhereNotConnected {
					if got := h.where(tc.agent); got != "identified w1:p1 label" {
						t.Fatalf("precondition: before the stream ends, whereIs is %q", got)
					}
					h.a.mu.Lock()
					h.a.connected = false // as Serve, when the terminal's stream ends
					h.a.mu.Unlock()
				}
				reply, err := ServeMem(t, h.a).Call(t, &wire.WhereReq{Envelope: wire.Envelope{Root: h.root, CallID: "c1"},
					Name: tc.agent, Panes: tc.panes})
				if err != nil {
					t.Fatal(err)
				}
				var got wire.WhereReply
				if err := json.Unmarshal([]byte(reply), &got); err != nil {
					t.Fatalf("reply %q: %v", reply, err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("where %s %q:\n got %+v\nwant %+v", tc.agent, tc.panes, got, tc.want)
				}
			})
		})
	}
}

// where refuses a call that names no agent, and a project serve does not serve: neither is "no agent".
func TestWhereRefuses(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := identInBubble(t)
		h.connect()
		s := ServeMem(t, h.a)
		for _, c := range []struct {
			req  wire.WhereReq
			want string
		}{
			{wire.WhereReq{Envelope: wire.Envelope{Root: h.root, CallID: "c1"}}, "where: no agent named"},
			{wire.WhereReq{Envelope: wire.Envelope{Root: h.root + "-other", CallID: "c2"}, Name: "pj-l-01"},
				"where: no served project at " + h.root + "-other"},
		} {
			if _, err := s.Call(t, &c.req); err == nil || err.Error() != c.want {
				t.Errorf("where %+v: err %v, want %q", c.req, err, c.want)
			}
		}
	})
}

// identInBubble is newIdentHarness inside a synctest bubble: its project's database is closed when the test ends, so
// no goroutine of the bubble outlives it.
func identInBubble(t *testing.T) *harness {
	h, _ := newIdentHarness(t)
	t.Cleanup(h.a.closeLog)
	return h
}
