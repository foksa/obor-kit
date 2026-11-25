package app

import (
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/testkit"
)

// A lane wait is not a bare "no lane": it names each lane that is dirty-free, its uncommitted paths too, and nothing
// about the lanes that hold a task. A lane with more paths than a wait names says the count, and a lane whose paths
// the table did not read is named without them. When every model of the request is paused the pause is the reason,
// no lane named.
func TestLaneWaitNamesDirtyFreeLanes(t *testing.T) {
	ls := []port.Lane{
		{Name: "L-01", State: port.LaneDirtyFree, Paths: []string{"src/x.ts", "out/"}},
		{Name: "L-02", State: port.LaneWorking, Task: "T-9", Paths: []string{"work.txt"}},
		{Name: "L-03", State: port.LaneDirtyFree, Paths: []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}},
		{Name: "L-04", State: port.LaneDirtyFree},
	}
	e := noLaneError{task: "T-1", level: "medium", types: []string{"sonnet", "deepseek"}, ls: ls}
	want := LaneWaitFree + " (L-01 dirty-free: src/x.ts, out/; L-03 dirty-free: a.txt, b.txt, c.txt, +2 more; " +
		"L-04 dirty-free)"
	if got := e.laneWait(); got != want {
		t.Errorf("laneWait = %q, want %q", got, want)
	}
	if got, want := e.Error(), "dispatch T-1: "+want; got != want {
		t.Errorf("Error = %q, want %q", got, want)
	}

	paused := noLaneError{task: "T-1", level: "medium", types: []string{"sonnet", "deepseek"}, ls: ls,
		paused: map[string]string{"sonnet": "sonnet paused: quota (until 18:00)",
			"deepseek": "deepseek paused: at its max 4"}}
	if got, want := paused.laneWait(), "sonnet paused: quota (until 18:00), deepseek paused: at its max 4"; got != want {
		t.Errorf("every model paused: laneWait = %q, want the pauses %q", got, want)
	}
}

// resolveLevel gives a request with no --model its spec's level's models (the config's list, the spec's Model: refs
// first); --model keeps its own path. A stored spec's Model: ref the config no longer has, or a level none of whose
// models the config knows, is refused, the request left as it was. (Wired to dispatch, nothing recorded on a
// refusal: TestDispatchRefusals' "no level and no models".)
func TestResolveLevel(t *testing.T) {
	t.Parallel()
	cfgOf := func(t *testing.T, edits ...func(map[string]any)) *config.Config {
		t.Helper()
		p := testkit.NewDir(t)
		p.Config(edits...)
		return p.Cfg()
	}
	noHaiku := func(c map[string]any) { delete(c["agents"].(map[string]any)["models"].(map[string]any), "haiku") }
	const named = "# T-1 — Named\n\nDifficulty: medium\nModel: haiku\n"
	for _, c := range []struct {
		name, spec string
		cfg        []func(map[string]any)
		types      []string // --model ("": none)
		level      string   // want
		want       []string // the request's models after
		refusal    []string // the refusal's parts ("": none)
	}{
		{name: "the spec's Model: refs first, then the level's", spec: "# T-1 — A\n\nDifficulty: medium\nModel: haiku:low, sonnet\n",
			level: "medium", want: []string{"haiku:low", "sonnet", "deepseek"}},
		{name: "the config's level", spec: "# T-1 — A\n\nDifficulty: medium\n",
			cfg: []func(map[string]any){testkit.Set("agents.difficulty.medium", []string{"luna:low"})}, level: "medium",
			want: []string{"luna:low"}},
		{name: "no model of the level: the config path", spec: "# T-1 — A\n\nDifficulty: hard\n",
			cfg:     []func(map[string]any){testkit.Set("agents.difficulty.hard", []string{})},
			refusal: []string{"no model of level hard is known to this project (obor config get .agents.difficulty)"}},
		{name: "a stored spec's Model: ref the config lost", spec: named, cfg: []func(map[string]any){noHaiku},
			refusal: []string{`"Model: haiku"`, "is not a model", `"haiku": {"kind": "claude", "id": "haiku"}`}},
		{name: "--model over the spec's lost ref", spec: named, cfg: []func(map[string]any){noHaiku},
			types: []string{"sonnet"}, want: []string{"sonnet"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			req := wire.DispatchReq{Task: "T-1", Types: slices.Clone(c.types)}
			err := resolveLevel(cfgOf(t, c.cfg...), &req, spec.Parse(c.spec))
			if c.refusal != nil {
				if err == nil {
					t.Fatalf("resolveLevel = nil (%s %v); want a refusal with %q", req.Level, req.Types, c.refusal)
				}
				for _, part := range c.refusal {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("refusal %q has no %q", err, part)
					}
				}
				if req.Level != "" || req.Types != nil {
					t.Errorf("a refusal changed the request: level %q, models %v", req.Level, req.Types)
				}
				return
			}
			if err != nil || req.Level != c.level || !slices.Equal(req.Types, c.want) {
				t.Errorf("resolveLevel = %v, level %q, models %v; want level %q, models %v", err, req.Level, req.Types,
					c.level, c.want)
			}
		})
	}
}
