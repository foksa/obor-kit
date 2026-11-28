package kind_test

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/kinds/pi/kind"
)

// pi is the kind as obor's plugin package runs it (agents.Kind from the protocol's replies), answered in process by
// Handle: the same requests and replies as the binary's, without the exec. stateDir: where a start's files would go.
func pi(t *testing.T, stateDir string) agents.Kind {
	t.Helper()
	const cmd = "obor-kind-pi"
	plugin.Local[cmd] = kind.Handle
	t.Cleanup(func() { delete(plugin.Local, cmd) })
	k := agents.Plugins.Kind(kind.Name, cmd, stateDir)
	if k.Name() != kind.Name || k.Caps().Status != agents.StatusTerminal {
		t.Fatalf("precondition: the kind describes itself: %s %+v", k.Name(), k.Caps())
	}
	return k
}

// The manifest: obor's hooks through pi's extension, acting on their answers, a resume, the model as a required
// provider/id, the session's inbox (the extension's socket, recorded from InboxVar), the terminal's status, /quit with
// one Enter and Ctrl+U, -p/--print and --mode and the config-writing subcommands denied, the default session dir's
// JSONL under HOME, and obor's MCP server registered per start by the extension.
func TestManifest(t *testing.T) {
	k := pi(t, t.TempDir())
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksPlugin, Inbox: true, HookAnswers: true, Resume: true,
		Transcript: true, ModelArg: true, Status: agents.StatusTerminal}) {
		t.Errorf("Caps = %+v", c)
	}
	if got := k.Efforts(); !slices.Equal(got, []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("Efforts = %q", got)
	}
	if s := k.Stop(); s.Quit != "/quit" || s.SecondEnter || !slices.Equal(s.ClearInput, []string{"ctrl+u"}) ||
		s.AfterEscape != nil || s.Dialog != nil {
		t.Errorf("Stop = %+v", s)
	}
	h := k.Headless()
	if !h.PrintFlag || !slices.Equal(h.Subcommands, []string{"install", "remove", "uninstall", "update", "config", "auth", "mcp"}) ||
		!slices.Contains(h.ValueOpts, "--mode") || !slices.Equal(h.Flags, []string{"--mode"}) || len(h.Anywhere) != 0 {
		t.Errorf("Headless = %+v", h)
	}
	env := func(m map[string]string) func(string) string { return func(v string) string { return m[v] } }
	if got := k.Transcript(env(map[string]string{"HOME": "/h"}), "01a2"); got != filepath.Join("/h", ".pi", "agent", "sessions", "*", "*_01a2.jsonl") {
		t.Errorf("Transcript = %q, want pi's session JSONL", got)
	}
	if got := k.Transcript(env(map[string]string{}), "s1"); got != "" {
		t.Errorf("Transcript with no HOME = %q, want none", got)
	}
	if !slices.Equal(k.Vars(), []string{kind.ResumeVar}) {
		t.Errorf("Vars = %q", k.Vars())
	}
	in, err := agents.InstallOf(k, "/kit", "", env(map[string]string{"HOME": "/h"}))
	if m := in.MCP; err != nil || m.Where != agents.PerStart || m.Path != "" || m.Entry != nil {
		t.Errorf("Install = %+v, %v: want per start, nothing user-global", in, err)
	}
	if how, reg, static := in.MCP.Lane([]string{"--model", "p/m"}, nil); !reg || static ||
		!strings.Contains(how, "/kit/pi/obor.js") || !strings.Contains(how, "/kit/bin/obor mcp") {
		t.Errorf("Lane = %q, %v, %v: want registered per start by the extension", how, reg, static)
	}
	if r := k.InboxRecord(env(map[string]string{"OBOR_PI_INBOX": "/h/.obor/pi/s1.sock"})); string(r) != `{"OBOR_PI_INBOX":"/h/.obor/pi/s1.sock"}` {
		t.Errorf("InboxRecord = %s, want the extension's socket", r)
	}
}

