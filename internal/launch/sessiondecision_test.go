package launch_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents/plugin"
	"obor/internal/launch"
	"obor/internal/store"
	"obor/internal/testkit"
	agykind "obor/kinds/agy/kind"
	ockind "obor/kinds/opencode/kind"
)

// mute is a plugin kind that resumes nothing (its manifest has no caps.resume), answered in process: the config names
// it as muteCmd.
const mute, muteCmd = "mute", "/obor-kind-mute"

func init() {
	plugin.Local[muteCmd] = func(call string, req []byte) ([]byte, error) {
		var reply map[string]any
		switch call {
		case "describe": // OpenCode's manifest under another name, without its resume
			b, err := ockind.Handle(call, []byte(`{"version":1}`))
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(b, &reply); err != nil {
				return nil, err
			}
			reply["name"] = mute
			delete(reply["caps"].(map[string]any), "resume")
		case "start":
			var in struct {
				Args   []string `json:"args"`
				Resume string   `json:"resume"`
			}
			if err := json.Unmarshal(req, &in); err != nil {
				return nil, err
			}
			args := in.Args
			if in.Resume != "" { // never asked: the kind resumes nothing
				args = append(args, "--resume", in.Resume)
			}
			reply = map[string]any{"args": args, "env": map[string]string{}}
		default:
			reply = map[string]any{}
		}
		reply["version"] = plugin.Version
		return json.Marshal(reply)
	}
}

// reviewerOn is the launcher's fixture with the reviewer (pj-rev) on model (neon: agy; mu: mute) and recs recorded,
// its reports landing in serve's store as serve records them (fx.serve): a restart after it reads what it recorded.
// The project and the reviewer's role worktree (its start dir, whose hooks file agy runs) hold agy's current hooks file
// (an agent's agy start refuses a start dir without it).
func reviewerOn(t *testing.T, model string, recs ...store.Session) *fx {
	t.Helper()
	x := setup(t, testkit.AddModels(`{"neon":{"kind":"agy","id":"gemini-4-neon-high"},"mu":{"kind":"mute","id":"m"}}`),
		testkit.Set("agents.kinds.mute", map[string]any{"plugin": muteCmd}),
		testkit.Set("agents.roles", `{"integrator":"opus","lead":"opus","reviewer":"`+model+`"}`))
	hooks, _ := json.Marshal(map[string]any{"obor": agykind.HooksEntry()})
	x.p.Write(agykind.HooksFile, string(hooks))
	x.p.Write(filepath.Join(x.p.Cfg().RoleWorktree("reviewer"), agykind.HooksFile), string(hooks)) // the file agy runs
	x.serve = true
	recordSessions(t, x.p.Root, recs...)
	return x
}

// current is agent's current recorded session as "<id> <kind>" ("": none).
func (x *fx) current(agent string) string {
	s, ok := store.CurrentAt(x.p.Root, agent)
	if !ok {
		return ""
	}
	return s.ID + " " + s.Kind
}

// lastStart is the last start the terminal was asked for ("": none).
func (x *fx) lastStart() string {
	starts := x.calls("Start")
	if len(starts) == 0 {
		return ""
	}
	return starts[len(starts)-1]
}

// stopped is every write, key and prompt the terminal was sent ("": none): a refusal before the stop sends none.
func (x *fx) stopped() []string {
	var out []string
	for _, m := range []string{"Write", "SendKeys", "Prompt"} {
		out = append(out, x.calls(m)...)
	}
	return out
}

