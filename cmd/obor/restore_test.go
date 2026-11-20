package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/store"
)

// fakeApp is serve's App as its launchers see it (serveApp): each answer is counted in asked, as "<answer> <what>".
type fakeApp struct {
	mu    sync.Mutex
	asked []string
	jobs  map[string]string // agent → the open job RestartJob answers ("": none)
	open  bool              // OpenWork's answer
}

func (f *fakeApp) ask(format string, args ...any) {
	f.mu.Lock()
	f.asked = append(f.asked, fmt.Sprintf(format, args...))
	f.mu.Unlock()
}

// count is how many answers began with prefix.
func (f *fakeApp) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.asked {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeApp) Where(_ context.Context, req wire.WhereReq) (wire.WhereReply, error) {
	f.ask("where %s %s", req.Root, req.Name)
	return wire.WhereReply{State: wire.WhereNone, Why: "no proof"}, nil
}

func (f *fakeApp) Sessions(root, agent string) (store.Session, bool) {
	f.ask("sessions %s %s", root, agent)
	return store.Session{}, false
}

func (f *fakeApp) Report(root string, s store.Session) error {
	f.ask("report %s %s %s", root, s.Agent, s.ID)
	return nil
}

func (f *fakeApp) Started(root, agent string) error {
	f.ask("started %s %s", root, agent)
	return nil
}

func (f *fakeApp) RestartJob(root, agent, role, lane string) (string, error) {
	f.ask("job %s %s %s %s", root, agent, role, lane)
	return f.jobs[agent], nil
}

func (f *fakeApp) OpenWork(agent, role string) (bool, error) {
	f.ask("openwork %s %s", agent, role)
	return f.open, nil
}

// noApp is mergeLoop's self for a test that starts no agent.
func noApp() serveApp { return &fakeApp{} }

// Serve's launchers get where, the session, the report and the start's confirmation from the App in process, each for
// the launcher's own project: the merge loop's (dispatch's starts, the merged worker's stop) and the restore's.
func TestServeLaunchersAnswerFromTheApp(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	app := &fakeApp{}
	loop, err := mergeLoop(p.Env(), port.Terminal{}, &serveEnd{stop: func() {}}, &kindCache{}, func() serveApp { return app })(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	merge, ok := loop.Agents.(*launch.Launcher)
	if !ok {
		t.Fatalf("the merge loop's agents are %T, want serve's launcher", loop.Agents)
	}
	for name, l := range map[string]*launch.Launcher{
		"merge loop": merge,
		"restore":    serveLauncher(cfg, p.Env(), nil, io.Discard, app),
	} {
		app.mu.Lock()
		app.asked = nil
		app.mu.Unlock()
		if l.Where == nil || l.Sessions == nil || l.Report == nil || l.Started == nil {
			t.Fatalf("%s: Where %v, Sessions %v, Report %v, Started %v: each must be set (nil dials serve's own socket)",
				name, l.Where != nil, l.Sessions != nil, l.Report != nil, l.Started != nil)
		}
		ctx := context.Background()
		if _, err := l.Where(ctx, wire.WhereReq{Envelope: wire.Envelope{Root: p.Root}, Name: "pj-l-01"}); err != nil {
			t.Fatal(err)
		}
		l.Sessions("pj-l-01")
		if err := l.Report(ctx, store.Session{Agent: "pj-l-01", ID: "sid-1"}); err != nil {
			t.Fatal(err)
		}
		if err := l.Started(ctx, "pj-l-01"); err != nil {
			t.Fatal(err)
		}
		want := []string{"where " + p.Root + " pj-l-01", "sessions " + p.Root + " pj-l-01",
			"report " + p.Root + " pj-l-01 sid-1", "started " + p.Root + " pj-l-01"}
		if !slices.Equal(app.asked, want) {
			t.Errorf("%s: the App was asked %q, want %q", name, app.asked, want)
		}
		if l.Job != nil && name == "merge loop" {
			t.Errorf("the merge loop's launcher has a Job: serve's own starts carry their job's wake")
		}
	}
}
