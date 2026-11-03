package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"obor/internal/config"
	"obor/internal/store"
)

// T-753, T-760: the owner's setup edit (config_set): serve applies a list of changes to .obor/config.json, validates
// them together, writes and commits that one file once (ConfigFile), records fact config.set and sends the lead one
// line; any change refused refuses them all.

// configJSON is the edit tests' config.json: two claude types and an opencode one a level names with an effort.
const configJSON = `{
  "version": 1,
  "name": "pj",
  "prefix": "pj",
  "difficulty": {
    "hard": [
      "opus:high"
    ],
    "simple": [
      "deepseek:high"
    ]
  },
  "lane_types": {
    "opus": {
      "kind": "claude",
      "args": [
        "--model",
        "opus",
        "--effort",
        "medium"
      ],
      "max": 4
    },
    "sonnet": {
      "kind": "claude",
      "args": [
        "--model",
        "sonnet"
      ]
    },
    "deepseek": {
      "kind": "opencode",
      "args": [
        "--model",
        "opencode-go/deepseek"
      ]
    }
  },
  "lanes": {
    "O-01": ""
  },
  "roles": {
    "lead": {
      "type": "opus:high"
    },
    "integrator": {}
  }
}
`

// fakeConfig is a ConfigFile on the harness's config.json: Read answers refused, Commit writes the file and records
// the message (failCommit: refused, the file left as it was).
type fakeConfig struct {
	mu         sync.Mutex
	path       string
	refused    string
	failCommit error
	commits    []string
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

func (f *fakeConfig) Commit(_ context.Context, doc []byte, msg string) (string, error) {
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
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: sched, Agents: sched, Typer: sched, Config: f}, nil }
	h.lanes.lanes[0].Task = "" // O-01 free
	h.connect()
	h.a.mu.Lock()
	lead := h.a.p.out.lead()
	h.a.mu.Unlock()
	if lead != "pj-lead" {
		t.Fatalf("precondition: the lead is %q, want pj-lead", lead)
	}
	return h, f
}

// set is the owner's config_set of key to val (JSON) from the web.
func (h *harness) set(callID, key, val string) (string, error) {
	return h.a.configCall(wire.Call{Root: h.root, CallID: callID, Changes: changes(key, val), Source: "web"})
}

// changes is key, value pairs (the value JSON) as a config_set call's changes.
func changes(kv ...string) []wire.ConfigChange {
	var out []wire.ConfigChange
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, wire.ConfigChange{Key: kv[i], Value: json.RawMessage(kv[i+1])})
	}
	return out
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

