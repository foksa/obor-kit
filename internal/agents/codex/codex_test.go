package codex_test

import (
	"slices"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/codex"
)

// T-495: Codex's start argv (resume first, obor's five -c options with a kit, the update check off, the prompt last
// unless resumed), its self-update screen, /quit, the headless forms, the per-start registration, rollout transcripts,
// typed delivery. T-570: the startup update check is off unless the type's args set it.
func TestKind(t *testing.T) {
	k, ok := agents.Lookup("codex")
	if !ok || k.Name() != "codex" {
		t.Fatalf("codex not registered: %v", agents.Names())
	}
	mcp := []string{"-c", `mcp_servers.obor.command="/kit/bin/obor"`, "-c", `mcp_servers.obor.args=["mcp"]`,
		"-c", `mcp_servers.obor.env_vars=["OBOR_ROLE","OBOR_LANE","HERDR_PANE_ID","OBOR_STATE_HOME"]`,
		"-c", `mcp_servers.obor.env={OBOR_NO_SELF_HOST="1"}`, "-c", `mcp_servers.obor.default_tools_approval_mode="approve"`}
	noUpdate := []string{"-c", "check_for_update_on_startup=false"}
	for _, c := range []struct {
		name string
		in   agents.Start
		want []string
	}{
		{"fresh, prompt", agents.Start{Kit: "/kit", Args: []string{"-m", "luna"}, Prompt: "go"},
			append(append(append([]string{"-m", "luna"}, mcp...), noUpdate...), "go")},
		{"resume first, no prompt", agents.Start{Kit: "/kit", Args: []string{"-m", "luna"}, Resume: "s1", Prompt: "go"},
			append(append([]string{"resume", "s1", "-m", "luna"}, mcp...), noUpdate...)},
		{"no kit: no registration; settings ignored", agents.Start{Args: []string{"-m", "luna"},
			Settings: map[string]any{"a": 1}}, append([]string{"-m", "luna"}, noUpdate...)},
		{"a type arg sets the update check: not added", agents.Start{Kit: "/kit",
			Args: []string{"-c", "check_for_update_on_startup=true"}},
			append([]string{"-c", "check_for_update_on_startup=true"}, mcp...)},
	} {
		if got := k.Start(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: Start =\n %q\nwant\n %q", c.name, got, c.want)
		}
	}
	n := 0
	for _, w := range k.Start(agents.Start{Kit: "/kit"}) {
		if w == "check_for_update_on_startup=false" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a start carries the update check %d times, want once", n)
	}
	if codex.EnvVarsTOML() != `["OBOR_ROLE","OBOR_LANE","HERDR_PANE_ID","OBOR_STATE_HOME"]` {
		t.Errorf("EnvVarsTOML = %s", codex.EnvVarsTOML())
	}
	for screen, want := range map[string]string{"Updated Codex to 0.9, restart codex": "updated itself and exited",
		"› Ready": "", "Do you trust this folder?": ""} {
		if got := k.NotReady(screen); got != want {
			t.Errorf("NotReady(%q) = %q, want %q", screen, got, want)
		}
	}
	if s := k.Stop(); s.Quit != "/quit" || s.SecondEnter || s.AfterEscape != nil || !slices.Equal(s.ClearInput, []string{"ctrl+u"}) || s.Dialog != nil {
		t.Errorf("Stop = %+v", s)
	}
	h := k.Headless()
	if h.PrintFlag || !slices.Equal(h.Subcommands, []string{"exec", "e"}) || !slices.Equal(h.Anywhere, []string{"exec"}) ||
		!slices.Contains(h.ValueOpts, "-c") || !slices.Contains(h.ValueOpts, "--ask-for-approval") {
		t.Errorf("Headless = %+v", h)
	}
	m := k.Install("/kit", nil).MCP
	how, reg, static := m.Lane([]string{"-a", "never"}, nil)
	if m.Where != agents.PerStart || m.Path != "" || !reg || static ||
		how != `launch args -c mcp_servers.obor.command=/kit/bin/obor, env_vars=`+codex.EnvVarsTOML()+`, env OBOR_NO_SELF_HOST=1` {
		t.Errorf("Install = %+v; Lane = %q, %v, %v", m, how, reg, static)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		env  func(string) string
		want string
	}{
		{env(map[string]string{"HOME": "/h"}), "/h/.codex/sessions/*/*/*/rollout-*-s1.jsonl"},
		{env(map[string]string{"HOME": "/h", "CODEX_HOME": "/cx"}), "/cx/sessions/*/*/*/rollout-*-s1.jsonl"},
	} {
		if got := k.Transcript(c.env, "s1"); got != c.want {
			t.Errorf("Transcript = %q, want %q", got, c.want)
		}
	}
	if d, ok := k.Deliver("hi", []byte(`{"socket":"/s"}`)); !ok || !d.Typed || d.Socket != "" {
		t.Errorf("Deliver = %+v, %v; want typed", d, ok)
	}
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksNone, Resume: true, Transcript: true, Turns: true, Sessions: true}) {
		t.Errorf("Caps = %+v", c)
	}
}

