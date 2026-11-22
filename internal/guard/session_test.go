package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// sessionFx: lane L-01 (task/T-05) whose lane row names T-05 on base main, whose stored spec unlocks one protected
// file; armed = false: no lane row.
func sessionFx(t *testing.T, armed bool) (f hfx, o1 string) {
	t.Helper()
	f = newHfx(t)
	o1 = f.PlainLane("L-01", "task/T-05")
	f.Config()
	if armed {
		f.Spec("T-05", "# T-05\n\n## Protected files this task may edit\n`docs/design.md`\n") // the store's
		f.State("L-01", `{"lane":"L-01","task":"T-05","branch":"task/T-05","base":"main"}`)
	}
	return f, o1
}

// runs makes lane L-01's open dispatch (task) run model: its kind is the lane's.
func (f hfx) runs(model string) {
	f.State("L-01", `{"lane":"L-01","task":"T-05","branch":"task/T-05","base":"main","type":"`+model+`"}`)
}

// session runs the SessionStart hook on an event in cwd (with session_id sid when not ""); env adds VAR=value.
func (f hfx) session(t *testing.T, cwd, sid string, env ...string) string {
	t.Helper()
	ev := map[string]any{"hook_event_name": "SessionStart", "source": "startup", "cwd": cwd}
	if sid != "" {
		ev["session_id"] = sid
	}
	b, _ := json.Marshal(ev)
	var out bytes.Buffer
	e := f.Env(env...)
	SessionStart(b, e, &out)
	return out.String()
}

// serve puts the project's fake herdr behind a socket and its path as env for a session run; its requests say what
// the hook asked herdr.
func (f hfx) serve(t *testing.T) (*herdrtest.Server, string) {
	t.Helper()
	srv := herdrtest.Serve(t, f.Herdr())
	return srv, "HERDR_SOCKET_PATH=" + srv.Path
}

// label sets pane's label in the fake herdr, adding a shell pane when it has none ("" = no label).
func (f hfx) label(pane, label string) {
	if p := f.Herdr().Pane(pane); p != nil {
		p.Label = label
		return
	}
	f.Herdr().Shell(pane, label, "")
}

// panes is how many pane.get requests srv got for pane so far.
func panes(srv *herdrtest.Server, pane string) int {
	n := 0
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r.String(), "pane.get pane_id="+pane) {
			n++
		}
	}
	return n
}

func contains(t *testing.T, out, what string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(out, n) {
			t.Errorf("%s: no %q in\n%s", what, n, out)
		}
	}
}

func lacks(t *testing.T, out, what string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(out, n) {
			t.Errorf("%s: %q in\n%s", what, n, out)
		}
	}
}

// bash plugin case_session_worker_armed, case_session_worker_unarmed
func TestSessionWorkerRows(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		armed           bool
		state           string   // a lane row other than the fixture's
		contains, lacks []string // in the session line
	}{
		{"an armed worker names the spec's protected files", true, "",
			[]string{"obor: role worker, lane L-01, task T-05 (cwd)",
				"Protected files this task may edit: docs/design.md (from T-05's spec, rev 1 in obor serve's store).",
				"edits outside your lane (other lanes, the main checkout, other obor projects) are denied", "task_done", "[obor",
				"0 deny rules from rules.deny_commands"},
			[]string{"No lane row", "you may edit only", "allowed files"}},
		{"a lane whose task has no stored spec", true, `{"lane":"L-01","task":"T-09","branch":"task/T-09","base":"main"}`,
			[]string{"obor: role worker, lane L-01, task T-09 (cwd)", "edits outside your lane"},
			[]string{"Protected files this task may edit:", "every edit in the lane is denied"}},
		{"no lane row (not dispatched)", false, "",
			[]string{"role worker, lane L-01", "No lane row (not dispatched)."}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, o1 := sessionFx(t, c.armed)
			if c.state != "" {
				f.State("L-01", c.state)
			}
			out := f.session(t, o1, "")
			contains(t, out, c.name, c.contains...)
			lacks(t, out, c.name, c.lacks...)
		})
	}
}

// No Stop hook line (they stop freely), no heartbeat.
func TestSessionLeadAndIntegratorHookLines(t *testing.T) {
	t.Parallel()
	f, _ := sessionFx(t, true)
	// the lead drives terminals; the integrator's line names the hc/herdr-app denial
	for role, hooks := range map[string]string{
		"lead":       "Hooks: git push, pkill/killall by pattern and headless agent runs (agy -p, claude -p, codex exec, opencode run, x-agent -p) are denied;",
		"integrator": "Hooks: git push, pkill/killall by pattern, headless agent runs (agy -p, claude -p, codex exec, opencode run, x-agent -p) and the hc/herdr-app commands that drive terminals are denied",
	} {
		out := f.session(t, f.Root, "", "OBOR_ROLE="+role)
		contains(t, out, role, "obor: role "+role, hooks, "0 deny rules from rules.deny_commands")
		lacks(t, out, role, "[obor", "cannot stop", "Stop hook", "heartbeat", "obor ack")
	}
}

