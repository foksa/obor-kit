package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/project"
)

// statusModel is `obor status`: obor serve's view of the project (its agents, open tasks and needs-you, each needs-you
// with the commands that answer it), the lanes (the store's rows and what git says of each worktree), serve up or
// down, the last full check, the pause and the lines serve has not typed yet (obor send, obor brief, a brief's finished
// line); rendered as text (writeStatus) or JSON (--json).
type statusModel struct {
	Serve     serveJSON           `json:"serve"`
	Lanes     []laneJSON          `json:"lanes"`
	Tasks     []app.TaskStatus    `json:"tasks"`  // serve's open tasks
	Agents    []app.AgentStatus   `json:"agents"` // the agents serve sees in the project
	NeedsYou  []needsYouJSON      `json:"needs_you"`
	FullCheck *fullCheckJSON      `json:"full_check"`
	Paused    *pauseJSON          `json:"paused"`
	Messages  []app.MessageStatus `json:"messages"` // the lines not typed yet
	facts     []app.Record        // serve's facts in force for the project (--facts)
}

// serveJSON: serve answered (Up, since when) or not (Error); the project's state in it.
type serveJSON struct {
	Up        bool   `json:"up"`
	Since     string `json:"since,omitempty"`
	Error     string `json:"error,omitempty"`     // why it is down
	Herdr     bool   `json:"herdr"`               // serve's herdr subscription is live
	Project   bool   `json:"project"`             // serve has this project loaded
	Merging   bool   `json:"merging"`             // its merge loop runs for it
	LogError  string `json:"log_error,omitempty"` // its event log's last append error
	sinceTime time.Time
}

// laneJSON is one configured lane: its worktree as git and the store's row say (state free, dirty-free, working,
// done, missing), serve's task on it and the agent serve sees there.
type laneJSON struct {
	Lane        string  `json:"lane"`
	Type        string  `json:"type"`
	Dir         string  `json:"dir"`
	Branch      *string `json:"branch"`
	Task        *string `json:"task"`
	State       string  `json:"state"`
	DoneAge     *int64  `json:"done_age"`
	Dirty       bool    `json:"dirty"`
	Warning     *string `json:"warning"`
	Op          string  `json:"op,omitempty"`
	TaskState   string  `json:"task_state,omitempty"` // serve's state of the task
	Attempt     int     `json:"attempt,omitempty"`
	Running     string  `json:"running,omitempty"` // the task's step running now (its gate, its publish …)
	Agent       string  `json:"agent,omitempty"`   // serve's agent in the lane, else the lane row's
	AgentStatus string  `json:"agent_status,omitempty"`
}

type needsYouJSON struct {
	Task   string `json:"task"`
	Lane   string `json:"lane,omitempty"`
	Reason string `json:"reason"`
	Answer string `json:"answer,omitempty"` // the commands that answer it (serve's ProjectStatus.Answers)
}

type fullCheckJSON struct {
	SHA     string `json:"sha"`
	Verdict string `json:"verdict"` // GREEN | RED
	At      int64  `json:"at"`
	Summary string `json:"summary"`
}

// pauseJSON is serve's pause in force (app.Pause): until (epoch seconds; null: until cleared), why, who paused.
type pauseJSON struct {
	Until  *int64 `json:"until"`
	Reason string `json:"reason"`
	By     string `json:"by,omitempty"`
}

// status is `obor status [--facts] [--json]`. Serve down is a line, never a non-zero exit: the lanes are read from
// the store alone. --facts adds the facts in force. --all is accepted and changes nothing for one release (T-522: a
// serve holds one project, this one); drop it once adopters run v0.14.
func status(args []string) int {
	asJSON, facts := false, false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--all":
		case "--facts":
			facts = true
		default:
			errf("usage: obor status [--facts] [--json]")
			return 2
		}
	}
	_, cfg, rc := load()
	if rc == 3 {
		errf("obor status: not an obor project")
		return 3
	}
	if rc != 0 {
		return rc
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ss, err := app.Ask(ctx, app.SocketPath(cfg.Root))
	st := buildStatus(checks.NewLaneTable(cfg), ss, err)
	if !asJSON {
		writeStatus(out, st, time.Now())
		if facts {
			writeFacts(out, "", st.facts)
		}
		return 0
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		errf("obor status: %v", err)
		return 1
	}
	return 0
}

