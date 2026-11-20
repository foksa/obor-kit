package herdr_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/herdr/herdrtest"
)

const startWake = "[obor job T-1/1/work] call the obor job_next tool"

// lines collects a start's Say lines.
type lines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lines) say(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format+"\n", args...)
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startOf is a start of a claude named pj-l-01 in pane with prompt, its lines into said, polling every millisecond.
func startOf(pane, prompt string, said *lines) port.AgentStart {
	return port.AgentStart{Pane: pane, Label: "pj-l-01", Kind: "claude", Prompt: prompt, Poll: time.Millisecond,
		Bound: 10 * time.Second, Say: said.say}
}

// A start with a first prompt that herdr gave up waiting on (its timeout) while the agent works on the prompt is a
// start: herdr's error and the start said, nothing renamed, herdr keeping no name. An agent blocked at herdr's
// timeout, or a start without a first prompt whose agent works, is a failed start herdr had accepted.
func TestStartBusyOnFirstPrompt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, prompt, status string
		ok                   bool
	}{
		{"wake, working at herdr's timeout: started", startWake, "working", true},
		{"wake, blocked at herdr's timeout: failed", startWake, "blocked", false},
		{"no wake, working at herdr's timeout: failed as before", "", "working", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := herdrtest.New()
			f.Shell("w1:p3", "pj-l-01", "")
			f.Set("w1:p3", func(p *herdrtest.Pane) { p.OnStart = c.status })
			srv := herdrtest.Serve(t, f)
			var said lines
			s := startOf("w1:p3", c.prompt, &said)
			s.Poll = 10 * time.Second // let herdr's immediate timeout win before the start polls the new agent
			a, err := srv.Client().Start(ctx, s)
			if n := count(srv, "agent.start"); n != 1 {
				t.Fatalf("precondition: one start, got %d", n)
			}
			if got := said.String() + fmt.Sprint(err); !strings.Contains(got, "(timeout)") {
				t.Fatalf("precondition: herdr gave up: said %q, error %v", said.String(), err)
			}
			if n := count(srv, "agent.rename"); n != 0 {
				t.Errorf("%d agent.rename calls, want none", n)
			}
			if !c.ok {
				if err == nil || !port.Accepted(err) || !port.TimedOut(err) {
					t.Fatalf("start = %+v, %v; want a failed start herdr accepted, timed out", a, err)
				}
				return
			}
			if err != nil || a.ID != "w1:p3" || a.Status != "working" {
				t.Fatalf("start = %+v, %v; want the agent working in w1:p3", a, err)
			}
			if !strings.Contains(said.String(), "pj-l-01 runs in w1:p3 (working on its first prompt): started") {
				t.Errorf("said %q, want the start said", said.String())
			}
			if f.Pane("w1:p3").Name != "" {
				t.Errorf("herdr's name %q, want none kept (the start stands)", f.Pane("w1:p3").Name)
			}
		})
	}
}

// A first-prompt start (a review's fresh start on its brief, a worker dispatch's on its first prompt): herdr answers
// at once with the launch pending and keeps it pending while the agent works on the prompt, past the start's bound.
// Started on the agent working, read after the start, not failed.
func TestStartWhileTheLaunchStaysPending(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, pane, kind string }{
		{"a codex reviewer", "w1:p7", "codex"},
		{"a claude worker", "w1:p3", "claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := herdrtest.New()
			p := f.Shell(c.pane, "pj-l-01", "")
			// pending for every read: herdr's own bound is past the start's
			f.Set(p.ID, func(p *herdrtest.Pane) { p.StartPending, p.PendingStatus = 1<<30, "working" })
			srv := herdrtest.Serve(t, f)
			var said lines
			s := startOf(c.pane, startWake, &said)
			s.Kind = c.kind
			a, err := srv.Client().Start(ctx, s)
			if n := count(srv, "agent.start"); n != 1 {
				t.Fatalf("precondition: one start, got %d", n)
			}
			if err != nil || a.Status != "working" {
				t.Fatalf("a first-prompt start whose agent works was not started: %+v, %v\n%s", a, err, said.String())
			}
			var pending bool
			var kind, status string
			f.Locked(func() { pending, kind, status = p.LaunchPending, p.Kind, p.Status })
			if !pending || kind != c.kind || status != "working" {
				t.Fatalf("precondition: herdr still lists the launch pending, %s working; got pending %t, %s %s", c.kind, pending, kind, status)
			}
			// A poll read of the pane runs alongside the start, so a loaded machine can record one before the start:
			// the clause is a read after the start, found by the last read's position, never the first.
			reqs := srv.Calls()
			start, read := -1, -1
			for i, r := range reqs {
				switch {
				case strings.HasPrefix(r, "agent.start ") && start < 0:
					start = i
				case strings.HasPrefix(r, "agent.get ") && strings.Contains(r, "target="+c.pane):
					read = i
				}
			}
			if start < 0 || read < start {
				t.Errorf("started without a listing read after the start: %q", reqs)
			}
		})
	}
}

