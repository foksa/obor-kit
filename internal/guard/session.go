package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/store"
)

// SessionStart is the SessionStart hook (its session's kind the event's agent_kind, a kind transport's own word, when
// the config knows it; else the kind the agent runs as, sessionKind): this session's obor context on out (role, lane, task, the protected files
// the task's spec unlocks, the role's hook summary, the role text to read, a lost-identity warning). No
// project → nothing; unknown config keys → a WARN line first; a config that does not load → only that line (the
// session's records are still written). Records the session id for obor restart (a kind that resumes) and its inbox
// (record). paneLabel is a herdr pane's label ("" when
// it cannot tell; cmd/obor: one bounded `herdr pane get`), asked only for the lost-identity warning. The session it
// returns is what cmd/obor reports to serve (ok false: no project, no agent name or no session id).
func SessionStart(input []byte, env project.Env, out io.Writer) (Began, bool) {
	var ev struct {
		Cwd            string `json:"cwd"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		AgentKind      string `json:"agent_kind"` // the kind whose transport ran the hook ("": the CLI's own hooks)
	}
	if json.Unmarshal(input, &ev) != nil {
		return Began{}, false
	}
	if ev.Cwd == "" {
		ev.Cwd, _ = os.Getwd()
	}
	repo, cfg, unknown, err := loadAt(ev.Cwd)
	if repo == nil || errors.Is(err, config.ErrNoConfig) {
		return Began{}, false
	}
	agent := project.Self(repo, cfg, env) // derived, never OBOR_AGENT
	kind := sessionKind(cfg, agent, ev.Cwd)
	if _, known := cfg.Kind(ev.AgentKind); known { // the CLI that runs the session says which it is
		kind = ev.AgentKind
	}
	if err != nil { // say so, guard nothing else on a default config (it derives no name: nothing recorded)
		fmt.Fprintln(out, "obor: "+unreadable(err))
		record(cfg, agent, project.InboxSlot(cfg, project.DetectRole(repo, cfg, env)), ev.SessionID, env)
		return Began{}, false
	}
	if len(unknown) > 0 {
		fmt.Fprintf(out, "obor: WARN: %s: unknown key(s) %s ignored: this obor binary is older than the config (or a key is misspelled) — run obor config validate\n",
			filepath.Join(repo.Root, ".obor"), strings.Join(unknown, ", "))
	}
	w, err := Whoami(repo, cfg, env)
	if err != nil { // the rest of the bookkeeping still runs, on the role alone
		fmt.Fprintf(out, "obor: could not read this lane or its spec: %v (restart obor serve, or ask the lead); protected files stay locked until it can.\n", err)
		w = &Who{Role: project.DetectRole(repo, cfg, env)}
	}

	lane, task := w.Role.Lane, "-"
	if lane == "" {
		lane = "-"
	}
	if w.State != nil {
		if t := w.State.Task; t != "" {
			task = t
		}
	}
	fmt.Fprintf(out, "obor: role %s, lane %s, task %s (%s)\n", w.Role.Role, lane, task, w.Role.Source)
	switch {
	case err != nil:
	case w.State == nil:
		fmt.Fprintln(out, "No lane row (not dispatched).")
	case len(w.Protected) > 0:
		// The stored spec (whoami): a change the lead sets applies at once.
		fmt.Fprintf(out, "Protected files this task may edit: %s (from %s's spec, rev %d in obor serve's store).\n",
			strings.Join(w.Protected, ", "), w.State.Task, w.Rev)
	}
	runs := loadRuns(len(cfg.DenyCommands()))
	forms := strings.Join(headlessForms(cfg.Kinds()), ", ")
	switch w.Role.Role {
	case "worker":
		fmt.Fprintln(out, "Hooks: git writes and push are denied, edits outside your lane (other lanes, the main checkout, other obor projects) are denied, protected files are denied unless your spec names them under Protected files this task may edit, headless agent runs ("+forms+") and the hc/herdr-app/rex commands that drive terminals are denied; "+runs+"; you cannot stop before you call the obor task_done tool with your report (Summary/Self-check/…; once per stop: asking works); prompts not tagged [obor … are answered with a wrong-tab question first.")
	case roles.Driver: // the owner's drive: no job pull, no report demand, no wrong-tab guard
		fmt.Fprintln(out, "Hooks: git push is denied, edits outside your lane (other lanes, the main checkout, other obor projects) are denied, protected files are denied, headless agent runs ("+forms+") and the hc/herdr-app/rex commands that drive terminals are denied; "+runs+". Commit your work on the lane's branch; the owner lands it with obor land.")
	case "lead":
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied; "+runs+".")
	case "integrator":
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern, headless agent runs ("+forms+") and the hc/herdr-app/rex commands that drive terminals are denied, protected files are denied; "+runs+".")
	default:
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern, headless agent runs ("+forms+") and the hc/herdr-app/rex commands that drive terminals are denied; protected files are denied for non-lead roles; "+runs+".")
	}
	if line := roleText(cfg, w.Role.Role, kind, project.KitRoot(env), repo.Top); line != "" {
		fmt.Fprintln(out, line)
	}
	if line := probeDirLine(cfg, w.Role.Role, agent); line != "" {
		fmt.Fprintln(out, line)
	}

	// An opencode lane's plugin runs this hook (its context transform), so the marker proves the plugin
	// loaded: obor status warns when it is missing (dispatch does not wait for one). It lives in the main checkout's
	// state dir, so it never dirties the lane worktree. No hook event carries the agent's version, so it
	// is the kind's VersionVar when the environment sets one, else "". A native-hooks session writes none.
	if k, ok := cfg.Kind(laneKind(cfg, w.Role.Lane)); w.Role.Lane != "" && ok && k.Caps().Hooks == agents.HooksPlugin {
		version := ""
		if v := k.Caps().VersionVar; v != "" {
			version = env.Get(v)
		}
		project.WritePluginMarker(repo.Root, w.Role.Lane, k.Name(), version)
	}
	record(cfg, agent, project.InboxSlot(cfg, w.Role), ev.SessionID, env)
	pane, _ := project.PaneOf(cfg, env) // none: a session outside a pane, recorded without one
	began := Began{Root: cfg.Root, Session: store.Session{Agent: agent, Kind: kind, ID: ev.SessionID,
		Transcript: ev.TranscriptPath, Pane: pane, Lane: w.Role.Lane, Source: "SessionStart hook"}}
	if w.State != nil {
		began.Task = w.State.Task
	}
	return began, began.ID != "" && recordable(began.Agent)
}

// Began is an agent session the SessionStart hook saw begin in the project at Root: the agent (project.Self:
// derived), its kind, the session id and its transcript file ("": the event names none), the pane, lane and
// task it began in, and its source. Started is the report's (cmd/obor stamps it).
type Began struct {
	Root string
	store.Session
}

// sessionKind is the kind agent runs as (project.RunningKind: a lane agent's is its lane's open dispatch's;
// else its role's configured type), else agents.Default.
func sessionKind(cfg *config.Config, agent, cwd string) string {
	if info, ok := cfg.AgentLookup(agent, cwd); ok {
		return project.RunningKind(cfg, info)
	}
	return agents.Default
}

// roleText is the line naming this role's text: the kit's templates/agents/<doc file>, read in place, plus the
// project's <paths.agents>/<doc file> when the worktree has one; a custom role without a kit text has the
// project's doc alone. The lead's text is its skill (skills/lead/SKILL.md): the slash command a kind with caps skills
// runs, else the file to read. "" when there is no text for the role (none …, a role cfg does not know). The
// integrator's line says its jobs carry the texts; the others say to read them.
func roleText(cfg *config.Config, role, kind, kit, top string) string {
	rc, known := cfg.Role(role)
	if !known {
		return ""
	}
	doc := cfg.RoleDoc(role)
	project := ""
	if regular(filepath.Join(top, doc)) {
		project = doc
	}
	if role == "lead" {
		if cfg.Kinds().Skills(kind) {
			return "obor: role text: run /obor:lead" + projectPart(project) + " — read it before acting"
		}
		if file := rc.Role.SkillFile(kit); file != "" && regular(file) {
			return "obor: role text: " + file + projectPart(project) + " — read it before acting"
		}
		if project != "" {
			return "obor: role text: " + project + " — read it before acting"
		}
		return ""
	}
	text := filepath.Join(kit, "templates", "agents", rc.Role.DocFile())
	tail := " — read it before acting"
	if role == "integrator" { // its review and resolve jobs carry the role texts
		tail = " — your jobs carry it (job_next), no read needed"
	}
	switch {
	case kit != "" && regular(text):
		return "obor: role text: " + text + projectPart(project) + tail
	case project != "":
		return "obor: role text: " + project + tail
	}
	return ""
}

// probeDirLine is the session line naming the dir role agent probes in (project.ProbeDir): the reviewer's and the
// deep dive's role texts send every probe there, and a static text cannot name a path that is per project and agent.
// "" for every other role.
func probeDirLine(cfg *config.Config, role, agent string) string {
	switch role {
	case "reviewer", "deep_dive":
	default:
		return ""
	}
	dir := project.ProbeDir(cfg, agent)
	if dir == "" {
		return ""
	}
	return "obor: probe dir: " + dir + " (probe only there; never set GOCACHE or CARGO_TARGET_DIR to a fresh dir)"
}

// projectPart is the role-text line's mention of the project's doc ("": none).
func projectPart(doc string) string {
	if doc == "" {
		return ""
	}
	return ", project: " + doc
}

// regular: p is a regular file.
func regular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// laneKind is the kind the configured lane named lane runs now (project.RunningKind: its open dispatch's model,
// else agents.Default); "" when it is not a lane.
func laneKind(cfg *config.Config, lane string) string {
	for _, l := range cfg.Lanes() {
		if l.Name == lane {
			return project.RunningKind(cfg, config.AgentInfo{Role: "worker", Lane: l.Name})
		}
	}
	return ""
}

// record writes this session's inbox under its slot in cfg's project (every agent, role-free; none for a session that
// derives no agent name or holds no slot; obor serve posts the lead's lines there, nothing is typed), as the bridge of
// the kind (of the project's kinds) whose environment this is builds it (Kind.InboxRecord); a session with none
// (messaging off, no inbox kind) removes a stale record. The session itself is recorded by serve alone (the session
// request), never in a file.
func record(cfg *config.Config, agent, slot, sessionID string, env project.Env) {
	if !recordable(agent) || slot == "" {
		return
	}
	kinds := cfg.Kinds()
	for _, name := range kinds.Names() {
		k, _ := kinds.Lookup(name)
		if in, ok := inboxOf(k, k.InboxRecord(env.Get)); ok {
			in.Session = sessionID
			_ = project.WriteInbox(cfg, slot, in)
			return
		}
	}
	project.RemoveInbox(cfg, slot)
}

// inboxOf is the inbox record of kind k's record b (nil: none): Claude's names its socket (and token) itself; a plugin
// kind's (its manifest's inbox_record variables) is kept whole as Record, with the socket k's deliver of it names, or
// Sends when that deliver says the kind sends the text itself (Delivery.Sent). ok false: no record, or neither.
func inboxOf(k agents.Kind, b []byte) (project.Inbox, bool) {
	var in project.Inbox
	if b == nil || json.Unmarshal(b, &in) != nil {
		return in, false
	}
	if in.Socket != "" {
		return in, true
	}
	d, ok := k.Deliver("", b)
	switch {
	case !ok || d.Typed:
	case d.Socket != "":
		return project.Inbox{Socket: d.Socket, Record: string(b)}, true
	case d.Sent:
		return project.Inbox{Sends: true, Record: string(b)}, true
	}
	return in, false
}

// CommandRecord records the inbox of the session an obor command runs in (cmd/obor: every command but hook, kind and
// mcp), from the command's environment: a session whose inbox exists only there (agy's agentapi address and token are
// in the environment of the commands the agent runs, never its hooks') gets its record from its first obor command (a
// lead's start checks, obor whoami and obor status) and every later one refreshes it (a new token after a restart; a
// refused send's mark cleared). Under the slot of the role the command runs as (project.DetectRole: none, nothing),
// as the kind that agent runs as (sessionKind) builds it from env (Kind.InboxRecord): a plugin kind's record (its
// manifest's inbox_record variables) only; a record that names its socket itself (Claude's) is its SessionStart
// hook's alone, with the session id serve checks it by. None in env: nothing is written or removed (the SessionStart
// hook owns removal). A record of the same inbox keeps its session id; an unchanged one is not written again. Written
// where the session can write (project.WriteInbox: the main checkout, else a sandboxed session's own worktree); a
// record no file took is one line on stderr (the agent sees it: its lines stay typed until a command records it).
// Otherwise silent: the command runs as it would without it.
func CommandRecord(cwd string, env project.Env, stderr io.Writer) {
	repo, cfg, _, err := loadAt(cwd)
	if repo == nil || err != nil {
		return
	}
	role := project.DetectRole(repo, cfg, env)
	agent, slot := project.SelfAgent(cfg, role), project.InboxSlot(cfg, role)
	if role.Role == "" || !recordable(agent) || slot == "" {
		return
	}
	k, ok := cfg.Kind(sessionKind(cfg, agent, cwd))
	if !ok {
		return
	}
	in, ok := inboxOf(k, k.InboxRecord(env.Get))
	if !ok || in.Record == "" {
		return
	}
	if old, err := project.ReadInbox(cfg, slot); err == nil {
		if old.Socket == in.Socket && old.Token == in.Token && old.Record == in.Record && old.Sends == in.Sends {
			in.Session = old.Session
		}
		if old == in {
			return
		}
	}
	if err := project.WriteInbox(cfg, slot, in); err != nil {
		fmt.Fprintf(stderr, "obor: inbox of %s not recorded (obor's lines to this session are typed until a command records it): %v\n", slot, err)
	}
}

// recordable: agent is a name records are kept under (not empty, no path).
func recordable(agent string) bool {
	return agent != "" && !strings.Contains(agent, "/") && !strings.HasPrefix(agent, ".")
}

// loadRuns is the hooks-line clause of rules.deny_commands: how many rules this project's config denies
// with.
func loadRuns(n int) string {
	if n == 1 {
		return "1 deny rule from rules.deny_commands"
	}
	return fmt.Sprintf("%d deny rules from rules.deny_commands", n)
}

// headlessForms are the headless runs the guard denies, one per kind of kinds (agents.Kind.Headless): its first program
// with its print flag, else its first subcommand, else its first long flag.
func headlessForms(kinds agents.Kinds) []string {
	var out []string
	for _, name := range kinds.Names() {
		k, _ := kinds.Lookup(name)
		switch h, prog := k.Headless(), agents.Programs(k)[0]; {
		case h.PrintFlag:
			out = append(out, prog+" -p")
		case len(h.Subcommands) > 0:
			out = append(out, prog+" "+h.Subcommands[0])
		case len(h.Flags) > 0:
			out = append(out, prog+" "+h.Flags[0])
		}
	}
	return out
}
