package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
)

// T-753, T-760: the owner's setup edit (config_set): serve applies a list of changes to .obor/config.json, validates
// them together, writes and commits that one file once (ConfigFile), records fact config.set and sends the lead one
// line; any change refused refuses them all.

// configJSON is the edit tests' config.json: an opencode model a level names with an effort, a kind's args, two lanes,
// and no trunk (branches.main ""): these edits test the config_set mechanism, not validate's trunk check (config's
// TestValidateTrunkRef), and this plain dir has no repository for that check to read, so it starts no git process here
// (T-1024: the app package's tests run no git).
const configJSON = `{
  "version": 2,
  "project": {
    "name": "pj",
    "agent_prefix": "pj"
  },
  "lanes": {
    "count": 2
  },
  "agents": {
    "roles": {
      "lead": "opus:high",
      "integrator": ""
    },
    "difficulty": {
      "hard": [
        "opus:high"
      ],
      "simple": [
        "kimi:high"
      ]
    },
    "kinds": {
      "claude": {
        "args": [
          "--x"
        ]
      },
      "opencode": {
        "plugin": "obor-kind-opencode"
      }
    },
    "models": {
      "kimi": {
        "kind": "opencode",
        "id": "opencode-go/kimi"
      }
    }
  },
  "merge": {
    "target_branch": ""
  }
}
`

// fakeConfig is a ConfigFile on the harness's config.json: Read answers refused, Commit writes the file and records
// the message (failCommit: refused, the file left as it was). commitGate, when set, holds Commit until it is closed
// and commitBegan gets a value when a Commit reaches it: a call in flight inside configMu (T-930).
type fakeConfig struct {
	mu          sync.Mutex
	path        string
	refused     string
	failCommit  error
	commitGate  chan struct{}
	commitBegan chan struct{}
	commits     []string
}

func (f *fakeConfig) Read(context.Context) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refused != "" {
		return nil, f.refused, nil
	}
	b, err := os.ReadFile(f.path)
	return b, "", err
}

func (f *fakeConfig) Commit(ctx context.Context, doc []byte, msg string) (string, error) {
	f.mu.Lock()
	gate, began := f.commitGate, f.commitBegan
	f.mu.Unlock()
	if gate != nil {
		if began != nil {
			began <- struct{}{}
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCommit != nil {
		return "", f.failCommit
	}
	if err := os.WriteFile(f.path, doc, 0o644); err != nil {
		return "", err
	}
	f.commits = append(f.commits, msg)
	return "c0ffee1234567", nil
}

func (f *fakeConfig) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commits...)
}

// configRig is a harness on configJSON, connected, whose loop has a fakeConfig and whose lead is pj-lead.
func configRig(t *testing.T) (*harness, *fakeConfig) {
	t.Helper()
	h := newHarness(t)
	path := filepath.Join(h.root, ".obor", "config.json")
	h.write(path, configJSON)
	f := &fakeConfig{path: path}
	sched := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(sched), Agents: sched, Typer: sched, Config: f}, nil
	}
	h.lanes.lanes[0].Task = "" // L-01 free
	h.connect()
	h.a.mu.Lock()
	lead := h.a.p.out.Lead()
	h.a.mu.Unlock()
	if lead != "pj-lead" {
		t.Fatalf("precondition: the lead is %q, want pj-lead", lead)
	}
	return h, f
}

// configFacts is the log's config.set facts' data.
func configFacts(t *testing.T, h *harness) []event.ConfigSet {
	t.Helper()
	var out []event.ConfigSet
	for _, r := range readLog(t, h.root) {
		if r.Task == event.ConfigTask && r.Step == event.StepConfigSet && r.Kind == event.KindFact {
			var d event.ConfigSet
			_ = json.Unmarshal(r.Data, &d)
			out = append(out, d)
		}
	}
	return out
}

