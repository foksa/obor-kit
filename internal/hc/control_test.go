package hc_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
)

var ctx = context.Background()

// agentCore is a core with t1, a shell terminal (pid 41000) at /proj whose foreground is an agent (pid 41100) that
// reports status s.
func agentCore(s *hc.Status) *hctest.Core {
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", PID: 41000, Cwd: "/proj", Labels: map[string]string{hc.LabelProject: "proj"},
		Reported: true, Status: s, Foreground: &hc.Foreground{PID: 41100, Name: "agent", Cwd: "/proj"}})
	return core
}

// The refusals Prompt decides itself, before any key: a terminal whose program reports no status runs no agent (not
// found), a blocked agent is refused (a key would answer its dialog), and hc's own blocked answer to the send is
// the refusal too; each with began false.
func TestPromptRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		core  *hctest.Core
		clear bool // ctrl+u first: pressed only once the read let the prompt through
		check func(err error) bool
	}{
		{"no agent", shellCore(), true, port.NotFound},
		{"blocked", agentCore(&hc.Status{State: "blocked", Kind: "permission", Msg: "approve Bash: touch f"}), true, blocked},
		{"blocked at the send", func() *hctest.Core {
			core := agentCore(&hc.Status{State: "idle", App: "claude-code"})
			core.Fail("terminal.send", "blocked") // the agent reported a dialog after the read
			return core
		}(), false, blocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			began, err := tc.core.Client().Prompt(ctx, "t1", "[obor job T-1/1/work] call the obor job_next tool", tc.clear)
			if began || !tc.check(err) {
				t.Errorf("Prompt = began %v, %v; want a refusal before any typing", began, err)
			}
			if got := tc.core.Sent("t1"); len(got) != 0 {
				t.Errorf("typed %q, want nothing", got)
			}
			if got := tc.core.Keys("t1"); len(got) != 0 {
				t.Errorf("pressed %q, want nothing", got)
			}
		})
	}
}

// blocked: err is hc's (or obor's) blocked refusal.
func blocked(err error) bool {
	var e *hc.Error
	return errors.As(err, &e) && e.Kind == "blocked"
}

// A send appends to the terminal's unsent input (P2), so Prompt with clear presses ctrl+u before it: the agent gets
// the text alone; without clear the text follows what was there.
func TestPromptClears(t *testing.T) {
	core := agentCore(&hc.Status{State: "done", App: "claude-code"})
	c := core.Client()
	core.Type("t1", "left")
	if began, err := c.Prompt(ctx, "t1", "wake", true); !began || err != nil {
		t.Fatalf("Prompt with clear = %v, %v", began, err)
	}
	core.Type("t1", "left")
	if began, err := c.Prompt(ctx, "t1", "wake", false); !began || err != nil {
		t.Fatalf("Prompt without clear = %v, %v", began, err)
	}
	if got, want := core.Sent("t1"), []string{"wake", "leftwake"}; !slices.Equal(got, want) {
		t.Errorf("delivered %q, want %q", got, want)
	}
	if got := core.Keys("t1"); !slices.Equal(got, []string{"ctrl+u"}) {
		t.Errorf("keys %q, want one ctrl+u (the clear)", got)
	}
}

// Prompt's send waits up to SendBound, above hc's 60 s draft_wait: a send hc holds 65 s for a person's draft is
// delivered; one held past the bound fails at 70 s with began true (hc may still paste it).
func TestPromptBound(t *testing.T) {
	if hc.SendBound != 70*time.Second {
		t.Fatalf("precondition: SendBound %s, want 70 s", hc.SendBound)
	}
	synctest.Test(t, func(t *testing.T) {
		core := agentCore(&hc.Status{State: "idle", App: "claude-code"})
		c := core.Client()
		core.SetSendWait(65 * time.Second)
		start := time.Now()
		if began, err := c.Prompt(ctx, "t1", "wake", false); !began || err != nil {
			t.Errorf("Prompt held 65 s = %v, %v; want delivered", began, err)
		}
		if d := time.Since(start); d != 65*time.Second {
			t.Errorf("the held send returned after %s, want 65 s", d)
		}
		core.SetSendWait(75 * time.Second)
		start = time.Now()
		began, err := c.Prompt(ctx, "t1", "wake", false)
		if !began || err == nil {
			t.Errorf("Prompt held 75 s = %v, %v; want a failure that may have typed", began, err)
		}
		if d := time.Since(start); d != hc.SendBound {
			t.Errorf("the send gave up after %s, want %s", d, hc.SendBound)
		}
		synctest.Wait()
		time.Sleep(10 * time.Second) // the core's held answer ends
		core.Down()
	})
}

