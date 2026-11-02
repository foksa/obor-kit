package view

// The agents' long-running background processes (T-714): Claude Code's background Bash shells lead their own
// sessions, outside the pane's process group herdr's process-info lists (internal/launch/stop.go), but they stay
// descendants of the agent's process. One `ps` per build lists the machine's processes; each agent serve sees with a
// pane is its pane's foreground leader (herdr process-info, as stop takes it), and its descendants that have run
// longer than BackgroundMin, the agent's own helpers left out, are its background processes.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/project"
)

// BackgroundMin is how long a process runs before it is shown.
const BackgroundMin = 30 * time.Second

// helperSlack is how close to the agent's own start a direct child must have started to count as started with it (its
// MCP servers, its language servers): such a child and its descendants are helpers, never shown.
const helperSlack = 10 * time.Second

// commandMax is how many characters of a command the views show.
const commandMax = 60

// Background is one long-running process of an agent: its pid, its command (shortened), the description of the
// background Bash call that started it when obor's hook recorded one ("" otherwise) and how long it has run (seconds).
type Background struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Label   string `json:"label,omitempty"`
	Age     int64  `json:"age"`
}

// Text is what a line shows of b: its description, else its command.
func (b Background) Text() string {
	if b.Label != "" {
		return b.Label
	}
	return b.Command
}

// Proc is one row of the process table: pid, parent, how long it has run and its full command.
type Proc struct {
	PID, PPID int
	Age       time.Duration
	Command   string
}

// psArgs is the one ps call (macOS and Linux alike): every process, full width, no header.
var psArgs = []string{"-A", "-ww", "-o", "pid=,ppid=,etime=,command="}

// ReadProcs is the machine's process table, by one ps under ctx.
func ReadProcs(ctx context.Context) ([]Proc, error) {
	out, err := exec.CommandContext(ctx, "ps", psArgs...).Output()
	if err != nil {
		return nil, err
	}
	return ParseProcs(string(out)), nil
}

// ParseProcs is ps's output (pid ppid etime command, one process per line) as rows; a line that does not parse is
// skipped.
func ParseProcs(out string) []Proc {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		age, ok := parseEtime(f[2])
		if err1 != nil || err2 != nil || !ok {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, Age: age, Command: strings.Join(f[3:], " ")})
	}
	return procs
}

// parseEtime is ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	secs := 0
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days*86400+secs) * time.Second, true
}

// shells are the programs a command runs in: a shell with children is passed through, its children are the processes
// shown (an agent's Bash tool wraps every command in one).
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true}

// program is a command's argv0 without its path or a login shell's leading dash.
func program(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimPrefix(filepath.Base(f[0]), "-")
}

// oborMCP: command is obor's own MCP server (`obor mcp`, through the shim or the binary).
func oborMCP(command string) bool {
	f := strings.Fields(command)
	for i := 0; i+1 < len(f); i++ {
		if filepath.Base(f[i]) == "obor" && f[i+1] == "mcp" {
			return true
		}
	}
	return false
}

// Descendants is the background processes of the agent with pid agent in procs: walking down from it, a shell with
// children is passed through and any other process is one entry (its own children are its business), kept when it has
// run longer than min. Left out, with all they started — the agent's own helpers: a direct child of the agent that is
// no shell (an agent runs commands through a shell; what it starts itself is its MCP servers, language servers, Claude
// Code's caffeinate per turn), a direct child that started with the agent (within helperSlack of its own start: an MCP
// server wrapped in a shell) and obor's own MCP server anywhere. Each entry's ancestors up to the agent (the shell that
// wraps a Bash tool call) come with it, to match a recorded call against. None when agent is not in procs.
func Descendants(procs []Proc, agent int, min time.Duration) []Found {
	kids := map[int][]Proc{}
	var self *Proc
	for i, p := range procs {
		kids[p.PPID] = append(kids[p.PPID], p)
		if p.PID == agent {
			self = &procs[i]
		}
	}
	if self == nil || agent <= 1 {
		return nil
	}
	var out []Found
	var walk func(pid int, chain []string)
	walk = func(pid int, chain []string) {
		for _, c := range kids[pid] {
			shell := shells[program(c.Command)]
			if oborMCP(c.Command) || pid == agent && (!shell || c.Age >= self.Age-helperSlack) {
				continue
			}
			if shell && len(kids[c.PID]) > 0 {
				walk(c.PID, append(chain[:len(chain):len(chain)], c.Command))
				continue
			}
			if c.Age > min {
				out = append(out, Found{Proc: c, Chain: chain})
			}
		}
	}
	walk(agent, nil)
	return out
}

