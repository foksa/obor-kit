package codex_test

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/codex"
)

// T-495: Codex's start argv (resume first, obor's five -c options with a kit, the update check off, the prompt last
// unless resumed), its self-update screen, /quit, the headless forms, the per-start registration, rollout transcripts,
// typed delivery. T-570: the startup update check is off unless the type's args set it.
func TestKind(t *testing.T) {
	k, ok := split()
	if !ok || k.Name() != "codex" {
		t.Fatalf("codex not registered: %v", agents.Names())
	}
	mcp := []string{"-c", `mcp_servers.obor.command="/kit/bin/obor"`, "-c", `mcp_servers.obor.args=["mcp"]`,
		"-c", `mcp_servers.obor.env_vars=["OBOR_ROLE","OBOR_LANE","OBOR_PANE","OBOR_STATE_HOME"]`,
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
	if codex.EnvVarsTOML() != `["OBOR_ROLE","OBOR_LANE","OBOR_PANE","OBOR_STATE_HOME"]` {
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
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksNone, Resume: true, Transcript: true, Status: agents.StatusTranscript, Sessions: true}) {
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
	k, _ := split()
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
	k, _ := split()
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
	k, _ := split()
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
	k, _ := split()
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
	k, _ := split()
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
	k, _ := split()
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

// projectFS is a reader of one start dir /lane holding .codex/config.toml with content ("": the dir holds nothing).
func projectFS(content string) agents.ReadFile {
	return func(path string) ([]byte, error) {
		if content != "" && path == "/lane/"+codex.ConfigFile {
			return []byte(content), nil
		}
		return nil, fs.ErrNotExist
	}
}

// T-1033: the start dir's committed .codex/config.toml outranks obor's policy flags: a top-level sandbox_mode drops
// the base -s workspace-write, a top-level check_for_update_on_startup drops obor's -c; a key under a [table], a
// file that does not parse, or no file: the flags as before.
func TestStartProjectConfig(t *testing.T) {
	k, _ := split()
	base := []string{"-s", "workspace-write", "-a", "never", "--no-daemon"}
	update := []string{"-c", "check_for_update_on_startup=false"}
	all := append(append([]string(nil), base...), update...)
	noSandbox := append([]string{"-a", "never", "--no-daemon"}, update...)
	for _, c := range []struct {
		name, file string
		want       []string
	}{
		{"absent", "", all},
		{"sets neither key", "model = \"gpt-6\"\n", all},
		{"sets sandbox_mode: no -s", "# mine\nsandbox_mode = \"read-only\"\n", noSandbox},
		{"sets check_for_update_on_startup: no -c", "check_for_update_on_startup = true\n", base},
		{"sets both", "sandbox_mode = 'danger-full-access'\n\"check_for_update_on_startup\" = true\n",
			[]string{"-a", "never", "--no-daemon"}},
		{"a multi-line value before the key", "notify = [\n  \"a]\",\n  [\"b\"],\n]\nsandbox_mode = \"read-only\" # c\n", noSandbox},
		{"a multi-line string before the key", "x = \"\"\"\nsandbox_mode = 1\n\"\"\"\ncheck_for_update_on_startup = true\n", base},
		{"the keys under a table: not top-level", "model = \"m\"\n[profiles.x]\nsandbox_mode = \"read-only\"\ncheck_for_update_on_startup = true\n", all},
		{"does not parse: as absent", "sandbox_mode \"read-only\"\n", all},
		{"an open value: as absent", "check_for_update_on_startup = true\nx = [1,\n", all},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := k.Start(agents.Start{Args: base, Dir: "/lane", Read: projectFS(c.file)})
			if !slices.Equal(got, c.want) {
				t.Errorf("Start =\n %q\nwant\n %q", got, c.want)
			}
		})
	}
	// No start dir: the file is never read.
	if got := k.Start(agents.Start{Args: base, Read: projectFS("sandbox_mode = \"x\"\n")}); !slices.Equal(got, all) {
		t.Errorf("no dir: %q", got)
	}
	// The type's own update-check option still wins over the default (T-570), with a project file there too.
	typed := append(append([]string(nil), base...), "-c", "check_for_update_on_startup=true")
	if got := k.Start(agents.Start{Args: typed, Dir: "/lane", Read: projectFS("sandbox_mode = \"x\"\n")}); !slices.Equal(got,
		[]string{"-a", "never", "--no-daemon", "-c", "check_for_update_on_startup=true"}) {
		t.Errorf("type args set the update check: %q", got)
	}
}

// T-1033: config validate notes a config file that does not parse; none for a clean or absent one.
func TestProjectWarnings(t *testing.T) {
	k, _ := split()
	pc, ok := k.(agents.ProjectChecker)
	if !ok {
		t.Fatal("codex reads no project file")
	}
	for file, want := range map[string]int{"": 0, "sandbox_mode = \"x\"\n[t]\nwhat ever\n": 0, "what ever\n": 1} {
		if got := pc.ProjectWarnings("/lane", projectFS(file)); len(got) != want {
			t.Errorf("%q: warnings %q, want %d", file, got, want)
		}
	}
}