// Write types whatever runs in the pane, a blocked program too (force), always with Enter: hc has no typing without
// it, so submit false is refused with nothing typed.
func TestWrite(t *testing.T) {
	core := agentCore(&hc.Status{State: "blocked", Kind: "permission"})
	c := core.Client()
	if err := c.Write(ctx, "t1", "/exit", false); err == nil {
		t.Error("Write without submit succeeded")
	}
	if err := c.Write(ctx, "t1", "/exit", true); err != nil {
		t.Errorf("Write at a blocked program = %v, want it typed", err)
	}
	if got := core.Sent("t1"); !slices.Equal(got, []string{"/exit"}) {
		t.Errorf("typed %q, want only the submitted /exit", got)
	}
}

// SendKeys sends hc's names: obor's mapped, others lowercased (hc reads names without case: no key depends on it).
func TestSendKeysNames(t *testing.T) {
	core := shellCore()
	if err := core.Client().SendKeys(ctx, "t1", port.KeyEsc, port.KeyCtrlU, "1", "Enter"); err != nil {
		t.Fatal(err)
	}
	if got, want := core.Keys("t1"), []string{"esc", "ctrl+u", "1", "enter"}; !slices.Equal(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
}

// A turn ends when the status leaves working for idle or done, for each kind's sequence as the probes saw it (P1):
// Claude and Codex end at done, OpenCode at idle (it never says done), an error turn at error (idle). Blocked or
// working again in between ends nothing. A wait for done alone is met by OpenCode's idle.
func TestWaitStatusTurnEnds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		app   string
		steps []string // after working; the last ends the turn
		until []string
	}{
		{"claude", "claude-code", []string{"blocked", "working", "done"}, []string{"idle", "done"}},
		{"codex", "codex", []string{"blocked", "working", "done"}, []string{"idle", "done"}},
		{"opencode", "opencode", []string{"blocked", "working", "idle"}, []string{"idle", "done"}},
		{"opencode for done", "opencode", []string{"working", "idle"}, []string{"done"}},
		{"error", "claude-code", []string{"working", "error"}, []string{"idle", "done"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				core := agentCore(&hc.Status{State: "working", App: tc.app})
				done := make(chan error, 1)
				go func() { done <- core.Client().WaitStatus(ctx, "t1", tc.until, time.Minute) }()
				synctest.Wait()
				if core.Subscribers() != 1 {
					t.Fatalf("precondition: %d subscribers, want the wait's", core.Subscribers())
				}
				for i, w := range tc.steps {
					core.SetStatus("t1", &hc.Status{State: w, App: tc.app, Msg: "msg"})
					synctest.Wait()
					select {
					case err := <-done:
						if i < len(tc.steps)-1 || err != nil {
							t.Fatalf("the wait ended at %s (step %d): %v; want it to end without error at %s", w, i,
								err, tc.steps[len(tc.steps)-1])
						}
					default:
						if i == len(tc.steps)-1 {
							t.Fatalf("the wait did not end at %s", w)
						}
					}
				}
				core.Down()
			})
		})
	}
}

// A wait ends with an error that says why: its timeout (TimedOut), no agent at all (a shell: not found), the agent
// exiting to the shell (not found), the terminal finishing (not found).
func TestWaitStatusFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core := agentCore(&hc.Status{State: "working", App: "claude-code"})
		c := core.Client()
		start := time.Now()
		if err := c.WaitStatus(ctx, "t1", []string{"idle", "done"}, 30*time.Second); !port.TimedOut(err) || time.Since(start) != 30*time.Second {
			t.Errorf("WaitStatus on a working agent = %v after %s; want TimedOut at 30 s", err, time.Since(start))
		}
		if err := shellCore().Client().WaitStatus(ctx, "t1", []string{"idle"}, time.Minute); !port.NotFound(err) || port.TimedOut(err) {
			t.Errorf("WaitStatus at a shell = %v, want not found at once", err)
		}
		for _, end := range []func(){
			func() { // /exit: status null, the shell back in the foreground
				core.SetStatus("t1", nil)
				core.SetForeground("t1", hc.Foreground{PID: 41000, Name: "zsh", Cwd: "/proj"})
			},
			func() { core.Exit("t1", 0) },
		} {
			core.Update("t1", func(r *hc.Record) {
				r.State, r.Status = "live", &hc.Status{State: "working"}
				r.Foreground = &hc.Foreground{PID: 41100, Name: "agent"}
			})
			done := make(chan error, 1)
			go func() { done <- c.WaitStatus(ctx, "t1", []string{"idle", "done"}, time.Minute) }()
			synctest.Wait()
			end()
			if err := <-done; !port.NotFound(err) || port.TimedOut(err) {
				t.Errorf("WaitStatus when the agent left = %v, want not found", err)
			}
		}
		core.Down()
	})
}

