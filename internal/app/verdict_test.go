package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app"
)

// verdictRig is the rig with an integrator (pj-int) configured, T-1 submitted and its review job (attempt 2, patch
// "p1") in the log before the app starts, and the app socket listening. Its merge loop is on (an integrator role) but has
// no adapters: the verdicts are answered, the loop does not act on them.
func verdictRig(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t)
	write(t, filepath.Join(r.root, ".obor", "config.json"),
		`{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
	r.loop = func(string) (*app.Loop, error) { return nil, errors.New("no adapters") }
	data, _ := json.Marshal(app.JobData{Patch: "p1"})
	if err := app.WriteLog(r.root, r.clock.Now(),
		app.Record{Task: "T-1", Attempt: 2, Step: "submitted", Kind: app.KindFact, Data: []byte(`{"lane":"O-01"}`)},
		app.Record{Task: "T-1", Attempt: 2, Step: app.StepReview, Kind: app.KindIntent, Class: app.ClassPrompt, Data: data},
	); err != nil {
		t.Fatal(err)
	}
	r.start()
	return r, r.listen()
}

// listen answers the app socket of the running app on a new socket path.
func (r *rig) listen() string {
	r.t.Helper()
	dir, err := os.MkdirTemp("", "app") // short: a unix socket path is limited
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { ln.Close() })
	go r.app.Listen(ln)
	return sock
}

// call is an integrator call on T-1 from pj-int; edit changes it.
func (r *rig) call(id string, edit ...func(*app.Call)) app.Call {
	c := app.Call{Root: r.root, Agent: "pj-int", Role: "integrator", CallID: id, Task: "T-1", Job: "T-1/2/review",
		Patch: "p1", Note: "why"}
	for _, e := range edit {
		e(&c)
	}
	return c
}

// record is the log's record of kind and step (zero when none).
func (r *rig) record(kind, step string) app.Record {
	for _, rec := range r.records() {
		if rec.Kind == kind && rec.Step == step {
			return rec
		}
	}
	return app.Record{}
}

// verdicts is the log's verdict records as "kind step".
func (r *rig) verdicts() []string {
	var out []string
	for _, rec := range r.records() {
		if strings.Contains(rec.Step, ".c") {
			out = append(out, rec.Kind+" "+rec.Step)
		}
	}
	return out
}

func TestVerdictRefusals(t *testing.T) {
	r, sock := verdictRig(t)
	ctx := context.Background()
	for _, c := range []struct {
		name, method string
		call         app.Call
		want         string
	}{
		{"a worker", app.MethodAccept, r.call("c1", func(c *app.Call) { c.Agent, c.Role = "pj-o-01", "worker" }),
			"only the integrator (pj-int) gives verdicts, not pj-o-01 (role worker)"},
		{"the integrator's role with another name", app.MethodAccept, r.call("c1", func(c *app.Call) { c.Agent = "pj-lead" }),
			"only the integrator"},
		{"no role", app.MethodReject, r.call("c1", func(c *app.Call) { c.Role = "" }), "only the integrator"},
		{"a stale job", app.MethodAccept, r.call("c1", func(c *app.Call) { c.Job = "T-1/1/review" }),
			"accept: stale job T-1/1/review (T-1's job is T-1/2/review)"},
		{"a task with no job", app.MethodSendback, r.call("c1", func(c *app.Call) { c.Task, c.Job = "T-2", "" }),
			"sendback: no job open for T-2"},
		{"a stale patch", app.MethodAccept, r.call("c1", func(c *app.Call) { c.Patch = "p0" }),
			"stale: re-read the candidate (T-1's patch is p1, not p0)"},
		{"resolved on a review job", app.MethodResolved, r.call("c1"), "is a review job"},
		{"a sendback without why", app.MethodSendback, r.call("c1", func(c *app.Call) { c.Note = " " }), "say why"},
		{"no call id", app.MethodAccept, r.call(""), "a call id and a task are required"},
		{"another project", app.MethodAccept, r.call("c1", func(c *app.Call) { c.Root = "/elsewhere" }),
			"no served project at /elsewhere"},
	} {
		reply, err := app.Verdict(ctx, sock, c.method, c.call)
		if err == nil || !strings.Contains(err.Error(), c.want) || reply != "" {
			t.Errorf("%s: %q, %v; want the error %q", c.name, reply, err, c.want)
		}
	}
	eq(t, r.verdicts(), nil, "verdict records after refusals")

	// The refused call id c1 is not taken: the integrator's own call with it is answered.
	reply, err := app.Verdict(ctx, sock, app.MethodAccept, r.call("c1"))
	if err != nil || reply != "ACCEPT T-1 (job T-1/2/review)" {
		t.Fatalf("accept after refusals: %q, %v", reply, err)
	}
}

func TestVerdictRepeatedCallIDGetsStoredReply(t *testing.T) {
	r, sock := verdictRig(t)
	ctx := context.Background()
	reply, err := app.Verdict(ctx, sock, app.MethodSendback, r.call("c7", func(c *app.Call) { c.Job = "" }))
	if err != nil || reply != "SENDBACK T-1 (job T-1/2/review)" {
		t.Fatalf("sendback (the current job): %q, %v", reply, err)
	}
	eq(t, r.verdicts(), []string{"intent sendback.c7", "result sendback.c7"}, "recorded once, a fact under Log.Do")
	var d struct{ Job, Caller, Note string }
	intent := r.record(app.KindIntent, "sendback.c7")
	if err := json.Unmarshal(intent.Data, &d); err != nil || d.Job != "T-1/2/review" || d.Caller != "pj-int" ||
		d.Note != "why" {
		t.Errorf("sendback intent data = %s (%v)", intent.Data, err)
	}

	// The job is answered: the same call again gets the stored reply and records nothing; a new call id is refused.
	reply, err = app.Verdict(ctx, sock, app.MethodSendback, r.call("c7"))
	if err != nil || reply != "SENDBACK T-1 (job T-1/2/review)" {
		t.Errorf("repeated call id: %q, %v; want the stored reply", reply, err)
	}
	if _, err := app.Verdict(ctx, sock, app.MethodAccept, r.call("c8")); err == nil ||
		!strings.Contains(err.Error(), "no job open for T-1") {
		t.Errorf("a second verdict on the answered job: %v", err)
	}
	eq(t, r.verdicts(), []string{"intent sendback.c7", "result sendback.c7"}, "nothing more recorded")

	// After a restart (replay) the call id still gets its reply, and the job stays answered.
	r.stop()
	r.start()
	sock = r.listen()
	reply, err = app.Verdict(ctx, sock, app.MethodSendback, r.call("c7"))
	if err != nil || reply != "SENDBACK T-1 (job T-1/2/review)" {
		t.Errorf("repeated call id after replay: %q, %v", reply, err)
	}
	if _, err := app.Verdict(ctx, sock, app.MethodReject, r.call("c9")); err == nil ||
		!strings.Contains(err.Error(), "no job open for T-1") {
		t.Errorf("a verdict on a job answered before the restart: %v", err)
	}
}

func TestVerdictServeNotRunning(t *testing.T) {
	_, err := app.Verdict(context.Background(), filepath.Join(t.TempDir(), "none.sock"), app.MethodAccept, app.Call{})
	if err == nil || !strings.Contains(err.Error(), "obor serve is not running") {
		t.Errorf("verdict without obor serve: %v", err)
	}
}

// agents records the restarts asked of it.
type agents struct{ restarts []app.Launch }

func (a *agents) Launch(_ context.Context, req app.Launch) (app.Started, error) {
	a.restarts = append(a.restarts, req)
	return app.Started{Line: "STARTED " + req.Agent}, nil
}

func (a *agents) Stop(context.Context, string, string, bool) (string, error) { return "", nil }

func TestStartIntegrator(t *testing.T) {
	r, _ := verdictRig(t)
	ag := &agents{}
	p := r.f.Agent("w1:p9", "pj-int", "idle", r.root)
	p.Workspace, p.Label = "w1", "pj-int"
	r.stop()
	r.start() // its snapshot has the integrator
	if n := len(r.app.Status().Project.Agents); n != 3 {
		t.Fatalf("precondition: %d agents seen, want 3 with the integrator", n)
	}
	s, err := app.StartIntegrator(r.app, context.Background(), ag, r.root, app.Launch{})
	line := s.Line
	if err != nil || line != "running" || len(ag.restarts) != 0 {
		t.Fatalf("integrator running: %q, %v, restarts %+v; want none", line, err, ag.restarts)
	}

	r.f.Remove("w1:p9")
	r.stop()
	r.start()
	if n := len(r.app.Status().Project.Agents); n != 2 {
		t.Fatalf("precondition: %d agents seen, want 2 without the integrator", n)
	}
	s, err = app.StartIntegrator(r.app, context.Background(), ag, r.root, app.Launch{})
	line = s.Line
	if err != nil || line != "STARTED pj-int" || len(ag.restarts) != 1 || ag.restarts[0] != (app.Launch{Agent: "pj-int"}) {
		t.Errorf("integrator not running: %q, %v, restarts %+v; want one start of pj-int", line, err, ag.restarts)
	}
}
