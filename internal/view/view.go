// Package view is the project's read model (T-664): one function, Build, joins obor serve's status answer (none when
// serve is down), the git lane table and the store (read-only) into one Project. Every reader of project state goes
// through it: `obor status` prints it as JSON or renders it as text, `obor monitor` renders it, and the web UI serves
// it. app never imports view; only entry packages do (internal/checks/boundary_test.go).
package view

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/spec"
	"obor/internal/store"
)

// Project is the project's state as one struct (the `obor status --json` shape): serve up or down, what serve does
// without an agent (running, pending), the lanes (the store's rows and what git says of each worktree, with serve's
// task and agent), serve's open tasks and agents, the role agents, needs-you with the commands that answer it, the
// last full check, the pause, the lines not typed yet, the review line, the lead's watches, the queue and the recent
// events.
type Project struct {
	Serve     Serve                `json:"serve"`
	Running   []Running            `json:"running"`
	Pending   []wire.PendingStep   `json:"pending"` // the open command steps whose effect does not run now
	Lanes     []Lane               `json:"lanes"`
	Tasks     []wire.TaskStatus    `json:"tasks"`          // serve's open tasks
	Agents    []wire.AgentStatus   `json:"agents"`         // the agents serve sees in the project
	Roles     []Role               `json:"roles"`          // the role agents on no lane (integrator, reviewer, deep dive, advisor)
	Lead      *Role                `json:"lead,omitempty"` // the lead's agent, when serve sees its pane (T-714: its background processes)
	NeedsYou  []NeedsYou           `json:"needs_you"`
	FullCheck *FullCheck           `json:"full_check"`
	Paused    *Pause               `json:"paused"`
	Messages  []wire.MessageStatus `json:"messages"`          // the lines not typed yet
	Review    string               `json:"review,omitempty"`  // the open review request, or "review due: …" (T-638)
	Watches   []wire.WatchStatus   `json:"watches,omitempty"` // the lead's open watches (obor monitor, T-658)
	Queue     Queue                `json:"queue"`
	Events    []Event              `json:"events"` // the newest worded records, newest first
	Facts     []event.Record       `json:"-"`      // serve's facts in force (obor status --facts)
	// Notes are the project's own lines above serve's (T-709): a config key this binary does not know, a stopped
	// project (Notes builds them).
	Notes []string `json:"notes,omitempty"`
}

// Running is one step that runs without an agent (wire.RunningStep, T-657) with the configured command it runs ("": a
// step of git alone, such as publish); the text renderers count its elapsed time from Since.
type Running struct {
	Step    string `json:"step"` // gate, publish, after_merge, nightly or restart
	Task    string `json:"task,omitempty"`
	Command string `json:"command,omitempty"`
	Since   int64  `json:"since"`
}

// Serve: serve answered (Up, since when) or not (Error); the project's state in it.
type Serve struct {
	Up       bool   `json:"up"`
	Since    string `json:"since,omitempty"`
	Error    string `json:"error,omitempty"`     // why it is down
	Herdr    bool   `json:"herdr"`               // serve's herdr subscription is live
	Project  bool   `json:"project"`             // serve has this project loaded
	Merging  bool   `json:"merging"`             // its merge loop runs for it
	LogError string `json:"log_error,omitempty"` // its event log's last append error
	PID      int    `json:"pid,omitempty"`       // the serve that answered (its serve.flock's pid)
	// Launchd is the project's launchd agent (T-596; darwin): "none" (not installed), "loaded" (installed, its
	// process not the serve that answered), "running" (the serve that answered is launchd's); "": not asked. The
	// caller fills PID and Launchd (cmd/obor asks the service adapter).
	Launchd   string    `json:"launchd,omitempty"`
	SinceTime time.Time `json:"-"` // Since, parsed (zero: unknown)
}