// start: --approve, -e {kit}/pi/obor.js, then --model <provider/id> and --thinking <effort>, --session <id> on a
// resume, and `--` <prompt>; the type's args' --model/-m and --thinking are replaced by those. The resumed id reaches
// the extension as OBOR_PI_RESUME (empty, unset, on a fresh start). A model without its provider, an effort without a
// model and an effort pi does not take are refused.
func TestStart(t *testing.T) {
	k := pi(t, t.TempDir())
	write := func(name string, _ []byte) (string, error) { return "/state/" + name, nil }
	for _, c := range []struct {
		name string
		in   agents.Start
		want []string
	}{
		{"fresh, prompt: --approve, -e, model, then -- and the prompt",
			agents.Start{Kit: "/kit", Args: []string{"--model", "anthropic/claude-sonnet-5-5"}, Prompt: "go"},
			[]string{"--approve", "-e", "/kit/pi/obor.js", "--model", "anthropic/claude-sonnet-5-5", "--", "go"}},
		{"a resume: --session before --, its prompt after",
			agents.Start{Kit: "/kit", Args: []string{"--model", "anthropic/claude-sonnet-5-5"}, Resume: "01a2", Prompt: "go"},
			[]string{"--approve", "-e", "/kit/pi/obor.js", "--model", "anthropic/claude-sonnet-5-5", "--session", "01a2",
				"--", "go"}},
		{"the effort: --thinking", agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}, Effort: "low",
			Prompt: "go"}, []string{"--approve", "-e", "/kit/pi/obor.js", "--model", "p/m", "--thinking", "low", "--", "go"}},
		{"the args' --thinking is the effort when the start names none",
			agents.Start{Kit: "/kit", Args: []string{"--model=p/m", "--thinking=high"}},
			[]string{"--approve", "-e", "/kit/pi/obor.js", "--model", "p/m", "--thinking", "high"}},
		{"the start's effort replaces the args'", agents.Start{Kit: "/kit", Args: []string{"-m", "p/m", "--thinking", "high"},
			Effort: "max"}, []string{"--approve", "-e", "/kit/pi/obor.js", "--model", "p/m", "--thinking", "max"}},
		{"a fixed flag the args repeat is not doubled",
			agents.Start{Kit: "/kit", Args: []string{"--approve", "--model", "p/m"}, Prompt: "go"},
			[]string{"--approve", "-e", "/kit/pi/obor.js", "--model", "p/m", "--", "go"}},
		{"no prompt: no --", agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}},
			[]string{"--approve", "-e", "/kit/pi/obor.js", "--model", "p/m"}},
		{"no kit: no extension to load", agents.Start{Args: []string{"--model", "p/m"}, Prompt: "go"},
			[]string{"--approve", "--model", "p/m", "--", "go"}},
	} {
		c.in.WriteState = write
		l, err := agents.StartOf(k, c.in)
		if err != nil || !slices.Equal(l.Args, c.want) {
			t.Errorf("%s: start = %q, %v; want %q", c.name, l.Args, err, c.want)
		}
	}
	for _, c := range []struct {
		name string
		in   agents.Start
		why  string
	}{
		{"a model without its provider", agents.Start{Kit: "/kit", Args: []string{"--model", "claude-sonnet-5-5"}},
			"names no provider"},
		{"an effort without a model", agents.Start{Kit: "/kit", Effort: "high"}, "needs a model id"},
		{"an effort pi does not take", agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}, Effort: "turbo"},
			"is not one of pi's"},
	} {
		if _, err := agents.StartOf(k, c.in); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v; want %q", c.name, err, c.why)
		}
	}
	start := func(resume string) agents.Launch {
		t.Helper()
		l, err := agents.StartOf(k, agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}, Resume: resume})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if v := start("01a2").Env[kind.ResumeVar]; v != "01a2" {
		t.Errorf("a resume: %s = %q, want 01a2", kind.ResumeVar, v)
	}
	if v, set := start("").Env[kind.ResumeVar]; !set || v != "" {
		t.Errorf("a fresh start: %s = %q (in the env: %v), want it there and empty (unset)", kind.ResumeVar, v, set)
	}
}

// The args check reads the manifest's efforts and the start's own model rule, as obor config validate and dispatch
// refuse a start before it runs.
func TestStartEffortCheck(t *testing.T) {
	k := pi(t, t.TempDir())
	if err := agents.CheckEffortOf(k, []string{"--model", "p/m"}, "xhigh"); err != nil {
		t.Errorf("the args check with a model: %v", err)
	}
	if err := agents.CheckEffortOf(k, []string{}, "high"); err == nil || !strings.Contains(err.Error(), "needs a model id") {
		t.Errorf("the args check without a model: %v; want the start's refusal", err)
	}
	if err := agents.CheckEffortOf(k, []string{"--model", "p/m"}, "turbo"); err == nil ||
		!strings.Contains(err.Error(), `effort "turbo" is not one of pi's`) {
		t.Errorf("an effort not among pi's: %v; want a refusal", err)
	}
}