// the hooks line names how many rules.deny_commands entries the project's config carries.
func TestSessionDenyCommandCount(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, rules, want string
	}{
		{"one rule", `[{"match": "^bun test --coverage", "alone": true, "message": "no coverage"}]`, "1 deny rule from rules.deny_commands"},
		{"two rules", `[{"match": "^bun test --coverage", "alone": true, "message": "no coverage"},
			{"match": "^bun test", "message": "two bun test runs at once"}]`, "2 deny rules from rules.deny_commands"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, _ := sessionFx(t, true)
			f.Config(testkit.Set("rules.deny_commands", c.rules))
			contains(t, f.session(t, f.Root, "", "OBOR_ROLE=lead"), c.name, c.want)
		})
	}
}

// the role-text line names the kit's text (absolute path) and, when the worktree has one, the project's
// doc under paths.agents; the lead gets /obor:lead on a kind with caps skills and the skill file otherwise; a role
// without a text gets none; a custom role's doc is its own.
func TestSessionRoleTextRows(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	kit := f.Base + "/kit"
	for _, r := range []string{"worker", "integrator", "reviewer", "researcher"} {
		f.Write(kit+"/templates/agents/"+r+".md", "# Role: "+r+"\n")
	}
	// custom is a custom role: a custom role's doc and a kit default row's model.
	custom := func() {
		f.Write("docs/agents/ux_buddy.md", "# Role: ux_buddy\n")
		f.Config(testkit.Set("agents.roles.ux_buddy", `{"model": "opus", "write": ["docs/ux/"]}`), testkit.Set("agents.roles.researcher", "sonnet"))
	}
	for _, c := range []struct {
		name            string
		before          func()
		role            string   // OBOR_ROLE
		cwd             string   // "root" or "" (the lane)
		contains, lacks []string // in the session line
	}{
		{"a worker without a project doc", nil, "", "",
			[]string{"obor: role worker", kit + "/templates/agents/worker.md — read it before acting"},
			[]string{", project:"}},
		{"a worker with a project doc", func() {
			if err := os.MkdirAll(o1+"/docs/agents", 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(o1+"/docs/agents/worker.md", []byte("# worker here\n"), 0o644)
		}, "", "",
			[]string{kit + "/templates/agents/worker.md, project: docs/agents/worker.md — read it before acting"}, nil},
		{"an integrator", nil, "integrator", "root",
			[]string{"obor: role integrator", kit + "/templates/agents/integrator.md — your jobs carry it (job_next), no read needed"},
			[]string{"read it before acting"}},
		{"an integrator with a project doc", func() {
			f.Write("docs/agents/integrator.md", "# integrator here\n")
		}, "integrator", "root",
			[]string{kit + "/templates/agents/integrator.md, project: docs/agents/integrator.md — your jobs carry it (job_next), no read needed"},
			[]string{"read it before acting"}},
		{"a reviewer with paths.agents", func() {
			f.Write("doc/roles/reviewer.md", "# reviewer here\n")
			f.Config(testkit.Set("agents.docs_dir", "doc/roles"))
		}, "reviewer", "root",
			[]string{"obor: role reviewer", kit + "/templates/agents/reviewer.md, project: doc/roles/reviewer.md — read it before acting"}, nil},
		{"the lead", nil, "lead", "root",
			[]string{"obor: role lead", "obor: role text: run /obor:lead — read it before acting"},
			[]string{"templates/agents/lead.md"}},
		{"the lead on a kind without skills", func() {
			f.Write(kit+"/skills/lead/SKILL.md", "# obor lead\n")
			f.Config(testkit.Set("agents.roles.lead", "luna"))
		}, "lead", "root",
			[]string{"obor: role lead", "obor: role text: " + kit + "/skills/lead/SKILL.md — read it before acting"},
			[]string{"run /obor:lead", "templates/agents/lead.md"}},
		{"a role without a kit text", nil, "deep_dive", "root", nil, []string{"obor: role text"}},
		{"a custom role's own doc", custom, "ux_buddy", "root",
			[]string{"obor: role ux_buddy", "obor: role text: docs/agents/ux_buddy.md — read it before acting"},
			[]string{"templates/agents/ux_buddy.md"}},
		{"a kit default row's kit text", custom, "researcher", "root",
			[]string{"obor: role text: " + kit + "/templates/agents/researcher.md — read it before acting"}, nil},
		{"a kit default row's project doc", func() {
			custom()
			f.Write("docs/agents/researcher.md", "# Role: researcher — here\n")
		}, "researcher", "root",
			[]string{"obor: role text: " + kit + "/templates/agents/researcher.md, project: docs/agents/researcher.md — read it before acting"}, nil},
		{"a role the config does not know", func() {
			f.Write("docs/agents/ux_buddy.md", "# Role: ux_buddy\n")
			f.Config(testkit.Set("agents.roles.researcher", "sonnet")) // the ux_buddy row removed: its doc stays, no role text
		}, "ux_buddy", "root",
			[]string{"obor: role ux_buddy"}, []string{"obor: role text"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.before != nil {
				c.before()
			}
			cwd := o1
			if c.cwd == "root" {
				cwd = f.Root
			}
			env := []string{"OBOR_PLUGIN_ROOT=" + kit}
			if c.role != "" {
				env = append(env, "OBOR_ROLE="+c.role)
			}
			out := f.session(t, cwd, "", env...)
			contains(t, out, c.name, c.contains...)
			lacks(t, out, c.name, c.lacks...)
		})
	}
}

// bash plugin case_session_no_config, case_session_bad_stdin
func TestSessionNoConfigOrBadInputSilent(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	if f.session(t, o1, "") == "" {
		t.Fatal("precondition: output with a config")
	}
	os.Remove(f.Path(".obor/config.json"))
	if out := f.session(t, o1, ""); out != "" {
		t.Errorf("no config: %q", out)
	}
	for _, in := range []string{"not json", "", `{"cwd": "` + f.Base + `/nowhere"}`} {
		var out bytes.Buffer
		SessionStart([]byte(in), f.Env(), &out)
		if out.Len() != 0 {
			t.Errorf("input %q: %q", in, out.String())
		}
	}
}

// the hook writes no session-id file: serve's store is the only session record (the session request); with
// serve down the session is not recorded. Nothing is written without OBOR_AGENT either.
func TestSessionWritesNoSidFile(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	state := f.Base + "/state"
	contains(t, f.session(t, o1, "sid-123", "OBOR_AGENT=pj-l1", "OBOR_STATE_HOME="+state), "hook ran", "obor: role worker")
	if f.Exists(state + "/agents/pj-l1.sid") {
		t.Error("the hook wrote a .sid file")
	}
	os.RemoveAll(state)
	contains(t, f.session(t, o1, "sid-123", "OBOR_STATE_HOME="+state), "hook ran without OBOR_AGENT", "obor: role worker")
	if f.Exists(state + "/agents") {
		t.Error("no OBOR_AGENT → something written")
	}
}

// an OpenCode lane's session-start (its plugin's shared process carries no OBOR_AGENT) records the session
// under the lane's agent (its Began names the agent, kind and session for serve).
func TestSessionOpencodeBegan(t *testing.T) {
	f, o1 := sessionFx(t, true)
	f.runs("deepseek")
	state := f.Base + "/state"
	start := func(cwd string, env ...string) (Began, bool) {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": cwd, "session_id": "sid-o",
			"transcript_path": "/data/opencode/opencode.db", "source": "startup"})
		var out bytes.Buffer
		return SessionStart(b, f.Env(append([]string{"OBOR_STATE_HOME=" + state}, env...)...), &out)
	}

	began, ok := start(o1)
	if !ok || began.Agent != "pj-l-01" || began.Kind != "opencode" || began.ID != "sid-o" ||
		began.Transcript != "/data/opencode/opencode.db" || began.Lane != "L-01" || began.Task != "T-05" {
		t.Errorf("no OBOR_AGENT: began %+v, %v; want the lane's agent and its session", began, ok)
	}

	began, ok = start(o1, "OBOR_AGENT=pj-old-name")
	if !ok || began.Agent != "pj-l-01" || began.Kind != "opencode" || began.ID != "sid-o" {
		t.Errorf("a stale OBOR_AGENT: began %+v, %v; want the derived pj-l-01", began, ok)
	}
	// every role's session begins as its derived name, a stale OBOR_AGENT ignored.
	for role, want := range map[string]string{"lead": "pj-lead", "integrator": "pj-int", "reviewer": "pj-rev"} {
		began, ok := start(f.Root, "OBOR_ROLE="+role, "OBOR_AGENT=pj-old-name")
		if !ok || began.Agent != want {
			t.Errorf("%s with a stale OBOR_AGENT: began %+v, %v; want %s", role, began, ok, want)
		}
	}
	if began, ok := start(f.Root, "OBOR_AGENT=pj-lead"); ok || began.Agent != "" {
		t.Errorf("no role in the main checkout, OBOR_AGENT only: began %+v, %v; want no agent", began, ok)
	}

	g, g1 := sessionFx(t, true)
	gState := g.Base + "/state"
	b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": g1, "session_id": "sid-c"})
	var out bytes.Buffer
	if began, ok := SessionStart(b, g.Env("OBOR_AGENT=pj-l-01", "OBOR_STATE_HOME="+gState), &out); !ok ||
		began.Kind != "claude" {
		t.Fatalf("control (claude): began %+v, %v", began, ok)
	}
}