// The start's session decision, one row per case of its rule, at the launcher (obor restart, serve's restart and a
// dispatch's start are one Launch): the session it resumes or a fresh start, what it says, and the agent's recorded
// session after it — a session the start resumes or adopts is recorded with the start's kind, whether or not the kind
// reports sessions or fires a start hook on resume (agy: neither). agy is the adopting kind (no named sessions),
// Claude the named one, mute a kind that resumes nothing.
func TestRestartSessionDecision(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	agyRec := store.Session{Agent: "pj-rev", Kind: "agy", ID: "conv-1", Started: now,
		Transcript: "/h/.gemini/antigravity-cli/brain/conv-1/.system_generated/logs/transcript_full.jsonl"}
	claudeRec := store.Session{Agent: "pj-rev", Kind: "claude", ID: "sid-c", Started: now}
	muteRec := store.Session{Agent: "pj-rev", Kind: mute, ID: "m-1", Started: now}
	agyLive := func(id string) func(x *fx) {
		return func(x *fx) { x.agent("w1:p7", "pj-rev", "agy", "idle").Session = id }
	}
	shell := func(x *fx) { x.shell("w1:p7", "pj-rev", "") }
	for _, c := range []struct {
		name  string
		x     func(t *testing.T) *fx
		pane  func(x *fx) // the agent's pane as herdr shows it
		agent string      // "": pj-rev in w1:p7
		req   launch.Request
		// resumed is the session the start resumes ("new": a fresh one); argv a part its start's argv has, not one it
		// lacks; said a line it says (each), quiet one it does not say ("": not checked).
		resumed, argv, not string
		said               []string
		quiet              string
		fail               string // the refusal ("": none)
		recorded           string // the agent's current session after it, "<id> <kind>" ("": none; "*": not checked)
	}{
		{name: "none recorded, a live Claude reviewer: refused, nothing stopped (RV-275)",
			x:    func(t *testing.T) *fx { return reviewerOn(t, "opus") },
			pane: func(x *fx) { x.agent("w1:p7", "pj-rev", "claude", "idle") },
			fail: "no session to resume for pj-rev (pass --fresh to start a new one)"},
		{name: "none recorded, a program in the role's pane, no agent field: refused",
			x:    func(t *testing.T) *fx { return reviewerOn(t, "opus") },
			pane: func(x *fx) { x.shell("w1:p7", "pj-rev", "").Foreground = 2 },
			fail: "no session to resume for pj-rev (pass --fresh to start a new one)"},
		{name: "same kind recorded, none live: the record resumes, recorded",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon", agyRec) }, pane: shell,
			resumed: "conv-1", argv: "--conversation conv-1", said: []string{"resuming conv-1 (from record)"},
			quiet: "warning:", recorded: "conv-1 agy"},
		{name: "same kind recorded, the same live: the record resumes, recorded",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon", agyRec) }, pane: agyLive("conv-1"),
			resumed: "conv-1", argv: "--conversation conv-1", quiet: "warning:", recorded: "conv-1 agy"},
		{name: "same kind recorded, another live id: the record resumes, a warning",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon", agyRec) }, pane: agyLive("conv-9"),
			resumed: "conv-1", argv: "--conversation conv-1", not: "conv-9",
			said: []string{"warning: the terminal reports session conv-9 for pane w1:p7, the recorded session is " +
				"conv-1: resuming conv-1"}, recorded: "conv-1 agy"},
		{name: "another kind recorded, this kind's live: adopted, recorded as this kind",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon", claudeRec) }, pane: agyLive("conv-live"),
			resumed: "conv-live", argv: "--conversation conv-live", not: "sid-c",
			said: []string{"session sid-c is claude's, the type neon is agy's: resuming the terminal's conv-live",
				"resuming conv-live (from herdr)"}, recorded: "conv-live agy"},
		{name: "another kind recorded, none live: fresh",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon", claudeRec) }, pane: shell,
			resumed: "new", argv: "kind=agy", not: "--conversation",
			said: []string{"session sid-c is claude's, the type neon is agy's: new session"}, recorded: "sid-c claude"},
		{name: "another kind recorded, its own kind live: fresh",
			x:       func(t *testing.T) *fx { return reviewerOn(t, "neon", claudeRec) },
			pane:    func(x *fx) { x.agent("w1:p7", "pj-rev", "claude", "idle").Session = "sid-c" },
			resumed: "new", argv: "kind=agy", not: "--conversation",
			said: []string{"session sid-c is claude's, the type neon is agy's: new session"}, recorded: "sid-c claude"},
		{name: "none recorded, this kind's live, not named: adopted, recorded",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon") }, pane: agyLive("conv-live"),
			resumed: "conv-live", argv: "--conversation conv-live", said: []string{"resuming conv-live (from herdr)"},
			recorded: "conv-live agy"},
		{name: "none recorded, another kind's live: not adopted (fresh with the prompt it carries)",
			x:       func(t *testing.T) *fx { return reviewerOn(t, "neon") },
			pane:    func(x *fx) { x.agent("w1:p7", "pj-rev", "codex", "idle").Session = "cx-9" },
			req:     launch.Request{Prompt: "go"},
			resumed: "new", argv: "-i go", not: "cx-9", quiet: "from herdr"},
		{name: "none recorded, a named-session kind: fresh (the prompt it carries), never herdr's",
			x: func(t *testing.T) *fx { x := setup(t); x.serve = true; return x }, agent: "pj-l-01",
			pane: func(x *fx) { x.shell("w1:p3", "pj-l-01", "").Session = "sid-h" }, req: launch.Request{Prompt: "go"},
			resumed: "new", argv: "--session-id", not: "--resume", quiet: "from herdr", recorded: "*"},
		{name: "none recorded, a role agent: fresh, said",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon") }, pane: shell,
			resumed: "new", argv: "kind=agy", not: "--conversation",
			said: []string{"no session recorded for pj-rev: new session"}},
		{name: "none recorded, the integrator: fresh, said",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon") }, agent: "pj-int",
			pane:    func(x *fx) { x.shell("w1:p3", "pj-int", "") },
			resumed: "new", argv: "kind=claude", not: "--resume",
			said: []string{"no session recorded for pj-int: new session"}, recorded: "*"},
		{name: "none recorded, a named-session kind, no prompt: refused, never herdr's",
			x: func(t *testing.T) *fx { x := setup(t); x.serve = true; return x }, agent: "pj-l-01",
			pane: func(x *fx) { x.shell("w1:p3", "pj-l-01", "").Session = "sid-h" },
			fail: "no session to resume for pj-l-01 (pass --fresh to start a new one)"},
		{name: "--fresh: fresh, the record kept",
			x: func(t *testing.T) *fx { return reviewerOn(t, "neon", agyRec) }, pane: agyLive("conv-1"),
			req: launch.Request{Fresh: true}, resumed: "new", argv: "kind=agy", not: "--conversation", quiet: "warning:",
			recorded: "conv-1 agy"},
		{name: "a dispatch, a session of its kind recorded: the record resumes",
			x: func(t *testing.T) *fx {
				x := setup(t)
				x.serve = true
				x.sid("pj-l-01", "sid-1")
				x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"opus"}`)
				return x
			}, agent: "pj-l-01", pane: func(x *fx) { x.shell("w1:p3", "pj-l-01", "") },
			resumed: "sid-1", argv: "--resume sid-1", not: "gpt-6-luna",
			said: []string{"resuming sid-1 (from record)"}, recorded: "sid-1 claude"},
		{name: "a dispatch, a session of another kind recorded: fresh of the dispatch's kind",
			x: func(t *testing.T) *fx {
				x := setup(t)
				x.serve = true
				x.sid("pj-l-01", "sid-1")
				x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"luna"}`)
				return x
			}, agent: "pj-l-01", pane: func(x *fx) { x.shell("w1:p3", "pj-l-01", "") },
			resumed: "new", argv: "kind=codex", not: "sid-1",
			said: []string{"session sid-1 is claude's, the type luna is codex's: new session"}, recorded: "sid-1 claude"},
		{name: "a kind that resumes nothing, a record: fresh, a warning",
			x: func(t *testing.T) *fx { return reviewerOn(t, "mu", muteRec) }, pane: shell,
			resumed: "new", argv: "kind=mute", not: "--resume", said: []string{"warning: no resume for kind mute: new session"},
			recorded: "m-1 mute"},
		{name: "a kind that resumes nothing, no record: fresh, no warning",
			x: func(t *testing.T) *fx { return reviewerOn(t, "mu") }, pane: shell,
			resumed: "new", argv: "kind=mute", not: "--resume", quiet: "warning:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := c.x(t)
			c.pane(x)
			agent, pane := c.agent, "w1:p3"
			if agent == "" {
				agent, pane = "pj-rev", "w1:p7"
			}
			c.req.Agent = agent
			out, ok := x.restart(c.req)
			if c.fail != "" {
				if ok || last(out) != "FAIL "+c.fail {
					t.Fatalf("result %q, want FAIL %s", last(out), c.fail)
				}
				lacks(t, out, "from herdr", "herdr's session not adopted")
				if x.startCall() != "" {
					t.Error("nothing started")
				}
				if stopped := x.stopped(); len(stopped) > 0 {
					t.Errorf("nothing stopped: %q", stopped)
				}
				return
			}
			if !ok {
				t.Fatal(out)
			}
			restarted(t, last(out), agent, pane, c.resumed)
			contains(t, x.startCall(), c.argv, "the start's argv")
			if c.not != "" {
				lacks(t, x.startCall(), c.not, "the start's argv")
			}
			for _, s := range c.said {
				contains(t, out, s, "said")
			}
			if c.quiet != "" {
				lacks(t, out, c.quiet, "not said")
			}
			if c.recorded != "*" {
				eq(t, x.current(agent), c.recorded, "the recorded session after the start")
			}
			if c.resumed != "new" { // the session it resumed, reported with the start's kind (recorded's) from its pane
				x.mu.Lock()
				reported := x.reported
				x.mu.Unlock()
				want := c.recorded + " " + pane
				if !strings.HasPrefix(want, c.resumed+" ") {
					t.Fatalf("precondition: a resume's row records the session it resumed: %q, %q", c.recorded, c.resumed)
				}
				if n := len(reported); n == 0 || reported[n-1].ID+" "+reported[n-1].Kind+" "+reported[n-1].Pane != want {
					t.Errorf("reported %+v, want the resumed session last: %s", reported, want)
				}
			}
		})
	}
}

