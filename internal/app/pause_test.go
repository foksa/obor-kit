package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// pauseSpecs is the spec check of a dispatch: it holds the admission at pause.
type pauseSpecs struct {
	*concFakes
	pause func()
}

func (s pauseSpecs) Check(context.Context, string, string) (string, error) {
	s.pause()
	return "", nil
}

// dispatchFacts is how many fact dispatch records task has in root's log.
func dispatchFacts(t *testing.T, root, task string) int {
	t.Helper()
	recs, err := event.ReadLog(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == task && rec.Step == event.StepDispatched && rec.Kind == event.KindFact {
			n++
		}
	}
	return n
}

// #3: a pause waits for a dispatch in admission (it holds a.verdicts); the dispatch is admitted before PAUSED returns,
// and a dispatch after the pause is refused with its reason.
func TestPauseWaitsForDispatchAdmission(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	defer r.a.closeLog()
	p := r.proj("pj")
	f := r.fakes["pj"]
	r.a.lanes = &fakeLanes{lanes: []port.Lane{{Name: "L-02", Dir: filepath.Join(t.TempDir(), "L-02"), State: "free"}}}
	fx := f.loopWithSpecs(r.kit)
	entered, release := make(chan struct{}), make(chan struct{})
	fx.Specs = pauseSpecs{f, func() { close(entered); <-release }}
	r.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }

	type result struct {
		reply string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reply, err := r.a.dispatchCall(context.Background(), wire.DispatchReq{Envelope: wire.Envelope{Root: p.root,
			CallID: "d1"}, Task: "T-8", Lane: "L-02", Types: []string{"sonnet"}})
		done <- result{reply, err}
	}()
	<-entered

	paused := make(chan result, 1)
	go func() {
		reply, err := r.a.pauseCall(wire.PauseReq{Envelope: wire.Envelope{Root: p.root, CallID: "p1"}, Why: "quota"})
		paused <- result{reply, err}
	}()
	select {
	case got := <-paused:
		t.Fatalf("PAUSED replied while a dispatch was in admission: %q, %v", got.reply, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	disp := <-done
	pr := <-paused
	if pr.err != nil {
		t.Fatal(pr.err)
	}
	if disp.err != nil {
		t.Fatalf("the dispatch in admission before the pause: %v", disp.err)
	}
	if n := dispatchFacts(t, p.root, "T-8"); n != 1 {
		t.Fatalf("T-8 dispatch facts %d when PAUSED replied, want the admitted dispatch (1)", n)
	}
	t.Logf("pause replied %q after the admitted dispatch", pr.reply)

	_, err := r.a.dispatchCall(context.Background(), wire.DispatchReq{Envelope: wire.Envelope{Root: p.root,
		CallID: "d2"}, Task: "T-9", Lane: "L-02", Types: []string{"sonnet"}})
	if err == nil || !strings.Contains(err.Error(), "paused: quota") {
		t.Errorf("dispatch after PAUSED: %v, want refused with the reason", err)
	}
}