// This is the pre-tool-use part of the lost-identity check: a session record is never an identity (see
// TestNoIdentityFromTheSessionID).
func TestSessionNoIdentityFromRecords(t *testing.T) {
	f, o1 := sessionFx(t, true)
	_, sock := f.serve(t)
	home := f.Base + "/state-home"
	f.Write(home+"/agents/pj-lead.pane", "w9:p1\n")
	f.label("w9:p1", "pj-lead")
	envFile := f.Base + "/claude-env"
	out := f.session(t, f.Root, "sid-lead", "OBOR_PANE=w9:p1", sock, "CLAUDE_ENV_FILE="+envFile)
	contains(t, out, "a recorded sid gives no identity", "obor: role none")
	if n := strings.Count(out, "pj-lead"); n != 0 { // no lost-identity line either
		t.Errorf("the pane's label is named: %d in\n%s", n, out)
	}
	if f.Exists(envFile) || f.Exists(home+"/sessions") {
		t.Error("something exported or recorded")
	}
	os.Remove(home + "/agents/pj-lead.pane")
	contains(t, f.session(t, o1, "sid-o1-new", "OBOR_AGENT=pj-l-01", "OBOR_PANE=w9:p2", sock), "hook ran", "obor: role worker")
	entries, _ := os.ReadDir(home + "/agents")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pane") {
			t.Errorf("a .pane file: %s", e.Name())
		}
	}
}

