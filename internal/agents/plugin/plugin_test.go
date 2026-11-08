package plugin_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
)

// manifest is the stub kind's describe reply.
const manifest = `{"version":1,"name":"stub","status":"transcript","caps":{"hooks":"none","resume":true,"sessions":true},
"efforts":["low","high"],"vars":["STUB_CONFIG"],"env":["HOME"],"stop":{"quit":"/quit","clear_input":["ctrl+u"],
"dialog":{"option":"Quit anyway","key":"(\\d)\\. Quit anyway"}},"headless":{"subcommands":["exec"],"print_flag":true},
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
	{"install", `{"version":1,"mcp":{"where":"config","path":"/u/.stub.json","key":["mcp","obor"],"entry":{"command":"obor"}},"plugin":{"from":"/kit/p.js","to":"/u/p.js"}}`,
		func(k *plugin.Kind, _ *bytes.Buffer) (string, string) {
			in, err := k.InstallErr("/kit", nil)
			if err != nil {
				return "", err.Error()
			}
			how, reg, static := in.MCP.Lane(nil, nil)
			return in.MCP.Where + " " + in.MCP.Path + " " + in.Plugin.To + " " + how + " " + boolText(reg, static), ""
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
		func(k *plugin.Kind, log *bytes.Buffer) (string, string) {
			d, ok := k.Deliver("hi", []byte(`{"STUB_INBOX":"/s"}`))
			if !ok {
				return "", strings.TrimSpace(log.String())
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
		"install":   "config /u/.stub.json /u/p.js STUB_CONFIG (the start's config) registered",
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

// TestStartRequest: start's request carries the version, the call, every field of the complete start and the
// manifest's env snapshot (closure 1 as data: the dir the plugin reads itself), in one call whose argv and env both
// have the file's path substituted (T-1042); an env var the manifest's vars do not name is an error.
func TestStartRequest(t *testing.T) {
	k, state := kind(t, `cat >"$REQ"; echo '{"version":1,"args":["-c","{file:c.json}"],"env":{"STUB_CONFIG":"{file:c.json}"},"files":[{"name":"c.json","bytes":"x"}]}'`)
	env := func(v string) string { return map[string]string{"HOME": "/home/u", "OTHER": "o"}[v] }
	l, err := k.StartWhole(agents.Start{Kit: "/kit", Args: []string{"-m", "x"}, Prompt: "go", Effort: "high",
		StrictMCP: true, Dir: "/lane", Env: env, Settings: map[string]any{"a": false}, Session: "s-1",
		WriteDirs: []string{"/reviews"}})
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
		Dir       string            `json:"dir"`
		Session   string            `json:"session"`
		WriteDirs []string          `json:"write_dirs"`
		Env       map[string]string `json:"env"`
		Settings  map[string]any    `json:"settings"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("request %s: %v", b, err)
	}
	if req.Version != 1 || req.Call != "start" || req.Kit != "/kit" || strings.Join(req.Args, " ") != "-m x" ||
		req.Prompt != "go" || req.Effort != "high" || !req.StrictMCP || req.Dir != "/lane" || req.Settings["a"] != false ||
		req.Session != "s-1" || strings.Join(req.WriteDirs, " ") != "/reviews" {
		t.Errorf("request %s", b)
	}
	if len(req.Env) != 1 || req.Env["HOME"] != "/home/u" {
		t.Errorf("request env %v, want HOME alone (the manifest's env)", req.Env)
	}

	k, _ = kind(t, `cat >/dev/null; echo '{"version":1,"env":{"PATH":"/x"}}'`)
	if _, err := k.StartWhole(agents.Start{}); err == nil || !strings.Contains(err.Error(), "kind stub: start: env PATH is not among the manifest's vars") {
		t.Errorf("an env var outside vars: %v", err)
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
	if caps.Status != agents.StatusTranscript || !caps.Resume || !caps.Sessions || !caps.Transcript || caps.Hooks != agents.HooksNone {
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
	if h := k.Headless(); !h.PrintFlag || h.Subcommands[0] != "exec" || agents.Programs(k)[0] != "stub" {
		t.Errorf("Headless = %+v", h)
	}
	agents.SetPlugins([]agents.Kind{k})
	t.Cleanup(func() { agents.SetPlugins(nil) })
	if err := agents.CheckEffort("stub", "max"); err == nil || agents.CheckEffort("stub", "high") != nil {
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
	if _, err := agents.InstallOf(k, "/kit", nil); err == nil {
		t.Error("install without a cache: no error")
	}
}

// TestManifestChecks: describe's reply is refused when it names another kind, version or status, or a bad pattern.
func TestManifestChecks(t *testing.T) {
	for _, c := range []struct{ reply, want string }{
		{`{"version":1,"name":"other","status":"terminal"}`, `manifest names kind "other", the config "stub"`},
		{`{"version":2,"name":"stub","status":"terminal"}`, "reply version 2"},
		{`{"version":1,"name":"stub","status":"busy"}`, `manifest status "busy"`},
		{`{"version":1,"name":"stub","status":"hooks","caps":{"hooks":"sometimes"}}`, `caps.hooks "sometimes"`},
		{`{"version":1,"name":"stub","status":"hooks","not_ready":{"pattern":"("}}`, "not_ready.pattern"},
		{`{"version":1,"name":"stub","status":"hooks","not_ready":{}}`, "one of them"},
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