// Each editable field set: the file is written with the change (and loads with it), committed with the message
// "config: <key> <old> → <new> (owner, web)", recorded as fact config.set with the commit, and the lead gets the line.
// A role's reply says it applies at its next start. The same call id again gets the stored reply: nothing more.
func TestConfigSetEachField(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		key, val, msg, wantIn, reply string
	}{
		{"agents.difficulty.hard", `["sonnet","opus:high"]`, `config: agents.difficulty.hard ["opus:high"] → ["sonnet","opus:high"] (owner, web)`,
			"\"hard\": [\n        \"sonnet\",\n        \"opus:high\"\n      ]", `SET agents.difficulty.hard ["opus:high"] → ["sonnet","opus:high"]`},
		{"agents.kinds.claude.args", `["--strict-mcp-config"]`, `config: agents.kinds.claude.args ["--x"] → ["--strict-mcp-config"] (owner, web)`,
			"\"args\": [\n          \"--strict-mcp-config\"\n        ]", `SET agents.kinds.claude.args ["--x"] → ["--strict-mcp-config"]`},
		{"lanes.count", `3`, `config: lanes.count 2 → 3 (owner, web)`, `"count": 3`, `SET lanes.count 2 → 3`},
		{"agents.roles.integrator", `"sonnet"`, `config: agents.roles.integrator unset → "sonnet" (owner, web)`,
			"\"integrator\": \"sonnet\"", `SET agents.roles.integrator unset → "sonnet" (at its next start)`},
	} {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			h, f := configRig(t)
			reply, err := h.set("c1", c.key, c.val)
			if err != nil || reply != c.reply {
				t.Fatalf("set: %q, %v; want %q", reply, err, c.reply)
			}
			b, _ := os.ReadFile(f.path)
			if !strings.Contains(string(b), c.wantIn) || !strings.HasSuffix(string(b), "}\n") {
				t.Errorf("the file has no %q:\n%s", c.wantIn, b)
			}
			if got := f.messages(); len(got) != 1 || got[0] != c.msg {
				t.Errorf("commits %q, want one: %q", got, c.msg)
			}
			facts := configFacts(t, h)
			if len(facts) != 1 || len(facts[0].Changes) != 1 || facts[0].Changes[0].Key != c.key ||
				facts[0].Commit != "c0ffee1234567" || facts[0].By != "owner" ||
				facts[0].Source != "web" || facts[0].Call != "c1" {
				t.Errorf("config.set facts %+v, want one of %s with the commit", facts, c.key)
			}
			if lines := leadLines(t, h); len(lines) != 1 || lines[0] != "[obor] "+c.msg {
				t.Errorf("lines to the lead %q, want %q", lines, "[obor] "+c.msg)
			}
			if again, err := h.set("c1", c.key, `"other"`); err != nil || again != reply {
				t.Errorf("the same call id again: %q, %v; want the stored reply", again, err)
			}
			if len(f.messages()) != 1 || len(configFacts(t, h)) != 1 {
				t.Errorf("the repeated call committed or recorded again: %q", f.messages())
			}
		})
	}
}

// A refused change leaves the file as it was, commits nothing and records nothing: a model ref naming no model, an
// effort its kind lacks, a key no longer editable, a config with uncommitted changes (named), a worker's call, a
// failed commit.
func TestConfigSetRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, key, val, refused string
		failCommit              error
		role, want              string
	}{
		{name: "unknown ref", key: "agents.difficulty.hard", val: `["gpt"]`, want: `"gpt" is not a model`},
		{name: "role ref", key: "agents.roles.lead", val: `"opus:turbo"`, want: `effort "turbo" is not one of claude's`},
		{name: "no caps", key: "lane_types.opus.max", val: `2`, want: "is not an editable key"},
		{name: "uncommitted", key: "lanes.count", val: `3`, want: ".obor/config.json has uncommitted changes",
			refused: ".obor/config.json has uncommitted changes in /pj: commit or discard them first"},
		{name: "worker", key: "lanes.count", val: `3`, role: "worker", want: "the owner or the lead changes the setup"},
		{name: "commit", key: "lanes.count", val: `3`, failCommit: errors.New("git commit: hook said no"), want: "hook said no"},
		{name: "not editable", key: "commands.check", val: `"x"`, want: "is not an editable key"},
		// T-781: obor nightly on|off's change
		{name: "nightly 25:00", key: "nightly.at", val: `"25:00"`, want: "nightly.at takes a local time HH:MM"},
		{name: "nightly no time", key: "nightly.at", val: `""`, want: "nightly.at takes a local time HH:MM"},
		{name: "nightly by a worker", key: "nightly.at", val: `"03:00"`, role: "worker",
			want: "the owner or the lead changes the setup"},
		{name: "nightly by the integrator", key: "nightly.at", val: `null`, role: "integrator",
			want: "the owner or the lead changes the setup"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := configRig(t)
			f.refused, f.failCommit = c.refused, c.failCommit
			call := wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "r1", Role: c.role},
				Changes: changes(c.key, c.val)}
			if c.role != "" {
				call.Agent = "pj-l-01"
			}
			reply, err := h.a.configCall(call)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("set: %q, %v; want refused with %q", reply, err, c.want)
			}
			if b, _ := os.ReadFile(f.path); string(b) != configJSON {
				t.Errorf("the file changed:\n%s", b)
			}
			if len(f.messages()) != 0 || len(configFacts(t, h)) != 0 || len(leadLines(t, h)) != 0 {
				t.Errorf("a refused change committed %q, recorded %+v, told the lead %q", f.messages(), configFacts(t, h), leadLines(t, h))
			}
		})
	}
}

