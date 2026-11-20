package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// merge_preview: a worker's lane holding a task gets the workflow's preview rendered (clean, or the conflicts capped
// with a tail); another role, a lane with no task and no lane are refused before the workflow is asked.
func TestMergePreview(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`)
	f := NewFakeWorkflow("")
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(f)}, nil }
	h.connect()
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	call := func(role, lane string) (string, error) {
		return h.a.mergePreview(wire.MergePreviewReq{Envelope: wire.Envelope{Root: h.root, Role: role, Agent: "pj-l-01",
			CallID: "c1"}, Lane: lane})
	}
	for _, tc := range []struct{ role, lane, want string }{
		{"integrator", "L-01", "merge_preview: the worker's tool, not pj-l-01's (role integrator)"},
		{"worker", "", "merge_preview: the caller's lane is required"},
		{"worker", "L-02", "merge_preview: lane L-02 holds no task in obor serve"},
	} {
		if got, err := call(tc.role, tc.lane); err == nil || err.Error() != tc.want {
			t.Errorf("%s in lane %q: %q, %v; want the refusal %q", tc.role, tc.lane, got, err, tc.want)
		}
	}

	f.SetPreview("L-01", port.Preview{Target: "main", TargetSHA: "0123456789abcdef"})
	got, err := call("worker", "L-01")
	if err != nil || got != "clean: your work merges onto main 0123456\n(a preview: the gate still decides)" {
		t.Errorf("clean: %q, %v; want the clean line naming the target and its short sha", got, err)
	}

	var cs []port.PreviewConflict
	for i := range previewPaths + 2 {
		path := fmt.Sprintf("f%02d.txt", i)
		cs = append(cs, port.PreviewConflict{Path: path, Messages: []string{"CONFLICT (content): Merge conflict in " + path}})
	}
	cs[0].Messages = []string{"m1", "m2", "m3", "m4", "m5", "m6"}
	f.SetPreview("L-01", port.Preview{Target: "main", TargetSHA: "fedcba9876543210", Conflicts: cs})
	got, err = call("worker", "L-01")
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	for _, want := range []string{
		"conflicts with main fedcba9:\n  f00.txt\n    m1\n    m2\n    m3\n    m4\n    … 2 more\n  f01.txt\n" +
			"    CONFLICT (content): Merge conflict in f01.txt\n",
		"  f19.txt\n    CONFLICT (content): Merge conflict in f19.txt\n… 2 more\n" +
			"(a preview: the gate still decides, and the integrator resolves real conflicts)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("conflicts: %q; want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "f20.txt") {
		t.Errorf("conflicts: %q; want the paths past the cap in the tail only", got)
	}
}
