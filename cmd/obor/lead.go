package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/setup"
)

// leadRun is one `obor lead` in dir (tests: a fake herdr, tune sets the launcher's hooks).
type leadRun struct {
	dir, kit string
	env      project.Env
	t        port.Terminal // nil fields: run builds the project's
	out      io.Writer
	tune     func(*launch.Launcher)
	// wake is obor start in a stopped project, its outside systems as this project's (nil: the real ones; tests fake
	// them).
	wake *sleeper
}

// lead is `obor lead [--resume]`: (re)start the project's lead (roles.lead) in a fresh session;
// --resume continues its last one. A stopped project is woken first: obor start's steps run, its STARTED said,
// and only then the lead starts; a start that fails ends obor lead with its error and starts no lead. Its agent already
// running → OK, nothing touched; an agent of that name running outside the project → FAIL. A pane labelled
// with its agent (the session ended, or Claude was quit to update it) → obor restart's start in that pane; from that
// pane itself the start runs once this command has exited (herdr starts an agent only at the shell prompt). No such
// pane → obor setup role lead's start in a new tab, fresh; only this needs roles.lead.type (a running or
// labelled untyped lead is restarted as any other).
func lead(args []string) int {
	cwd, _ := os.Getwd()
	r := leadRun{dir: cwd, kit: project.KitRoot(project.OSEnv), env: project.OSEnv, out: out}
	return r.run(context.Background(), args)
}

func (r leadRun) run(ctx context.Context, args []string) int {
	fail := func(format string, a ...any) int { fmt.Fprintf(r.out, "FAIL "+format+"\n", a...); return 1 }
	fresh := true // a fresh session unless --resume
	for _, a := range args {
		switch a {
		case "--resume":
			fresh = false
		default:
			return fail("%s", help.Usage("lead"))
		}
	}
	repo, err := project.Find(r.dir)
	if err != nil {
		return fail("not in a git repository")
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return fail("the config does not load (obor config validate)")
	}
	if r.t.Control == nil {
		t, err := terminal(cfg, r.env)
		if err != nil {
			return fail("%v", err)
		}
		r.t = t
	}
	if project.Stopped(cfg.Root) { // no lead starts in a stopped project: obor start's steps go first
		s := r.wake
		if s == nil {
			s = &sleeper{Cfg: cfg, Env: r.env, Cwd: r.dir, Out: r.out, H: r.t.Control}
		}
		if rc := s.start(ctx); rc != 0 {
			return rc
		}
	}
	agent := cfg.AgentName("lead", "")

	running, err := r.t.Setup.Agents(ctx)
	if err != nil {
		return fail("terminal agent list failed (is the terminal running?)")
	}
	// The lead that already runs is the agent in the pane labelled with its name.
	for _, a := range running {
		if a.Label == agent && !leadOurs(cfg, agent, a.Cwd) {
			return fail("%s runs in pane %s with cwd %s, outside %s (another project's agent of that name? the "+
				"config's prefix names every agent: obor setup rename changes it)", agent, a.ID, a.Cwd, cfg.Root)
		}
		if a.Label == agent {
			fmt.Fprintf(r.out, "OK %s runs (pane %s)\n", agent, a.ID)
			return 0
		}
	}

	l := &launch.Launcher{Cfg: cfg, Env: r.env, H: r.t.Control, Out: r.out}
	if r.tune != nil {
		r.tune(l)
	}
	if pane, err := l.PaneOf(ctx, agent, ""); err == nil {
		req := launch.Request{Agent: agent, Pane: pane, Fresh: fresh}
		if rc, _ := cfg.Role("lead"); fresh && rc.Type != "" { // a fresh Claude lead loads its skill itself
			lt, _ := cfg.Model(rc.Type)
			lead, _ := roles.Lookup("lead")
			req.Prompt = lead.StartPrompt(lt.Kind)
		}
		line, err := l.Restart(ctx, req)
		if err == nil && strings.HasPrefix(line, "SCHEDULED ") {
			line = fmt.Sprintf("STARTING %s in this pane (%s) once obor lead exits", agent, pane)
		}
		return launchResult(r.out, line, err)
	}
	if rc, _ := cfg.Role("lead"); rc.Type == "" {
		return fail("roles.lead is not set (a model ref, e.g. <model>[:<effort>]): a new lead pane needs it")
	}
	s := &setup.Setup{Kit: r.kit, Dir: r.dir, Env: r.env, T: r.t.Setup, Out: r.out, Starter: launcherFor(r.env, r.kit, r.t.Control, r.out, r.tune)}
	return s.Run(ctx, []string{"role", "lead", "--new-tab", "lead", "--start"})
}

// leadOurs: the running agent with cwd is this project's lead, by the launcher's ownership rule (agent names are
// machine-wide): its cwd in the main checkout, a lanes.dir worktree, the lead's configured cwd or
// another worktree of this repository. No cwd known: yes.
func leadOurs(cfg *config.Config, agent, cwd string) bool {
	if cwd == "" || project.LabelOurs(cfg, agent, cwd, "") {
		return true
	}
	if info, ok := cfg.AgentLookup(agent, ""); ok && info.Cwd != "" && project.LabelOurs(cfg, agent, cwd, info.Cwd) {
		return true
	}
	r, err := project.Find(cwd)
	return err == nil && r.Root == project.Physical(cfg.Root)
}