// T-765: the lanes count is not lowered while a lane it drops has a task (the lanes read's, its row's or the merge
// loop's open task there); once the task ended, it is.
func TestConfigSetLanesWithTask(t *testing.T) {
	t.Parallel()
	busy := newHarness(t) // the lanes read puts T-1 on L-01
	busy.write(filepath.Join(busy.root, ".obor", "config.json"), configJSON)
	bf := &fakeConfig{path: filepath.Join(busy.root, ".obor", "config.json")}
	busy.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Config: bf}, nil }
	busy.connect()
	if reply, err := busy.set("b1", "lanes.count", `0`); err == nil || !strings.Contains(err.Error(), "lane L-01 has task T-1") {
		t.Errorf("lanes.count 0 while the lanes read puts T-1 on L-01: %q, %v; want refused", reply, err)
	}
	h, f := configRig(t)
	if reply, err := h.set("l0", "lanes.count", `2`); err != nil || reply != "UNCHANGED lanes.count" {
		t.Fatalf("precondition: an unchanged count: %q, %v", reply, err)
	}
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-02"] = store.LaneRow{Lane: "L-02", Task: "T-1"}
	h.a.mu.Unlock()
	if reply, err := h.set("l1", "lanes.count", `1`); err == nil || !strings.Contains(err.Error(), "lane L-02 has task T-1: lower lanes once it is free") {
		t.Fatalf("lanes.count 1 while L-02 has T-1: %q, %v; want refused", reply, err)
	}
	if reply, err := h.set("l1b", "lanes.count", `3`); err != nil || !strings.HasPrefix(reply, "SET lanes.count 2 → 3") {
		t.Fatalf("raising the count while L-02 has T-1: %q, %v; want it set (no lane dropped)", reply, err)
	}
	h.a.mu.Lock()
	h.a.p.folded.loop["T-1"] = &mtask{task: "T-1", lane: "L-02", state: stMerged}
	delete(h.a.p.folded.lr.rows, "L-02")
	h.a.mu.Unlock()
	if reply, err := h.set("l2", "lanes.count", `1`); err != nil || !strings.HasPrefix(reply, "SET lanes.count 3 → 1") {
		t.Fatalf("lanes.count 1 once T-1 merged: %q, %v", reply, err)
	}
	if n := len(f.messages()); n != 2 {
		t.Errorf("commits %d, want 2", n)
	}
}

// A change racing a publish waits: while the publish runs nothing is written or committed; once it ends the change
// goes in. It is a difficulty change: the rig's L-01 holds T-1, whose merge this is, so a lane change would depend on
// when the lane is read again.
func TestConfigSetWaitsForPublish(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	path := filepath.Join(h.root, ".obor", "config.json")
	h.write(path, `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":1},`+
		`"agents":{"roles":{"integrator":"","reviewer":"","deep_dive":""}},"merge":{"target_branch":""},"reviews":{"dir":"reviews"}}`)
	f := &fakeConfig{path: path}
	r.fx.Config = f
	r.publishing(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := h.set("w1", "agents.difficulty.hard", `["opus"]`)
		done <- err
	}()
	if !waiting(done) {
		t.Fatal("the change answered while the publish ran")
	}
	if len(f.messages()) != 0 {
		t.Fatalf("committed %q while the publish ran", f.messages())
	}
	r.released(ctx)
	if err := <-done; err != nil {
		t.Fatalf("the change after the publish: %v", err)
	}
	if got := f.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "config: agents.difficulty.hard") {
		t.Errorf("commits %q, want the change's", got)
	}
}

