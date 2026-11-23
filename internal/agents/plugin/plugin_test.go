package plugin_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
)

// manifest is the stub kind's describe reply.
const manifest = `{"version":1,"name":"stub","status":"transcript","caps":{"hooks":"none","resume":true,"sessions":true,
"hook_answers":true,"app":"stub-code"},"efforts":["low","high"],"vars":["STUB_CONFIG"],"env":["HOME"],"stop":{"quit":"/quit","clear_input":["ctrl+u"],
"dialog":{"option":"Quit anyway","key":"(\\d)\\. Quit anyway"}},"headless":{"subcommands":["exec"],"print_flag":true,"flags":["--prompt"]},
"transcript":"${HOME}/.stub/{sid}.jsonl","inbox_record":["STUB_INBOX"],"not_ready":{"call":true},
"mcp_lane":{"how":"STUB_CONFIG (the start's config)","registered":true}}`

// The stub plugin: one script for the package (macOS scans each new executable at its first run, ~100 ms), whose
// describe and other calls source the files describe and body of its dir, which each test writes. $REQ is where a
// body saves its request (`cat >"$REQ"`).
var stubDir, stubCmd string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "obor-plugin-test")
	if err != nil {
		panic(err)
	}
	stubDir, stubCmd = dir, filepath.Join(dir, "obor-kind-stub")
	script := "#!/bin/sh\nREQ=" + filepath.Join(dir, "req.json") + "\ncase \"$1\" in\n" +
		"describe) . " + filepath.Join(dir, "describe") + " ;;\n*) . " + filepath.Join(dir, "body") + " ;;\nesac\n"
	if err := os.WriteFile(stubCmd, []byte(script), 0o755); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// reply is a body answering s.
func reply(s string) string { return "cat >/dev/null; cat <<'EOF'\n" + s + "\nEOF\n" }