// leadLines is the lines to the lead in the log.
func leadLines(t *testing.T, h *harness) []string {
	t.Helper()
	var out []string
	for _, r := range readLog(t, h.root) {
		var m event.Message
		if r.Step == event.StepMessage && json.Unmarshal(r.Data, &m) == nil && m.To == "pj-lead" {
			out = append(out, m.Line)
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
		{"difficulty.hard", `["sonnet","opus:high"]`, `config: difficulty.hard ["opus:high"] → ["sonnet","opus:high"] (owner, web)`,
			"\"hard\": [\n      \"sonnet\",\n      \"opus:high\"\n    ]", `SET difficulty.hard ["opus:high"] → ["sonnet","opus:high"]`},
		{"lane_types.opus.model", `"fable"`, `config: lane_types.opus.model "opus" → "fable" (owner, web)`,
			"\"--model\",\n        \"fable\",\n        \"--effort\",\n        \"medium\"", `SET lane_types.opus.model "opus" → "fable"`},
		{"lane_types.opus.effort", `"high"`, `config: lane_types.opus.effort "medium" → "high" (owner, web)`,
			"\"--effort\",\n        \"high\"", `SET lane_types.opus.effort "medium" → "high"`},
		{"lane_types.opus.max", `0`, `config: lane_types.opus.max 4 → 0 (owner, web)`,
			"        \"medium\"\n      ]\n    },", `SET lane_types.opus.max 4 → 0`},
		{"lanes.O-01", `"sonnet"`, `config: lanes.O-01 "" → "sonnet" (owner, web)`, `"O-01": "sonnet"`,
			`SET lanes.O-01 "" → "sonnet"`},
		{"roles.integrator.type", `"sonnet"`, `config: roles.integrator.type unset → "sonnet" (owner, web)`,
			"\"integrator\": {\n      \"type\": \"sonnet\"\n    }", `SET roles.integrator.type unset → "sonnet" (at its next start)`},
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

// A refused change leaves the file as it was, commits nothing and records nothing: a type reference naming no lane
// type, an unknown lane type, a change Validate fails (the opencode type's model gone while simple names it with an
// effort), a config with uncommitted changes (named), a worker's call, a failed commit.
func TestConfigSetRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, key, val, refused string
		failCommit              error
		role, want              string
	}{
		{name: "unknown ref", key: "difficulty.hard", val: `["gpt"]`, want: `"gpt" is not a lane type of this project`},
		{name: "unknown type", key: "lane_types.gpt.model", val: `"x"`, want: `"gpt" is not a lane type of this project`},
		{name: "role ref", key: "roles.lead.type", val: `"opus:turbo"`, want: `effort "turbo" is not one of claude's`},
		{name: "validate", key: "lane_types.deepseek.model", val: `""`, want: "FAIL .difficulty.simple[0]"},
		{name: "uncommitted", key: "lanes.O-01", val: `"opus"`, want: ".obor/config.json has uncommitted changes",
			refused: ".obor/config.json has uncommitted changes in /pj: commit or discard them first"},
		{name: "worker", key: "lanes.O-01", val: `"opus"`, role: "worker", want: "the owner or the lead changes the setup"},
		{name: "commit", key: "lanes.O-01", val: `"opus"`, failCommit: errors.New("git commit: hook said no"), want: "hook said no"},
		{name: "not editable", key: "commands.check", val: `"x"`, want: "is not an editable key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := configRig(t)
			f.refused, f.failCommit = c.refused, c.failCommit
			call := wire.Call{Root: h.root, CallID: "r1", Changes: changes(c.key, c.val), Role: c.role}
			if c.role != "" {
				call.Agent = "pj-o-01"
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

// A lane's type is not changed while the lane has a task (the lanes read's, its row's or the merge loop's open task
// there); once the task ended, it is.
func TestConfigSetLaneWithTask(t *testing.T) {
	t.Parallel()
	busy := newHarness(t) // the lanes read puts T-1 on O-01
	busy.write(filepath.Join(busy.root, ".obor", "config.json"), configJSON)
	bf := &fakeConfig{path: filepath.Join(busy.root, ".obor", "config.json")}
	busy.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Config: bf}, nil }
	busy.connect()
	if reply, err := busy.set("b1", "lanes.O-01", `"opus"`); err == nil || !strings.Contains(err.Error(), "lane O-01 has task T-1") {
		t.Errorf("set while the lanes read puts T-1 on O-01: %q, %v; want refused", reply, err)
	}
	h, f := configRig(t)
	if reply, err := h.set("l0", "lanes.O-01", `""`); err != nil || reply != "UNCHANGED lanes.O-01" {
		t.Fatalf("precondition: O-01 free takes a change: %q, %v", reply, err)
	}
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1"}
	h.a.mu.Unlock()
	if reply, err := h.set("l1", "lanes.O-01", `"opus"`); err == nil || !strings.Contains(err.Error(), "lane O-01 has task T-1") {
		t.Fatalf("set while O-01 has T-1: %q, %v; want refused", reply, err)
	}
	if b, _ := os.ReadFile(f.path); string(b) != configJSON || len(f.messages()) != 0 {
		t.Fatalf("the refused change wrote the file or committed: %q", f.messages())
	}
	h.a.mu.Lock()
	h.a.p.folded.loop["T-1"] = &mtask{task: "T-1", lane: "O-01", state: stMerged}
	h.a.mu.Unlock()
	if reply, err := h.set("l2", "lanes.O-01", `"opus"`); err != nil || !strings.HasPrefix(reply, "SET lanes.O-01") {
		t.Fatalf("set once T-1 merged: %q, %v", reply, err)
	}
}

// A change racing a publish waits: while the publish runs nothing is written or committed; once it ends the change
// goes in. It is a difficulty change: the rig's O-01 holds T-1, whose merge this is, so a lane change would depend on
// when the lane is read again.
func TestConfigSetWaitsForPublish(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	path := filepath.Join(h.root, ".obor", "config.json")
	h.write(path, `{"version":1,"name":"pj","prefix":"pj","roles":{"integrator":{},"reviewer":{},"deep_dive":{}},`+
		`"paths":{"reviews":"reviews"},"lane_types":{"opus":{"kind":"claude"}},"lanes":{"O-01":""}}`)
	f := &fakeConfig{path: path}
	r.fx.Config = f
	r.publishing(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := h.set("w1", "difficulty.hard", `["opus"]`)
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
	if got := f.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "config: difficulty.hard") {
		t.Errorf("commits %q, want the change's", got)
	}
}

// T-760: several changes are one commit — applied in order, each to the file the one before wrote (a model and an
// effort of the same lane type both land) — with a subject naming the keys, one body line per change, one fact
// listing them, one line to the lead and one reply. A change that leaves its key as it is is left out.
func TestConfigSetBatchIsOneCommit(t *testing.T) {
	t.Parallel()
	h, f := configRig(t)
	reply, err := h.a.configCall(wire.Call{Root: h.root, CallID: "b1", Source: "web", Changes: changes(
		"lane_types.opus.model", `"fable"`, "lane_types.opus.effort", `"high"`, "lanes.O-01", `""`,
		"roles.integrator.type", `"sonnet"`)})
	want := `SET lane_types.opus.model "opus" → "fable"; SET lane_types.opus.effort "medium" → "high"; ` +
		`SET roles.integrator.type unset → "sonnet" (at its next start)`
	if err != nil || reply != want {
		t.Fatalf("batch: %q, %v; want %q", reply, err, want)
	}
	b, _ := os.ReadFile(f.path)
	if !strings.Contains(string(b), "\"--model\",\n        \"fable\",\n        \"--effort\",\n        \"high\"") ||
		!strings.Contains(string(b), "\"integrator\": {\n      \"type\": \"sonnet\"") {
		t.Errorf("the file lacks a change:\n%s", b)
	}
	msg := "config: lane_types.opus.model, lane_types.opus.effort, roles.integrator.type (owner, web)\n\n" +
		"- lane_types.opus.model \"opus\" → \"fable\"\n- lane_types.opus.effort \"medium\" → \"high\"\n" +
		"- roles.integrator.type unset → \"sonnet\""
	if got := f.messages(); len(got) != 1 || got[0] != msg {
		t.Errorf("commits %q, want one:\n%s", got, msg)
	}
	facts := configFacts(t, h)
	if len(facts) != 1 || len(facts[0].Changes) != 3 || facts[0].Changes[1] != (event.ConfigSetChange{
		Key: "lane_types.opus.effort", Old: `"medium"`, New: `"high"`}) || facts[0].Reply != want {
		t.Errorf("facts %+v, want one with the three changes", facts)
	}
	line := `[obor] config: lane_types.opus.model "opus" → "fable"; lane_types.opus.effort "medium" → "high"; ` +
		`roles.integrator.type unset → "sonnet" (owner, web)`
	if lines := leadLines(t, h); len(lines) != 1 || lines[0] != line {
		t.Errorf("lines to the lead %q, want %q", lines, line)
	}
	if r, err := h.a.configCall(wire.Call{Root: h.root, CallID: "b2", Changes: changes("lanes.O-01", `""`)}); err != nil ||
		r != "UNCHANGED lanes.O-01" {
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
		{"second refused", []string{"lane_types.opus.model", `"fable"`, "difficulty.hard", `["gpt"]`}, `"gpt" is not a lane type`},
		{"validate", []string{"lanes.O-01", `"opus"`, "lane_types.deepseek.model", `""`},
			"lanes.O-01, lane_types.deepseek.model refused: FAIL .difficulty.simple[0]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := configRig(t)
			reply, err := h.a.configCall(wire.Call{Root: h.root, CallID: "x1", Changes: changes(c.kv...)})
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
	if _, err := h.a.configCall(wire.Call{Root: h.root, CallID: "t1", Base: base, Changes: changes("lanes.O-01", `"opus"`)}); err != nil {
		t.Fatalf("the first tab's save: %v", err)
	}
	_, err := h.a.configCall(wire.Call{Root: h.root, CallID: "t2", Base: base, Changes: changes("lanes.O-01", `"sonnet"`)})
	if err == nil || !strings.Contains(err.Error(), "changed since this page read it") {
		t.Fatalf("the second tab's save on the old file: %v, want refused", err)
	}
	b, _ := os.ReadFile(f.path)
	if !strings.Contains(string(b), `"O-01": "opus"`) || len(f.messages()) != 1 {
		t.Errorf("the stale save wrote or committed: %q\n%s", f.messages(), b)
	}
	if _, err := h.a.configCall(wire.Call{Root: h.root, CallID: "t3", Base: config.Rev(b), Changes: changes("lanes.O-01", `"sonnet"`)}); err != nil {
		t.Errorf("a save on the current file: %v", err)
	}
}
