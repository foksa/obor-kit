package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// sessionFx: lane L-01 (task/T-05) whose lane row names T-05 on base main, whose stored spec unlocks one protected
// file; armed = false: no lane row.
func sessionFx(t *testing.T, armed bool) (f hfx, o1 string) {
	t.Helper()
	f = newHfx(t)
	o1 = f.Lane("L-01", "task/T-05")
	f.Config()
	if armed {
		f.Spec("T-05", "# T-05\n\n## Protected files this task may edit\n`docs/design.md`\n") // the store's (T-440)
		f.State("L-01", `{"lane":"L-01","task":"T-05","branch":"task/T-05","base":"main"}`)
	}
	return f, o1
}

// runs makes lane L-01's open dispatch (task T-05) run model: its kind is the lane's (T-722, T-765).
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

// bash plugin case_session_worker_armed
func TestSessionWorkerArmed(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	out := f.session(t, o1, "")
	contains(t, out, "armed worker", "obor: role worker, lane L-01, task T-05 (cwd)",
		"Protected files this task may edit: docs/design.md (from T-05's spec, rev 1 in obor serve's store).",
		"edits outside your lane (other lanes, the main checkout, other obor projects) are denied", "task_done", "[obor",
		"Go test load runs (`-count`>1, `-race`, two test commands at once) are denied")
	lacks(t, out, "armed worker", "No lane row", "you may edit only", "allowed files")
	f.State("L-01", `{"lane":"L-01","task":"T-09","branch":"task/T-09","base":"main"}`)
	out = f.session(t, o1, "")
	contains(t, out, "no stored spec", "obor: role worker, lane L-01, task T-09 (cwd)", "edits outside your lane")
	lacks(t, out, "no stored spec", "Protected files this task may edit:", "every edit in the lane is denied")
}

// bash plugin case_session_worker_unarmed
func TestSessionWorkerUnarmed(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, false)
	contains(t, f.session(t, o1, ""), "no lane row", "role worker, lane L-01", "No lane row (not dispatched).")
}

// bash plugin case_session_lead_and_integrator; T-465: no Stop hook line (they stop freely), no heartbeat.
func TestSessionLeadAndIntegratorHookLines(t *testing.T) {
	t.Parallel()
	f, _ := sessionFx(t, true)
	for _, role := range []string{"lead", "integrator"} {
		out := f.session(t, f.Root, "", "OBOR_ROLE="+role)
		contains(t, out, role, "obor: role "+role, "Hooks: git push, pkill/killall by pattern and headless agent runs (claude -p, codex exec, opencode run, x-agent -p) are denied",
			"Go test load runs (`-count`>1, `-race`, two test commands at once) are denied")
		lacks(t, out, role, "[obor", "cannot stop", "Stop hook", "heartbeat", "obor ack")
	}
}

// T-204: worker, integrator and reviewer get the line naming the kit's role text (absolute path) and, when the
// worktree has one, the project's doc under paths.agents; the lead gets /obor:lead; a role without a kit text none.
func TestSessionRoleTextLine(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	kit := f.Base + "/kit"
	for _, r := range []string{"worker", "integrator", "reviewer"} {
		f.Write(kit+"/templates/agents/"+r+".md", "# Role: "+r+"\n")
	}
	env := func(kv ...string) []string { return append([]string{"OBOR_PLUGIN_ROOT=" + kit}, kv...) }
	want := func(role string) string { return "obor: role text: " + kit + "/templates/agents/" + role + ".md" }

	out := f.session(t, o1, "", env()...)
	contains(t, out, "worker, no project doc", "obor: role worker", want("worker")+" — read it before acting")
	lacks(t, out, "worker, no project doc", ", project:")
	if err := os.MkdirAll(o1+"/docs/agents", 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(o1+"/docs/agents/worker.md", []byte("# worker here\n"), 0o644)
	contains(t, f.session(t, o1, "", env()...), "worker with a project doc",
		want("worker")+", project: docs/agents/worker.md — read it before acting")

	contains(t, f.session(t, f.Root, "", env("OBOR_ROLE=integrator")...), "integrator", "obor: role integrator",
		want("integrator")+" — read it before acting")
	f.Write("doc/roles/reviewer.md", "# reviewer here\n")
	f.Config(testkit.Set("paths.agents", "doc/roles"))
	contains(t, f.session(t, f.Root, "", env("OBOR_ROLE=reviewer")...), "reviewer, paths.agents", "obor: role reviewer",
		want("reviewer")+", project: doc/roles/reviewer.md — read it before acting")

	out = f.session(t, f.Root, "", env("OBOR_ROLE=lead")...)
	contains(t, out, "lead", "obor: role lead", "obor: role text: run /obor:lead — read it before acting")
	lacks(t, out, "lead", "templates/agents/lead.md")
	lacks(t, f.session(t, f.Root, "", env("OBOR_ROLE=deep_dive")...), "a role without a kit text", "obor: role text")
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

// T-599: the hook writes no session-id file: serve's store is the only session record (the session request); with
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

// T-581: an OpenCode lane's session-start (its plugin's shared process carries no OBOR_AGENT) records the session
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
	// T-786: every role's session begins as its derived name, a stale OBOR_AGENT ignored.
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

// bash plugin case_session_no_identity_fallback (T-159; its pre-tool-use part: guard TestNoIdentityFromTheSessionID)
func TestSessionNoIdentityFromRecords(t *testing.T) {
	f, o1 := sessionFx(t, true)
	_, sock := f.serve(t)
	home := f.Base + "/state-home"
	f.Write(home+"/agents/pj-lead.pane", "w9:p1\n")
	f.label("w9:p1", "pj-lead")
	envFile := f.Base + "/claude-env"
	out := f.session(t, f.Root, "sid-lead", "HERDR_PANE_ID=w9:p1", sock, "CLAUDE_ENV_FILE="+envFile)
	contains(t, out, "a recorded sid gives no identity", "obor: role none")
	if n := strings.Count(out, "pj-lead"); n != 0 { // T-786: no lost-identity line either
		t.Errorf("the pane's label is named: %d in\n%s", n, out)
	}
	if f.Exists(envFile) || f.Exists(home+"/sessions") {
		t.Error("something exported or recorded")
	}
	os.Remove(home + "/agents/pj-lead.pane")
	contains(t, f.session(t, o1, "sid-o1-new", "OBOR_AGENT=pj-l-01", "HERDR_PANE_ID=w9:p2", sock), "hook ran", "obor: role worker")
	entries, _ := os.ReadDir(home + "/agents")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pane") {
			t.Errorf("a .pane file: %s", e.Name())
		}
	}
}