// T-930: a repeated call id waits for the call in flight (configMu is taken before the stored-reply lookup): the second
// gets the first's reply and nothing is written or committed again.
func TestConfigSetSameCallWhileInFlight(t *testing.T) {
	t.Parallel()
	h, f := configRig(t)
	gate, began := make(chan struct{}), make(chan struct{}, 1)
	f.commitGate, f.commitBegan = gate, began
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	first := make(chan callReply, 1)
	go func() {
		reply, err := h.set("c1", "lanes.count", `3`)
		first <- callReply{reply, err}
	}()
	select {
	case <-began: // the first is in its commit, holding configMu
	case <-time.After(5 * time.Second):
		t.Fatal("precondition: the first call never reached its commit")
	}
	second := make(chan callReply, 1)
	go func() {
		reply, err := h.set("c1", "lanes.count", `4`)
		second <- callReply{reply, err}
	}()
	if !waiting(second) {
		t.Error("the repeated call answered while the first held configMu, want it to wait")
	}
	release()
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.reply != `SET lanes.count 2 → 3` || b.reply != a.reply {
		t.Fatalf("calls: %q, %v and %q, %v; want both the first's reply", a.reply, a.err, b.reply, b.err)
	}
	if got := f.messages(); len(got) != 1 || len(configFacts(t, h)) != 1 {
		t.Errorf("commits %q, %d facts; want one commit and one fact (the repeated call id did nothing)", got,
			len(configFacts(t, h)))
	}
}

// T-760: several changes are one commit — applied in order, each to the file the one before wrote (a level and a
// role both land) — with a subject naming the keys, one body line per change, one fact listing them, one line to the
// lead and one reply. A change that leaves its key as it is is left out.
func TestConfigSetBatchIsOneCommit(t *testing.T) {
	t.Parallel()
	h, f := configRig(t)
	reply, err := h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "b1"}, Source: "web",
		Changes: changes(
			"agents.difficulty.hard", `["fable"]`, "agents.difficulty.hard", `["fable:high"]`, "lanes.count", `2`,
			"agents.roles.integrator", `"sonnet"`)})
	want := `SET agents.difficulty.hard ["opus:high"] → ["fable"]; SET agents.difficulty.hard ["fable"] → ["fable:high"]; ` +
		`SET agents.roles.integrator unset → "sonnet" (at its next start)`
	if err != nil || reply != want {
		t.Fatalf("batch: %q, %v; want %q", reply, err, want)
	}
	b, _ := os.ReadFile(f.path)
	if !strings.Contains(string(b), "\"hard\": [\n        \"fable:high\"\n      ]") ||
		!strings.Contains(string(b), "\"integrator\": \"sonnet\"") {
		t.Errorf("the file lacks a change:\n%s", b)
	}
	msg := "config: agents.difficulty.hard, agents.difficulty.hard, agents.roles.integrator (owner, web)\n\n" +
		"- agents.difficulty.hard [\"opus:high\"] → [\"fable\"]\n- agents.difficulty.hard [\"fable\"] → [\"fable:high\"]\n" +
		"- agents.roles.integrator unset → \"sonnet\""
	if got := f.messages(); len(got) != 1 || got[0] != msg {
		t.Errorf("commits %q, want one:\n%s", got, msg)
	}
	facts := configFacts(t, h)
	if len(facts) != 1 || len(facts[0].Changes) != 3 || facts[0].Changes[1] != (event.ConfigSetChange{
		Key: "agents.difficulty.hard", Old: `["fable"]`, New: `["fable:high"]`}) || facts[0].Reply != want {
		t.Errorf("facts %+v, want one with the three changes", facts)
	}
	line := `[obor] config: agents.difficulty.hard ["opus:high"] → ["fable"]; agents.difficulty.hard ["fable"] → ["fable:high"]; ` +
		`agents.roles.integrator unset → "sonnet" (owner, web)`
	if lines := leadLines(t, h); len(lines) != 1 || lines[0] != line {
		t.Errorf("lines to the lead %q, want %q", lines, line)
	}
	if r, err := h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "b2"},
		Changes: changes("lanes.count", `2`)}); err != nil ||
		r != "UNCHANGED lanes.count" {
		t.Errorf("a batch that changes nothing: %q, %v", r, err)
	}
}

