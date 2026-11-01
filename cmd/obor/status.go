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
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
)

// statusModel is `obor status`: obor serve's view of the project (its agents, open tasks and needs-you, each needs-you
// with the commands that answer it), what serve does without an agent now (the running line, T-657), the lanes (the
// store's rows and what git says of each worktree), serve up or down, the last full check, the pause and the lines serve
// has not typed yet (obor send, obor brief, a brief's finished line); rendered as text (writeStatus, writeStatusShort)
// or JSON (--json).
type statusModel struct {
	Serve     serveJSON           `json:"serve"`
	Running   []runningJSON       `json:"running"`
	Lanes     []laneJSON          `json:"lanes"`
	Tasks     []app.TaskStatus    `json:"tasks"`  // serve's open tasks
	Agents    []app.AgentStatus   `json:"agents"` // the agents serve sees in the project
	NeedsYou  []needsYouJSON      `json:"needs_you"`
	FullCheck *fullCheckJSON      `json:"full_check"`
	Paused    *pauseJSON          `json:"paused"`
	Messages  []app.MessageStatus `json:"messages"`          // the lines not typed yet
	Review    string              `json:"review,omitempty"`  // the open review request, or "review due: …" (T-638)
	Watches   []app.WatchStatus   `json:"watches,omitempty"` // the lead's open watches (obor monitor, T-658)
	facts     []app.Record        // serve's facts in force for the project (--facts)
}

// runningJSON is one step that runs without an agent (app.RunningStep, T-657) with the configured command it runs ("":
// a step of git alone, such as publish); the text renderers count its elapsed time from Since.
type runningJSON struct {
	Step    string `json:"step"` // gate, publish, after_merge, nightly or restart
	Task    string `json:"task,omitempty"`
	Command string `json:"command,omitempty"`
	Since   int64  `json:"since"`
}

// serveJSON: serve answered (Up, since when) or not (Error); the project's state in it.
type serveJSON struct {
	Up       bool   `json:"up"`
	Since    string `json:"since,omitempty"`
	Error    string `json:"error,omitempty"`     // why it is down
	Herdr    bool   `json:"herdr"`               // serve's herdr subscription is live
	Project  bool   `json:"project"`             // serve has this project loaded
	Merging  bool   `json:"merging"`             // its merge loop runs for it
	LogError string `json:"log_error,omitempty"` // its event log's last append error
	PID      int    `json:"pid,omitempty"`       // the serve that answered (its serve.flock's pid)
	// Launchd is the project's launchd agent (T-596; darwin): "none" (not installed), "loaded" (installed, its
	// process not the serve that answered), "running" (the serve that answered is launchd's); "": not asked.
	Launchd   string `json:"launchd,omitempty"`
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
	// Since is when the state column began, as far as this command can tell (epoch seconds; nil: unknown, or a lane with
	// no task): the held needs-you's fact, the gate or publish running on the task, the task_done's record (done), the
	// lane row's dispatch (a task working). obor monitor counts its elapsed time from it (T-658).
	Since *int64 `json:"since,omitempty"`
}

