package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/roles"
	"obor/internal/store"
	"obor/internal/testkit"
)

// stubKind is a plugin kind (T-1036) as a POSIX sh script that appends every request to a log, one line each, and
// answers start with args and env naming one generated file whose bytes carry the request's kit (as the conformance
// script's start does); LOG is the log.
const stubKind = `#!/bin/sh
req=$(cat)
printf '%s\n' "$req" >>'LOG'
field() { printf '%s' "$req" | sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p'; }
case "$1" in
describe) cat <<'JSON'
{"version":1,"name":"stub","status":"terminal","caps":{"resume":true,"named_session":true},"efforts":["low","high"],
 "vars":["STUB_CONFIG"],"env":["HOME"],"stop":{"quit":"/exit"},"transcript":"${HOME}/.stub/{sid}.jsonl",
 "mcp_lane":{"how":"STUB_CONFIG (the start's config)","registered":true}}
JSON
;;
start) cat <<JSON
{"version":1,"args":["--config","{file:stub.json}"],"env":{"STUB_CONFIG":"{file:stub.json}"},
 "files":[{"name":"stub.json","bytes":"{\"mcp\":\"$(field kit)/bin/obor mcp\"}"}]}
JSON
;;
install) echo '{"version":1,"mcp":{"where":"start"}}' ;;
*) echo "no call $1" >&2; exit 2 ;;
esac
`