// stub sets the stub's describe and body (sh source).
func stub(t *testing.T, describe, body string) {
	t.Helper()
	for name, src := range map[string]string{"describe": describe, "body": body} {
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// kind is the stub kind answering manifest and running body, its manifest cached in a new state dir (returned).
func kind(t *testing.T, body string) (*plugin.Kind, string) {
	t.Helper()
	stub(t, reply(manifest), body)
	state := t.TempDir()
	k, err := plugin.Cache("stub", stubCmd, state)
	if err != nil {
		t.Fatalf("Cache: %v", err)
	}
	return k, state
}

// short makes every call's timeout d until the returned func restores them.
func short(d time.Duration) (restore func()) {
	old := plugin.Timeouts
	plugin.Timeouts = map[string]time.Duration{}
	for c := range old {
		plugin.Timeouts[c] = d
	}
	return func() { plugin.Timeouts = old }
}

// logged captures plugin.Log for the test.
func logged(t *testing.T) *bytes.Buffer {
	var b bytes.Buffer
	old := plugin.Log
	plugin.Log = &b
	t.Cleanup(func() { plugin.Log = old })
	return &b
}

// call is one protocol call as a Kind method runs it.
type call struct {
	name string
	good string                                                              // the good reply
	do   func(k *plugin.Kind, log *bytes.Buffer) (result string, err string) // the call's result and error text
}

// calls are the calls a Kind method runs (describe aside: Cache, TestManifestChecks).
var calls = []call{
	{"start", `{"version":1,"args":["--x","{file:c.json}"],"env":{"STUB_CONFIG":"{file:c.json}"},"files":[{"name":"c.json","bytes":"{}"}]}`,
		func(k *plugin.Kind, _ *bytes.Buffer) (string, string) {
			l, err := k.StartWhole(agents.Start{Args: []string{"--a"}})
			return strings.Join(l.Args, " ") + " " + l.Env["STUB_CONFIG"], errText(err)
		}},
	{"not-ready", `{"version":1,"why":"updating"}`, func(k *plugin.Kind, _ *bytes.Buffer) (string, string) {
		why := k.NotReady("screen")
		if strings.HasPrefix(why, "kind ") {
			return "", why
		}
		return why, ""
	}},
	{"install", `{"version":1,"mcp":{"where":"start"}}`,
		func(k *plugin.Kind, _ *bytes.Buffer) (string, string) {
			in, err := k.InstallErr("/kit", "", nil)
			if err != nil {
				return "", err.Error()
			}
			how, reg, static := in.MCP.Lane(nil, nil)
			return in.MCP.Where + " " + how + " " + boolText(reg, static), ""
		}},
	{"turn", `{"version":1,"ok":true,"turn":{"id":"t1","error":"limit","ended":true,"reset":"2026-11-08T18:00:00Z"}}`,
		func(k *plugin.Kind, log *bytes.Buffer) (string, string) {
			turn, ok := k.LastTurn([]byte("{}\n"))
			if !ok {
				return "", strings.TrimSpace(log.String())
			}
			return turn.ID + " " + turn.Error + " " + turn.Reset.UTC().Format(time.RFC3339), ""
		}},
	{"deliver", `{"version":1,"ok":true,"delivery":{"socket":"/s","lines":["a","b"]}}`,
		func(k *plugin.Kind, _ *bytes.Buffer) (string, string) {
			d, ok := k.Deliver("hi", []byte(`{"STUB_INBOX":"/s"}`))
			if !ok { // the failed call's error is the delivery's Refused
				return "", d.Refused
			}
			return d.Socket + " " + strings.Join(d.Lines, ","), ""
		}},
}

// TestCalls: per call, a good reply's result, and a malformed reply, another version, an error field and a non-zero
// exit as an error naming the kind and the call (the error, or for a method with none: NotReady's reason, the Log
// line). TestTimeouts has the timeout row.
func TestCalls(t *testing.T) {
	want := map[string]string{
		"not-ready": "updating",
		"install":   "start STUB_CONFIG (the start's config) registered",
		"turn":      "t1 limit 2026-11-08T18:00:00Z",
		"deliver":   "/s a,b",
	}
	for _, c := range calls {
		for _, r := range []struct{ name, body, err string }{
			{"good", reply(c.good), ""},
			{"malformed", "cat >/dev/null; echo 'not json'", "kind stub: " + c.name + ": malformed reply"},
			{"other version", `cat >/dev/null; echo '{"version":2}'`, "kind stub: " + c.name + ": reply version 2, obor speaks 1"},
			{"error field", `cat >/dev/null; echo '{"version":1,"error":"no auth"}'`, "kind stub: " + c.name + ": no auth"},
			{"non-zero exit", "cat >/dev/null; echo boom >&2; exit 3", "kind stub: " + c.name + ": exit 3: boom"},
		} {
			k, state := kind(t, r.body)
			got, err := c.do(k, logged(t))
			switch {
			case r.err != "" && !strings.Contains(err, r.err):
				t.Errorf("%s %s: error %q, want %q in it", c.name, r.name, err, r.err)
			case r.err != "":
			case err != "":
				t.Errorf("%s %s: error %q", c.name, r.name, err)
			case c.name == "start":
				conf := filepath.Join(state, "c.json")
				if got != "--x "+conf+" "+conf {
					t.Errorf("start good: args and env %q, want --x %s %s (one call: argv and env)", got, conf, conf)
				}
				if b, err := os.ReadFile(conf); err != nil || string(b) != "{}" {
					t.Errorf("start good: the reply's file: %q, %v", b, err)
				}
			case got != want[c.name]:
				t.Errorf("%s good: %q, want %q", c.name, got, want[c.name])
			}
		}
	}
}

// A deliver reply that is ok false hands its why to the caller as the delivery's Refused (the refusal the lead or serve
// marks the record with), not dropped: nothing was delivered, and the line goes through the hooks.
func TestDeliverHandsItsWhyToTheRefusal(t *testing.T) {
	const why = "STUB_INBOX is not localhost:<port> or 127.0.0.1:<port>"
	k, _ := kind(t, reply(`{"version":1,"ok":false,"why":"`+why+`"}`))
	d, ok := k.Deliver("hi", []byte(`{"STUB_INBOX":"evil.example:1"}`))
	if ok || d.Typed || d.Sent || d.Socket != "" || d.Refused != why {
		t.Errorf("Deliver = %+v, %v; want no delivery refused with %q", d, ok, why)
	}
}

// A start's warnings are notes on a start it made anyway, not a failure: each goes to Log as one line naming the kind
// and the call, and the start's argv is still the reply's.
func TestStartLogsItsWarnings(t *testing.T) {
	k, _ := kind(t, reply(`{"version":1,"args":["--x"],"warnings":["WARN no model list: the ref is unchecked"]}`))
	log := logged(t)
	l, err := k.StartWhole(agents.Start{Args: []string{"--a"}})
	if err != nil || !slices.Equal(l.Args, []string{"--x"}) {
		t.Errorf("StartWhole = %q, %v; want the reply's argv", l.Args, err)
	}
	if want := "obor: kind stub: start: WARN no model list: the ref is unchecked\n"; log.String() != want {
		t.Errorf("Log = %q, want %q", log.String(), want)
	}
}

// a kind cannot install to the user config, so install's reply may not name a user config entry or a plugin link
// under ~/; a kind that does is refused with an error naming the rule.
func TestInstallRefusesAUserConfigPath(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"a user config entry": {`{"version":1,"mcp":{"where":"config","path":"/u/.stub.json","key":["mcp","obor"],"entry":{"command":"obor"}}}`,
			"a kind registers per launch or through the project's files"},
		"a plugin link": {`{"version":1,"mcp":{"where":"start"},"plugin":{"from":"/kit/p.js","to":"/u/p.js"}}`,
			"a kind installs nothing under ~/"},
	} {
		k, _ := kind(t, reply(c.body))
		_, err := agents.InstallOf(k, "/kit", "", nil)
		if err == nil || !strings.Contains(err.Error(), "kind stub: install: ") ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q in it", name, err, c.want)
		}
	}
}