type needsYouJSON struct {
	Task   string `json:"task"`
	Lane   string `json:"lane,omitempty"`
	Reason string `json:"reason"`
	Answer string `json:"answer,omitempty"` // the commands that answer it (serve's ProjectStatus.Answers)
	Since  int64  `json:"since,omitempty"`  // when serve recorded the fact (epoch seconds)
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

// status is `obor status [--facts] [--json] [--short]`. Serve down is a line, never a non-zero exit: the lanes are read
// from the store alone. --facts adds the facts in force; --short prints only the serve line, the running line and
// needs-you (T-657). --all is accepted and changes nothing for one release (T-522: a serve holds one project, this one);
// drop it once adopters run v0.14.
func status(args []string) int {
	asJSON, facts, short := false, false, false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--all":
		case "--facts":
			facts = true
		case "--short":
			short = true
		default:
			errf("usage: obor status [--facts] [--json] [--short]")
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
	st.Serve.PID, st.Serve.Launchd = serveProcess(ctx, cfg, st.Serve.Up)
	if !asJSON {
		switch {
		case short:
			writeStatusShort(out, st, time.Now())
		default:
			writeStatus(out, st, time.Now())
			if facts {
				writeFacts(out, "", st.facts)
			}
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

// serveProcess is the serve that answered (up: its pid) and the project's launchd agent (serveJSON.Launchd).
func serveProcess(ctx context.Context, cfg *config.Config, up bool) (pid int, launchd string) {
	if up {
		pid = servePID(cfg.Root)
	}
	s := serviceOf(cfg, project.OSEnv)
	if !s.Supported() {
		return pid, ""
	}
	switch lpid, loaded := s.PID(ctx); {
	case !loaded:
		return pid, "none"
	case pid != 0 && lpid == pid:
		return pid, "running"
	}
	return pid, "loaded"
}

// buildStatus joins serve's answer (ss, or err when it gave none) to the lane table of p.
func buildStatus(p *checks.LaneTable, ss app.Status, err error) statusModel {
	st := statusModel{Lanes: []laneJSON{}, Tasks: []app.TaskStatus{}, Agents: []app.AgentStatus{},
		NeedsYou: []needsYouJSON{}, Messages: []app.MessageStatus{}, Running: []runningJSON{}}
	var ps app.ProjectStatus
	if err != nil {
		st.Serve.Error = err.Error()
	} else {
		st.Serve.Up, st.Serve.Since, st.Serve.Herdr = true, ss.Since, ss.Connected
		st.Serve.sinceTime, _ = time.Parse(time.RFC3339, ss.Since)
		if x := ss.Project; x != nil && filepath.Clean(x.Root) == filepath.Clean(p.Root) {
			ps, st.Serve.Project = *x, true
		}
		for _, r := range ps.Running {
			st.Running = append(st.Running, runningJSON{Step: r.Step, Task: r.Task, Command: runningCommand(p, r),
				Since: r.Since})
		}
		st.Serve.Merging, st.Serve.LogError = ps.Merging, ps.LogError
		st.Tasks, st.Agents = append(st.Tasks, ps.Tasks...), append(st.Agents, ps.Agents...)
		st.Messages = append(st.Messages, ps.Messages...)
		st.Review, st.Watches = ps.Review, ps.Watches
		st.facts = ps.Facts
		for _, f := range ps.Facts {
			if f.Step == app.StepNeedsYou {
				n := needsYouJSON{Task: f.Task, Lane: f.Fact().Lane, Reason: f.Fact().Reason,
					Answer: ps.Answers[f.Seq]}
				if t, err := time.Parse(time.RFC3339, f.TS); err == nil {
					n.Since = t.Unix()
				}
				st.NeedsYou = append(st.NeedsYou, n)
			}
		}
	}
	for _, s := range p.States() {
		l := laneJSON{Lane: s.Lane, Type: s.Type, Dir: s.Dir, Branch: orNull(s.Branch), Task: orNull(s.Task),
			State: s.State, DoneAge: s.DoneAge, Dirty: s.Dirty, Warning: orNull(s.Warning), Op: restartOp(p.Root, s.Lane),
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
		l.Since = laneSince(l, st, p.Now, p.Rows()[s.Lane].DispatchedAt)
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

// writeStatus is the text of st at now: serve's line, the running line (what serve does without an agent), one line per
// lane, serve's open tasks on no lane (not dropped), the agents on no lane, each needs-you with its answer, the lines
// not typed yet, the full check and the pause.
func writeStatus(w io.Writer, st statusModel, now time.Time) {
	for _, s := range statusSections(st, now) {
		writeLines(w, s)
	}
}

// statusSections is writeStatus' text as sections, in its order: serve's line and the running line, the lanes (with
// serve's tasks on no lane and the agents on no lane), needs-you, then the lines not typed yet and what reads under
// them (the review, the full check, the pause). obor monitor draws serveSection and needsYouSection the same way
// (T-658: one source for those lines).
func statusSections(st statusModel, now time.Time) [][]string {
	return [][]string{serveSection(st, now), laneSection(st, now), needsYouSection(st), tailSection(st, now)}
}

// serveSection is serve's line with the running line under it.
func serveSection(st statusModel, now time.Time) []string {
	var b strings.Builder
	writeServe(&b, st.Serve, now)
	writeRunning(&b, st, now)
	return textLines(b.String())
}

// laneSection is one line per lane, serve's open tasks on no lane (not dropped) and the agents on no lane.
func laneSection(st statusModel, now time.Time) []string {
	var b strings.Builder
	shown := map[string]bool{}
	for _, l := range st.Lanes {
		fmt.Fprintln(&b, laneLine(l, st))
		if l.TaskState != "" {
			shown[*l.Task] = true
		}
	}
	for _, t := range st.Tasks {
		if !shown[t.Task] && t.State != "dropped" { // dropped stays in serve's list for good; --json has it
			if t.State == "waiting" { // no lane, no attempt yet
				fmt.Fprintf(&b, "task %s: %s\n", t.Task, waitingOn(t))
				continue
			}
			fmt.Fprintf(&b, "task %s (%s): %s (attempt %d)%s\n", t.Task, app.Dash(t.Lane), t.State, t.Attempt, running(t.Running))
		}
	}
	for _, a := range st.Agents {
		if a.Lane == "" {
			fmt.Fprintf(&b, "agent %s: %s %s\n", app.Dash(a.Name), a.Status, app.Age(time.Duration(a.Age)*time.Second))
		}
	}
	return textLines(b.String())
}

// laneLine is l's line: its lane, type, branch, the state column (T-657), the task's age and id, then serve's state of
// the task, a dirty or warned worktree and the agent.
func laneLine(l laneJSON, st statusModel) string {
	ago := "-"
	if l.DoneAge != nil {
		ago = app.Age(time.Duration(*l.DoneAge) * time.Second)
	}
	line := fmt.Sprintf("%-6s  %-7s  %-14s  %-10s  %4s  (%s)", l.Lane, l.Type, orDash(l.Branch),
		laneState(l, st), ago, orDash(l.Task))
	if l.TaskState != "" {
		line += fmt.Sprintf("  serve: %s (attempt %d)%s", l.TaskState, l.Attempt, running(l.Running))
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
	return line
}

// needsYouSection is one line per needs-you with the commands that answer it.
func needsYouSection(st statusModel) []string {
	var b strings.Builder
	writeNeedsYou(&b, st.NeedsYou)
	return textLines(b.String())
}

// tailSection is the lines not typed yet, the open review (or "review due"), the full check and the pause.
func tailSection(st statusModel, now time.Time) []string {
	var b strings.Builder
	for _, m := range st.Messages {
		fmt.Fprintf(&b, "undelivered to %s (from %s, %s): %s\n", m.To, app.Dash(m.From), app.Age(time.Duration(m.Age)*time.Second), m.Line)
	}
	if st.Review != "" {
		fmt.Fprintln(&b, st.Review)
	}
	if fc := st.FullCheck; fc == nil {
		fmt.Fprintln(&b, "full check: none yet")
	} else {
		fmt.Fprintf(&b, "full check: %s %s, %s ago (%s)\n", fc.Verdict, fc.SHA[:min(7, len(fc.SHA))],
			app.Age(max(now.Sub(time.Unix(fc.At, 0)), 0)), fc.Summary)
	}
	if p := st.Paused; p != nil {
		b.WriteString(pauseLine(p))
	}
	return textLines(b.String())
}

// pauseLine is the pause in force as obor status writes it (obor monitor adds it to its needs-a-look section).
func pauseLine(p *pauseJSON) string {
	until := "cleared"
	if p.Until != nil {
		until = time.Unix(*p.Until, 0).Format("15:04")
	}
	return fmt.Sprintf("paused: %s, until %s (obor pause --clear ends it; no dispatch meanwhile)\n", p.Reason, until)
}

// textLines is text split into its lines (a trailing newline ends the last, it does not add one).
func textLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// writeLines writes each line of s with its newline.
func writeLines(w io.Writer, s []string) {
	for _, l := range s {
		fmt.Fprintln(w, l)
	}
}

// writeStatusShort is `obor status --short` (T-657): serve's line, the running line and each needs-you, nothing else
// (a status bar or a quick glance). It is the head of writeStatus' text, so both read the same.
func writeStatusShort(w io.Writer, st statusModel, now time.Time) {
	writeServe(w, st.Serve, now)
	writeRunning(w, st, now)
	writeNeedsYou(w, st.NeedsYou)
}

// writeServe is serve's line at now: up (launchd, a pane, since when), down and why, and a broken event log under it.
func writeServe(w io.Writer, s serveJSON, now time.Time) {
	switch {
	case !s.Up && s.Launchd == "none":
		fmt.Fprintln(w, "serve: down (obor serve install)")
	case !s.Up:
		fmt.Fprintf(w, "serve: down (%s)\n", s.Error)
	default:
		line := "serve: up"
		switch {
		case s.PID != 0 && s.Launchd == "running":
			line = fmt.Sprintf("serve: launchd pid %d", s.PID)
		case s.PID != 0:
			line = fmt.Sprintf("serve: pane pid %d", s.PID)
		}
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
}

// writeRunning is the running line right under serve's (T-657): what serve does without an agent, each step with the
// command it runs and its elapsed time (`running: gate T-656 (make -s check, 2m14s) · after_merge T-655 (make build,
// 12s)`), or `running: nothing`. A restart in progress reads "restarting serve", a step without a command (publish, a
// gate of git alone) its elapsed time only.
func writeRunning(w io.Writer, st statusModel, now time.Time) {
	if len(st.Running) == 0 {
		fmt.Fprintln(w, "running: nothing")
		return
	}
	items := make([]string, 0, len(st.Running))
	for _, r := range st.Running {
		item := runningName(r.Step)
		if r.Task != "" {
			item += " " + r.Task
		}
		if r.Step != app.StepServeRestart {
			if r.Command != "" {
				item += fmt.Sprintf(" (%s, %s)", r.Command, elapsed(now.Sub(time.Unix(r.Since, 0))))
			} else {
				item += fmt.Sprintf(" (%s)", elapsed(now.Sub(time.Unix(r.Since, 0))))
			}
		}
		items = append(items, item)
	}
	fmt.Fprintln(w, "running: "+strings.Join(items, " · "))
}

// runningName is a running step's name in the line: the log's own step, but the nightly run (a day's key) and the
// self-restart, which say what they are.
func runningName(step string) string {
	switch step {
	case app.StepNightly:
		return "nightly check"
	case app.StepServeRestart:
		return "restarting serve"
	}
	return step
}

// writeNeedsYou is one line per needs-you with the commands that answer it.
func writeNeedsYou(w io.Writer, needs []needsYouJSON) {
	for _, n := range needs {
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
}

// laneState is l's state column (T-657): what serve does for its task now — held (needs-you), gating, merging
// (publishing), reviewing (an integrator job is out) — else the lane table's state (done, working, free …).
func laneState(l laneJSON, st statusModel) string {
	if l.TaskState == "needs-you" || held(st.NeedsYou, l) {
		return "held"
	}
	for _, r := range st.Running {
		if r.Task == "" || l.Task == nil || r.Task != *l.Task {
			continue
		}
		switch r.Step {
		case app.StepGate:
			return "gating"
		case app.StepPublish:
			return "merging"
		}
	}
	if l.TaskState == "awaiting-accept" || l.TaskState == "resolving" { // an integrator job is out
		return "reviewing"
	}
	return l.State
}

// held: a needs-you is in force for l's lane or its task (a watchdog's, the merge loop's, the project's).
func held(needs []needsYouJSON, l laneJSON) bool {
	for _, n := range needs {
		if n.Lane == l.Lane || l.Task != nil && n.Task == *l.Task {
			return true
		}
	}
	return false
}

// laneSince is when l's state column began, as far as this command can tell (nil: a free lane, no task, or nothing
// recorded): the held needs-you's fact, the gate or publish running on its task, the lane row's dispatch (a task
// working in the lane) or the task_done's record (done). obor monitor counts the elapsed time of the state from it
// (T-658).
func laneSince(l laneJSON, st statusModel, now, dispatched int64) *int64 {
	at := func(v int64) *int64 { return &v }
	switch laneState(l, st) {
	case "held":
		var best *int64
		for _, n := range st.NeedsYou {
			if n.Since == 0 || n.Lane != l.Lane && (l.Task == nil || n.Task != *l.Task) {
				continue
			}
			if best == nil || n.Since > *best {
				best = at(n.Since)
			}
		}
		return best
	case "gating", "merging":
		for _, r := range st.Running {
			if l.Task != nil && r.Task == *l.Task && r.Since != 0 {
				return at(r.Since)
			}
		}
	case "done":
		if l.DoneAge != nil {
			return at(now - *l.DoneAge)
		}
	}
	if l.Task != nil && dispatched != 0 {
		return at(dispatched)
	}
	return nil
}

// runningCommand is the configured command r runs, for the running line: the project's checking step for a gate or the
// nightly check, commands.after_merge for after_merge; "" for a step of git alone (publish) or an unset command.
func runningCommand(p *checks.LaneTable, r app.RunningStep) string {
	name := ""
	switch r.Step {
	case app.StepGate, app.StepNightly:
		name = "check"
	case app.StepAfterMerge:
		name = app.StepAfterMerge
	}
	cmd, _ := p.Cfg.Command(name)
	return cmd.Cmd
}

// elapsed is a running step's age as the running line shows it: 12s, 2m14s, 1h02m, 3d04h.
func elapsed(d time.Duration) string {
	s := max(int64(d/time.Second), 0)
	switch {
	case s < 60:
		return strconv.FormatInt(s, 10) + "s"
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd%02dh", s/86400, s%86400/3600)
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

// restartOp is "restart" while a restart of lane holds its restart lock (T-192, T-500), else "".
func restartOp(root, lane string) string {
	if launch.RestartLock(root, lane, "").Held() {
		return "restart"
	}
	return ""
}
