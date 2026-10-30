package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// T-444, T-437: a dispatch starts against a herdr that refuses an agent.start argument with a newline: the start's
// first prompt is the work job's one-line wake, its job result sent (start prompt), nothing typed. The lane type's
// prompt_extra is many lines, as a written note may be: it rides in the job's text, which job_next fetches (the real
// templates/worker-prompt.txt), never on the start.
func TestTypedDispatchStartsOnOneLine(t *testing.T) {
	r := newDispatchRig(t, testkit.Set("lane_types.opus.prompt_extra", "Also read docs/notes.md.\nThen start."))
	cfg, err := config.Load(r.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	r.startWithAgents(&launch.Launcher{Cfg: cfg, Env: r.p.Env(), Kit: kit, H: r.srv.Client(), Out: io.Discard,
		Wait: time.Second, Poll: time.Millisecond, PS: func(...string) ([]byte, error) { return nil, nil }})
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	same(t, r.result("work"), `"sent (start prompt)"`, "work result")
	var start struct {
		Name string   `json:"name"`
		Args []string `json:"args"`
	}
	for _, q := range r.srv.Requests() {
		var p struct {
			Name string   `json:"name"`
			Args []string `json:"args"`
		}
		if q.Method == "agent.start" && json.Unmarshal(q.Params, &p) == nil && p.Name == "pj-o1" {
			start = p
		}
	}
	if start.Name == "" {
		t.Fatalf("no start of pj-o1: %+v", r.srv.Requests())
	}
	text := strings.Join(start.Args, " ")
	if strings.Contains(text, "\n") {
		t.Errorf("the start's args carry a newline, herdr refuses them: %q", start.Args)
	}
	if !strings.Contains(text, wake("T-1/1/work")) || strings.Contains(text, "implement docs/tasks") {
		t.Errorf("the start's first prompt is not the work job's wake alone:\n%s", text)
	}
	same(t, len(r.prompts()), 0, "herdr prompts")
	jr := r.workerNext("n1")
	for _, want := range []string{"implement the spec of T-1 exactly", "Also read docs/notes.md.\nThen start."} {
		if !strings.Contains(jr.Text, want) {
			t.Errorf("the fetched work job has no %q:\n%s", want, jr.Text)
		}
	}
}

// startWithAgents starts the app as start does, with agents as its loop's Agents: a real launcher, so the start's
// args reach the fake herdr (T-444).
func (r *loopRig) startWithAgents(agents app.Agents) {
	r.t.Helper()
	panes := herdr.Panes{S: r.srv.Client(), PS: func(context.Context) ([]byte, error) { return []byte("2 1 2\n"), nil }}
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.p.Root, Tick: 20 * time.Millisecond, Retry: 5 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Refresh: func(err error) bool {
			var e *herdr.Error
			return errors.As(err, &e) && e.Code == "refresh"
		}, Loop: func(string) (*app.Loop, error) {
			fx := &app.Loop{Scratch: r.scratch, Agents: agents, Typer: panes}
			r.disp.loop(fx, panes)
			return fx, nil
		}},
		panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
	r.sock = (&rig{t: r.t, app: r.app}).listen()
}