// Lane is one configured lane: its worktree as git and the store's row say (state free, dirty-free, working, done,
// missing), serve's task on it and the agent serve sees there.
type Lane struct {
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
	Effort      string  `json:"effort,omitempty"`  // the effort the task's dispatch started its agent with (T-695)
	Agent       string  `json:"agent,omitempty"`   // serve's agent in the lane, else the lane row's
	AgentStatus string  `json:"agent_status,omitempty"`
	// Column is the state column of obor status and obor monitor (T-657): held, gating, merging, gate pending,
	// publish pending, reviewing, else State.
	Column string `json:"column"`
	// Since is when the state column began, as far as the view can tell (epoch seconds; nil: unknown, or a lane with
	// no task): the held needs-you's fact, the gate or publish running on the task, the task_done's record (done), the
	// lane row's dispatch (a task working). obor monitor counts its elapsed time from it (T-658).
	Since *int64 `json:"since,omitempty"`
	// SpecRev is the store's current rev of the task's spec (0: none stored), Title its title.
	SpecRev int    `json:"spec_rev,omitempty"`
	Title   string `json:"title,omitempty"`
	// Job is the task's open job (nil: none).
	Job *Job `json:"job,omitempty"`
	// Background is the lane agent's long-running background processes (T-714; Input.Background).
	Background []Background `json:"background,omitempty"`
}

// Job is an open job: its id (<task>/<attempt>/<step>) and whether its agent's job_next fetched it.
type Job struct {
	ID        string `json:"id"`
	Delivered bool   `json:"delivered"`
}

// Role is a role agent on no lane: its role and agent name, its pane's status and for how long (seconds; Status "":
// serve sees no pane of it; RoleStopped: serve or the lead stopped it), the brief it is on (nil: none) and its current
// recorded session.
type Role struct {
	Role    string            `json:"role"`
	Agent   string            `json:"agent"`
	Status  string            `json:"status,omitempty"`
	Age     int64             `json:"age,omitempty"`
	Brief   *wire.BriefStatus `json:"brief,omitempty"`
	Session string            `json:"session,omitempty"`
	// Background is the agent's long-running background processes (T-714; Input.Background).
	Background []Background `json:"background,omitempty"`
}

// NeedsYou is one needs-you fact in force.
type NeedsYou struct {
	Task   string `json:"task"`
	Lane   string `json:"lane,omitempty"`
	Reason string `json:"reason"`
	Answer string `json:"answer,omitempty"` // the commands that answer it (serve's ProjectStatus.Answers)
	Since  int64  `json:"since,omitempty"`  // when serve recorded the fact (epoch seconds)
}

// FullCheck is the last full check (.obor/state/last-full-check).
type FullCheck struct {
	SHA     string `json:"sha"`
	Verdict string `json:"verdict"` // GREEN | RED
	At      int64  `json:"at"`
	Summary string `json:"summary"`
}

// Pause is serve's pause in force (event.Pause): until (epoch seconds; null: until cleared), why, who paused.
type Pause struct {
	Until  *int64 `json:"until"`
	Reason string `json:"reason"`
	By     string `json:"by,omitempty"`
}

// Input is what Build joins: serve's answer (Err: it gave none), the lane table, the store (nil: none yet), the
// clock and how many worded events to keep.
type Input struct {
	Status wire.Status
	Err    error
	Lanes  *checks.LaneTable
	DB     *sql.DB
	Now    time.Time
	Events int
	Notes  []string // the project's own lines (Notes)
	// Background is each agent's long-running background processes by agent name (ScanBackground; nil: none read).
	Background map[string][]Background
}

// Notes is the project's own lines for Input.Notes: warn (config.LoadReader's, "" for none), then
// project.StoppedLine when root is stopped (obor stop, T-709).
func Notes(root, warn string) []string {
	var out []string
	if warn != "" {
		out = append(out, warn)
	}
	if project.Stopped(root) {
		out = append(out, project.StoppedLine)
	}
	return out
}

// RoleStopped is a Role's Status when its only labelled pane is at its shell: serve stopped the agent once its work was
// done (T-672) or the lead did (T-677), and no pane runs it.
const RoleStopped = "stopped"