// startRequest is a start request as the stub logged it.
type startRequest struct {
	Call      string         `json:"call"`
	Kit       string         `json:"kit"`
	Args      []string       `json:"args"`
	Settings  map[string]any `json:"settings"`
	Resume    string         `json:"resume"`
	Prompt    string         `json:"prompt"`
	Effort    string         `json:"effort"`
	StrictMCP bool           `json:"strict_mcp"`
	Session   string         `json:"session"`
	WriteDirs []string       `json:"write_dirs"`
	Dir       string         `json:"dir"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// T-1042 (RV-147 #1): every launch of a plugin kind makes exactly one start call, with the complete start (the kit, the
// type's args, the role's settings and strictness, the effort, the cwd, the write dirs, the first prompt or the resume,
// the session obor names), and applies that one reply's argv, env and file together: setup's first start (a new pane)
// and a reused pane, a restart fresh and resumed, and setup's MCP model inspection. The generated file holds the
// kit's `<kit>/bin/obor mcp` after each (the partial starts once wrote `/bin/obor mcp` over it).
//
// Not parallel: a config load replaces the registry's plugin kinds (agents.SetPlugins), so a parallel test's load
// would drop this one's mid-launch.
func TestPluginKindStartsOnce(t *testing.T) {
	dir := t.TempDir()
	cmd, logFile := filepath.Join(dir, "obor-kind-stub"), filepath.Join(dir, "requests")
	if err := os.WriteFile(cmd, []byte(strings.ReplaceAll(stubKind, "LOG", logFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	x := newFx(t, testkit.Set("agents.kinds", `{"stub":{"plugin":"`+cmd+`","args":["--base"]}}`),
		testkit.Set("agents.models", `{"st":{"kind":"stub","id":""}}`), testkit.Set("agents.roles", `{"reviewer":"st:high"}`))
	state := filepath.Join(x.Root, ".obor", "state")
	if _, err := plugin.Cache("stub", cmd, state); err != nil { // obor setup agents' cache
		t.Fatal(err)
	}
	t.Cleanup(func() { agents.SetPlugins(nil) })
	lanes := map[string]string{}
	for _, l := range []string{"L-01", "L-02", "L-03"} {
		x.lane(t, l)
		x.State(l, map[string]any{"task": "T-" + l[3:], "type": "st:low"})
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range cfg.Lanes() {
		lanes[l.Name] = l.Dir
	}
	if !agents.IsPlugin("stub") {
		t.Fatal("precondition: the config registers kind stub as a plugin kind")
	}
	file := filepath.Join(state, "stub.json")
	settings := map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}

	// once runs do and returns the one start request it made (a failure if it made none, or more).
	seen := 0
	once := func(what string, do func()) startRequest {
		t.Helper()
		os.Remove(file)
		do()
		b, _ := os.ReadFile(logFile)
		var starts []startRequest
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		for _, ln := range lines[seen:] {
			var r startRequest
			if err := json.Unmarshal([]byte(ln), &r); err != nil {
				t.Fatalf("%s: request %q: %v", what, ln, err)
			}
			if r.Call == "start" {
				starts = append(starts, r)
			}
		}
		seen = len(lines)
		if len(starts) != 1 {
			t.Fatalf("%s: %d start requests, want exactly one: %+v", what, len(starts), starts)
		}
		r := starts[0]
		if r.Kit != kit || !slices.Equal(r.Args, []string{"--base"}) {
			t.Errorf("%s: kit %q, args %q (want %s, [--base])", what, r.Kit, r.Args, kit)
		}
		if got := x.Read(file); got != `{"mcp":"`+filepath.Join(kit, "bin", "obor")+` mcp"}` {
			t.Errorf("%s: the generated file holds %q, want the kit's bin/obor mcp", what, got)
		}
		return r
	}
	// check is r against the launch's fields: the strictness, settings, effort, cwd, write dirs, prompt, resume and
	// whether obor named a session.
	check := func(what string, r startRequest, strict bool, effort, dir string, dirs []string, prompt, resume string,
		named bool) {
		t.Helper()
		if r.StrictMCP != strict || r.Effort != effort || r.Dir != dir || !slices.Equal(r.WriteDirs, dirs) ||
			r.Prompt != prompt || r.Resume != resume || !equalSettings(r.Settings, settings) {
			t.Errorf("%s: start request %+v; want strict %v, effort %q, dir %s, write dirs %q, prompt %q, resume %q, "+
				"the settings", what, r, strict, effort, dir, dirs, prompt, resume)
		}
		if named != uuidRE.MatchString(r.Session) {
			t.Errorf("%s: session %q, want a session obor names: %v", what, r.Session, named)
		}
	}
	// applied: the herdr requests since the mark carry the reply's argv (with the session) and its env, together.
	applied := func(what string, r startRequest, env string) {
		t.Helper()
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "args=[--config "+file+"]", what+": the reply's argv started")
		has(t, calls, env, what+": the reply's env")
		if !strings.Contains(calls, "kind=stub") {
			t.Errorf("%s: no start of kind stub in\n%s", what, calls)
		}
	}

	// a restart of a lane's worker in its labelled pane at its shell (obor restart), fresh and resumed
	sessions := map[string]store.Session{}
	restart := func(req launch.Request) {
		t.Helper()
		var out bytes.Buffer
		l := &launch.Launcher{Cfg: cfg, Env: x.Env(), Kit: kit, H: x.srv.Client(), Out: &out, Poll: time.Millisecond,
			Sessions: func(agent string) (store.Session, bool) { s, ok := sessions[agent]; return s, ok },
			Report:   func(context.Context, store.Session) error { return nil }}
		if line, err := l.Restart(context.Background(), req); err != nil || !strings.HasPrefix(line, "RESTARTED ") {
			t.Fatalf("restart %s: %q %v\n%s", req.Agent, line, err, out.String())
		}
	}
	w1, w2 := cfg.AgentName("worker", "L-01"), cfg.AgentName("worker", "L-02")
	x.h.Shell("w1:p8", w1, lanes["L-01"])
	x.h.Shell("w1:p9", w2, lanes["L-02"])
	x.reset()
	r := once("a restart, fresh", func() { restart(launch.Request{Agent: w1, Fresh: true, Prompt: "go"}) })
	check("a restart, fresh", r, true, "low", lanes["L-01"], nil, "go", "", true)
	applied("a restart, fresh", r, "STUB_CONFIG="+file)
	transcript := filepath.Join(dir, "sid-2.jsonl")
	x.Write(transcript, "")
	sessions[w2] = store.Session{Agent: w2, Kind: "stub", ID: "sid-2", Transcript: transcript}
	x.reset()
	r = once("a restart, resumed", func() { restart(launch.Request{Agent: w2}) })
	check("a restart, resumed", r, true, "low", lanes["L-02"], nil, "", "sid-2", false)
	applied("a restart, resumed", r, "STUB_CONFIG="+file)

	reviewer, _ := roles.Lookup("reviewer")
	x.reset()
	r = once("setup's first start", func() { x.ok(t, "role", "reviewer", "--tab", "w1:t1", "--start") })
	check("setup's first start", r, false, "high", cfg.RoleDir(reviewer), cfg.RoleWriteDirs(reviewer), "", "", true)
	applied("setup's first start", r, "STUB_CONFIG="+file)
	if len(r.WriteDirs) == 0 {
		t.Error("precondition: a reviewer writes the reviews dir (write dirs)")
	}

	x.reset()
	r = once("setup's reused pane", func() { x.ok(t, "pane", "--start", "L-03", "--pane", "w1:q1") })
	check("setup's reused pane", r, true, "low", lanes["L-03"], nil, "", "", true)
	applied("setup's reused pane", r, "STUB_CONFIG="+file)

	x.reset()
	r = once("setup's MCP model inspection", func() { x.ok(t, "mcp", "--model", "st") })
	check("setup's MCP model inspection", r, true, "", x.Root, nil, "", "", false)
}

// equalSettings: the request's settings are want (JSON has no typed booleans to compare otherwise).
func equalSettings(got, want map[string]any) bool {
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return bytes.Equal(g, w)
}