// The session header the transcript begins with names the session, its cwd and when it started.
func TestSession(t *testing.T) {
	k := pi(t, t.TempDir())
	at := time.Date(2026, 11, 28, 11, 0, 0, 0, time.UTC)
	first := `{"type":"session","version":3,"id":"01a2","timestamp":"2026-11-28T11:00:00.000Z","cwd":"/repo"}`
	if s, ok := k.Session([]byte(first)); !ok || s.ID != "01a2" || s.Cwd != "/repo" || !s.Started.Equal(at) {
		t.Errorf("Session = %+v, %v; want 01a2 in /repo at %v", s, ok, at)
	}
	for _, bad := range []string{"", `{}`, `{"type":"message","id":"x"}`, "not json"} {
		if s, ok := k.Session([]byte(bad)); ok {
			t.Errorf("Session(%q) = %+v, true; want none", bad, s)
		}
	}
}

// sessionTail is a session's lines: the header, a user message, two assistant messages (the second ends with a tool
// use) and a tool result; the last assistant message's usage is 10 + 200 + 30 tokens.
const sessionTail = `{"type":"session","version":3,"id":"01a2","timestamp":"2026-11-28T11:00:00.000Z","cwd":"/repo"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-11-28T11:00:01.000Z","message":{"role":"user","content":"hi","timestamp":1795940401000}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-11-28T11:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"}],"provider":"anthropic","model":"claude-sonnet-5-5","usage":{"input":4,"cacheRead":100,"cacheWrite":20,"output":8,"totalTokens":132},"stopReason":"stop","timestamp":1795940402000}}
{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-11-28T11:00:03.000Z","message":{"role":"assistant","content":[],"provider":"anthropic","model":"claude-sonnet-5-5","usage":{"input":10,"cacheRead":200,"cacheWrite":30,"output":5,"totalTokens":245},"stopReason":"toolUse","timestamp":1795940403000}}
{"type":"message","id":"t1","parentId":"a2","timestamp":"2026-11-28T11:00:04.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"ok"}],"isError":false,"timestamp":1795940404000}}
`

// The last turn is the tail's last assistant message: its id, ended, no error for a normal stop, and its reset only
// for a limit's error text (Codex's "Try again in ~N min.", Anthropic's 429); a line still being written is not read.
func TestLastTurn(t *testing.T) {
	k := pi(t, t.TempDir())
	if got, ok := k.LastTurn([]byte(sessionTail)); !ok || got.ID != "a2" || !got.Ended || got.Error != "" || !got.Reset.IsZero() {
		t.Errorf("LastTurn = %+v, %v; want a2 ended, no error", got, ok)
	}
	at := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	codex := `{"type":"message","id":"a3","timestamp":"2026-11-28T12:00:00Z","message":{"role":"assistant","usage":{"input":1},` +
		`"stopReason":"error","errorMessage":"You have hit your ChatGPT usage limit (plus plan). Try again in ~120 min."}}`
	got, ok := k.LastTurn([]byte(codex + "\n"))
	if !ok || got.ID != "a3" || !got.Ended || got.Error != "error" ||
		!strings.Contains(got.Details, "Try again in ~120 min") || !got.Reset.Equal(at.Add(120*time.Minute)) {
		t.Errorf("Codex limit = %+v, %v; want a3's error with its reset at %v", got, ok, at.Add(120*time.Minute))
	}
	anthropic := `{"type":"message","id":"a4","timestamp":"2026-11-28T12:00:00Z","message":{"role":"assistant",` +
		`"stopReason":"error","errorMessage":"429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"This request would exceed your account's rate limit.\"}}"}}`
	if got, ok := k.LastTurn([]byte(anthropic + "\n")); !ok || got.Error != "error" || !got.Reset.IsZero() ||
		!strings.Contains(got.Details, "429") {
		t.Errorf("Anthropic 429 = %+v, %v; want the error without a named reset", got, ok)
	}
	partial := codex[:len(codex)-20]
	if got, ok := k.LastTurn([]byte(sessionTail + "\n" + partial)); !ok || got.ID != "a2" {
		t.Errorf("a partial last line changed the turn: %+v, %v; want a2", got, ok)
	}
	if got, ok := k.LastTurn([]byte("no newline")); ok {
		t.Errorf("LastTurn = %+v, true; want none", got)
	}
}