// install's request carries the project's root; a where "project" reply names a file of the project, its path then
// under that root (no root: as named, relative); a path that leaves the project, or a reply with no key or entry, is
// refused naming the field. A reply's hooks are such a file too (none: no hooks file), and only such a file.
func TestInstallProjectFile(t *testing.T) {
	const good = `{"version":1,"mcp":{"where":"project","path":".stub/mcp.json","key":["servers","obor"],"entry":{"command":"obor"}},` +
		`"hooks":{"where":"project","path":".stub/hooks.json","key":["obor"],"entry":{"Stop":[]}}}`
	k, _ := kind(t, "cat >\"$REQ\"; cat <<'EOF'\n"+good+"\nEOF\n")
	in, err := agents.InstallOf(k, "/kit", "/p", nil)
	var req struct {
		Root string `json:"root"`
	}
	b, _ := os.ReadFile(filepath.Join(stubDir, "req.json"))
	if json.Unmarshal(b, &req) != nil || req.Root != "/p" {
		t.Errorf("the install request %s: want root /p", b)
	}
	if m := in.MCP; err != nil || m.Where != agents.Project || m.Path != "/p/.stub/mcp.json" ||
		!slices.Equal(m.Key, []string{"servers", "obor"}) || m.Entry["command"] != "obor" {
		t.Errorf("Install = %+v, %v; want the project's file under its root", m, err)
	}
	if h := in.Hooks; h.Where != agents.Project || h.Path != "/p/.stub/hooks.json" || !slices.Equal(h.Key, []string{"obor"}) ||
		h.Entry["Stop"] == nil {
		t.Errorf("Install's hooks = %+v; want the project's hooks file under its root", h)
	}
	if in, err := agents.InstallOf(k, "/kit", "", nil); err != nil || in.MCP.Path != ".stub/mcp.json" {
		t.Errorf("no root: %+v, %v; want the path as named", in.MCP, err)
	}
	k2, _ := kind(t, reply(`{"version":1,"mcp":{"where":"start"}}`))
	if in, err := agents.InstallOf(k2, "/kit", "/p", nil); err != nil || in.Hooks.Where != "" {
		t.Errorf("a reply with no hooks: %+v, %v; want no hooks file", in.Hooks, err)
	}
	for name, c := range map[string]struct{ body, want string }{
		"an absolute path": {`{"version":1,"mcp":{"where":"project","path":"/u/.stub.json","key":["a"],"entry":{}}}`,
			`mcp.path "/u/.stub.json" is no file of the project`},
		"a path out of the project": {`{"version":1,"mcp":{"where":"project","path":"../x.json","key":["a"],"entry":{}}}`,
			`mcp.path "../x.json" is no file of the project`},
		"no entry": {`{"version":1,"mcp":{"where":"project","path":"x.json","key":["a"]}}`,
			`mcp.where "project" names no key or entry`},
		"an unknown where": {`{"version":1,"mcp":{"where":"elsewhere"}}`,
			`mcp.where "elsewhere" is none of manifest, start, project`},
		"hooks in a manifest": {`{"version":1,"mcp":{"where":"start"},"hooks":{"where":"manifest","path":"h.json"}}`,
			`hooks.where "manifest": a kind's hooks entry is a file of the project (project)`},
		"hooks out of the project": {`{"version":1,"mcp":{"where":"start"},"hooks":{"where":"project","path":"../h.json","key":["a"],"entry":{}}}`,
			`hooks.path "../h.json" is no file of the project`},
	} {
		k, _ := kind(t, reply(c.body))
		_, err := agents.InstallOf(k, "/kit", "/p", nil)
		if err == nil || !strings.Contains(err.Error(), "kind stub: install: "+c.want) {
			t.Errorf("%s: %v, want %q in it", name, err, c.want)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func boolText(registered, static bool) string {
	s := ""
	if registered {
		s += "registered"
	}
	if static {
		s += " static"
	}
	return s
}

// Context is turn {what: context}: the request carries the kind's tail, the reply its tokens, time and TTL; a turn
// that answers ok false reads none.
func TestContext(t *testing.T) {
	k, _ := kind(t, `cat >"$REQ"; `+reply(`{"version":1,"ok":true,"context":{"tokens":7,"at":"2026-11-08T18:00:00Z","ttl":"1h"}}`))
	c, ok := k.Context([]byte("a\nb\n"))
	if !ok || c.Tokens != 7 || c.TTL != "1h" || !c.At.Equal(time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("Context = %+v, %v; want 7 at 18:00 with 1h", c, ok)
	}
	b, err := os.ReadFile(filepath.Join(stubDir, "req.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Call string `json:"call"`
		What string `json:"what"`
		Tail string `json:"tail"`
	}
	if err := json.Unmarshal(b, &req); err != nil || req.Call != "turn" || req.What != "context" || req.Tail != "a\nb\n" {
		t.Errorf("request %s (%v): want turn {what: context, tail \"a\\nb\\n\"}", b, err)
	}
	k, _ = kind(t, reply(`{"version":1,"ok":false}`))
	if _, ok := k.Context([]byte("{}\n")); ok {
		t.Error("ok false: a context read")
	}
}

// The context read is bounded by its own timeout, shorter than turn's (it runs inside a lead's hook and under serve's
// lock): a reply slower than it is no context, while a turn read of the same speed is answered.
func TestContextTimeout(t *testing.T) {
	if plugin.Timeouts["context"] <= 0 || plugin.Timeouts["context"] >= plugin.Timeouts["turn"] {
		t.Fatalf("precondition: context timeout %s, turn %s", plugin.Timeouts["context"], plugin.Timeouts["turn"])
	}
	k, _ := kind(t, `sleep 0.4; `+reply(`{"version":1,"ok":true,"context":{"tokens":7,"at":"2026-11-08T18:00:00Z"},"turn":{"id":"t1"}}`))
	old := plugin.Timeouts
	plugin.Timeouts = map[string]time.Duration{"turn": 5 * time.Second, "context": 100 * time.Millisecond}
	defer func() { plugin.Timeouts = old }()
	log := logged(t)
	start := time.Now()
	if c, ok := k.Context([]byte("{}\n")); ok {
		t.Errorf("Context = %+v past its timeout, want none", c)
	}
	if took := time.Since(start); took > 300*time.Millisecond {
		t.Errorf("Context took %s (its timeout is 100ms)", took)
	}
	if want := "kind stub: turn: no reply within 100ms"; !strings.Contains(log.String(), want) {
		t.Errorf("log %q, want %q", log.String(), want)
	}
	if turn, ok := k.LastTurn([]byte("{}\n")); !ok || turn.ID != "t1" {
		t.Errorf("LastTurn = %+v, %v; want t1 within turn's timeout", turn, ok)
	}
}

// TestStartRequest: start's request carries the version, the call, every field of the complete start (check, trim_user
// and the project's override files as the start holds them, name → document; a lead's start: false and none) and the
// manifest's env snapshot (closure 1 as data: the dir the plugin reads itself), in one call whose argv and env both
// have the file's path substituted; an env var with a value the manifest's vars do not name is an error, while one
// named empty is that start's own clear.
func TestStartRequest(t *testing.T) {
	k, state := kind(t, `cat >"$REQ"; echo '{"version":1,"args":["-c","{file:c.json}"],"env":{"STUB_CONFIG":"{file:c.json}"},"files":[{"name":"c.json","bytes":"x"}]}'`)
	env := func(v string) string { return map[string]string{"HOME": "/home/u", "OTHER": "o"}[v] }
	over := map[string]string{"stub_override.json": `{"k": null}`, "stub_override.toml": "k = 1\n"}
	l, err := k.StartWhole(agents.Start{Kit: "/kit", Args: []string{"-m", "x"}, Prompt: "go", Effort: "high",
		StrictMCP: true, Dir: "/lane", Env: env, Settings: map[string]any{"a": false}, Session: "s-1",
		WriteDirs: []string{"/reviews"}, TrimUser: true, Check: true, Overrides: over, Root: "/main"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(state, "c.json")
	if l.Env["STUB_CONFIG"] != want || strings.Join(l.Args, " ") != "-c "+want {
		t.Errorf("StartWhole = %+v, want -c %s and STUB_CONFIG=%s", l, want, want)
	}
	b, err := os.ReadFile(filepath.Join(stubDir, "req.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Version   int               `json:"version"`
		Call      string            `json:"call"`
		Kit       string            `json:"kit"`
		Args      []string          `json:"args"`
		Prompt    string            `json:"prompt"`
		Effort    string            `json:"effort"`
		StrictMCP bool              `json:"strict_mcp"`
		TrimUser  bool              `json:"trim_user"`
		Check     bool              `json:"check"`
		Overrides map[string]string `json:"overrides"`
		Dir       string            `json:"dir"`
		Root      string            `json:"root"`
		Session   string            `json:"session"`
		WriteDirs []string          `json:"write_dirs"`
		Env       map[string]string `json:"env"`
		Settings  map[string]any    `json:"settings"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("request %s: %v", b, err)
	}
	if req.Version != 1 || req.Call != "start" || req.Kit != "/kit" || strings.Join(req.Args, " ") != "-m x" ||
		req.Prompt != "go" || req.Effort != "high" || !req.StrictMCP || !req.TrimUser || !req.Check || req.Dir != "/lane" || req.Root != "/main" || req.Settings["a"] != false ||
		req.Session != "s-1" || strings.Join(req.WriteDirs, " ") != "/reviews" {
		t.Errorf("request %s", b)
	}
	if !reflect.DeepEqual(req.Overrides, over) {
		t.Errorf("request overrides %q, want the start's %q, each document as written (request %s)", req.Overrides, over, b)
	}
	if len(req.Env) != 1 || req.Env["HOME"] != "/home/u" {
		t.Errorf("request env %v, want HOME alone (the manifest's env)", req.Env)
	}
	// a lead's start: trim_user false, no override file
	if _, err := k.StartWhole(agents.Start{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	if b, err = os.ReadFile(filepath.Join(stubDir, "req.json")); err != nil {
		t.Fatal(err)
	}
	var lead struct {
		Prompt    string            `json:"prompt"`
		TrimUser  bool              `json:"trim_user"`
		Overrides map[string]string `json:"overrides"`
	}
	if err := json.Unmarshal(b, &lead); err != nil || lead.Prompt != "go" || lead.TrimUser || len(lead.Overrides) != 0 {
		t.Errorf("a lead's start: request %s (%v), want trim_user false and no overrides", b, err)
	}

	k, _ = kind(t, `cat >/dev/null; echo '{"version":1,"env":{"PATH":"/x"}}'`)
	if _, err := k.StartWhole(agents.Start{}); err == nil || !strings.Contains(err.Error(), "kind stub: start: env PATH is not among the manifest's vars") {
		t.Errorf("an env var outside vars with a value: %v", err)
	}
	// empty, outside vars: a var this start clears from the pane's shell (OpenCode's OPENCODE_CONFIG_CONTENT on a lane's)
	k, _ = kind(t, `cat >/dev/null; echo '{"version":1,"env":{"PATH":""}}'`)
	if l, err := k.StartWhole(agents.Start{}); err != nil || l.Env["PATH"] != "" || len(l.Env) != 1 {
		t.Errorf("an env var outside vars, empty: %+v, %v; want PATH empty (the start clears it)", l, err)
	}
	k, _ = kind(t, `cat >/dev/null; echo '{"version":1,"files":[{"name":"../x","bytes":""}]}'`)
	if _, err := k.StartWhole(agents.Start{}); err == nil || !strings.Contains(err.Error(), "is no name in the state dir") {
		t.Errorf("a file outside the state dir: %v", err)
	}
}

// TestManifestFromCache: New reads the manifest setup cached and runs nothing (the command now fails every call, and
// the manifest's answers stand); a changed command (its mtime) is described again by the next call that runs it.
func TestManifestFromCache(t *testing.T) {
	_, state := kind(t, reply(`{"version":1,"args":["one"]}`))
	b, err := os.ReadFile(plugin.CacheFile(state, "stub"))
	if err != nil {
		t.Fatalf("precondition: Cache wrote no manifest: %v", err)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	fail := "echo ran >>" + ran + "; exit 1"
	stub(t, fail, fail)
	k := plugin.New("stub", stubCmd, state)
	caps := k.Caps()
	if caps.Status != agents.StatusTranscript || !caps.Resume || !caps.Sessions || !caps.Transcript || caps.Hooks != agents.HooksNone ||
		caps.App != "stub-code" || !caps.HookAnswers || caps.Inbox {
		t.Errorf("Caps = %+v", caps)
	}
	if !agents.TurnsRead(k) {
		t.Error("status transcript: TurnsRead false")
	}
	if got := k.Transcript(func(v string) string { return "/home/u" }, "s1"); got != "/home/u/.stub/s1.jsonl" {
		t.Errorf("Transcript = %q", got)
	}
	if got := k.Transcript(func(string) string { return "" }, "s1"); got != "" {
		t.Errorf("Transcript with HOME empty = %q, want none", got)
	}
	if got := string(k.InboxRecord(func(v string) string { return map[string]string{"STUB_INBOX": "/s"}[v] })); got != `{"STUB_INBOX":"/s"}` {
		t.Errorf("InboxRecord = %q", got)
	}
	st := k.Stop()
	if st.Quit != "/quit" || st.Dialog == nil || st.Dialog.Key.FindStringSubmatch("2. Quit anyway")[1] != "2" {
		t.Errorf("Stop = %+v", st)
	}
	if h := k.Headless(); !h.PrintFlag || h.Subcommands[0] != "exec" || !slices.Equal(h.Flags, []string{"--prompt"}) ||
		agents.Programs(k)[0] != "stub" {
		t.Errorf("Headless = %+v", h)
	}
	kinds := agents.WithPlugins([]agents.Kind{k})
	if err := kinds.CheckEffort("stub", "max"); err == nil || kinds.CheckEffort("stub", "high") != nil {
		t.Errorf("effort max: %v, want refused (efforts low, high), high taken", err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the manifest's answers ran the command")
	}

	// a new command (another mtime): the next start describes it again, and fails as it does
	var c struct{ Mtime time.Time }
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stubCmd, time.Now(), c.Mtime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := k.StartWhole(agents.Start{}); err == nil || !strings.Contains(err.Error(), "kind stub: describe: exit 1") {
		t.Errorf("start after the command changed: %v, want describe's failure", err)
	}
}

// TestNoCache: a kind with no cached manifest has no answers, and every call that runs it says to run setup agents.
func TestNoCache(t *testing.T) {
	stub(t, reply(manifest), reply(`{"version":1}`))
	k := plugin.New("stub", stubCmd, t.TempDir())
	if c := k.Caps(); c != (agents.Caps{}) {
		t.Errorf("Caps = %+v, want none", c)
	}
	_, err := k.StartWhole(agents.Start{})
	if err == nil || !strings.Contains(err.Error(), "kind stub: no manifest cached") || !strings.Contains(err.Error(), "obor setup agents") {
		t.Errorf("start without a cache: %v", err)
	}
	if _, err := agents.InstallOf(k, "/kit", "", nil); err == nil {
		t.Error("install without a cache: no error")
	}
	if err := k.Uncached(); err == nil || !strings.Contains(err.Error(), "no manifest cached") {
		t.Errorf("Uncached = %v, want the missing cache (the guard denies its program, T-1054)", err)
	}
}

// TestEnsure (obor serve's cache step): no cache → describe and cache it; a cache of the command as it is → no
// process runs; the command changed (another mtime) → described again; a describe that fails is the error, and wrote
// false. A kind New loads from what Ensure wrote has its manifest (Uncached nil).
func TestEnsure(t *testing.T) {
	count := filepath.Join(t.TempDir(), "describes")
	runs := func() int { b, _ := os.ReadFile(count); return strings.Count(string(b), "x") }
	stub(t, "echo x >>"+count+"; "+reply(manifest), reply(`{"version":1}`))
	state := t.TempDir()
	if _, err := os.Stat(plugin.CacheFile(state, "stub")); !os.IsNotExist(err) {
		t.Fatalf("precondition: no cache (%v)", err)
	}
	if wrote, err := plugin.Ensure("stub", stubCmd, state); !wrote || err != nil || runs() != 1 {
		t.Fatalf("no cache: wrote %v, %v, %d describes; want written by one describe", wrote, err, runs())
	}
	if err := plugin.New("stub", stubCmd, state).Uncached(); err != nil {
		t.Errorf("the kind of Ensure's cache: Uncached = %v, want nil", err)
	}
	if wrote, err := plugin.Ensure("stub", stubCmd, state); wrote || err != nil || runs() != 1 {
		t.Errorf("current cache: wrote %v, %v, %d describes; want nothing run", wrote, err, runs())
	}
	if err := os.Chtimes(stubCmd, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if wrote, err := plugin.Ensure("stub", stubCmd, state); !wrote || err != nil || runs() != 2 {
		t.Errorf("command changed: wrote %v, %v, %d describes; want described again", wrote, err, runs())
	}
	stub(t, "exit 1", reply(`{"version":1}`))
	if err := os.Chtimes(stubCmd, time.Now(), time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if wrote, err := plugin.Ensure("stub", stubCmd, state); wrote || err == nil || !strings.Contains(err.Error(), "kind stub: describe: exit 1") {
		t.Errorf("describe fails: wrote %v, %v; want its error", wrote, err)
	}
}

// A kind answered in process: the loader picks Local by the config's command, not the kind's name. The registered
// command is answered by its handler (manifest from its describe, nothing cached; Ensure, Cache and Check run no
// process and write nothing); another command for the same name runs (no handler call: a loaded kind with no cache
// is uncached). A handler that panics fails its call with an error naming the kind and the call; the process lives.
func TestLocalByCommand(t *testing.T) {
	const local = "obor-kind-stub-linked"
	var got []string
	plugin.Local[local] = func(call string, req []byte) ([]byte, error) {
		got = append(got, call)
		if call == "start" {
			panic("boom")
		}
		return []byte(manifest), nil
	}
	t.Cleanup(func() { delete(plugin.Local, local) })
	state := t.TempDir()
	if !plugin.InProcess(local) || plugin.InProcess(stubCmd) {
		t.Fatalf("precondition: InProcess(%s) %v, InProcess(%s) %v", local, plugin.InProcess(local), stubCmd,
			plugin.InProcess(stubCmd))
	}

	k := agents.Plugins.Kind("stub", local, state)
	if c := k.Caps(); !c.Resume || c.Status != agents.StatusTranscript || !slices.Equal(got, []string{"describe"}) {
		t.Errorf("in process: caps %+v, calls %q; want the handler's manifest from one describe", c, got)
	}
	if wrote, err := plugin.Ensure("stub", local, state); wrote || err != nil {
		t.Errorf("Ensure in process: wrote %v, %v; want nothing", wrote, err)
	}
	if _, err := plugin.Cache("stub", local, state); err != nil {
		t.Errorf("Cache in process: %v", err)
	}
	if _, err := agents.Plugins.Check("stub", local); err != nil {
		t.Errorf("Check in process: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "kinds")); !os.IsNotExist(err) {
		t.Errorf("in process: something cached (%v); want nothing", err)
	}

	_, err := k.(agents.Whole).StartWhole(agents.Start{})
	if err == nil || !strings.HasPrefix(err.Error(), "kind stub: start: panic: boom") {
		t.Errorf("a handler that panics: %v; want the call's error naming the kind", err)
	}

	got = nil
	stub(t, reply(manifest), reply(`{"version":1}`))
	other := agents.Plugins.Kind("stub", stubCmd, state)
	if err := other.(agents.Uncached).Uncached(); err == nil || len(got) != 0 {
		t.Errorf("another command: Uncached %v, handler calls %q; want the command's kind (no cache), no handler call",
			err, got)
	}
}

// TestManifestChecks: describe's reply is refused when it names another kind, version or status, a bad pattern, or
// an override file that is no file name.
func TestManifestChecks(t *testing.T) {
	for _, c := range []struct{ reply, want string }{
		{`{"version":1,"name":"other","status":"terminal"}`, `manifest names kind "other", the config "stub"`},
		{`{"version":2,"name":"stub","status":"terminal"}`, "reply version 2"},
		{`{"version":1,"name":"stub","status":"busy"}`, `manifest status "busy"`},
		{`{"version":1,"name":"stub","status":"hooks","caps":{"hooks":"sometimes"}}`, `caps.hooks "sometimes"`},
		{`{"version":1,"name":"stub","status":"hooks","not_ready":{"pattern":"("}}`, "not_ready.pattern"},
		{`{"version":1,"name":"stub","status":"hooks","not_ready":{}}`, "one of them"},
		{`{"version":1,"name":"stub","status":"hooks","override_files":["../x.json"]}`, `override_files: "../x.json" is no file name`},
	} {
		stub(t, reply(c.reply), "exit 1")
		_, err := plugin.Cache("stub", stubCmd, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "kind stub: describe: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.reply, err, c.want)
		}
		if _, err := agents.Plugins.Check("stub", stubCmd); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Check %s: %v, want %q", c.reply, err, c.want)
		}
	}
	stub(t, reply(manifest), "exit 1")
	if k, err := agents.Plugins.Check("stub", stubCmd); err != nil || k == nil {
		t.Errorf("Check of a good manifest: %v", err)
	} else if !slices.Equal(k.Efforts(), []string{"low", "high"}) {
		t.Errorf("Check's kind: efforts %q, want the manifest's (low, high)", k.Efforts())
	}
	if _, err := agents.Plugins.Check("stub", "/no/such/obor-kind"); err == nil {
		t.Error("Check of a missing command: no error")
	}
}

// TestManifestAnswers: a not_ready pattern and typed delivery are answered from the manifest, no call.
func TestManifestAnswers(t *testing.T) {
	m := `{"version":1,"name":"stub","status":"terminal","typed":true,"not_ready":{"pattern":"Updating","why":"self-update"}}`
	stub(t, reply(m), "exit 1")
	k, err := plugin.Cache("stub", stubCmd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := k.NotReady("Updating to 2.0"); got != "self-update" {
		t.Errorf("NotReady = %q", got)
	}
	if got := k.NotReady("> "); got != "" {
		t.Errorf("NotReady on a ready screen = %q", got)
	}
	if d, ok := k.Deliver("hi", nil); !ok || !d.Typed {
		t.Errorf("Deliver = %+v %v, want typed", d, ok)
	}
}

// TestTimeouts: per call, a plugin that does not answer is an error naming the kind and the call once the call's
// timeout passes, not a hang (its process group killed).
func TestTimeouts(t *testing.T) {
	for _, c := range calls {
		k, _ := kind(t, "sleep 5")
		restore := short(100 * time.Millisecond)
		start := time.Now()
		_, err := c.do(k, logged(t))
		restore()
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("%s: took %s (the timeout is 100ms)", c.name, took)
		}
		if want := "kind stub: " + c.name + ": no reply within 100ms"; !strings.Contains(err, want) {
			t.Errorf("%s: error %q, want %q in it", c.name, err, want)
		}
	}
}
