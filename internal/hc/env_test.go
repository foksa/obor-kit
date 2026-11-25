package hc_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
)

// leakedEnv is a core's environment after an hc call from a herdr pane started it (P4's made-up names, as hc
// printed them), with one name set empty, which reads as unset.
var leakedEnv = map[string]string{"HOME": "/Users/probe", "PATH": "/usr/bin:/bin", "HERDR_PANE_ID": "w99:p99",
	"OBOR_ROLE": "worker", "OBOR_PANE": "w99:p99", "OBOR_LANE": "", "HC_SESSION": "obor-probe-4"}

// The env guard: CheckEnv reads the core's environment through machine.exec of /usr/bin/env and refuses with LeakLine
// naming each identity name of another terminal it holds with a value (an empty one reads as unset); a clean core
// passes.
func TestCheckEnv(t *testing.T) {
	t.Parallel()
	core := hctest.New()
	if err := core.Client().CheckEnv(context.Background()); err != nil {
		t.Fatalf("a scrubbed core's environment: %v, want it to pass", err)
	}
	if !slices.Contains(core.Requests(), "machine.exec") {
		t.Fatalf("the check asked %q, want machine.exec among them", core.Requests())
	}
	core.SetEnv(leakedEnv)
	err := core.Client().CheckEnv(context.Background())
	want := hc.LeakLine + " (its environment holds HERDR_PANE_ID, OBOR_PANE, OBOR_ROLE)"
	if err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("a core started from an obor pane: %v, want the refusal ending %q", err, want)
	}
	if port.NotRunning(err) || port.NotFound(err) {
		t.Errorf("the refusal reads as not running or not found: a caller would wait for a core or drop a pane")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the refusal %q is more than one line", err)
	}
}

// The RV-265 case: a core started from a Rex block holds the block's identity and gives it to every hc terminal it
// makes, so it is refused like a core started from an obor pane (its own HC_TERMINAL_ is hc's and no leak).
func TestCheckEnvRefusesRexIdentity(t *testing.T) {
	t.Parallel()
	core := hctest.New()
	core.SetEnv(map[string]string{"HOME": "/Users/probe", "REX_BLOCK": "block:old", "REX_SESSION": "session:old",
		"HC_TERMINAL_ID": "t7"})
	err := core.Client().CheckEnv(context.Background())
	want := hc.LeakLine + " (its environment holds REX_BLOCK, REX_SESSION)"
	if err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("a core started from a Rex block: %v, want the refusal ending %q", err, want)
	}
}

// Leaked takes every identity name of another terminal with a value - obor's, herdr's, a Rex block's, a Claude
// session's - and nothing else; Scrub drops those, keeps hc's own names and the session's.
func TestLeakedAndScrub(t *testing.T) {
	t.Parallel()
	leaked := map[string]string{"HERDR_PANE_ID": "w1:p2", "OBOR_ROLE": "worker", "REX_BLOCK": "block:old",
		"REX_SESSION": "session:old", "CLAUDE_CODE_SESSION_ID": "s", "CLAUDECODE": "1", "HC_TERMINAL_ID": "t7",
		"OBOR_LANE": ""}
	wantLeaked := []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "HERDR_PANE_ID", "OBOR_ROLE", "REX_BLOCK", "REX_SESSION"}
	if got := hc.Leaked(leaked); !slices.Equal(got, wantLeaked) {
		t.Errorf("Leaked = %q, want %q", got, wantLeaked)
	}
	environ := []string{"PATH=/usr/bin", "HOME=/Users/x", "HERDR_PANE_ID=w1:p2", "HERDR_SOCKET_PATH=/s", "OBOR_ROLE=worker",
		"OBOR_PANE=", "REX_BLOCK=block:old", "REX_SESSION=session:old", "CLAUDE_CODE_SESSION_ID=s", "CLAUDECODE=1",
		"HC_TERMINAL_ID=t3", "HC_TERMINAL_SOCKET=/t", "HC_SESSION=s", "HC_SOCKET=/h.sock", "XDG_STATE_HOME=/st"}
	want := []string{"PATH=/usr/bin", "HOME=/Users/x", "HC_TERMINAL_ID=t3", "HC_TERMINAL_SOCKET=/t", "HC_SESSION=s",
		"HC_SOCKET=/h.sock", "XDG_STATE_HOME=/st"}
	if got := hc.Scrub(environ); !slices.Equal(got, want) {
		t.Errorf("Scrub = %q\nwant %q", got, want)
	}
}