// T-760: one refused change refuses the batch (the others before it applied in memory only): nothing written,
// committed, recorded or told; and a batch the whole fails Validate on is refused naming its keys.
func TestConfigSetBatchAllOrNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		kv   []string
		want string
	}{
		{"second refused", []string{"lanes.count", `3`, "agents.difficulty.hard", `["gpt"]`}, `"gpt" is not a model`},
		{"validate", []string{"lanes.count", `3`, "agents.kinds.opencode.args", `["--"]`}, "`--` is not allowed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := configRig(t)
			reply, err := h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "x1"},
				Changes: changes(c.kv...)})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("batch: %q, %v; want refused with %q", reply, err, c.want)
			}
			if b, _ := os.ReadFile(f.path); string(b) != configJSON {
				t.Errorf("the file changed:\n%s", b)
			}
			if len(f.messages()) != 0 || len(configFacts(t, h)) != 0 || len(leadLines(t, h)) != 0 {
				t.Errorf("a refused batch committed %q, recorded %+v, told %q", f.messages(), configFacts(t, h), leadLines(t, h))
			}
		})
	}
}

// T-760: a call names the config.json revision its page read; once the file is another one (another tab saved, the
// lead or a hand edit changed it) the call is refused and nothing is written. The current revision is taken.
func TestConfigSetBaseIsTheFileRead(t *testing.T) {
	t.Parallel()
	h, f := configRig(t)
	base := config.Rev([]byte(configJSON))
	if _, err := h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "t1"}, Base: base,
		Changes: changes("lanes.count", `3`)}); err != nil {
		t.Fatalf("the first tab's save: %v", err)
	}
	_, err := h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "t2"}, Base: base,
		Changes: changes("lanes.count", `4`)})
	if err == nil || !strings.Contains(err.Error(), "changed since this page read it") {
		t.Fatalf("the second tab's save on the old file: %v, want refused", err)
	}
	b, _ := os.ReadFile(f.path)
	if !strings.Contains(string(b), `"count": 3`) || len(f.messages()) != 1 {
		t.Errorf("the stale save wrote or committed: %q\n%s", f.messages(), b)
	}
	if _, err := h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: "t3"},
		Base: config.Rev(b), Changes: changes("lanes.count", `4`)}); err != nil {
		t.Errorf("a save on the current file: %v", err)
	}
}

// T-1125: a config.json of shape 1 refuses config_set with the hint (the rewrite comes first: an edit would mix the
// shapes): nothing written, committed, recorded or told.
func TestConfigSetRefusesShape1(t *testing.T) {
	t.Parallel()
	h, f := configRig(t)
	shape1 := `{"version":1,"name":"pj","prefix":"pj","branches":{"main":""},"lanes":2}` + "\n"
	h.write(f.path, shape1)
	reply, err := h.set("s1", "lanes.count", `3`)
	if err == nil || !strings.Contains(err.Error(), "is config shape 1: obor config migrate rewrites it") {
		t.Fatalf("config_set on shape 1: %q, %v; want refused with the hint", reply, err)
	}
	if b, _ := os.ReadFile(f.path); string(b) != shape1 {
		t.Errorf("the file changed:\n%s", b)
	}
	if len(f.messages()) != 0 || len(configFacts(t, h)) != 0 || len(leadLines(t, h)) != 0 {
		t.Errorf("a refused edit committed %q, recorded %+v, told %q", f.messages(), configFacts(t, h), leadLines(t, h))
	}
}