// buildStatus joins serve's answer (ss, or err when it gave none) to the lane table of p.
func buildStatus(p *checks.LaneTable, ss app.Status, err error) statusModel {
	st := statusModel{Lanes: []laneJSON{}, Tasks: []app.TaskStatus{}, Agents: []app.AgentStatus{},
		NeedsYou: []needsYouJSON{}, Messages: []app.MessageStatus{}}
	var ps app.ProjectStatus
	if err != nil {
		st.Serve.Error = err.Error()
	} else {
		st.Serve.Up, st.Serve.Since, st.Serve.Herdr = true, ss.Since, ss.Connected
		st.Serve.sinceTime, _ = time.Parse(time.RFC3339, ss.Since)
		if x := ss.Project; x != nil && filepath.Clean(x.Root) == filepath.Clean(p.Root) {
			ps, st.Serve.Project = *x, true
		}
		st.Serve.Merging, st.Serve.LogError = ps.Merging, ps.LogError
		st.Tasks, st.Agents = append(st.Tasks, ps.Tasks...), append(st.Agents, ps.Agents...)
		st.Messages = append(st.Messages, ps.Messages...)
		st.facts = ps.Facts
		for _, f := range ps.Facts {
			if f.Step == app.StepNeedsYou {
				st.NeedsYou = append(st.NeedsYou, needsYouJSON{Task: f.Task, Lane: f.Fact().Lane,
					Reason: f.Fact().Reason, Answer: ps.Answers[f.Seq]})
			}
		}
	}
	for _, s := range p.States() {
		l := laneJSON{Lane: s.Lane, Type: s.Type, Dir: s.Dir, Branch: orNull(s.Branch), Task: orNull(s.Task),
			State: s.State, DoneAge: s.DoneAge, Dirty: s.Dirty, Warning: orNull(s.Warning), Op: s.Op,
			Agent: p.Rows()[s.Lane].Agent}
		if l.Warning == nil && s.State == "working" && pluginMissing(p, s) {
			w := "no-plugin"
			l.Warning = &w
		}
		for _, t := range ps.Tasks {
			if t.Lane == s.Lane && t.Task == s.Task {
				l.TaskState, l.Attempt, l.Running = t.State, t.Attempt, t.Running
			}
		}
		for _, a := range ps.Agents {
			if a.Lane == s.Lane {
				l.Agent, l.AgentStatus = a.Name, a.Status
			}
		}
		st.Lanes = append(st.Lanes, l)
	}
	st.FullCheck = readFullCheck(p.Root)
	if ps.Paused != nil {
		st.Paused = &pauseJSON{Reason: ps.Paused.Why, By: ps.Paused.Caller}
		if n := ps.Paused.Until; n != 0 {
			st.Paused.Until = &n
		}
	}
	return st
}

// readFullCheck is .obor/state/last-full-check ("<sha> <epoch> GREEN|RED <summary>", obor nightly / milestone-check);
// nil when there is none.
func readFullCheck(root string) *fullCheckJSON {
	b, err := os.ReadFile(filepath.Join(project.StateDir(root), "last-full-check"))
	if err != nil {
		return nil
	}
	f := strings.SplitN(project.FirstLine(string(b)), " ", 4)
	if len(f) < 3 {
		return nil
	}
	at, _ := strconv.ParseInt(f[1], 10, 64)
	fc := &fullCheckJSON{SHA: f[0], At: at, Verdict: f[2]}
	if len(f) == 4 {
		fc.Summary = f[3]
	}
	return fc
}

