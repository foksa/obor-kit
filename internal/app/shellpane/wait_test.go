package shellpane_test

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/shellpane"
	"obor/internal/testkit"
)

// agentTerm is a terminal with p1, an agent pane of kind claude whose status is st.
func agentTerm(st string) *testkit.Term {
	tm := testkit.NewTerm()
	tm.Put(port.Pane{ID: "p1", Agent: "claude", Status: st})
	return tm
}

// wait runs WaitStatus on tm's p1 in the background; its result arrives on the channel.
func wait(tm *testkit.Term, until []string, timeout time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() { done <- shellpane.WaitStatus(ctx, "tm", tm.Panes(), "p1", until, timeout) }()
	return done
}

// A wait ends when the report's word is a ready one (progstatus.TurnEnded: the rule's rows are there), not before:
// working and a program that reports nothing (unknown) keep it waiting.
func TestWaitStatusTurnEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := agentTerm("working")
		done := wait(tm, []string{"idle", "done"}, time.Minute)
		synctest.Wait()
		if !tm.Streaming() {
			t.Fatal("precondition: the wait subscribed")
		}
		for i, st := range []string{"unknown", "working", "done"} {
			tm.Set("p1", func(p *port.Pane) { p.Status = st })
			synctest.Wait()
			select {
			case err := <-done:
				if i < 2 || err != nil {
					t.Fatalf("the wait ended at %s: %v; want it to end without error at done", st, err)
				}
			default:
				if i == 2 {
					t.Fatal("the wait did not end at done")
				}
			}
		}
	})
}

// A wait ends with an error that says why: its timeout (TimedOut, at the timeout), no such pane and no agent at all (a
// shell: not found at once), the agent leaving to its shell, the pane exiting or closing (not found).
func TestWaitStatusFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if err := <-wait(agentTerm("working"), []string{"idle"}, 30*time.Second); !port.TimedOut(err) || time.Since(start) != 30*time.Second {
			t.Errorf("a working agent = %v after %s; want TimedOut at 30 s", err, time.Since(start))
		}
		shell := testkit.NewTerm()
		shell.Put(port.Pane{ID: "p1"})
		for name, tm := range map[string]*testkit.Term{"a shell": shell, "no pane": testkit.NewTerm()} {
			start := time.Now()
			if err := <-wait(tm, []string{"idle"}, time.Minute); !port.NotFound(err) || port.TimedOut(err) || time.Since(start) != 0 {
				t.Errorf("%s = %v after %s, want not found at once", name, err, time.Since(start))
			}
		}
		for name, end := range map[string]func(tm *testkit.Term){
			"the agent exits to its shell": func(tm *testkit.Term) { tm.Set("p1", func(p *port.Pane) { p.Agent, p.Status = "", "" }) },
			"the pane exits":               func(tm *testkit.Term) { tm.Remove("p1", true) },
			"the pane closes":              func(tm *testkit.Term) { tm.Remove("p1", false) },
		} {
			tm := agentTerm("working")
			done := wait(tm, []string{"idle"}, time.Minute)
			synctest.Wait()
			end(tm)
			if err := <-done; !port.NotFound(err) || port.TimedOut(err) {
				t.Errorf("%s: the wait = %v, want not found", name, err)
			}
		}
	})
}

// A stream that ends as a refresh is followed by a new subscription, whose snapshot the wait reads; any other end (a
// lost connection) is the wait's error.
func TestWaitStatusStreamEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := agentTerm("working")
		done := wait(tm, []string{"idle"}, time.Minute)
		synctest.Wait()
		tm.Edit("p1", func(p *port.Pane) { p.Status = "idle" }) // missed by the stream: the next snapshot has it
		tm.End(testkit.Refresh())
		if err := <-done; err != nil || len(tm.Calls(testkit.OpSubscribe)) != 2 {
			t.Errorf("after a refresh: %v, %d subscribes; want the second's snapshot to end the wait", err, len(tm.Calls(testkit.OpSubscribe)))
		}
		tm = agentTerm("working")
		done = wait(tm, []string{"idle"}, time.Minute)
		synctest.Wait()
		tm.End(&testkit.TermError{Msg: "gone", Gone: true})
		if err := <-done; !port.NotRunning(err) || len(tm.Calls(testkit.OpSubscribe)) != 1 {
			t.Errorf("after a lost connection: %v, %d subscribes; want NotRunning, no new subscribe", err, len(tm.Calls(testkit.OpSubscribe)))
		}
	})
}

// A turn that ends on an error (an idle report whose note is the error's) is logged once, and ends the wait.
func TestWaitStatusLogsAnErrorTurn(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	tm := testkit.NewTerm()
	tm.Put(port.Pane{ID: "p1", Agent: "claude", Status: "idle", Note: "error: stream error"})
	if err := <-wait(tm, []string{"idle"}, time.Minute); err != nil {
		t.Fatalf("WaitStatus = %v, want the turn ended", err)
	}
	if got := buf.String(); strings.Count(got, "tm: the agent in p1 ended its turn on an error: stream error") != 1 {
		t.Errorf("logged %q, want the error line once", got)
	}
}