// the session-start hook of an opencode lane writes the plugin marker into the main checkout's state dir,
// <root>/.obor/state/lanes/<LANE>.plugin (what obor dispatch waits for and obor status reads), never the lane worktree,
// so the lane reads clean. Another kind, or the main checkout, writes none.
func TestSessionWritesOpencodePluginMarker(t *testing.T) {
	t.Parallel()
	marker := func(root string) (kind, version string, at int64, ok bool) {
		b, err := os.ReadFile(root + "/.obor/state/lanes/L-01.plugin")
		if err != nil {
			return "", "", 0, false
		}
		var m struct {
			Kind    string `json:"kind"`
			Version string `json:"version"`
			At      int64  `json:"at"`
		}
		if json.Unmarshal(b, &m) != nil {
			return "", "", 0, false
		}
		return m.Kind, m.Version, m.At, true
	}

	f, o1 := sessionFx(t, true)
	f.runs("deepseek")
	before := time.Now().Unix()
	f.session(t, o1, "sid-1")
	kind, version, mAt, ok := marker(f.Root)
	if !ok || kind != "opencode" || version != "" || mAt < before {
		t.Errorf("opencode lane: kind %q version %q at %d ok %v (before %d)", kind, version, mAt, ok, before)
	}
	if f.Exists(o1 + "/.obor-plugin") {
		t.Error("the lane worktree holds a marker")
	}
	// The lane holds nothing but its own .git file and the settings every lane has: the state written is the main
	// checkout's (a git status would say the same; a plain lane has no git to ask).
	var extra []string
	err := filepath.WalkDir(o1, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch rel, _ := filepath.Rel(o1, p); rel {
		case ".", ".git", ".claude", ".claude/settings.json":
		default:
			extra = append(extra, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) > 0 {
		t.Errorf("session-start dirtied the lane worktree: %v", extra)
	}
	// The version, when the environment knows one.
	contains(t, f.session(t, o1, "sid-2", "OPENCODE_VERSION=1.2.3"), "hook ran", "obor: role worker")
	if _, version, _, _ = marker(f.Root); version != "1.2.3" {
		t.Errorf("version: %q", version)
	}

	// A claude-kind lane, and the main checkout: none.
	h, h1 := sessionFx(t, true)
	h.session(t, h1, "sid-1")
	if h.Exists(".obor/state/lanes/L-01.plugin") {
		t.Error("a claude-kind lane: a marker")
	}
	h.session(t, h.Root, "sid-1")
	if h.Exists(".obor/state/lanes/L-01.plugin") {
		t.Error("the main checkout: a marker")
	}
}

// the session's kind and the plugin marker follow the model a lane's open dispatch runs: an
// OpenCode dispatch is kind opencode with a marker, a Claude dispatch kind claude without one; with no open dispatch
// the default kind. A kind's transport that names its kind (agent_kind) is the session's kind, whatever the dispatch;
// a name the config does not know is not.
func TestSessionKindIsTheDispatchs(t *testing.T) {
	t.Parallel()
	start := func(f hfx, cwd, agentKind string) Began {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": cwd, "session_id": "sid-1",
			"agent_kind": agentKind})
		var out bytes.Buffer
		began, ok := SessionStart(b, f.Env("OBOR_STATE_HOME="+f.Base+"/state"), &out)
		if !ok {
			t.Fatalf("no session began:\n%s", out.String())
		}
		return began
	}
	const marker = ".obor/state/lanes/L-01.plugin"
	for _, c := range []struct {
		name, rowType string
		armed         bool
		kind          string
		agentKind     string
	}{
		{"OpenCode dispatch", "deepseek", true, "opencode", ""},
		{"OpenCode dispatch with an effort", "deepseek:high", true, "opencode", ""},
		{"a project's OpenCode model", "oc", true, "opencode", ""},
		{"Claude dispatch", "opus", true, "claude", ""},
		{"legacy dispatch (no model)", "", true, "claude", ""},
		{"no open dispatch", "", false, "claude", ""},
		{"no open dispatch, agy's transport", "", false, "agy", "agy"},
		{"Claude dispatch, agy's transport", "opus", true, "agy", "agy"},
		{"no open dispatch, a kind the config does not know", "", false, "claude", "nokind"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, o1 := sessionFx(t, false)
			f.Config(testkit.Set("agents.models.oc", `{"kind":"opencode","id":"go/x"}`))
			if c.armed {
				f.State("L-01", `{"task":"T-05","branch":"task/T-05","base":"main","type":"`+c.rowType+`"}`)
			}
			if f.Exists(marker) {
				t.Fatal("precondition: a marker before the session")
			}
			if began := start(f, o1, c.agentKind); began.Kind != c.kind || began.Lane != "L-01" {
				t.Errorf("began %+v, want kind %s on lane L-01", began, c.kind)
			}
			if got, want := f.Exists(marker), c.kind == "opencode"; got != want {
				t.Errorf("plugin marker written: %v, want %v", got, want)
			}
		})
	}
}

