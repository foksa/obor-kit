package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// goldenStatus is a project with a task of every kind the view knows: one on a lane at work (T-01), one merging
// (T-02), one waiting for a lane (T-03), one waiting --after (T-04), one needing you on no lane (T-05) and one only
// specified (T-06), with serve's answer for it; and the view's JSON and text of it at an hour after now.
func goldenStatus(t *testing.T) (string, string) {
	t.Helper()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	o2 := p.Lane("L-02", "task/T-02")
	p.Lane("L-03", "")
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01")
	p.Commit(o2)
	p.Submit("L-02")
	for _, id := range []string{"T-01", "T-02", "T-03", "T-04", "T-05", "T-06"} {
		p.Spec(id, "# "+id+": the "+id+" title\n\nbody\n")
	}
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true,
		Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l-01", Agent: "claude", Lane: "L-01", Task: "T-01",
			Status: "working"}},
		Tasks: []wire.TaskStatus{
			{Task: "T-01", Lane: "L-01", Attempt: 1, State: "dispatched", Open: true, Phase: wire.PhaseWorking, Effort: "high",
				Job: "T-01/1/work", Delivered: true},
			{Task: "T-02", Lane: "L-02", Attempt: 2, State: "accepted", Open: true, Phase: wire.PhasePublish,
				Stage: wire.StagePublish, Job: "T-02/2/review"},
			{Task: "T-03", State: "waiting", Open: true, Phase: wire.PhaseWaiting, Level: "medium", Types: []string{"opus"},
				Why: app.LaneWaitFree},
			{Task: "T-04", State: "waiting", Open: true, Phase: wire.PhaseWaiting, After: []string{"T-02"}},
			{Task: "T-05", Lane: "L-09", Attempt: 1, State: "needs-you", Open: true, Phase: wire.PhaseNeedsYou},
		},
		ScratchQueue: []wire.ScratchWait{{Task: "T-02", WaitsOn: "T-01", Why: wire.ScratchHeld}},
		Facts:        []event.Record{fact(t, 1, "T-05", event.FactData{Lane: "L-09", Reason: "asks: which file?"})},
		Answers:      map[int64]string{1: `obor resume T-05 "<answer>"`},
		Idle:         &wire.IdleStatus{Since: 100, Last: "T-00", Acked: true, Why: "done"},
		ReviewRequest: &wire.ReviewRequestStatus{After: []string{"T-02"}, Waiting: []string{"T-02"}, Note: "after",
			By: "pj-lead", Since: 200},
	}}
	now := time.Now().Add(time.Hour)
	st, text := statusOf(t, p, ss, nil, now)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return scrubGolden(string(b), p), scrubGolden(text, p)
}

// scrubGolden replaces p's paths and the clock's numbers and times in s with fixed words.
func scrubGolden(s string, p *testkit.Project) string {
	s = strings.ReplaceAll(s, p.Base, "$BASE")
	s = strings.ReplaceAll(s, p.Root, "$ROOT")
	s = regexp.MustCompile(`"(since|done_age|next|ts|at)": "?[0-9TZ:+-]+"?`).ReplaceAllString(s, `"$1": 0`)
	s = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`).ReplaceAllString(s, "TS")
	s = regexp.MustCompile(` +\d+[smhd]  \(T-`).ReplaceAllString(s, "  age  (T-")
	s = regexp.MustCompile(`in \d+[smhd]\)`).ReplaceAllString(s, "in N)")
	s = regexp.MustCompile(`at \d\d:\d\d`).ReplaceAllString(s, "at HH:MM")
	return s
}

// updateGolden is OBOR_UPDATE_GOLDEN set, read before TestMain's testkit.Isolate clears obor's environment.
var updateGolden = os.Getenv("OBOR_UPDATE_GOLDEN") != ""

// T-883: `obor status --json` and its text, against testdata/status.json.golden and status.txt.golden
// (OBOR_UPDATE_GOLDEN=1 rewrites them).
func TestStatusGolden(t *testing.T) {
	js, text := goldenStatus(t)
	for _, g := range []struct{ file, got string }{{"status.json.golden", js}, {"status.txt.golden", text}} {
		path := filepath.Join("testdata", g.file)
		if updateGolden {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(g.got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal([]byte(g.got), want) {
			t.Errorf("%s differs (OBOR_UPDATE_GOLDEN=1 rewrites it); got:\n%s", g.file, g.got)
		}
	}
}