// A session the start adopted (herdr's live one, no record of its kind) is the agent's record from then on: the next
// restart after its process exited (the pane at its shell, herdr reporting no session) and after its pane is gone (a
// new pane placed) resumes it from the record, where before neither a stale record of another kind nor none could
// (RV-240 #2's probe: agy fires no SessionStart on a --conversation resume).
func TestRestartResumesTheSessionItAdopted(t *testing.T) {
	t.Parallel()
	claudeRec := store.Session{Agent: "pj-rev", Kind: "claude", ID: "sid-c", Started: time.Now().Unix()}
	for _, c := range []struct {
		name string
		recs []store.Session
	}{
		{"over another kind's record", []store.Session{claudeRec}},
		{"with no record", nil},
	} {
		for _, gone := range []string{"exited", "pane gone"} {
			t.Run(c.name+", "+gone, func(t *testing.T) {
				t.Parallel()
				x := reviewerOn(t, "neon", c.recs...)
				x.agent("w1:p7", "pj-rev", "agy", "idle").Session = "conv-live"
				out, ok := x.restart(launch.Request{Agent: "pj-rev"})
				if !ok || !strings.Contains(x.lastStart(), "--conversation conv-live") {
					t.Fatalf("precondition: the first restart adopts conv-live: %q\n%s", x.lastStart(), out)
				}
				eq(t, x.current("pj-rev"), "conv-live agy", "the adopted session recorded")
				req := launch.Request{Agent: "pj-rev"}
				var pl placer
				pane := "w1:p7"
				if gone == "exited" {
					x.shell("w1:p7", "pj-rev", "").Session = ""
				} else {
					x.reset()
					pane, req.Place = "w1:p9", pl.at(x, "w1:p9", "")
				}
				out, ok = x.restart(req)
				if !ok {
					t.Fatal(out)
				}
				contains(t, out, "resuming conv-live (from record)", "the record")
				restarted(t, last(out), "pj-rev", pane, "conv-live")
				contains(t, x.lastStart(), "--conversation conv-live", "the adopted session resumed again")
				contains(t, x.lastStart(), "pane="+pane, "in its pane")
				lacks(t, out, "new session", "no new session")
			})
		}
	}
}