// T-1045, T-1049: on darwin, the files beside the resolved codex (symlinks followed) whose com.apple.quarantine lacks
// the user-approved bit (0x0040) make one warning naming them and the fix on the package's version dir; an approved
// file (0x01c1), none, codex not on the PATH, or a machine that is not a Mac: no warning.
func TestInstallWarnings(t *testing.T) {
	k, _ := split()
	c, ok := k.(agents.InstallChecker)
	if !ok {
		t.Fatal("precondition: codex is an InstallChecker")
	}
	const link, ver = "/opt/homebrew/bin/codex", "/opt/homebrew/Caskroom/codex/0.167.0"
	host := func(goos string, found bool, flags map[string]uint32) agents.InstallHost {
		return agents.InstallHost{
			GOOS: goos,
			LookPath: func(name string) (string, error) {
				if !found || name != "codex" {
					return "", fs.ErrNotExist
				}
				return link, nil
			},
			Resolve: func(p string) (string, error) {
				if p == link {
					return ver + "/bin/codex", nil
				}
				return p, nil
			},
			Quarantine: func(p string) (uint32, bool) { f, ok := flags[p]; return f, ok },
		}
	}
	const hostBin = ver + "/bin/codex-code-mode-host"
	fix := "fix: xattr -dr com.apple.quarantine " + ver
	for _, tc := range []struct {
		name string
		h    agents.InstallHost
		want string // "" no warning; else the files part of the one line
	}{
		{"the host quarantined", host("darwin", true, map[string]uint32{hostBin: 0x0181}), hostBin + " carry"},
		{"the host the user approved", host("darwin", true, map[string]uint32{hostBin: 0x01c1}), ""},
		{"a browser's flags", host("darwin", true, map[string]uint32{hostBin: 0x0083}), hostBin + " carry"},
		{"all three", host("darwin", true, map[string]uint32{ver + "/bin/codex": 0x0181, hostBin: 0x0181,
			ver + "/bin/codex-exec-server": 0x0181}),
			ver + "/bin/codex, " + hostBin + ", " + ver + "/bin/codex-exec-server carry"},
		{"one approved among them", host("darwin", true, map[string]uint32{ver + "/bin/codex": 0x01c1, hostBin: 0x0181}),
			hostBin + " carry"},
		{"the symlink's own dir is not checked", host("darwin", true, map[string]uint32{"/opt/homebrew/bin/codex-code-mode-host": 0x0181}), ""},
		{"nothing quarantined", host("darwin", true, nil), ""},
		{"no codex on the PATH", host("darwin", false, map[string]uint32{hostBin: 0x0181}), ""},
		{"not a Mac", host("linux", true, map[string]uint32{hostBin: 0x0181}), ""},
	} {
		got := c.InstallWarnings(tc.h)
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: want no warning, got %q", tc.name, got)
			}
			continue
		}
		if len(got) != 1 || !strings.HasPrefix(got[0], tc.want+" com.apple.quarantine") || !strings.HasSuffix(got[0], fix) ||
			!strings.Contains(got[0], "code-mode host") {
			t.Errorf("%s: want one line %q … %q, got %q", tc.name, tc.want, fix, got)
		}
		if strings.Contains(strings.Join(got, ""), "--no-quarantine") {
			t.Errorf("%s: the stale --no-quarantine fix is back: %q", tc.name, got)
		}
	}
}

// T-1052: the start check refuses on darwin for the files beside the resolved codex whose com.apple.quarantine lacks
// the user-approved bit (0x0040), naming them and the fix; an approved file, none, no codex, or another OS: no refusal.
// It is the same read of the install as TestInstallWarnings, and it runs no process.
func TestStartRefusal(t *testing.T) {
	k, _ := agents.Lookup("codex")
	c, ok := k.(agents.StartChecker)
	if !ok {
		t.Fatal("precondition: codex is a StartChecker")
	}
	const link, ver = "/opt/homebrew/bin/codex", "/opt/homebrew/Caskroom/codex/0.167.0"
	host := func(goos string, found bool, flags map[string]uint32) agents.InstallHost {
		return agents.InstallHost{
			GOOS: goos,
			LookPath: func(name string) (string, error) {
				if !found || name != "codex" {
					return "", fs.ErrNotExist
				}
				return link, nil
			},
			Resolve: func(p string) (string, error) {
				if p == link {
					return ver + "/bin/codex", nil
				}
				return p, nil
			},
			Quarantine: func(p string) (uint32, bool) { f, ok := flags[p]; return f, ok },
		}
	}
	const hostBin = ver + "/bin/codex-code-mode-host"
	fix := "xattr -dr com.apple.quarantine " + ver
	for _, tc := range []struct {
		name string
		h    agents.InstallHost
		want string // "" no refusal; else the files part of the line
	}{
		{"the host quarantined", host("darwin", true, map[string]uint32{hostBin: 0x0181}), hostBin},
		{"the host the user approved", host("darwin", true, map[string]uint32{hostBin: 0x01c1}), ""},
		{"nothing quarantined", host("darwin", true, nil), ""},
		{"no codex on the PATH", host("darwin", false, map[string]uint32{hostBin: 0x0181}), ""},
		{"not a Mac", host("linux", true, map[string]uint32{hostBin: 0x0181}), ""},
	} {
		got := c.StartRefusal(tc.h)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: want no refusal, got %q", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want+" carry com.apple.quarantine") || !strings.Contains(got, fix) ||
			!strings.Contains(got, "refused") {
			t.Errorf("%s: want a refusal naming %q and the fix %q, got %q", tc.name, tc.want, fix, got)
		}
	}
}

// split is the registered codex kind with its two start methods (agents.Split); ok false: none registered.
func split() (agents.Split, bool) {
	k, ok := agents.Lookup("codex")
	s, _ := k.(agents.Split)
	return s, ok
}
