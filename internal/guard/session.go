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

// SessionStart is the SessionStart hook: this session's obor context on out (role, lane, task, the protected files
// the task's spec unlocks, the role's hook summary, the role text to read, a lost-identity warning). No
// project → nothing; unknown config keys → a WARN line first; a config that does not load → only that line (the
// session's records are still written). Records the session id for obor restart (a kind that resumes) and its inbox
// (record). paneLabel is a herdr pane's label ("" when
// it cannot tell; cmd/obor: one bounded `herdr pane get`), asked only for the lost-identity warning. The session it
// returns is what cmd/obor reports to serve (T-577; ok false: no project, no agent name or no session id).
func SessionStart(input []byte, env project.Env, out io.Writer) (Began, bool) {
	var ev struct {
		Cwd            string `json:"cwd"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
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
	agent := project.Self(repo, cfg, env) // derived, never OBOR_AGENT (T-786)
	kind := sessionKind(cfg, agent, ev.Cwd)
	if err != nil { // DD-v0.13 A1: say so, guard nothing else on a default config (it derives no name: nothing recorded)
		fmt.Fprintln(out, "obor: "+unreadable(err))
		record(repo.Root, agent, project.InboxSlot(cfg, project.DetectRole(repo, cfg, env)), ev.SessionID, env)
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
		// The stored spec (whoami, T-440): a change the lead sets applies at once.
		fmt.Fprintf(out, "Protected files this task may edit: %s (from %s's spec, rev %d in obor serve's store).\n",
			strings.Join(w.Protected, ", "), w.State.Task, w.Rev)
	}
	runs := loadRuns(len(cfg.DenyCommands()))
	forms := strings.Join(headlessForms(), ", ")
	switch w.Role.Role {
	case "worker":
		fmt.Fprintln(out, "Hooks: git writes and push are denied, edits outside your lane (other lanes, the main checkout, other obor projects) are denied, protected files are denied unless your spec names them under Protected files this task may edit, headless agent runs ("+forms+") are denied; "+runs+"; you cannot stop before you call the obor task_done tool with your report (Summary/Self-check/…; once per stop: asking works); prompts not tagged [obor … are answered with a wrong-tab question first.")
	case roles.Driver: // the owner's drive (T-751): no job pull, no report demand, no wrong-tab guard
		fmt.Fprintln(out, "Hooks: git push is denied, edits outside your lane (other lanes, the main checkout, other obor projects) are denied, protected files are denied, headless agent runs ("+forms+") are denied; "+runs+". Commit your work on the lane's branch; the owner lands it with obor land.")
	case "lead":
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied; "+runs+".")
	case "integrator":
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied, protected files are denied; "+runs+".")
	default:
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied; protected files are denied for non-lead roles; "+runs+".")
	}
	if line := roleText(cfg, w.Role.Role, project.KitRoot(env), repo.Top); line != "" {
		fmt.Fprintln(out, line)
	}

	// T-307/T-310: an opencode lane's plugin runs this hook (its context transform), so the marker proves the plugin
	// loaded: obor status warns when it is missing (dispatch does not wait for one). It lives in the main checkout's
	// state dir, so it never dirties the lane worktree (RV-34 #4). No hook event carries the agent's version, so it
	// is the kind's VersionVar when the environment sets one, else "". A native-hooks session writes none.
	if k, ok := agents.Lookup(laneKind(cfg, w.Role.Lane)); w.Role.Lane != "" && ok && k.Caps().Hooks == agents.HooksPlugin {
		version := ""
		if v := k.Caps().VersionVar; v != "" {
			version = env.Get(v)
		}
		project.WritePluginMarker(repo.Root, w.Role.Lane, k.Name(), version)
	}
	record(repo.Root, agent, project.InboxSlot(cfg, w.Role), ev.SessionID, env)
	began := Began{Root: cfg.Root, Session: store.Session{Agent: agent, Kind: kind, ID: ev.SessionID,
		Transcript: ev.TranscriptPath, Pane: env.Get("HERDR_PANE_ID"), Lane: w.Role.Lane, Source: "SessionStart hook"}}
	if w.State != nil {
		began.Task = w.State.Task
	}
	return began, began.ID != "" && recordable(began.Agent)
}

// Began is an agent session the SessionStart hook saw begin (T-577) in the project at Root: the agent (project.Self:
// derived, T-786), its kind, the session id and its transcript file ("": the event names none), the pane, lane and
// task it began in, and its source. Started is the report's (cmd/obor stamps it).
type Began struct {
	Root string
	store.Session
}

// sessionKind is the kind agent runs as (project.RunningKind: a lane agent's is its lane's open dispatch's, T-739;
// else its role's or agents entry's configured type), else agents.Default.
func sessionKind(cfg *config.Config, agent, cwd string) string {
	if info, ok := cfg.AgentLookup(agent, cwd); ok {
		return project.RunningKind(cfg, info)
	}
	return agents.Default
}

// roleText is the line naming this role's text (T-204): the kit's templates/agents/<doc file>, read in place, plus the
// project's <paths.agents>/<doc file> when the worktree has one; a custom role (T-801) without a kit text has the
// project's doc alone. The lead's text is the obor:lead skill. "" when there is no text for the role (none …, a role
// cfg does not know). The integrator's line says its jobs carry the texts (T-941); the others say to read them.
func roleText(cfg *config.Config, role, kit, top string) string {
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
		return "obor: role text: run /obor:lead" + projectPart(project) + " — read it before acting"
	}
	text := filepath.Join(kit, "templates", "agents", rc.Role.DocFile())
	tail := " — read it before acting"
	if role == "integrator" { // T-941: its review and resolve jobs carry the role texts
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
// T-739; else agents.Default); "" when it is not a lane.
func laneKind(cfg *config.Config, lane string) string {
	for _, l := range cfg.Lanes() {
		if l.Name == lane {
			return project.RunningKind(cfg, config.AgentInfo{Role: "worker", Lane: l.Name})
		}
	}
	return ""
}

// record writes this session's inbox under its slot in the project at root (DD-consolidation I4: every agent,
// role-free; none for a session that derives no agent name or holds no slot; T-485: obor serve posts the lead's lines
// there, nothing is typed), as the bridge of the kind whose environment this is builds it (Kind.InboxRecord); a
// session with none (messaging off, no inbox kind) removes a stale record. The session itself is recorded by serve
// alone (the session request), never in a file.
func record(root, agent, slot, sessionID string, env project.Env) {
	if !recordable(agent) || slot == "" {
		return
	}
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		var in project.Inbox
		if b := k.InboxRecord(env.Get); b != nil && json.Unmarshal(b, &in) == nil && in.Socket != "" {
			in.Session = sessionID
			_ = project.WriteInbox(root, slot, in)
			return
		}
	}
	_ = os.Remove(project.InboxFile(root, slot))
}

// recordable: agent is a name records are kept under (not empty, no path).
func recordable(agent string) bool {
	return agent != "" && !strings.Contains(agent, "/") && !strings.HasPrefix(agent, ".")
}

// loadRuns is the hooks-line clause of guards.deny_commands (T-905): how many rules this project's config denies
// with (the count T-543's fixed sentence named by its rule forms).
func loadRuns(n int) string {
	if n == 1 {
		return "1 deny rule from guards.deny_commands"
	}
	return fmt.Sprintf("%d deny rules from guards.deny_commands", n)
}

// headlessForms are the headless runs the guard denies, one per kind (agents.Kind.Headless): its first program with its
// first subcommand, else its print flag.
func headlessForms() []string {
	var out []string
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		switch h, prog := k.Headless(), agents.Programs(k)[0]; {
		case len(h.Subcommands) > 0:
			out = append(out, prog+" "+h.Subcommands[0])
		case h.PrintFlag:
			out = append(out, prog+" -p")
		}
	}
	return out
}