// T-310: the session-start hook of an opencode lane writes the plugin marker into the main checkout's state dir,
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
	if st := f.Git(o1, "status", "--porcelain"); st != "" {
		t.Errorf("session-start dirtied the lane worktree:\n%s", st)
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

// T-739 (RV-88 #2), T-765: the session's kind and the plugin marker follow the model a lane's open dispatch runs: an
// OpenCode dispatch is kind opencode with a marker, a Claude dispatch kind claude without one; with no open dispatch
// the default kind.
func TestSessionKindIsTheDispatchs(t *testing.T) {
	t.Parallel()
	start := func(f hfx, cwd string) Began {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": cwd, "session_id": "sid-1"})
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
	}{
		{"OpenCode dispatch", "deepseek", true, "opencode"},
		{"OpenCode dispatch with an effort", "deepseek:high", true, "opencode"},
		{"a project's OpenCode model", "oc", true, "opencode"},
		{"Claude dispatch", "opus", true, "claude"},
		{"legacy dispatch (no model)", "", true, "claude"},
		{"no open dispatch", "", false, "claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, o1 := sessionFx(t, false)
			f.Config(testkit.Set("models.oc", `{"kind":"opencode","model":"go/x"}`))
			if c.armed {
				f.State("L-01", `{"task":"T-05","branch":"task/T-05","base":"main","type":"`+c.rowType+`"}`)
			}
			if f.Exists(marker) {
				t.Fatal("precondition: a marker before the session")
			}
			if began := start(f, o1); began.Kind != c.kind || began.Lane != "L-01" {
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
	plain := f.session(t, o1, "sid-o1", "OBOR_AGENT=pj-l-01", "HERDR_PANE_ID=w9:p1", sock)
	contains(t, plain, "hook ran", "obor: role worker, lane L-01, task T-05 (cwd)")
	home := f.Base + "/state-home"
	f.Write(home+"/agents/pj-lead.pane", "w9:p1\n")
	before := panes(srv, "w9:p1")
	if out := f.session(t, o1, "sid-o1", "OBOR_AGENT=pj-l-01", "HERDR_PANE_ID=w9:p1", sock); out != plain {
		t.Errorf("env set: output changed by another agent's records:\n%s\nwas\n%s", out, plain)
	}
	if f.Exists(home+"/sessions") || panes(srv, "w9:p1") != before {
		t.Errorf("env set: a session record or a herdr pane get: %d before, %d after", before, panes(srv, "w9:p1"))
	}
}

// T-786 (was R-28 §2's lost-identity warning): a name is derived, so a labelled pane without OBOR_AGENT is no lost
// identity: no warning, and the hook asks herdr nothing.
func TestSessionLabelledPaneNoLostIdentity(t *testing.T) {
	f, o1 := sessionFx(t, true)
	srv, sock := f.serve(t)
	for _, c := range []struct{ label, cwd, role string }{{"pj-l-01", o1, ""}, {"pj-lead", f.Root, "OBOR_ROLE=lead"}} {
		f.label("w9:p1", c.label)
		before := len(srv.Requests())
		out := f.session(t, c.cwd, "sid-zzz", "HERDR_PANE_ID=w9:p1", c.role, sock)
		contains(t, out, "precondition: the hook ran", "obor: role ")
		lacks(t, out, c.label+": no lost-identity line", "WARNING", "OBOR_AGENT")
		if n := len(srv.Requests()) - before; n != 0 {
			t.Errorf("%s: %d herdr requests, want none", c.label, n)
		}
	}
}

// RV-48 #3: a database read error is said, and the other bookkeeping (the session id) still runs.
func TestSessionUnreadableDatabase(t *testing.T) {
	f, o1 := sessionFx(t, true)
	if out := f.session(t, o1, "sid-ok"); !strings.Contains(out, "Protected files this task may edit:") {
		t.Fatalf("precondition: a readable database shows the spec's unlocks:\n%s", out)
	}
	f.Write(".obor/state/obor.db", "not a database\n")
	out := f.session(t, o1, "sid-bad", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock")
	contains(t, out, "unreadable database", "could not read this lane or its spec", "obor: role worker, lane L-01")
	lacks(t, out, "unreadable database", "No lane row", "Protected files this task may edit:")
	if in, err := project.ReadInbox(f.Root, "L-01"); err != nil || in.Session != "sid-bad" {
		t.Errorf("inbox = %+v, %v; want session sid-bad (the bookkeeping still runs)", in, err)
	}
}

// T-485, T-786, DD-consolidation I4: a session with CLAUDE_CODE_MESSAGING_SOCKET records the inbox of its slot in the
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
		got, err := project.ReadInbox(f.Root, c.slot)
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
