package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/setup"
)

const leadUsage = "usage: obor lead [--resume]"

// leadRun is one `obor lead` in dir (tests: a fake herdr, tune sets the launcher's hooks).
type leadRun struct {
	dir, kit string
	env      project.Env
	h        *herdr.Socket
	out      io.Writer
	tune     func(*launch.Launcher)
}

// lead is `obor lead [--resume]` (T-479, T-553): (re)start the project's lead (roles.lead) in a fresh session;
// --resume continues its last one, and --fresh stays accepted as the default. Its agent already running → OK, nothing
// touched; an agent of that name running outside the project → FAIL
// (RV-51 #5). A pane labelled with its agent (the session ended, or Claude was quit to update it) → obor restart's
// start in that pane; from that pane itself the start runs once this command has exited (herdr starts an agent only
// at the shell prompt). No such pane → obor setup role lead's start in a new tab, fresh; only this needs
// roles.lead.type (RV-51 #4: a running or labelled untyped lead is restarted as any other).
func lead(args []string) int {
	cwd, _ := os.Getwd()
	r := leadRun{dir: cwd, kit: project.KitRoot(project.OSEnv), env: project.OSEnv, h: herdrSocket(project.OSEnv), out: out}
	return r.run(context.Background(), args)
}

func (r leadRun) run(ctx context.Context, args []string) int {
	fail := func(format string, a ...any) int { fmt.Fprintf(r.out, "FAIL "+format+"\n", a...); return 1 }
	fresh := true // T-553: a fresh session unless --resume; --fresh is the same (old notes keep working)
	for _, a := range args {
		switch a {
		case "--resume":
			fresh = false
		case "--fresh":
		default:
			return fail("%s", leadUsage)
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
	agent := cfg.AgentName("lead", "")

	running, err := r.h.Agents(ctx)
	if err != nil {
		return fail("herdr agent list failed (is herdr running?)")
	}
	for _, a := range running {
		if a.Name == agent && !leadOurs(cfg, agent, a.Cwd) {
			return fail("%s runs in pane %s with cwd %s, outside %s (another project's agent of that name? give this "+
				"project's lead its own name in roles.lead.agent)", agent, a.Pane, a.Cwd, cfg.Root)
		}
		if a.Name == agent {
			fmt.Fprintf(r.out, "OK %s runs (pane %s)\n", agent, a.Pane)
			return 0
		}
	}

	l := &launch.Launcher{Cfg: cfg, Env: r.env, H: r.h, Out: r.out}
	if r.tune != nil {
		r.tune(l)
	}
	if pane, err := l.PaneOf(ctx, agent, ""); err == nil {
		req := launch.Request{Agent: agent, Pane: pane, Fresh: fresh}
		if rc, _ := cfg.Role("lead"); fresh && rc.Type != "" { // T-594: a fresh Claude lead loads its skill itself
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
		return fail("roles.lead is not set (a model ref, e.g. \"opus:high\"): a new lead pane needs it")
	}
	s := &setup.Setup{Kit: r.kit, Dir: r.dir, Env: r.env, H: r.h, Out: r.out, Starter: launcherFor(r.env, r.kit, r.h, r.out, r.tune)}
	return s.Run(ctx, []string{"role", "lead", "--new-tab", "lead", "--start"})
}

// leadOurs: the running agent with cwd is this project's lead, by the launcher's ownership rule (T-301, RV-19 #3:
// agent names are machine-wide): its cwd in the main checkout, a lanes_dir worktree, the lead's configured cwd or
// another worktree of this repository. No cwd known: yes.
func leadOurs(cfg *config.Config, agent, cwd string) bool {
	if cwd == "" || project.LabelOurs(cfg, agent, cwd, "") {
		return true
	}
	if info, ok := project.AgentLookup(cfg, agent, ""); ok && info.Cwd != "" && project.LabelOurs(cfg, agent, cwd, info.Cwd) {
		return true
	}
	r, err := project.Find(cwd)
	return err == nil && r.Root == project.Physical(cfg.Root)
}
