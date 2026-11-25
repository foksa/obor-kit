package rex

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"obor/internal/app/layout"
	"obor/internal/app/port"
)

// recordedView is the recorded session.view (testdata/session-view.json): session proj with a lead window holding
// pj-lead and a lanes window holding pj-l-01 and an unlabelled block (Rex's "shell").
func recordedView(t *testing.T) view {
	t.Helper()
	b, err := os.ReadFile("testdata/session-view.json")
	if err != nil {
		t.Fatal(err)
	}
	var r viewAnswer
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	v := r.view()
	if len(v.Windows) != 2 || v.Windows[0].Label != "lead" || len(v.Windows[0].Blocks) != 1 || v.Windows[1].Label != "lanes" ||
		len(v.Windows[1].Blocks) != 2 {
		t.Fatalf("precondition: the recorded view %+v, want lead (1 block) and lanes (2 blocks)", v)
	}
	return v
}

// kindOf is the project's kinds by label: its lead and its lanes' agents.
func kindOf(l string) port.PaneKind {
	switch l {
	case "pj-lead":
		return port.KindLead
	case "pj-l-01", "pj-l-02":
		return port.KindLane
	}
	return ""
}

// The placement table on the recorded session: app/layout decides (its rules' rows are there) from the windows'
// labels and their blocks' kinds by label, an unlabelled block aside; the call carries the answer out: a new window of
// the answer's name, unfocused, or a split after the last block of the window it names, its direction alternating.
func TestPlaceTable(t *testing.T) {
	v := recordedView(t)
	lanes := v.Windows[1]
	for _, c := range []struct {
		name   string
		p      port.Placement
		max    int
		method string
		want   map[string]any // the params beside session_id, focus and layout
	}{
		{"a lane, the lanes window has room: a split after its last block", port.Placement{Kind: port.KindLane}, 4,
			"session.new_split", map[string]any{"anchor_id": lanes.Blocks[1].ID, "direction": "vertical", "side": "after"}},
		{"a lane, the lanes window full: a new lanes window", port.Placement{Kind: port.KindLane}, 2,
			"session.new_window", map[string]any{"window_label": "lanes"}},
		{"a role: a new roles window", port.Placement{Kind: port.KindRole}, 4,
			"session.new_window", map[string]any{"window_label": "roles"}},
		{"the lead, its window taken: a new lead window", port.Placement{Kind: port.KindLead}, 4,
			"session.new_window", map[string]any{"window_label": "lead"}},
		{"a lane --new-tab review: a new window review", port.Placement{Kind: port.KindLane, Group: "review"}, 4,
			"session.new_window", map[string]any{"window_label": "review"}},
		{"a lane --tab lanes: that window", port.Placement{Kind: port.KindLane, In: "lanes"}, 4,
			"session.new_split", map[string]any{"anchor_id": lanes.Blocks[1].ID, "direction": "vertical", "side": "after"}},
		{"a lane --tab lead: refused", port.Placement{Kind: port.KindLane, In: "lead"}, 4, "", nil},
	} {
		p := c.p
		p.Project = port.PlaceProject{Name: "proj", MaxPanes: c.max, KindOf: kindOf}
		ans := layout.Place(ask(p, v))
		if c.method == "" {
			if ans.Refused == "" {
				t.Errorf("%s: answer %+v, want a refusal", c.name, ans)
			}
			continue
		}
		if ans.Refused != "" {
			t.Errorf("%s: refused: %s", c.name, ans.Refused)
			continue
		}
		node := shellBlock(blockEnv{Unset: []string{"OBOR_ROLE"}, Shell: "/bin/zsh"}, "/proj/lanes/x", []string{"A=1"})
		method, params, _ := placeCall("session:s", ans, v, node)
		if n, ok := params["layout"].(blockNode); method != c.method || params["session_id"] != "session:s" || params["focus"] != false ||
			!ok || !slices.Equal(n.Block.Options.Command, node.Block.Options.Command) {
			t.Errorf("%s: %s %v; want %s in session:s, unfocused, with the block", c.name, method, params, c.method)
		}
		for k, want := range c.want {
			if params[k] != want {
				t.Errorf("%s: %s = %v, want %v", c.name, k, params[k], want)
			}
		}
	}
}

// The block a placement makes: the shell flavor with no shell of Rex's around the scrubbed command, in the
// placement's cwd.
func TestShellBlock(t *testing.T) {
	b, err := json.Marshal(shellBlock(blockEnv{Unset: []string{"OBOR_ROLE"}, Shell: "/bin/zsh"}, "/proj", []string{"A=1"}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"block":{"flavor":"com.superlogical.terminal.shell","options":{"shell":"none","command":["/usr/bin/env","-u","OBOR_ROLE","A=1","/bin/zsh","-l"],"cwd":"/proj"}}}`
	if string(b) != want {
		t.Errorf("block %s, want %s", b, want)
	}
}