// writeStatus is the text of st at now: serve's line, one line per lane, serve's open tasks on no lane (not dropped),
// the agents on no lane, each needs-you with its answer, the lines not typed yet, the full check and the pause.
func writeStatus(w io.Writer, st statusModel, now time.Time) {
	switch s := st.Serve; {
	case !s.Up:
		fmt.Fprintf(w, "serve: down (%s)\n", s.Error)
	default:
		line := "serve: up"
		if !s.sinceTime.IsZero() {
			line += fmt.Sprintf(" since %s (%s)", s.sinceTime.Local().Format("Jan 2 15:04"),
				app.Age(now.Sub(s.sinceTime)))
		}
		switch {
		case !s.Project:
			line += "; this project is not loaded (obor serve's output says why)"
		case !s.Merging:
			line += "; merge loop off (no integrator role)"
		}
		if !s.Herdr {
			line += "; herdr: not connected"
		}
		fmt.Fprintln(w, line)
		if s.LogError != "" {
			fmt.Fprintf(w, "serve: event log broken: %s\n", s.LogError)
		}
	}
	shown := map[string]bool{}
	for _, l := range st.Lanes {
		ago := "-"
		if l.DoneAge != nil {
			ago = app.Age(time.Duration(*l.DoneAge) * time.Second)
		}
		line := fmt.Sprintf("%-6s  %-7s  %-14s  %-10s  %4s  (%s)", l.Lane, l.Type, orDash(l.Branch), l.State, ago,
			orDash(l.Task))
		if l.TaskState != "" {
			line += fmt.Sprintf("  serve: %s (attempt %d)%s", l.TaskState, l.Attempt, running(l.Running))
			shown[*l.Task] = true
		}
		if l.Dirty && l.State != "dirty-free" {
			line += "  dirty"
		}
		if l.Warning != nil {
			line += "  warning: " + *l.Warning
		}
		if l.Agent != "" {
			line += "  agent: " + l.Agent
			if l.AgentStatus != "" {
				line += " (" + l.AgentStatus + ")"
			}
		}
		fmt.Fprintln(w, line)
	}
	for _, t := range st.Tasks {
		if !shown[t.Task] && t.State != "dropped" { // dropped stays in serve's list for good; --json has it
			if t.State == "waiting" { // no lane, no attempt yet
				fmt.Fprintf(w, "task %s: %s\n", t.Task, waitingOn(t))
				continue
			}
			fmt.Fprintf(w, "task %s (%s): %s (attempt %d)%s\n", t.Task, app.Dash(t.Lane), t.State, t.Attempt, running(t.Running))
		}
	}
	for _, a := range st.Agents {
		if a.Lane == "" {
			fmt.Fprintf(w, "agent %s: %s %s\n", app.Dash(a.Name), a.Status, app.Age(time.Duration(a.Age)*time.Second))
		}
	}
	for _, n := range st.NeedsYou {
		line := "needs-you " + n.Task
		if n.Lane != "" {
			line += " (" + n.Lane + ")"
		}
		line += ": " + n.Reason
		if n.Answer != "" {
			line += " → " + n.Answer
		}
		fmt.Fprintln(w, line)
	}
	for _, m := range st.Messages {
		fmt.Fprintf(w, "undelivered to %s (from %s, %s): %s\n", m.To, app.Dash(m.From), app.Age(time.Duration(m.Age)*time.Second), m.Line)
	}
	if fc := st.FullCheck; fc == nil {
		fmt.Fprintln(w, "full check: none yet")
	} else {
		fmt.Fprintf(w, "full check: %s %s, %s ago (%s)\n", fc.Verdict, fc.SHA[:min(7, len(fc.SHA))],
			app.Age(max(now.Sub(time.Unix(fc.At, 0)), 0)), fc.Summary)
	}
	if p := st.Paused; p != nil {
		until := "cleared"
		if p.Until != nil {
			until = time.Unix(*p.Until, 0).Format("15:04")
		}
		fmt.Fprintf(w, "paused: %s, until %s (obor pause --clear ends it; no dispatch meanwhile)\n", p.Reason, until)
	}
}

// pluginMissing (T-307): a lane whose kind runs obor's hooks through obor's plugin (agents.HooksPlugin: opencode) has
// no plugin marker newer than the dispatch that started its work (obor status warns: the lane runs with no obor
// guard). None at all, or one older than the lane row's dispatched_at (a leftover from before this dispatch): missing.
// No row: a marker at all is fresh, nothing to compare it against.
func pluginMissing(p *checks.LaneTable, s checks.LaneStatus) bool {
	lt, ok := p.Cfg.LaneType(s.Type)
	if k, known := agents.Lookup(lt.Kind); !ok || !known || k.Caps().Hooks != agents.HooksPlugin {
		return false
	}
	m, ok := project.PluginMarkerAt(p.Root, s.Lane)
	if !ok {
		return true
	}
	row, ok := p.Rows()[s.Lane]
	return ok && m.At < row.DispatchedAt
}

// orDash is *s, "-" for nil.
func orDash(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// orNull is s, or JSON null when it is "".
func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// why is a waiting task's last refused dispatch as both status renderers append it ("": none).
func why(t app.TaskStatus) string {
	if t.Why == "" {
		return ""
	}
	return ": " + t.Why
}

// running is a task's running step as its status line ends ("": none).
func running(step string) string {
	if step == "" {
		return ""
	}
	return ", running " + step
}

// writeFacts is one line per fact in force: its seq, time, task, step{reason} and lane.
func writeFacts(w io.Writer, indent string, recs []app.Record) {
	for _, f := range recs {
		fmt.Fprintf(w, "%sfact #%d %s %s %s (%s)\n", indent, f.Seq, f.TS, f.Task, f, f.Fact().Lane)
	}
}

// waitingOn is a waiting task's status line after "task <T-xx>: ": "waiting (after T-1, T-2)" and ": <last refusal>", or,
// for a task that waits only for a lane of its level (T-538), "waiting for a <level> lane (<types>)".
func waitingOn(t app.TaskStatus) string {
	if len(t.After) > 0 {
		return t.State + " (after " + strings.Join(t.After, ", ") + ")" + why(t)
	}
	if t.Level == "" {
		return t.State
	}
	return fmt.Sprintf("waiting for a %s lane (%s)", t.Level, strings.Join(t.Types, ", "))
}
