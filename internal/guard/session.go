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
)

// SessionStart is the SessionStart hook: this session's obor context on out (role, lane, task, the protected files
// the task's spec unlocks, the role's hook summary, the role text to read, a lost-identity warning). No
// project → nothing; unknown config keys → a WARN line first; a config that does not load → only that line (the
// session's records are still written). Records the session id for obor restart (a kind that resumes) and its inbox
// (record). paneLabel is a herdr pane's label ("" when
// it cannot tell; cmd/obor: one bounded `herdr pane get`), asked only for the lost-identity warning. The session it
// returns is what cmd/obor reports to serve (T-577; ok false: no project, no agent name or no session id).
func SessionStart(input []byte, env project.Env, out io.Writer, paneLabel func(pane string) string) (Began, bool) {
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
	agent := env.Get("OBOR_AGENT")
	kind := sessionKind(cfg, agent, ev.Cwd)
	if err != nil { // DD-v0.13 A1: say so, keep the session's records, guard nothing else on a default config
		fmt.Fprintln(out, "obor: "+unreadable(err))
		record(agent, ev.SessionID, kind, env)
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
	forms := strings.Join(headlessForms(), ", ")
	switch w.Role.Role {
	case "worker":
		fmt.Fprintln(out, "Hooks: git writes and push are denied, edits outside your lane (other lanes, the main checkout, other obor projects) are denied, protected files are denied unless your spec names them under Protected files this task may edit, headless agent runs ("+forms+") are denied; "+loadRuns+"; you cannot stop before you call the obor task_done tool with your report (Summary/Self-check/…; once per stop: asking works); prompts not tagged [obor … are answered with a wrong-tab question first.")
	case "lead":
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied; "+loadRuns+".")
	case "integrator":
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied, protected files are denied; "+loadRuns+".")
	default:
		fmt.Fprintln(out, "Hooks: git push, pkill/killall by pattern and headless agent runs ("+forms+") are denied; protected files are denied for non-lead roles; "+loadRuns+".")
	}
	if line := roleText(w.Role.Role, project.KitRoot(env), repo.Top, cfg.Data.Paths["agents"]); line != "" {
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
	record(agent, ev.SessionID, kind, env)
	began := Began{Root: cfg.Root, Agent: sessionAgent(cfg, w.Role, env), Kind: kind, ID: ev.SessionID,
		Transcript: ev.TranscriptPath, Pane: env.Get("HERDR_PANE_ID"), Lane: w.Role.Lane}
	if w.State != nil {
		began.Task = w.State.Task
	}
	// Lost identity (T-126, R-28 §2): this pane is labelled obor agent X but OBOR_AGENT is unset.
	if pane := env.Get("HERDR_PANE_ID"); agent == "" && pane != "" {
		if label := paneLabel(pane); label != "" {
			if _, known := project.AgentLookup(cfg, label, ""); known {
				fmt.Fprintf(out, "obor: WARNING: this pane is labelled obor agent %s but OBOR_AGENT is unset (herdr restore?) — run obor restart %s from another pane\n", label, label)
			}
		}
	}
	return began, began.ID != "" && recordable(began.Agent)
}

// Began is an agent session the SessionStart hook saw begin (T-577): the project, the agent (OBOR_AGENT, else its
// lane's: T-581), its kind, the session id and its transcript file ("": the event names none), and the pane, lane and
// task it began in.
type Began struct {
	Root, Agent, Kind, ID, Transcript string
	Pane, Lane, Task                  string
}

// sessionKind is the kind agent runs as by cfg (its lane's, role's or agents entry's type), else agents.Default.
func sessionKind(cfg *config.Config, agent, cwd string) string {
	if info, ok := project.AgentLookup(cfg, agent, cwd); ok {
		if lt, ok := cfg.LaneType(info.Type); ok && lt.Kind != "" {
			return lt.Kind
		}
	}
	return agents.Default
}

// sessionAgent is the agent a hook event is about: OBOR_AGENT when the session's environment names one, else, in a
// lane, the agent its lane state names (a kind whose hooks run in one shared process carries no env identity: T-308,
// T-581); "" outside a lane.
func sessionAgent(cfg *config.Config, role project.Role, env project.Env) string {
	if role.Lane == "" {
		return env.Get("OBOR_AGENT")
	}
	return project.SelfAgent(cfg, role, env)
}

// roleText is the line naming this role's text (T-204): the kit's templates/agents/<role>.md, read in place, plus the
// project's <paths.agents>/<role>.md when the worktree has one. The lead's text is the obor:lead skill. "" when the
// kit has no text for the role (none, triage …).
func roleText(role, kit, top, agents string) string {
	file := strings.ReplaceAll(role, "_", "-") + ".md"
	project := ""
	if fi, err := os.Stat(filepath.Join(top, agents, file)); err == nil && fi.Mode().IsRegular() {
		project = ", project: " + filepath.Join(agents, file)
	}
	if role == "lead" {
		return "obor: role text: run /obor:lead" + project + " — read it before acting"
	}
	text := filepath.Join(kit, "templates", "agents", file)
	if role == "none" || kit == "" {
		return ""
	}
	if fi, err := os.Stat(text); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return "obor: role text: " + text + project + " — read it before acting"
}

// laneKind is the kind of the configured lane named lane ("" when it is not one).
func laneKind(cfg *config.Config, lane string) string {
	for _, l := range cfg.Lanes() {
		if l.Name == lane {
			lt, _ := cfg.LaneType(l.Type)
			return lt.Kind
		}
	}
	return ""
}

// record writes this session's records under agent's name (every agent, role-free; none without a usable name): its
// session id (T-113: obor restart resumes it after a kill) for a kind that resumes (a kind that resumes nothing, such
// as OpenCode, keeps no .sid: T-581) and its inbox (T-485: obor serve posts the lead's lines there, nothing is typed),
// as the bridge of the kind whose environment this is builds it (Kind.InboxRecord); a session with none (messaging
// off, no inbox kind) removes a stale record.
func record(agent, sessionID, kind string, env project.Env) {
	if !recordable(agent) {
		return
	}
	if agents.Resumes(kind) {
		if sessionID != "" {
			f := project.SidFile(agent, env)
			if os.MkdirAll(filepath.Dir(f), 0o755) == nil {
				os.WriteFile(f, []byte(sessionID+"\n"), 0o644)
			}
		}
	} else {
		_ = os.Remove(project.SidFile(agent, env)) // T-581: no resume, no .sid (a stale one goes too)
	}
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		var in project.Inbox
		if b := k.InboxRecord(env.Get); b != nil && json.Unmarshal(b, &in) == nil && in.Socket != "" {
			in.Session = sessionID
			_ = project.WriteInbox(agent, in, env)
			return
		}
	}
	_ = os.Remove(project.InboxFile(agent, env))
}

// recordable: agent is a name records are kept under (not empty, no path).
func recordable(agent string) bool {
	return agent != "" && !strings.Contains(agent, "/") && !strings.HasPrefix(agent, ".")
}

// loadRuns is the hooks-line clause of the load-run guards (T-543, every role).
const loadRuns = "Go test load runs (`-count`>1, `-race`, two test commands at once) are denied"

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