// roleNames is the roles a Role row is listed for (the worker's are the lanes, the lead is the reader).
var roleNames = []string{"integrator", "reviewer", "deep_dive", "advisor", "researcher"}

// Build is the project's view: serve's answer joined to the lane table and the store. Serve down: its error, no
// agents and no roles (nothing stale), the lanes from git and the store alone.
func Build(in Input) Project {
	p := in.Lanes
	v := Project{Lanes: []Lane{}, Tasks: []wire.TaskStatus{}, Agents: []wire.AgentStatus{}, Roles: []Role{},
		NeedsYou: []NeedsYou{}, Messages: []wire.MessageStatus{}, Running: []Running{}, Pending: []wire.PendingStep{},
		Events: []Event{}, Notes: in.Notes}
	var ps wire.ProjectStatus
	if in.Err != nil {
		v.Serve.Error = in.Err.Error()
	} else {
		ss := in.Status
		v.Serve.Up, v.Serve.Since, v.Serve.Herdr = true, ss.Since, ss.Connected
		v.Serve.SinceTime, _ = time.Parse(time.RFC3339, ss.Since)
		if x := ss.Project; x != nil && filepath.Clean(x.Root) == filepath.Clean(p.Root) {
			ps, v.Serve.Project = *x, true
		}
		for _, r := range ps.Running {
			v.Running = append(v.Running, Running{Step: r.Step, Task: r.Task, Command: runningCommand(p, r),
				Since: r.Since})
		}
		v.Pending = append(v.Pending, ps.Pending...)
		v.Serve.Merging, v.Serve.LogError = ps.Merging, ps.LogError
		v.Tasks, v.Agents = append(v.Tasks, ps.Tasks...), append(v.Agents, ps.Agents...)
		v.Messages = append(v.Messages, ps.Messages...)
		v.Review, v.Watches = ps.Review, ps.Watches
		v.Facts = ps.Facts
		for _, f := range ps.Facts {
			if f.Step == event.StepNeedsYou {
				n := NeedsYou{Task: f.Task, Lane: f.Fact().Lane, Reason: f.Fact().Reason, Answer: ps.Answers[f.Seq]}
				if t, err := time.Parse(time.RFC3339, f.TS); err == nil {
					n.Since = t.Unix()
				}
				v.NeedsYou = append(v.NeedsYou, n)
			}
		}
		v.Roles = roles(p, ps, in.Background)
		v.Lead = lead(p, ps, in.Background)
	}
	if ps.Paused != nil {
		v.Paused = &Pause{Reason: ps.Paused.Why, By: ps.Paused.Caller}
		if n := ps.Paused.Until; n != 0 {
			v.Paused.Until = &n
		}
	}
	specs := readSpecs(in.DB)
	for _, s := range p.States() {
		l := lane(p, s, &v, ps, specs)
		if l.Agent != "" {
			l.Background = in.Background[l.Agent]
		}
		v.Lanes = append(v.Lanes, l)
	}
	v.FullCheck = readFullCheck(p.Root)
	v.Queue = queue(&v, ps, p.Cfg.Data.Merge.NightlyAt, specs, dispatchedTasks(in.DB), in.Now)
	v.Events = events(in.DB, in.Events, v.Serve)
	return v
}