// Found is a background process and the commands of the shells between it and the agent.
type Found struct {
	Proc
	Chain []string
}

// label is the description of the newest call in calls whose command f runs: the call's command (whitespace folded)
// is in f's command or in a shell's that wraps it (Claude's shell quotes it: a ' as '\”), or f's command is in the
// call's (a call of several commands). "" when none matches.
func label(f Found, calls []project.BackgroundCall) string {
	cmd := fold(f.Command)
	for i := len(calls) - 1; i >= 0; i-- {
		want := fold(calls[i].Command)
		if want == "" {
			continue
		}
		if strings.Contains(cmd, want) || len(cmd) >= 8 && strings.Contains(want, cmd) {
			return calls[i].Description
		}
		for _, sh := range f.Chain {
			if strings.Contains(fold(strings.ReplaceAll(sh, `'\''`, `'`)), want) {
				return calls[i].Description
			}
		}
	}
	return ""
}

// fold is s with its whitespace runs as one space.
func fold(s string) string { return strings.Join(strings.Fields(s), " ") }

// shorten is a command as the views show it: argv0 without its path, whitespace folded, cut at commandMax characters.
func shorten(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	f[0] = filepath.Base(f[0])
	s := strings.Join(f, " ")
	if r := []rune(s); len(r) > commandMax {
		s = string(r[:commandMax-1]) + "…"
	}
	return s
}

// AgentPID is the agent's process in pane: the leader of the pane's foreground group (herdr process-info, as obor stop
// takes it); 0 when the pane's shell has the foreground or herdr does not say.
func AgentPID(pi port.ProcessInfo) int {
	if pi.AtShell() || pi.FgPGID <= 1 {
		return 0
	}
	return pi.FgPGID
}

// BackgroundOf is each agent's background processes, by agent name: for every agent with a pane, its pane's process
// info (info) and its descendants in procs, labelled from root's recorded calls of that agent (or, with no agent name
// recorded, of its pane). An agent with none is not in the map.
func BackgroundOf(ctx context.Context, root string, agents []wire.AgentStatus, procs []Proc,
	info func(context.Context, string) (port.ProcessInfo, error)) map[string][]Background {
	out := map[string][]Background{}
	var calls []project.BackgroundCall
	read := false
	for _, a := range agents {
		if a.Name == "" || a.Pane == "" {
			continue
		}
		pi, err := info(ctx, a.Pane)
		if err != nil {
			continue
		}
		found := Descendants(procs, AgentPID(pi), BackgroundMin)
		if len(found) == 0 {
			continue
		}
		if !read {
			calls, read = project.BackgroundCalls(root), true
		}
		var mine []project.BackgroundCall
		for _, c := range calls {
			if c.Agent == a.Name || c.Agent == "" && c.Pane == a.Pane {
				mine = append(mine, c)
			}
		}
		for _, f := range found {
			out[a.Name] = append(out[a.Name], Background{PID: f.PID, Command: shorten(f.Command), Label: label(f, mine),
				Age: int64(f.Age / time.Second)})
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

// BackgroundCount is how many background processes v's agents have (obor web's overview card).
func (v Project) BackgroundCount() int {
	n := 0
	for _, l := range v.Lanes {
		n += len(l.Background)
	}
	for _, r := range v.Roles {
		n += len(r.Background)
	}
	if v.Lead != nil {
		n += len(v.Lead.Background)
	}
	return n
}

// ScanBackground is BackgroundOf for the agents of serve's answer for root (none: serve down or another project's),
// with one ps and herdr's process info per agent pane over its socket (env's). Any failure is no processes: the
// readers show the rest.
func ScanBackground(ctx context.Context, root string, ss wire.Status, err error, env project.Env) map[string][]Background {
	if err != nil || ss.Project == nil || filepath.Clean(ss.Project.Root) != filepath.Clean(root) ||
		len(ss.Project.Agents) == 0 {
		return nil
	}
	procs, perr := ReadProcs(ctx)
	if perr != nil {
		return nil
	}
	h := &herdr.Socket{Path: herdr.SocketPath(env), Timeout: 2 * time.Second}
	return BackgroundOf(ctx, root, ss.Project.Agents, procs, func(ctx context.Context, pane string) (port.ProcessInfo, error) {
		return h.ProcessInfo(ctx, pane)
	})
}