// Check is obor setup check's line: a clean core passes, a core that is not running passes with the line that serve
// starts it, a leaked one is the refusal.
func TestCheck(t *testing.T) {
	t.Parallel()
	core := hctest.New()
	line, err := core.Client().Check(context.Background())
	if err != nil || line != "hc's core environment holds no other terminal's or agent's identity" {
		t.Errorf("a clean core: %q, %v", line, err)
	}
	core.Down()
	line, err = core.Client().Check(context.Background())
	if err != nil || !strings.Contains(line, "not running: obor serve starts it from a scrubbed environment") {
		t.Errorf("no core: %q, %v", line, err)
	}
	core.Up()
	core.SetEnv(leakedEnv)
	if line, err := core.Client().Check(context.Background()); err == nil || !strings.Contains(err.Error(), hc.LeakLine) {
		t.Errorf("a leaked core: %q, %v, want the refusal", line, err)
	}
}

// fakeStart is a Core.Run that records each call and starts core.
type fakeStart struct {
	core  *hctest.Core
	calls [][]string // args
	envs  [][]string
	fail  error
}

func (f *fakeStart) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	f.calls, f.envs = append(f.calls, args), append(f.envs, env)
	if f.fail != nil {
		return []byte("hc: no such file"), f.fail
	}
	f.core.Up()
	return nil, nil
}

// Serve's subscribe (Panes.Core) starts a core that is down with `hc server start` from serve's environment scrubbed,
// then subscribes; a core that runs is not started again; a core whose environment leaks is refused before any
// subscription; a start that fails reads as not running (serve waits and retries).
func TestServeStartsDownCoreScrubbed(t *testing.T) {
	t.Parallel()
	core := hctest.New()
	core.Down()
	f := &fakeStart{core: core}
	environ := []string{"PATH=/usr/bin", "HERDR_PANE_ID=w1:p2", "OBOR_ROLE=lead", "HC_SESSION=s"}
	p := hc.Panes{C: core.Client(), Core: &hc.Core{Environ: environ, Run: f.run}}

	st, err := p.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe with the core down: %v, want a start and a stream", err)
	}
	st.Close()
	if len(f.calls) != 1 || !slices.Equal(f.calls[0], []string{"server", "start"}) {
		t.Fatalf("hc calls %q, want one `hc server start`", f.calls)
	}
	if want := []string{"PATH=/usr/bin", "HC_SESSION=s"}; !slices.Equal(f.envs[0], want) {
		t.Errorf("the start's env %q, want serve's scrubbed: %q", f.envs[0], want)
	}

	st, err = p.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe with the core up: %v", err)
	}
	st.Close()
	if len(f.calls) != 1 {
		t.Errorf("a core that runs was started again: hc calls %q", f.calls)
	}

	core.SetEnv(leakedEnv)
	before := subscribes(core)
	if _, err := p.Subscribe(context.Background()); err == nil || !strings.Contains(err.Error(), hc.LeakLine) {
		t.Errorf("subscribe to a leaked core: %v, want the refusal", err)
	}
	if subscribes(core) != before || len(f.calls) != 1 {
		t.Errorf("a leaked core: %d events.subscribe (was %d), hc calls %q: want no subscription and no start",
			subscribes(core), before, f.calls)
	}

	core.Down()
	f.fail = errors.New("exec: \"hc\": executable file not found in $PATH")
	_, err = p.Subscribe(context.Background())
	if err == nil || !port.NotRunning(err) || !strings.Contains(err.Error(), "hc server start") {
		t.Errorf("a start that fails: %v, want a not-running error naming hc server start", err)
	}
}

// subscribes is how many events.subscribe the core was asked.
func subscribes(core *hctest.Core) int {
	n := 0
	for _, m := range core.Requests() {
		if m == "events.subscribe" {
			n++
		}
	}
	return n
}

// Without Core a subscribe only dials: no core is started and no environment read (obor's other commands).
func TestSubscribeWithoutCoreStartsNothing(t *testing.T) {
	t.Parallel()
	core := hctest.New()
	core.Down()
	if _, err := (hc.Panes{C: core.Client()}).Subscribe(context.Background()); !port.NotRunning(err) {
		t.Fatalf("subscribe with the core down and no Core: %v, want not running", err)
	}
	core.Up()
	st, err := hc.Panes{C: core.Client()}.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if slices.Contains(core.Requests(), "machine.exec") {
		t.Errorf("requests %q: a subscribe without Core read the core's environment", core.Requests())
	}
}