// The context size is the last assistant message's input + cacheRead + cacheWrite, at its line's time; no assistant
// message with usage, or a line whose time does not parse, reads none.
func TestContext(t *testing.T) {
	k := pi(t, t.TempDir())
	at := time.Date(2026, 11, 28, 11, 0, 3, 0, time.UTC)
	c, ok := k.Context([]byte(sessionTail))
	if !ok || c.Tokens != 240 || !c.At.Equal(at) || c.TTL != "" {
		t.Errorf("Context = %+v, %v; want 240 at %v", c, ok, at)
	}
	for _, tail := range []string{"", `{"type":"message","message":{"role":"user","usage":{"input":7}}}` + "\n",
		`{"type":"message","timestamp":"soon","message":{"role":"assistant","usage":{"input":7}}}` + "\n"} {
		if c, ok := k.Context([]byte(tail)); ok {
			t.Errorf("Context(%q) = %+v, true; want none", tail, c)
		}
	}
}

// A turn's error is limited when its text names a usage or rate limit: its reset from "Try again in ~N min.", a clock
// time, or none; another error is no limit.
func TestReset(t *testing.T) {
	k := pi(t, t.TempDir())
	at := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, err, details string
		want               time.Time
		limited            bool
	}{
		{"Codex's minute count", "error", "You have hit your ChatGPT usage limit. Try again in ~45 min.", at.Add(45 * time.Minute), true},
		{"Anthropic's 429 without a time", "error", "429 rate_limit_error: please slow down", time.Time{}, true},
		{"a clock time", "error", "rate limit: try again at 6:30 PM", time.Date(2026, 11, 28, 18, 30, 0, 0, time.UTC), true},
		{"another error", "error", "connection refused", time.Time{}, false},
	} {
		got, limited := k.Reset(c.err, c.details, at)
		if !got.Equal(c.want) || limited != c.limited {
			t.Errorf("%s: Reset = %v, %v; want %v, %v", c.name, got, limited, c.want, c.limited)
		}
	}
}

// The session's inbox: the record is the extension's socket (InboxVar, set in its session-start hook's env; Claude's
// variables are none of it), and deliver posts one line there, {"type":"user","text"}, the text whole (a batched post's
// rows in one line); a record naming no socket delivers nothing.
func TestDeliver(t *testing.T) {
	k := pi(t, t.TempDir())
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if r := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s"})); r != nil {
		t.Errorf("InboxRecord of Claude's variables = %s, want none", r)
	}
	rec := k.InboxRecord(env(map[string]string{kind.InboxVar: "/h/.obor/pi/s1.sock"}))
	if string(rec) != `{"OBOR_PI_INBOX":"/h/.obor/pi/s1.sock"}` {
		t.Fatalf("InboxRecord = %s, want the extension's socket", rec)
	}
	d, ok := k.Deliver("[obor other] hi\n[obor] needs-you T-1", rec)
	if !ok || d.Typed || d.Socket != "/h/.obor/pi/s1.sock" ||
		!slices.Equal(d.Lines, []string{`{"text":"[obor other] hi\n[obor] needs-you T-1","type":"user"}`}) {
		t.Errorf("Deliver = %+v, %v; want one user line to the record's socket", d, ok)
	}
	for _, bad := range []string{"", "{}", `{"socket":"/s"}`, "not json"} {
		if d, ok := k.Deliver("hi", []byte(bad)); ok {
			t.Errorf("Deliver with record %q = %+v, true; want none", bad, d)
		}
	}
}

// The binary's own edges: a request with another version, a call it does not answer and a request that is no JSON
// exit 2 with the reason on stderr; a call with no argument prints its usage.
func TestMainRefuses(t *testing.T) {
	for _, c := range []struct {
		name, req, why string
		args           []string
	}{
		{"another version", `{"version":2}`, "request version 2", []string{"describe"}},
		{"an unknown call", `{"version":1}`, `no call "mcp-lane"`, []string{"mcp-lane"}},
		{"no JSON", `hello`, "malformed request", []string{"start"}},
		{"no call", `{"version":1}`, "usage:", nil},
	} {
		var out, errs bytes.Buffer
		if rc := kind.Main(c.args, strings.NewReader(c.req), &out, &errs); rc != 2 || out.Len() != 0 || !strings.Contains(errs.String(), c.why) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want 2, nothing, %q", c.name, rc, out.String(), errs.String(), c.why)
		}
	}
	var out, errs bytes.Buffer
	if rc := kind.Main([]string{"describe"}, strings.NewReader(`{"version":1,"call":"describe"}`), &out, &errs); rc != 0 ||
		!strings.Contains(out.String(), `"name":"pi"`) || errs.Len() != 0 {
		t.Errorf("describe: exit %d, stdout %q, stderr %q", rc, out.String(), errs.String())
	}
}