// Rollout lines in the shape of a real one (2026-10-15): a turn's start, its token count, a clean end, and an end on
// the usage limit.
const (
	started  = `{"timestamp":"2026-10-15T11:25:22.258Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1792063522}}` + "\n"
	tokens   = `{"timestamp":"2026-10-15T12:23:07.800Z","type":"event_msg","payload":{"type":"token_count","info":null}}` + "\n"
	item     = `{"timestamp":"2026-10-15T12:23:07.810Z","type":"response_item","payload":{"type":"message","turn_id":"t1"}}` + "\n"
	limitMsg = "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at 6:30 PM."
	limited  = `{"timestamp":"2026-10-15T12:23:07.845Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":null,"error":{"message":"` + limitMsg + `","codex_error_info":"usage_limit_exceeded"}}}` + "\n"
	clean    = `{"timestamp":"2026-10-15T17:26:46.135Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"Implemented T-997."}}` + "\n"
	started2 = `{"timestamp":"2026-10-15T17:24:09.150Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}` + "\n"
)

// limitedAt630 is the 6:30 PM limited's message names: its next one after the line's time, on this machine's clock.
var limitedAt630 = func() time.Time {
	at := time.Date(2026, 10, 15, 12, 23, 7, 0, time.UTC).In(time.Local)
	t := time.Date(at.Year(), at.Month(), at.Day(), 18, 30, 0, 0, time.Local)
	if t.Before(at) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}()

// sameTurn: a and b are the same turn, their resets the same instant.
func sameTurn(a, b agents.Turn) bool {
	return a.ID == b.ID && a.Error == b.Error && a.Details == b.Details && a.Ended == b.Ended && a.Reset.Equal(b.Reset)
}