// herdr answers agent.start at once (launch_pending), before the agent runs; a start whose agent never comes up
// (live: the command cut off at the shell, left at an open quote) fails at its bound, timed out, a start herdr
// accepted; sent once, nothing renamed.
func TestStartAgentNeverUp(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Shell("w1:p3", "pj-l-01", "")
	f.Set("w1:p3", func(p *herdrtest.Pane) { p.OnStart = "never" })
	srv := herdrtest.Serve(t, f)
	var said lines
	s := startOf("w1:p3", strings.Repeat("a long brief ", 130), &said)
	s.Bound = 200 * time.Millisecond
	a, err := srv.Client().Start(ctx, s)
	if n := count(srv, "agent.start"); n != 1 {
		t.Fatalf("precondition: one start, sent once; got %d", n)
	}
	if err == nil {
		t.Fatalf("a start with no agent up started: %+v", a)
	}
	if strings.Contains(err.Error(), "(timeout)") {
		t.Fatalf("precondition: herdr answered the start with success, got %v", err)
	}
	if !strings.Contains(err.Error(), "no claude agent came up in w1:p3 within 200ms") || !port.TimedOut(err) || !port.Accepted(err) {
		t.Errorf("error %v: want no agent up within the bound, timed out, a start herdr accepted", err)
	}
	if n := count(srv, "agent.rename"); n != 0 {
		t.Errorf("%d agent.rename calls, want none", n)
	}
}

// a listing read before herdr answered the start is no proof (it cannot tell the old agent from the new): with herdr's
// answer held while its state already shows the agent up, the start waits for the answer and is started only by it
// (herdr left the agent unnamed; nothing renames it back); held past the bound, it fails.
func TestStartWaitsForHerdrsAnswer(t *testing.T) {
	t.Parallel()
	t.Run("held, the agent up meanwhile: started by the answer", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		pane := f.Shell("w1:p3", "pj-l-01", "")
		f.Set(pane.ID, func(p *herdrtest.Pane) { p.OnStart = "unnamed" })
		srv := herdrtest.Serve(t, f)
		release, _ := srv.Hold("agent.start")
		var said lines
		done := make(chan error, 1)
		go func() {
			_, err := srv.Client().Start(ctx, startOf("w1:p3", "", &said))
			done <- err
		}()
		up := func() bool {
			var ok bool
			f.Locked(func() { ok = pane.Kind == "claude" && pane.InteractiveReady })
			return ok
		}
		for deadline := time.Now().Add(time.Second); !up() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		if !up() {
			t.Fatal("precondition: herdr's state shows the agent up while its answer is held")
		}
		select {
		case err := <-done:
			t.Fatalf("the start ended before herdr's answer: %v\n%s", err, said.String())
		case <-time.After(100 * time.Millisecond):
		}
		release()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the start: %v\n%s", err, said.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the start did not end with herdr's answer")
		}
		if n := count(srv, "agent.rename"); n != 0 {
			t.Errorf("%d agent.rename calls, want none", n)
		}
		if f.Pane("w1:p3").Name != "" {
			t.Errorf("herdr's name %q, want none kept (the start stands)", f.Pane("w1:p3").Name)
		}
	})
	t.Run("held past the bound: failed", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Shell("w1:p3", "pj-l-01", "")
		srv := herdrtest.Serve(t, f)
		release, _ := srv.Hold("agent.start")
		defer release()
		var said lines
		s := startOf("w1:p3", "", &said)
		s.Bound = 60 * time.Millisecond
		_, err := srv.Client().Start(ctx, s)
		if err == nil || !strings.Contains(err.Error(), "no claude agent came up in w1:p3 within 60ms") {
			t.Fatalf("start = %v, want no agent up within the bound", err)
		}
	})
}

// a start is its own agent. An agent of the kind already in the pane (herdr lost its name) is not taken for the start:
// the start waits for herdr's answer, which refuses it (the pane hosts an agent), PaneBusy.
func TestStartNotTakenForAnOldAgent(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Agent("w1:p3", "", "idle", "") // an old claude herdr lost the name of
	srv := herdrtest.Serve(t, f)
	release, _ := srv.Hold("agent.start")
	var said lines
	done := make(chan error, 1)
	go func() {
		_, err := srv.Client().Start(ctx, startOf("w1:p3", "", &said))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the start took the old agent for its own: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pane w1:p3 already hosts an agent") || !port.PaneBusy(err) {
			t.Fatalf("the start's answer: %v, want herdr's refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the start did not end with herdr's answer")
	}
	if f.Pane("w1:p3").Name != "" {
		t.Errorf("the old agent is named %q, want none", f.Pane("w1:p3").Name)
	}
}

