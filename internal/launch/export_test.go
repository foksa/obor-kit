package launch

import (
	"context"
	"os"
	"slices"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/testkit"
)

// StartArgs is a kind's start over launch args, as a launch's build runs it (a compiled kind's agents.StartOf second
// pass, a plugin kind's one start; the prompt one line), for the external tests' case table.
func StartArgs(kind, kit string, args []string, prompt string) []string {
	k, _ := testkit.Kinds().Lookup(kind)
	in := agents.Start{Kit: kit, Args: args, Prompt: oneLine(prompt), Read: os.ReadFile}
	if s, ok := k.(agents.Split); ok {
		return s.Start(in)
	}
	in.WriteState = func(name string, _ []byte) (string, error) { return "/state/" + name, nil }
	l, _ := agents.StartOf(k, in)
	return l.Args
}

// MCPArgs is the --mcp-config pair a fresh start of kind adds for kit's obor server (nil: none), as its argv shows it.
func MCPArgs(kind, kit string, args []string) []string {
	k, ok := testkit.Kinds().Lookup(kind)
	if !ok {
		return nil
	}
	pair := []string{"--mcp-config", claude.MCPConfig(kit)}
	argv := k.(agents.Split).Start(agents.Start{Kit: kit, Args: args, Read: os.ReadFile})
	for i := range argv {
		if slices.Equal(argv[i:min(i+2, len(argv))], pair) {
			return pair
		}
	}
	return nil
}

// StartSeen is Start with the agent the start ended with (the case table asserts the observation returned).
func (l *Launcher) StartSeen(ctx context.Context, s Start) (port.Pane, error) { return l.start(ctx, s) }

// Target is a launch's target as its steps read it (the external tests of sessionOf, firstPrompt and outcome): the
// agent, its pane, kind and config facts, its recorded session (Sid, SidKind, Transcript), the session and kind the
// terminal reports for its pane (Live, LiveKind), and whether its pane was just placed.
type Target struct {
	Agent, Pane, Kind, Role, Lane, Type string
	Sid, SidKind, Transcript            string
	Live, LiveKind                      string
	Placed                              bool
}

func (x Target) target() *target {
	t := &target{agent: x.Agent, known: true, pane: x.Pane, kind: x.Kind, sid: x.Sid, sidKind: x.SidKind,
		transcript: x.Transcript}
	t.info.Role, t.info.Lane, t.info.Type = x.Role, x.Lane, x.Type
	t.entry = port.Pane{ID: x.Pane, Session: x.Live, Agent: x.LiveKind}
	t.placed = x.Placed
	return t
}

// SessionOf is the launch's session step for x: the session resumed ("": fresh), its source, and whether the start
// records it.
func (l *Launcher) SessionOf(x Target, req Request) (sid, src string, record bool, err error) {
	r, err := l.sessionOf(x.target(), req)
	return r.sid, r.src, r.record, err
}

// FirstPrompt is the launch's first prompt step for x, resuming sid ("": fresh): the prompt in the start's argv, the
// wake typed once a resumed session is up, and the job whose wake it is.
func (l *Launcher) FirstPrompt(ctx context.Context, x Target, req Request, sid string) (prompt, wake, job string, err error) {
	o, err := l.firstPrompt(ctx, x.target(), req, sid)
	return o.prompt, o.wake, o.job, err
}

// Outcome is the launch's start outcome step for x, start's error err (s the start it made: its agent and pane x's).
func (l *Launcher) Outcome(ctx context.Context, x Target, s Start, err error) (*port.TrustDialog, error) {
	s.Agent, s.Pane = x.Agent, x.Pane
	return l.outcome(ctx, x.target(), s, err)
}

// Build and Built are an agent's start as the launch builds it, for the external tests of what a kind's start gets
// and gives (a plugin kind's one start call, its override files, its generated config).
type (
	Build = startSpec
	Built = startBuilt
)

// Build is buildStart.
func (l *Launcher) Build(b Build) (Built, error) { return l.buildStart(b) }

// SetEnv types the env line of e at pane's shell, as a restart does (setEnv, the start's own Env step; setup's pane with
// no agent in an existing pane takes it through a NoAgent launch).
func (l *Launcher) SetEnv(ctx context.Context, pane string, e AgentEnv) error {
	return l.setEnv(ctx, &target{agent: e.Agent, known: true, pane: pane,
		info: config.AgentInfo{Role: e.Role, Lane: e.Lane, Cwd: e.Cwd}, env: e.Vars})
}