// lane is s's row of the view: the lane table's facts, serve's task and agent in it, its state column and since when,
// its task's spec rev and open job.
func lane(p *checks.LaneTable, s checks.LaneStatus, v *Project, ps wire.ProjectStatus, specs map[string]store.SpecRow) Lane {
	row := p.Rows()[s.Lane]
	l := Lane{Lane: s.Lane, Type: s.Type, Dir: s.Dir, Branch: orNull(s.Branch), Task: orNull(s.Task), State: s.State,
		DoneAge: s.DoneAge, Dirty: s.Dirty, Warning: orNull(s.Warning), Op: restartOp(p.Root, s.Lane), Agent: row.Agent}
	if l.Warning == nil && s.State == "working" && pluginMissing(p, s) {
		w := "no-plugin"
		l.Warning = &w
	}
	for _, t := range ps.Tasks {
		if t.Lane == s.Lane && t.Task == s.Task {
			l.TaskState, l.Attempt, l.Running, l.Effort = t.State, t.Attempt, t.Running, t.Effort
			if t.Job != "" {
				l.Job = &Job{ID: t.Job, Delivered: t.Delivered}
			}
		}
	}
	for _, a := range ps.Agents {
		if a.Lane == s.Lane {
			l.Agent, l.AgentStatus = a.Name, a.Status
		}
	}
	if s.Task != "" {
		l.SpecRev, l.Title = specs[s.Task].Rev, spec.Title(specs[s.Task].Text)
	}
	l.Column = column(l, v)
	l.Since = laneSince(l, v, p.Now, row.DispatchedAt)
	return l
}

// roles is one row per role agent on no lane that the config names (an optional role only when configured), with its
// pane's status and session as serve sees it and its open brief.
func roles(p *checks.LaneTable, ps wire.ProjectStatus, bg map[string][]Background) []Role {
	out := []Role{}
	for _, name := range roleNames {
		rc, ok := p.Cfg.Role(name)
		if !ok || rc.Role.Optional && !rc.Configured {
			continue
		}
		agent := p.Cfg.AgentName(name, "")
		if agent == "" {
			continue
		}
		r := Role{Role: name, Agent: agent, Background: bg[agent]}
		for _, a := range ps.Agents {
			if a.Name == agent && a.Lane == "" {
				r.Status, r.Age, r.Session = a.Status, a.Age, a.Session
			}
		}
		if r.Status == "" && slices.Contains(ps.Stopped, agent) {
			r.Status = RoleStopped // serve or the lead stopped it: not missing (T-672, T-677)
		}
		for _, b := range ps.Briefs {
			if b.To == agent {
				b := b
				r.Brief = &b
			}
		}
		out = append(out, r)
	}
	return out
}

// lead is the lead's row (its status and session as serve sees its pane, its background processes); nil when the
// config names no lead agent or serve sees no pane of it.
func lead(p *checks.LaneTable, ps wire.ProjectStatus, bg map[string][]Background) *Role {
	agent := p.Cfg.AgentName("lead", "")
	if agent == "" {
		return nil
	}
	for _, a := range ps.Agents {
		if a.Name == agent && a.Lane == "" {
			return &Role{Role: "lead", Agent: agent, Status: a.Status, Age: a.Age, Session: a.Session, Background: bg[agent]}
		}
	}
	return nil
}

// column is l's state column (T-657): what serve does for its task now — held (needs-you), gating, merging
// (publishing), reviewing (an integrator job is out) — else the lane table's state (done, working, free …). A gate or
// publish whose effect does not run now but whose intent waits reads `gate pending` / `publish pending` (T-662): it is
// not progress, it is stuck work (a failed step, a replay the next pass has yet to run).
func column(l Lane, v *Project) string {
	if l.TaskState == "needs-you" || held(v.NeedsYou, l) {
		return "held"
	}
	for _, r := range v.Running {
		if r.Task == "" || l.Task == nil || r.Task != *l.Task {
			continue
		}
		switch r.Step {
		case event.StepGate:
			return "gating"
		case event.StepPublish:
			return "merging"
		}
	}
	for _, p := range v.Pending {
		if l.Task == nil || p.Task != *l.Task {
			continue
		}
		switch p.Step {
		case event.StepGate, event.StepPublish:
			return p.Step + " pending"
		}
	}
	if l.TaskState == "awaiting-accept" || l.TaskState == "resolving" { // an integrator job is out
		return "reviewing"
	}
	return l.State
}

// held: a needs-you is in force for l's lane or its task (a watchdog's, the merge loop's, the project's).
func held(needs []NeedsYou, l Lane) bool {
	for _, n := range needs {
		if n.Lane == l.Lane || l.Task != nil && n.Task == *l.Task {
			return true
		}
	}
	return false
}