// T-563: LastTurn reads the rollout's last turn event from its tail: a task_complete with an error is a hit (the
// error as codex_error_info gives it, the message its details); a clean one, a later turn start after the error, or
// the error's line cut short (no newline yet) is none; a tail with no turn event says nothing. Each says its phase
// (T-571): task_complete and turn_aborted are the turn's end (Ended), task_started its start.
func TestLastTurn(t *testing.T) {
	k, _ := agents.Lookup("codex")
	for _, c := range []struct {
		name string
		tail string
		want agents.Turn
		ok   bool
	}{
		{"usage limit: hit", "cut first line\"}}\n" + started + item + limited + tokens,
			agents.Turn{ID: "t1", Error: "usage_limit_exceeded", Details: limitMsg, Ended: true, Reset: limitedAt630}, true},
		{"clean end: no hit", started + item + clean, agents.Turn{ID: "t1", Ended: true}, true},
		{"a later turn start after the error: no hit", started + limited + started2 + item, agents.Turn{ID: "t2"}, true},
		{"the error's line cut short: no hit", started + item + limited[:len(limited)-20], agents.Turn{ID: "t1"}, true},
		{"an error info with data: its key",
			started + `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"m","codex_error_info":{"http_connection_failed":{"http_status_code":502}}}}}` + "\n",
			agents.Turn{ID: "t1", Error: "http_connection_failed", Details: "m", Ended: true}, true},
		{"an interrupted turn: ended, no hit", started + `{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1"}}` + "\n",
			agents.Turn{ID: "t1", Ended: true}, true},
		{"no turn event in the tail", item + tokens, agents.Turn{}, false},
		{"no whole line", limited[:40], agents.Turn{}, false},
	} {
		got, ok := k.LastTurn([]byte(c.tail))
		if !sameTurn(got, c.want) || ok != c.ok {
			t.Errorf("%s: LastTurn = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// tokenLimits is a token_count line of the shape seen 2026-10-28 with the rate limits' windows (null: none).
func tokenLimits(primary, secondary string) string {
	return `{"timestamp":"2026-10-28T15:10:55.441Z","type":"event_msg","payload":{"type":"token_count","info":null,` +
		`"rate_limits":{"limit_id":"codex","primary":` + primary + `,"secondary":` + secondary + `,"credits":null}}}` + "\n"
}

// T-568: a turn ended on the usage limit has its reset from its last token count with a window at 100% (that
// window's resets_at; both full: the later; a later count of another limit with no windows passed over), else from
// the message's clock time, else none; a full window of an earlier turn is not this turn's.
func TestLastTurnReset(t *testing.T) {
	k, _ := agents.Lookup("codex")
	full := `{"used_percent":100.0,"window_minutes":300,"resets_at":1793206840}`
	part := `{"used_percent":21.0,"window_minutes":10080,"resets_at":1793775605}`
	fullWeek := `{"used_percent":100.0,"window_minutes":10080,"resets_at":1793775605}`
	noClock := `{"timestamp":"2026-10-15T12:23:07.845Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"You've hit your usage limit.","codex_error_info":"usage_limit_exceeded"}}}` + "\n"
	for _, c := range []struct {
		name string
		tail string
		want time.Time
	}{
		{"the full window's resets_at", started + tokenLimits(full, part) + tokenLimits("null", "null") + limited,
			time.Unix(1793206840, 0)},
		{"both full: the later", started + tokenLimits(full, fullWeek) + limited, time.Unix(1793775605, 0)},
		{"no window full: the message's time", started + tokenLimits(part, part) + limited, limitedAt630},
		{"the message only", started + item + limited, limitedAt630},
		{"none", started + item + noClock, time.Time{}},
		{"an earlier turn's full window: not this turn's", tokenLimits(full, part) + started + noClock, time.Time{}},
	} {
		got, ok := k.LastTurn([]byte(c.tail))
		if !ok || got.Error != "usage_limit_exceeded" {
			t.Fatalf("%s: precondition: LastTurn = %+v, %v; want the limit's turn", c.name, got, ok)
		}
		if !got.Reset.Equal(c.want) {
			t.Errorf("%s: Reset = %v, want %v", c.name, got.Reset, c.want)
		}
	}
}

// T-568: usage_limit_exceeded is a limit, its message's clock time its reset; another error is no limit.
func TestReset(t *testing.T) {
	k, _ := agents.Lookup("codex")
	at := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	if r, ok := k.Reset("usage_limit_exceeded", limitMsg, at); !ok || !r.Equal(time.Date(2026, 10, 30, 18, 30, 0, 0, time.UTC)) {
		t.Errorf("usage limit: %v, %v; want 18:30 that day, limited", r, ok)
	}
	if r, ok := k.Reset("http_connection_failed", "try again at 6:30 PM", at); ok || !r.IsZero() {
		t.Errorf("a failed connection: %v, %v; want no limit", r, ok)
	}
}

// T-577: a rollout's first line, its session_meta, names the session: its id, cwd and start (the payload's
// timestamp, else the line's); any other line names none.
func TestSessionMeta(t *testing.T) {
	k, _ := agents.Lookup("codex")
	if !k.Caps().Sessions {
		t.Error("Caps.Sessions: launch reports a fresh start's rollout")
	}
	s, ok := k.Session([]byte(`{"timestamp":"2026-10-30T09:00:01.500Z","type":"session_meta","payload":{"id":"cx-1",` +
		`"timestamp":"2026-10-30T09:00:00.250Z","cwd":"/w/L-01","originator":"codex_cli_rs"}}` + "\n"))
	if !ok || s.ID != "cx-1" || s.Cwd != "/w/L-01" || !s.Started.Equal(time.Date(2026, 10, 30, 9, 0, 0, 250e6, time.UTC)) {
		t.Errorf("session_meta: %+v %v", s, ok)
	}
	s, ok = k.Session([]byte(`{"timestamp":"2026-10-30T09:00:01Z","type":"session_meta","payload":{"id":"cx-2","cwd":"/w"}}`))
	if !ok || !s.Started.Equal(time.Date(2026, 10, 30, 9, 0, 1, 0, time.UTC)) {
		t.Errorf("no payload timestamp: the line's: %+v %v", s, ok)
	}
	for _, line := range []string{`{"type":"event_msg","payload":{"type":"task_started"}}`, `not json`,
		`{"type":"session_meta","payload":{"cwd":"/w"}}`} {
		if _, ok := k.Session([]byte(line)); ok {
			t.Errorf("%s: no session", line)
		}
	}
}

// T-695: Start with an effort sets -c model_reasoning_effort=<effort> once, in place of the args' own (each spelling);
// without one the args are as given.
func TestStartEffort(t *testing.T) {
	k, _ := agents.Lookup("codex")
	for _, args := range [][]string{
		{"-m", "luna", "-c", "model_reasoning_effort=high", "-s", "workspace-write"},
		{"-m", "luna", "--config", "model_reasoning_effort = high", "-s", "workspace-write"},
		{"-m", "luna", "-c=model_reasoning_effort=high", "-s", "workspace-write"},
		{"-m", "luna", "--config=model_reasoning_effort=high", "-s", "workspace-write"},
		{"-m", "luna", "-s", "workspace-write"},
	} {
		got := k.Start(agents.Start{Args: args, Effort: "xhigh"})
		want := []string{"-m", "luna", "-s", "workspace-write", "-c", "model_reasoning_effort=xhigh", "-c", "check_for_update_on_startup=false"}
		if !slices.Equal(got, want) {
			t.Errorf("Start(%q, xhigh) = %q, want %q", args, got, want)
		}
	}
	args := []string{"-m", "luna", "-c", "model_reasoning_effort=high"}
	if got := k.Start(agents.Start{Args: args}); !slices.Equal(got[:4], args) || len(got) != 6 {
		t.Errorf("no effort: args as given: %q", got)
	}
}

// T-853: a start with WriteDirs (a reviewer's or deep dive's reviews dir in the main checkout) adds one --add-dir per
// dir after the args, on a resume too; none: no --add-dir.
func TestStartWriteDirs(t *testing.T) {
	k, _ := agents.Lookup("codex")
	args := []string{"-s", "workspace-write", "-m", "terra"}
	dirs := []string{"/p/docs/reviews", "/p/docs/research"}
	want := []string{"-s", "workspace-write", "-m", "terra", "--add-dir", "/p/docs/reviews", "--add-dir", "/p/docs/research",
		"-c", "check_for_update_on_startup=false"}
	if got := k.Start(agents.Start{Args: args, WriteDirs: dirs}); !slices.Equal(got, want) {
		t.Errorf("Start = %q, want %q", got, want)
	}
	if got := k.Start(agents.Start{Args: args, WriteDirs: dirs, Resume: "sid"}); !slices.Equal(got, append([]string{"resume", "sid"}, want...)) {
		t.Errorf("resume: %q", got)
	}
	if got := k.Start(agents.Start{Args: args}); slices.Contains(got, "--add-dir") {
		t.Errorf("no write dirs: %q", got)
	}
}
