package view

// The agents' long-running processes (T-714, T-727): Claude Code's background Bash shells lead their own sessions,
// outside the pane's process group herdr's process-info lists (internal/launch/stop.go), but they stay descendants of
// the agent's process. One `ps` per scan lists the machine's processes, indexed once by parent (ProcIndex) for all the
// scan's agents; each agent serve sees with a pane is its pane's foreground leader (herdr process-info, as stop takes
// it), and its descendants that have run longer than BackgroundMin, the agent's own helpers left out, are its long
// processes. Only one that runs a recorded background Bash call (run_in_background, obor's PreToolUse hook) is
// background work (`bg`); any other is a long foreground command (`running`). The process table and the call's matching
// are internal/background's (T-1142: the hook and serve check a call's liveness with them too).

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/background"
	"obor/internal/herdr"
	"obor/internal/project"
)

// BackgroundMin is how long a process runs before it is shown.
const BackgroundMin = 30 * time.Second

// now is the clock BackgroundOf takes the processes' start from (a test sets it).
var now = time.Now

// Background is one long-running process of an agent: its pid, its command (shortened), whether it runs a background
// Bash call obor's hook recorded (T-727: false is a long foreground command), that call's description ("" otherwise)
// and how long it has run (seconds).
type Background struct {
	PID        int    `json:"pid"`
	Command    string `json:"command"`
	Background bool   `json:"background"`
	Label      string `json:"label,omitempty"`
	Age        int64  `json:"age"`
}

// Text is what a line shows of b: its description, else its command.
func (b Background) Text() string {
	if b.Label != "" {
		return b.Label
	}
	return b.Command
}

// Line is what a view shows of b, its age (app.Age) given: `bg: <description or command> · 15m` for a recorded
// background call, `running <command> · 15m` for any other long process (T-727).
func (b Background) Line(age string) string {
	if b.Background {
		return "bg: " + b.Text() + " · " + age
	}
	return "running " + b.Text() + " · " + age
}

// Proc is one row of the process table: pid, parent, how long it has run and its full command.
type Proc = background.Proc

// ProcIndex is a process table indexed for Descendants (internal/background's, T-1143: the Stop hook walks the same).
type ProcIndex = background.ProcIndex

// indexBuilds counts NewProcIndex calls (a test reads it: one index per scan).
var indexBuilds atomic.Int64

// NewProcIndex is procs indexed by parent and by pid (background.NewProcIndex, counted).
func NewProcIndex(procs []Proc) *ProcIndex {
	indexBuilds.Add(1)
	return background.NewProcIndex(procs)
}

// AgentPID is the agent's process in pane: the leader of the pane's foreground group (herdr process-info, as obor stop
// takes it); 0 when the pane's shell has the foreground or herdr does not say.
func AgentPID(pi port.ProcessInfo) int {
	if pi.AtShell() || pi.FgPGID <= 1 {
		return 0
	}
	return pi.FgPGID
}

// BackgroundOf is each agent's long-running processes, by agent name: for every agent with a pane, its pane's process
// info (info) and its descendants in procs (one ProcIndex for all the agents), matched against root's recorded
// background calls of that agent (Mine's: its session's, or its name's when it records no session): one that started the
// process, by command and by time (background.Match), makes it background, with the call's description; a later
// foreground run of the same command, or an earlier session's call, is `running` (RV-85 verify #3). An agent with none
// is not in the map.
func BackgroundOf(ctx context.Context, root string, agents []wire.AgentStatus, procs []Proc,
	info func(context.Context, string) (port.ProcessInfo, error)) map[string][]Background {
	out := map[string][]Background{}
	var calls []project.BackgroundCall
	read := false
	var x *ProcIndex
	for _, a := range agents {
		if a.Name == "" || a.Pane == "" {
			continue
		}
		pi, err := info(ctx, a.Pane)
		if err != nil {
			continue
		}
		pid := AgentPID(pi)
		if pid == 0 {
			continue
		}
		if x == nil {
			x = NewProcIndex(procs)
		}
		found := x.Descendants(pid, BackgroundMin)
		if len(found) == 0 {
			continue
		}
		if !read {
			calls, read = project.BackgroundCalls(root), true
		}
		mine := background.Mine(calls, a.Name, a.Session, a.Pane)
		at := now()
		for _, f := range found {
			c, bg := background.Match(f, mine, at)
			out[a.Name] = append(out[a.Name], Background{PID: f.PID, Command: background.Shorten(f.Command),
				Background: bg, Label: c.Description, Age: int64(f.Age / time.Second)})
		}
	}
	return out
}

// AgentBackground is the background processes of the agent named agent in v: its lane's, its role's or the lead's.
func (v Project) AgentBackground(agent string) []Background {
	if agent == "" {
		return nil
	}
	for _, l := range v.Lanes {
		if l.Agent == agent && len(l.Background) > 0 {
			return l.Background
		}
	}
	for _, r := range v.Roles {
		if r.Agent == agent {
			return r.Background
		}
	}
	if v.Lead != nil && v.Lead.Agent == agent {
		return v.Lead.Background
	}
	return nil
}

// BackgroundCount is how many recorded background processes v's agents have (obor web's overview card, `N bg`; a
// long foreground command is not counted, T-727).
func (v Project) BackgroundCount() int {
	lists := [][]Background{}
	for _, l := range v.Lanes {
		lists = append(lists, l.Background)
	}
	for _, r := range v.Roles {
		lists = append(lists, r.Background)
	}
	if v.Lead != nil {
		lists = append(lists, v.Lead.Background)
	}
	n := 0
	for _, bs := range lists {
		for _, b := range bs {
			if b.Background {
				n++
			}
		}
	}
	return n
}

// ScanBackground is BackgroundOf for the agents of serve's answer for root (none: serve down or another project's),
// with one ps and herdr's process info per agent pane over its socket (env's). Any failure is no processes: the
// readers show the rest.
func ScanBackground(ctx context.Context, root string, ss wire.Status, err error, env project.Env) map[string][]Background {
	return scanBackground(ctx, root, ss, err, env, background.ReadProcs)
}

// background is ScanBackground with ps read by r.procs (background.ReadProcs when nil): the agents' long-running
// processes of serve's answer for root (none: serve down or another project's).
func (r reading) background(ctx context.Context, root string, ss wire.Status, err error) map[string][]Background {
	procs := r.procs
	if procs == nil {
		procs = background.ReadProcs
	}
	return scanBackground(ctx, root, ss, err, project.OSEnv, procs)
}

// scanBackground is ScanBackground with ps read by procs (a read's own, T-880): any failure is no processes.
func scanBackground(ctx context.Context, root string, ss wire.Status, err error, env project.Env,
	procs func(ctx context.Context) ([]Proc, error)) map[string][]Background {
	if err != nil || ss.Project == nil || filepath.Clean(ss.Project.Root) != filepath.Clean(root) ||
		len(ss.Project.Agents) == 0 {
		return nil
	}
	ps, perr := procs(ctx)
	if perr != nil {
		return nil
	}
	h := &herdr.Socket{Path: herdr.SocketPath(env), Timeout: 2 * time.Second}
	return BackgroundOf(ctx, root, ss.Project.Agents, ps, func(ctx context.Context, pane string) (port.ProcessInfo, error) {
		return h.ProcessInfo(ctx, pane)
	})
}