// bash plugin case_session_env_unchanged
func TestSessionWithOborAgentIgnoresOtherRecords(t *testing.T) {
	f, o1 := sessionFx(t, true)
	srv, sock := f.serve(t)
	f.label("w9:p1", "pj-l-01")
	plain := f.session(t, o1, "sid-o1", "OBOR_AGENT=pj-l-01", "OBOR_PANE=w9:p1", sock)
	contains(t, plain, "hook ran", "obor: role worker, lane L-01, task T-05 (cwd)")
	home := f.Base + "/state-home"
	f.Write(home+"/agents/pj-lead.pane", "w9:p1\n")
	before := panes(srv, "w9:p1")
	if out := f.session(t, o1, "sid-o1", "OBOR_AGENT=pj-l-01", "OBOR_PANE=w9:p1", sock); out != plain {
		t.Errorf("env set: output changed by another agent's records:\n%s\nwas\n%s", out, plain)
	}
	if f.Exists(home+"/sessions") || panes(srv, "w9:p1") != before {
		t.Errorf("env set: a session record or a herdr pane get: %d before, %d after", before, panes(srv, "w9:p1"))
	}
}

// A name is derived, so a labelled pane without OBOR_AGENT is no lost
// identity: no warning, and the hook asks herdr nothing.
func TestSessionLabelledPaneNoLostIdentity(t *testing.T) {
	f, o1 := sessionFx(t, true)
	srv, sock := f.serve(t)
	for _, c := range []struct{ label, cwd, role string }{{"pj-l-01", o1, ""}, {"pj-lead", f.Root, "OBOR_ROLE=lead"}} {
		f.label("w9:p1", c.label)
		before := len(srv.Requests())
		out := f.session(t, c.cwd, "sid-zzz", "OBOR_PANE=w9:p1", c.role, sock)
		contains(t, out, "precondition: the hook ran", "obor: role ")
		lacks(t, out, c.label+": no lost-identity line", "WARNING", "OBOR_AGENT")
		if n := len(srv.Requests()) - before; n != 0 {
			t.Errorf("%s: %d herdr requests, want none", c.label, n)
		}
	}
}