// A name herdr kept with no live agent behind it is cleared and the start retried once, said on the start's Say
// (TestSocketStartNameTaken has the cases); a failed clear's warning is said too, and nothing else. herdr answering
// agent_not_ready is a failed start that matches NotReady, not retried.
func TestStartNameTakenAndNotReady(t *testing.T) {
	t.Parallel()
	t.Run("agent_name_taken once: name cleared, retried, started", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Shell("w1:p3", "pj-l-01", "")
		srv := herdrtest.Serve(t, f)
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-01 taken"})
		var said lines
		if _, err := srv.Client().Start(ctx, startOf("w1:p3", "", &said)); err != nil {
			t.Fatalf("start: %v\n%s", err, said.String())
		}
		if !strings.Contains(said.String(), "agent start: name pj-l-01 taken: clearing it and retrying") {
			t.Errorf("said %q, want the retry", said.String())
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want one retry", n)
		}
	})
	t.Run("the clear fails too: the retry line and the warning, nothing else", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Shell("w1:p3", "pj-l-01", "")
		srv := herdrtest.Serve(t, f)
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-01 taken"})
		srv.Once("agent.rename", herdrtest.Failure{Code: "agent_not_found", Message: "agent pj-l-01 not found"})
		var said lines
		if _, err := srv.Client().Start(ctx, startOf("w1:p3", "", &said)); err != nil {
			t.Fatalf("start: %v\n%s", err, said.String())
		}
		got := strings.Split(strings.TrimSuffix(said.String(), "\n"), "\n")
		if len(got) != 2 {
			t.Fatalf("said %q, want the retry line and the failed clear's warning only", said.String())
		}
		if got[0] != "agent start: name pj-l-01 taken: clearing it and retrying" {
			t.Errorf("the retry line = %q", got[0])
		}
		if !strings.HasPrefix(got[1], "warning: herdr kept the name pj-l-01: ") || !strings.Contains(got[1], "agent_not_found") {
			t.Errorf("the warning = %q, want the failed clear", got[1])
		}
	})
	t.Run("agent_not_ready: NotReady, accepted, not retried", func(t *testing.T) {
		t.Parallel()
		f := herdrtest.New()
		f.Shell("w1:p3", "pj-l-01", "")
		srv := herdrtest.Serve(t, f)
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_not_ready", Message: "blocked during startup"})
		var said lines
		_, err := srv.Client().Start(ctx, startOf("w1:p3", "", &said))
		if err == nil || !port.NotReady(err) || !port.Accepted(err) || !strings.Contains(err.Error(), "agent_not_ready") {
			t.Fatalf("start = %v, want herdr's agent_not_ready, NotReady and accepted", err)
		}
		if n := count(srv, "agent.start"); n != 1 {
			t.Errorf("agent.start sent %d times, want once", n)
		}
	})
}

// A start is up only on herdr's word that its launch is no longer pending: with the old Claude still listed after its
// exit (linger), the start accepted at once with launch_pending, and for three reads the pane listing the start's kind
// with a stale idle, still pending, the start reads the pane through every pending read until the new agent is up
// (interactive_ready); with no replacement ever up it fails at its bound.
func TestStartWaitsForTheLaunch(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, reads int) (*herdrtest.Fake, *herdrtest.Server, *herdrtest.Pane) {
		f := herdrtest.New()
		p := f.Agent("w1:p6", "", "idle", "")
		f.Set(p.ID, func(p *herdrtest.Pane) { p.Foreground = 1; p.StartPending = reads }) // the old one exited
		return f, herdrtest.Serve(t, f), p
	}
	t.Run("up after the pending reads: started", func(t *testing.T) {
		t.Parallel()
		f, srv, p := setup(t, 3)
		var said lines
		if _, err := srv.Client().Start(ctx, startOf("w1:p6", "", &said)); err != nil {
			t.Fatalf("start: %v\n%s", err, said.String())
		}
		calls := srv.Calls()
		reads := 0
		for i := len(calls) - 1; i >= 0 && !strings.HasPrefix(calls[i], "agent.start "); i-- {
			if strings.HasPrefix(calls[i], "agent.get ") && strings.Contains(calls[i], "target=w1:p6") {
				reads++
			}
		}
		f.Locked(func() {
			if p.StartPending != 0 || p.LaunchPending || !p.InteractiveReady {
				t.Errorf("pending reads left %d, launch_pending %t, interactive_ready %t: want the launch read until up",
					p.StartPending, p.LaunchPending, p.InteractiveReady)
			}
		})
		if reads < 4 {
			t.Errorf("%d reads of w1:p6 after the accepted start: want the 3 pending ones and the one up", reads)
		}
	})
	t.Run("the replacement never up: failed", func(t *testing.T) {
		t.Parallel()
		_, srv, _ := setup(t, 1<<30)
		var said lines
		s := startOf("w1:p6", "", &said)
		s.Bound = 60 * time.Millisecond
		_, err := srv.Client().Start(ctx, s)
		if err == nil || !strings.Contains(err.Error(), "no claude agent came up in w1:p6 within 60ms") {
			t.Fatalf("start = %v, want no agent up within the bound", err)
		}
	})
}