// A kept terminal whose shell exits with a code is finished for good (P8): the pane is gone to every read and write
// (not found), the snapshot leaves it out, and the stream sends its exit and stays open (no `restarted` to wait for).
func TestFinishedWithCode(t *testing.T) {
	core, st := startedCore(t)
	c := core.Client()
	if _, err := c.Pane(ctx, "t1"); err != nil {
		t.Fatalf("precondition: t1 is live: %v", err)
	}
	core.Exit("t1", 1)
	if ev, ok := next(t, st); !ok || ev.Kind != port.EventExited || ev.Pane != "t1" {
		t.Errorf("event = %+v, %v; want t1's exit", ev, ok)
	}
	if _, err := c.Pane(ctx, "t1"); !port.NotFound(err) {
		t.Errorf("Pane = %v, want not found", err)
	}
	if began, err := c.Prompt(ctx, "t1", "wake", true); began || !port.NotFound(err) {
		t.Errorf("Prompt = %v, %v; want not found before typing", began, err)
	}
	if err := c.Write(ctx, "t1", "echo x", true); !port.NotFound(err) {
		t.Errorf("Write = %v, want not found (hc's finished)", err)
	}
	if err := c.SendKeys(ctx, "t1", port.KeyEnter); !port.NotFound(err) {
		t.Errorf("SendKeys = %v, want not found (hc's finished)", err)
	}
	if err := c.WaitStatus(ctx, "t1", []string{"idle"}, time.Second); !port.NotFound(err) {
		t.Errorf("WaitStatus = %v, want not found", err)
	}
	if snap, err := (hc.Panes{C: c}).Snapshot(ctx); err != nil || len(snap.Panes) != 0 {
		t.Errorf("Snapshot = %+v, %v; want no pane", snap, err)
	}
	select {
	case ev, ok := <-st.C:
		t.Errorf("after the exit the stream sent %+v (open %v), err %v; want it open and quiet", ev, ok, st.Err())
	case <-time.After(100 * time.Millisecond):
	}
}

// RenameProject relabels the project's terminals (obor.project old → name, each agent label relabel maps), leaves
// another project's alone, and says Already on a rerun.
func TestRenameProject(t *testing.T) {
	core := hctest.New()
	for _, r := range []hc.Record{
		{Address: "t1", Labels: map[string]string{hc.LabelProject: "pj", hc.LabelAgent: "pj-w1"}},
		{Address: "t2", Labels: map[string]string{hc.LabelProject: "pj", hc.LabelAgent: "mine"}},
		{Address: "t3", Labels: map[string]string{hc.LabelProject: "other", hc.LabelAgent: "pj-w1"}},
	} {
		core.Add(r)
	}
	c := core.Client()
	relabel := func(l string) string {
		if l == "pj-w1" {
			return "nw-w1"
		}
		return l
	}
	done, err := c.RenameProject(ctx, "pj", "nw", relabel)
	if err != nil || done.Already {
		t.Fatalf("RenameProject = %+v, %v", done, err)
	}
	panes, _ := (hc.Panes{C: c}).Snapshot(ctx)
	got := map[string]string{}
	for _, p := range panes.Panes {
		got[p.ID] = p.Group + "/" + p.Label
	}
	if want := map[string]string{"t1": "nw/nw-w1", "t2": "nw/mine", "t3": "other/pj-w1"}; !maps.Equal(got, want) {
		t.Errorf("after the rename %v, want %v", got, want)
	}
	if again, err := c.RenameProject(ctx, "pj", "nw", relabel); err != nil || !again.Already {
		t.Errorf("a rerun = %+v, %v; want Already", again, err)
	}
}