// a database read error is said, and the other bookkeeping (the session id) still runs.
func TestSessionUnreadableDatabase(t *testing.T) {
	f, o1 := sessionFx(t, true)
	if out := f.session(t, o1, "sid-ok"); !strings.Contains(out, "Protected files this task may edit:") {
		t.Fatalf("precondition: a readable database shows the spec's unlocks:\n%s", out)
	}
	f.Write(".obor/state/obor.db", "not a database\n")
	out := f.session(t, o1, "sid-bad", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock")
	contains(t, out, "unreadable database", "could not read this lane or its spec", "obor: role worker, lane L-01")
	lacks(t, out, "unreadable database", "No lane row", "Protected files this task may edit:")
	if in, err := project.ReadInbox(f.Cfg(), "L-01"); err != nil || in.Session != "sid-bad" {
		t.Errorf("inbox = %+v, %v; want session sid-bad (the bookkeeping still runs)", in, err)
	}
}

// a session with CLAUDE_CODE_MESSAGING_SOCKET records the inbox of its slot in the
// project's main checkout (<root>/.obor/state/inbox/<slot>), mode 0600, with the socket, the token and the session id:
// a worker under its lane, the lead under lead, the drive agent under its lane's .drive, triage under triage; nothing in the global state
// home; a stale OBOR_AGENT (a reused shell) is ignored; a later session without one removes it; a session that holds no
// slot (the owner in the main checkout) writes none.
func TestSessionRecordsTheInbox(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	state := f.Base + "/state"
	dir := f.Root + "/.obor/state/inbox"
	if got := project.InboxFile(f.Root, "L-01/drive"); got != dir+"/L-01.drive" {
		t.Fatalf("drive slot's record at %s, want %s/L-01.drive", got, dir)
	}
	f.session(t, f.Root, "sid-1", "OBOR_AGENT=pj-l-01", "OBOR_STATE_HOME="+state, "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock")
	if f.Exists(dir) {
		t.Error("no role in the main checkout (a stale OBOR_AGENT only) → something written")
	}
	for _, c := range []struct{ label, cwd, role, slot string }{
		{"a worker", o1, "", "L-01"},
		{"a worker with OBOR_ROLE=lead in its lane", o1, "lead", "L-01"},
		{"the lead", f.Root, "lead", "lead"},
		{"the drive agent", o1, "driver", "L-01/drive"},
		{"the triage agent (T-796)", f.Root, "triage", "triage"},
	} {
		inbox := project.InboxFile(f.Root, c.slot)
		f.session(t, c.cwd, "sid-1", "OBOR_AGENT=pj-old", "OBOR_ROLE="+c.role, "OBOR_STATE_HOME="+state,
			"CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock", "CLAUDE_CODE_MESSAGING_TOKEN=tok-1")
		got, err := project.ReadInbox(f.Cfg(), c.slot)
		if err != nil || got != (project.Inbox{Socket: "/tmp/cc-socks/1.sock", Token: "tok-1", Session: "sid-1"}) {
			t.Fatalf("%s: inbox of slot %s %+v, %v", c.label, c.slot, got, err)
		}
		if fi, err := os.Stat(inbox); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: inbox mode %v, %v; want 0600", c.label, fi.Mode(), err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("%s: %d records in %s, want only slot %s's", c.label, len(entries), dir, c.slot)
		}
		if f.Exists(state + "/agents") {
			t.Errorf("%s: a record in the global state home", c.label)
		}
		f.session(t, c.cwd, "sid-1", "OBOR_AGENT=pj-old", "OBOR_ROLE="+c.role, "OBOR_STATE_HOME="+state)
		if f.Exists(inbox) {
			t.Errorf("%s: a session without a messaging socket kept the inbox record", c.label)
		}
	}
}