// laneSince is when l's state column began, as far as the view can tell (nil: a free lane, no task, or nothing
// recorded): the held needs-you's fact, the gate or publish running on its task (a waiting one: its failure's time,
// else its intent's), the lane row's dispatch (a task working in the lane) or the task_done's record (done).
func laneSince(l Lane, v *Project, now, dispatched int64) *int64 {
	at := func(n int64) *int64 { return &n }
	switch l.Column {
	case "held":
		var best *int64
		for _, n := range v.NeedsYou {
			if n.Since == 0 || n.Lane != l.Lane && (l.Task == nil || n.Task != *l.Task) {
				continue
			}
			if best == nil || n.Since > *best {
				best = at(n.Since)
			}
		}
		return best
	case "gating", "merging":
		for _, r := range v.Running {
			if l.Task != nil && r.Task == *l.Task && r.Since != 0 {
				return at(r.Since)
			}
		}
	case "gate pending", "publish pending":
		for _, p := range v.Pending {
			if l.Task == nil || p.Task != *l.Task {
				continue
			}
			if p.FailedAt != 0 {
				return at(p.FailedAt)
			}
			if p.Since != 0 {
				return at(p.Since)
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

// runningCommand is the configured command r runs, for the running line: for a gate the command serve says it runs
// now (T-669: the class's, the guard or check; its name when not configured; "" while unknown, never a guess), the
// full check for the nightly check, commands.after_merge for after_merge; "" for a step of git alone (publish) or an
// unset command.
func runningCommand(p *checks.LaneTable, r wire.RunningStep) string {
	name := ""
	switch r.Step {
	case event.StepGate:
		if name = r.Command; name == "" {
			return ""
		}
	case event.StepNightly:
		name = "check"
	case event.StepAfterMerge:
		name = event.StepAfterMerge
	}
	cmd, ok := p.Cfg.Command(name)
	if !ok && r.Step == event.StepGate {
		return name
	}
	return cmd.Cmd
}

// readFullCheck is .obor/state/last-full-check ("<sha> <epoch> GREEN|RED <summary>", obor nightly / milestone-check);
// nil when there is none.
func readFullCheck(root string) *FullCheck {
	b, err := os.ReadFile(filepath.Join(project.StateDir(root), "last-full-check"))
	if err != nil {
		return nil
	}
	f := strings.SplitN(project.FirstLine(string(b)), " ", 4)
	if len(f) < 3 {
		return nil
	}
	at, _ := strconv.ParseInt(f[1], 10, 64)
	fc := &FullCheck{SHA: f[0], At: at, Verdict: f[2]}
	if len(f) == 4 {
		fc.Summary = f[3]
	}
	return fc
}

// A Claude lane is missing it when its worktree has no .claude/settings.json enabling obor's plugin (T-705).
// pluginMissing (T-307): a lane whose kind runs obor's hooks through obor's plugin (agents.HooksPlugin: opencode) has
// no plugin marker newer than the dispatch that started its work (obor status warns: the lane runs with no obor
// guard). None at all, or one older than the lane row's dispatched_at (a leftover from before this dispatch): missing.
// No row: a marker at all is fresh, nothing to compare it against.
func pluginMissing(p *checks.LaneTable, s checks.LaneStatus) bool {
	lt, ok := p.Cfg.LaneType(s.Type)
	if k, known := agents.Lookup(lt.Kind); ok && known && k.Caps().ProjectPlugin { // Claude loads the plugin from the worktree's own .claude/settings.json (T-705)
		return !config.PluginEnabledIn(s.Dir)
	}
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

// restartOp is "restart" while a restart of lane holds its restart lock (T-192, T-500), else "".
func restartOp(root, lane string) string {
	if launch.RestartLock(root, lane, "").Held() {
		return "restart"
	}
	return ""
}

// orNull is s, or JSON null when it is "".
func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
