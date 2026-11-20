package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
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
	// initRun is obor init with no flags, which starts the lead itself (nil: the real one, in the current directory;
	// tests fake it).
	initRun func(ctx context.Context) int
}

// lead is `obor lead [--resume]`: (re)start the project's lead (roles.lead) in a fresh session;
// --resume continues its last one. A project with no .obor/config.json (or no repository) runs obor init, which
// starts the lead as its last step: obor lead returns init's rc. A stopped project is woken first: obor start's steps run, its STARTED said,
// and only then the lead starts; a start that fails ends obor lead with its error and starts no lead. Where the lead
// runs is launch's Locate (serve's identity table, the pane labels, the terminal's name): its agent already running →
// OK naming its pane (and the pane's label when a person relabelled it), nothing touched; ambiguous, not decidable
// (serve down beside an unaccounted agent of the project) or an agent of that name running outside the project →
// FAIL, no pane made. A pane labelled with its agent at its shell (the session ended, or Claude was quit to update
// it) → obor restart's start in that pane; from that pane itself the start runs once this command has exited (herdr
// starts an agent only at the shell prompt). No such pane → the caller's own pane when it qualifies (setup's
// LeadHere: no OBOR_ROLE, alone in its tab, the terminal takes it and labels it the lead's), started there as from the
// lead's own pane; else obor setup role lead's start in a new tab, fresh, its last line naming the workspace (hc: the
// space), tab and pane. Only these need roles.lead.type (a running or labelled untyped lead is restarted as any other).
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
	var cfg *config.Config
	if err == nil {
		cfg, err = config.Load(repo.Root)
	}
	if err != nil && (errors.Is(err, config.ErrNoConfig) || errors.Is(err, project.ErrNotGit)) {
		// a project never set up (or no repository yet): obor init is the one setup path, and it ends by starting
		// the lead itself
		fmt.Fprintln(r.out, "no obor config here: running obor init")
		init := r.initRun
		if init == nil {
			init = func(context.Context) int { return initCmd(nil) }
		}
		return init(ctx)
	}
	if err != nil {
		return fail("the config does not load: %v (obor config validate)", err)
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

	l := &launch.Launcher{Cfg: cfg, Env: r.env, H: r.t.Control, Out: r.out}
	if r.tune != nil {
		r.tune(l)
	}
	// The decision before anything is touched (launch's Locate): the lead runs (nothing touched), or its home pane, or
	// none (a new tab). Its lock is released here: the restart or setup's start below asks Locate again under it.
	loc, end, err := l.Locate(ctx, agent, "", true)
	if err != nil {
		return launchResult(r.out, "", err)
	}
	end()
	if loc.Runs {
		labelled := ""
		switch loc.Label {
		case agent:
		case "":
			labelled = ", unlabelled"
		default:
			labelled = ", labelled " + loc.Label
		}
		fmt.Fprintf(r.out, "OK %s runs (pane %s%s)\n", agent, loc.Pane, labelled)
		return 0
	}
	pane := loc.Pane
	if pane == "" { // no home pane: the caller's own when the terminal takes it, else a new tab
		if rc, _ := cfg.Role("lead"); rc.Type == "" {
			return fail("roles.lead is not set (a model ref, e.g. <model>[:<effort>]): a new lead pane needs it")
		}
		s := &setup.Setup{Kit: r.kit, Dir: r.dir, Env: r.env, T: r.t.Setup, Out: r.out, Starter: launcherFor(r.env, r.kit, r.t.Control, r.out, r.tune)}
		here, line, err := s.LeadHere(ctx)
		if err != nil {
			return launchResult(r.out, "", err)
		}
		if here == "" {
			fmt.Fprintf(r.out, "this pane stays as it is: %s\n", line)
			if rc := s.Run(ctx, []string{"role", "lead", "--start"}); rc != 0 {
				return rc
			}
			r.sayWhere(ctx, cfg, agent)
			return 0
		}
		fmt.Fprintf(r.out, "lead pane: %s\n", line)
		pane = here
	}
	// A fresh start passes no first prompt: launch gives it the role's, the lead's plain /obor:lead.
	req := launch.Request{Agent: agent, Pane: pane, Fresh: fresh}
	line, err := l.Restart(ctx, req)
	if err == nil && strings.HasPrefix(line, "SCHEDULED ") {
		line = fmt.Sprintf("STARTING %s in this pane (%s) once obor lead exits", agent, pane)
	}
	return launchResult(r.out, line, err)
}

// sayWhere is the last line of a lead started in a new tab: its group (herdr: the workspace, hc: the space), its tab
// and its pane, as the terminal's snapshot names them; nothing when the terminal cannot say.
func (r leadRun) sayWhere(ctx context.Context, cfg *config.Config, agent string) {
	k, _ := kindOf(cfg)
	group := k.Group
	if r.t.Panes == nil {
		return
	}
	snap, err := r.t.Panes.Snapshot(ctx)
	if err != nil {
		return
	}
	for _, p := range snap.Panes {
		if p.Label == agent {
			fmt.Fprintf(r.out, "OK %s runs in %s %s, tab lead (pane %s)\n", agent, group, cmp.Or(p.Group, "(unlabelled)"), p.ID)
			return
		}
	}
}