// An OpenCode session's record is its plugin's socket (OBOR_OPENCODE_INBOX, the kind's inbox_record, in the hook's env):
// the socket its deliver names, and the kind's own record kept whole for deliver; a later session without the variable
// removes it.
func TestSessionRecordsAPluginKindsInbox(t *testing.T) {
	t.Parallel()
	f, _ := sessionFx(t, true)
	f.Config(testkit.Set("agents.roles.lead", "deepseek"))
	f.session(t, f.Root, "ses_1", "OBOR_ROLE=lead", "OBOR_OPENCODE_INBOX=/h/.obor/opencode/ses_1.sock")
	got, err := project.ReadInbox(f.Cfg(), "lead")
	want := project.Inbox{Socket: "/h/.obor/opencode/ses_1.sock", Session: "ses_1",
		Record: `{"OBOR_OPENCODE_INBOX":"/h/.obor/opencode/ses_1.sock"}`}
	if err != nil || got != want {
		t.Fatalf("inbox %+v, %v; want %+v", got, err, want)
	}
	f.session(t, f.Root, "ses_2", "OBOR_ROLE=lead")
	if f.Exists(project.InboxFile(f.Root, "lead")) {
		t.Error("a session without the variable kept the record")
	}
}

// agyLead is the config edit making the lead an agy agent (model gem, of the kit's agy kind).
func agyLead() func(map[string]any) {
	return func(c map[string]any) {
		testkit.Set("agents.models.gem", map[string]any{"kind": "agy", "id": "gemini-3-flash"})(c)
		testkit.Set("agents.roles.lead", "gem")(c)
	}
}

// agyVars are the agentapi variables of a command an agy session runs, with token tok: its inbox record's, and agy's
// executable, which the record leaves out.
func agyVars(tok string) []string {
	return []string{"ANTIGRAVITY_LS_ADDRESS=localhost:5011", "ANTIGRAVITY_CSRF_TOKEN=" + tok,
		"ANTIGRAVITY_AGENTAPI_EXE=/h/.gemini/antigravity-cli/bin/agentapi", "ANTIGRAVITY_CONVERSATION_ID=conv-1"}
}

// An obor command an agy lead runs records its inbox from the command's environment (agy's agentapi variables, which
// its hooks never see): the kind's own record kept whole, the kind sending with it, mode 0600, live. A later command
// refreshes it: a refused send's mark cleared, a new token (agy restarted) recorded. A command without the variables
// leaves the record as it is; a Claude lead's command with its messaging socket writes none (its SessionStart hook's
// record alone).
func TestCommandRecordsTheInbox(t *testing.T) {
	t.Parallel()
	f, _ := sessionFx(t, true)
	f.Config(agyLead())
	inbox := project.InboxFile(f.Root, "lead")
	if f.Exists(inbox) {
		t.Fatal("precondition: a record before any command")
	}
	cfg, err := config.Load(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	CommandRecord(f.Root, f.Env(append([]string{"OBOR_ROLE=lead"}, agyVars("tok-1")...)...), io.Discard)
	rec := `{"ANTIGRAVITY_CONVERSATION_ID":"conv-1","ANTIGRAVITY_CSRF_TOKEN":"tok-1","ANTIGRAVITY_LS_ADDRESS":"localhost:5011"}`
	if got, err := project.ReadInbox(f.Cfg(), "lead"); err != nil || got != (project.Inbox{Sends: true, Record: rec}) {
		t.Fatalf("inbox %+v, %v; want the kind's record, sent by the kind", got, err)
	}
	if fi, err := os.Stat(inbox); err != nil {
		t.Errorf("no inbox: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("inbox mode %v; want 0600", fi.Mode())
	}
	if _, live := project.InboxLive(cfg, "pj-lead"); !live {
		t.Error("the recorded inbox is not live")
	}

	if err := project.RefuseInbox(f.Cfg(), "lead", project.Inbox{Sends: true, Record: rec}, "agentapi send-message: exit 1: invalid CSRF token"); err != nil {
		t.Fatal(err)
	}
	if _, live := project.InboxLive(cfg, "pj-lead"); live {
		t.Fatal("precondition: a refused record is live")
	}
	CommandRecord(f.Root, f.Env(append([]string{"OBOR_ROLE=lead"}, agyVars("tok-1")...)...), io.Discard)
	if got, _ := project.ReadInbox(f.Cfg(), "lead"); got.Refused != "" {
		t.Errorf("a later command kept the refused mark: %+v", got)
	}
	if _, live := project.InboxLive(cfg, "pj-lead"); !live {
		t.Error("the refreshed inbox is not live")
	}
	CommandRecord(f.Root, f.Env(append([]string{"OBOR_ROLE=lead"}, agyVars("tok-2")...)...), io.Discard)
	if got, _ := project.ReadInbox(f.Cfg(), "lead"); !strings.Contains(got.Record, `"ANTIGRAVITY_CSRF_TOKEN":"tok-2"`) {
		t.Errorf("a new token not recorded: %+v", got)
	}

	CommandRecord(f.Root, f.Env("OBOR_ROLE=lead"), io.Discard)
	if got, _ := project.ReadInbox(f.Cfg(), "lead"); !strings.Contains(got.Record, "tok-2") {
		t.Errorf("a command without the variables changed the record: %+v", got)
	}
	f.Config()
	if err := os.Remove(inbox); err != nil {
		t.Fatal(err)
	}
	CommandRecord(f.Root, f.Env("OBOR_ROLE=lead", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock", "CLAUDE_CODE_MESSAGING_TOKEN=t"), io.Discard)
	if f.Exists(inbox) {
		t.Error("a Claude lead's command wrote a record")
	}
}

// An agy worker's obor command in a sandbox that refuses writes to the main checkout (agy's auto mode) records its
// inbox in its lane's gitignored state dir instead, mode 0600, saying nothing: that record is the one read (live). A
// later command outside the sandbox writes the main checkout's: the newer record is the one read. A record neither file
// takes is one line on the command's stderr.
func TestCommandRecordsASandboxedInbox(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	f.Config(testkit.Set("agents.models.gem", map[string]any{"kind": "agy", "id": "gemini-3-flash"}))
	f.runs("gem")
	refuse := func(root string) { // a file where the inbox dir would be: every write there fails, as the sandbox's
		t.Helper()
		if err := os.RemoveAll(filepath.Join(root, ".obor", "state", "inbox")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".obor", "state", "inbox"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	main, lane := project.InboxFile(f.Root, "L-01"), project.InboxFile(o1, "L-01")
	if got := project.InboxFiles(f.Cfg(), "L-01"); !slices.Equal(got, []string{main, lane}) {
		t.Fatalf("precondition: L-01's record files %q, want the main checkout's then the lane's", got)
	}
	refuse(f.Root)
	var stderr bytes.Buffer
	CommandRecord(o1, f.Env(agyVars("tok-1")...), &stderr)
	if stderr.Len() != 0 {
		t.Errorf("a record the lane took printed %q", stderr.String())
	}
	if fi, err := os.Stat(lane); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the lane's record: %v, %v; want mode 0600", fi, err)
	}
	if in, err := project.ReadInbox(f.Cfg(), "L-01"); err != nil || !in.Sends || !strings.Contains(in.Record, `"tok-1"`) {
		t.Errorf("the record read: %+v, %v; want the lane's, sent by the kind", in, err)
	}
	if _, live := project.InboxLive(f.Cfg(), "pj-l-01"); !live {
		t.Error("the lane's record is not live")
	}

	if err := os.Remove(filepath.Join(f.Root, ".obor", "state", "inbox")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lane, old, old); err != nil {
		t.Fatal(err)
	}
	CommandRecord(o1, f.Env(agyVars("tok-2")...), &stderr)
	if !f.Exists(main) {
		t.Fatal("precondition: a command outside the sandbox wrote no record in the main checkout")
	}
	if in, err := project.ReadInbox(f.Cfg(), "L-01"); err != nil || !strings.Contains(in.Record, `"tok-2"`) {
		t.Errorf("the record read: %+v, %v; want the main checkout's newer one", in, err)
	}

	refuse(f.Root)
	refuse(o1)
	CommandRecord(o1, f.Env(agyVars("tok-3")...), &stderr)
	want := "obor: inbox of L-01 not recorded (obor's lines to this session are typed until a command records it): " +
		"mkdir " + filepath.Dir(main) + ": not a directory; mkdir " + filepath.Dir(lane) + ": not a directory\n"
	if stderr.String() != want {
		t.Errorf("a record no file took: stderr %q, want %q", stderr.String(), want)
	}
}

// An agy subagent's conversation fires SessionStart with its parent's environment and workspace: under the owner's
// role-less session in the main checkout it derives no agent (recordable), so no session is recorded or reported; the
// lead's same start (the precondition: the event records) begins one.
func TestSessionRoleLessSubagentRecordsNone(t *testing.T) {
	t.Parallel()
	f, _ := sessionFx(t, true)
	start := func(env ...string) (Began, bool) {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": f.Root, "session_id": "sid-sub",
			"agent_kind": "agy"})
		return SessionStart(b, f.Env(append([]string{"OBOR_STATE_HOME=" + f.Base + "/state"}, env...)...), io.Discard)
	}
	if began, ok := start("OBOR_ROLE=lead"); !ok || began.Agent == "" {
		t.Fatalf("precondition: the lead's start began %+v, %v; want a session", began, ok)
	}
	if began, ok := start(); ok || began.Agent != "" {
		t.Errorf("a role-less session's subagent in the main checkout: began %+v, %v; want none", began, ok)
	}
}
