package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/mcp"
	"obor/internal/project"
	"obor/internal/setup"
	"obor/internal/store"
)

// driveTitle is a drive's spec title before its name (internal/app's "Owner drive: ").
const driveTitle = "Owner drive: "

// driveCmd is `obor drive <name> [--model <ref>]` and `obor drive <T-xx>`: the owner iterates by hand on a
// branch in a plain interactive agent (role driver). Serve records a new drive (a new task on the first free lane) and
// answers a wire.DriveReply (waiting for the lane to be prepared); this command then starts the
// drive agent in a pane placed as a lane's (a lanes tab of the project), labelled like a lane pane, its first prompt one line saying what it
// is. On a drive already (by task id or name) serve records nothing: its agent not running, this command starts
// it again on its recorded lane and model (a failed start's retry; --model ignored); running, serve's DRIVING reply
// and OK, nothing started. A task id that is not a drive is serve's refusal (a drive is new work only). Its
// lines, then OK or FAIL (exit 1).
func driveCmd(args []string) int {
	var name, ref string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--model" && i+1 < len(args) && args[i+1] != "" && ref == "":
			i++
			ref = args[i]
		case a == "" || a[0] == '-' || name != "":
			fmt.Fprintln(out, "FAIL "+help.Usage("drive"))
			return 1
		default:
			name = a
		}
	}
	if name == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("drive"))
		return 1
	}
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 1
	}
	cwd, _ := os.Getwd()
	c := &wire.DriveReq{Envelope: caller(project.OSEnv, cwd, cfg)}
	if cfg.TaskIDOK(name) {
		c.Task = name
	} else {
		c.Name = name
	}
	if ref != "" {
		c.Types = []string{ref}
	}
	ctx := context.Background()
	reply, err := wire.Verdict(ctx, wire.SocketPath(c.Root), c)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	var d wire.DriveReply
	if err := json.Unmarshal([]byte(reply), &d); err != nil || d.Task == "" {
		fmt.Fprintf(out, "FAIL serve answered in another obor's format (it is restarting onto the new binary): the drive may be recorded; obor drive %s again in a minute\n", project.ShellQuote(name))
		return 1
	}
	fmt.Fprintln(out, d.Line())
	if d.Running { // its drive agent runs already: nothing to start
		fmt.Fprintf(out, "OK %s → %s: %s runs already (pane %s)\n", d.Task, d.Lane, d.Agent, d.Pane)
		return 0
	}
	if err := startDrive(ctx, cfg, d, project.OSEnv); err != nil {
		fmt.Fprintln(out, driveFailed(err, d.Task))
		return 1
	}
	fmt.Fprintf(out, "OK %s: %s drives in lane %s on %s; obor land %s when done\n", d.Task, d.Agent, d.Lane,
		branchOf(cfg, d.Task), d.Task)
	return 0
}

// driveFailed is the FAIL line of a drive whose start failed: the start's own message, the command that starts the
// drive again when that message does not already name it, and what ends the recorded drive.
func driveFailed(err error, task string) string {
	if project.RetryHinted(err) {
		return fmt.Sprintf("FAIL %v (the drive is recorded; obor drop %s ends it)", err, task)
	}
	return fmt.Sprintf("FAIL %v: start it again with %s (the drive is recorded; obor drop %s ends it)",
		err, project.RestartCmd("obor drive "+task, "", false, ""), task)
}

// branchOf is task's branch (branches.task).
func branchOf(cfg *config.Config, task string) string {
	return cfg.TaskBranch(task)
}

// startDrive starts d's agent: the lane prepared (serve's lane.prepare puts it on the branch; serve waits for it and
// says in d.Prepared whether it finished), then the drive agent started in a pane placed as a lane's. A drive agent already running there is left as it is.
func startDrive(ctx context.Context, cfg *config.Config, d wire.DriveReply, env project.Env) error {
	var dir string
	for _, l := range cfg.Lanes() {
		if l.Name == d.Lane {
			dir = l.Dir
		}
	}
	if dir == "" {
		return fmt.Errorf("unknown lane %s (not in the config's lanes)", d.Lane)
	}
	if !d.Prepared { // serve waited for lane.prepare and it did not finish
		return fmt.Errorf("lane %s is not prepared for %s after serve's wait (obor status says why)", d.Lane, d.Task)
	}
	kit := project.KitRoot(env)
	t, err := terminal(cfg, env)
	if err != nil {
		return err
	}
	typ, effort := config.ParseModelRef(d.Type)
	if typ == "" { // its advice is the whole start: obor drive adds no retry to it
		return project.RetryHint(fmt.Errorf("lane %s runs no model: obor drop %s, then obor drive <name> --model <model>",
			d.Lane, d.Task))
	}
	cwd, _ := os.Getwd()
	s := &setup.Setup{Kit: kit, Dir: cwd, Env: env, T: t.Setup, Out: out, McpCheck: mcp.Check,
		Launcher: launcherFor(env, kit, t.Control, out, nil)}
	return s.StartDrive(ctx, setup.Drive{Task: d.Task, Agent: d.Agent, Lane: d.Lane, Cwd: dir, Type: typ, Effort: effort,
		Prompt: drivePrompt(cfg, d)})
}

// drivePrompt is the drive agent's first prompt: one line saying what it is, that it commits on the branch as it goes,
// how it lands the branch and how it reaches the lead and the lead's answers arrive (no backticks: typed
// by herdr).
func drivePrompt(cfg *config.Config, d wire.DriveReply) string {
	return fmt.Sprintf("[obor drive] You are the owner's drive of %s in lane %s, on branch %s: work with the owner here "+
		"and commit on that branch as you go (no push). When the owner is done, obor land %s sends it through the gate, "+
		"the integrator and the merge: it commits nothing and refuses uncommitted changes. To reach this project's lead "+
		"(direction, contracts, protected docs, queued work): obor tell-lead \"<text>\"; the lead's answers arrive here "+
		"as [obor answer] lines.", d.Task, d.Lane, branchOf(cfg, d.Task), d.Task)
}

// landCmd is `obor land <T-xx|name> ["<summary>"]`: the drive's branch submitted on the owner's behalf. It
// commits nothing: a dirty lane worktree is refused with its paths. The summary is the report (default: the branch's
// commit subjects since the target); serve's reply LANDED <task>: submitted …, then the gate, the integrator and the
// merge as for any task. FAIL (exit 1): no open drive by that id or name, a dirty worktree, no commits, serve's
// refusal.
func landCmd(args []string) int {
	if len(args) < 1 || len(args) > 2 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(out, "FAIL "+help.Usage("land"))
		return 1
	}
	ctx := context.Background()
	return request(ctx, project.OSEnv, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		task, lane, err := landTask(ctx, cfg, args[0])
		if err != nil {
			return nil, err
		}
		dir := ""
		for _, l := range cfg.Lanes() {
			if l.Name == lane {
				dir = l.Dir
			}
		}
		if dir == "" {
			return nil, fmt.Errorf("%s's lane %s is not in the config's lanes", task, lane)
		}
		dirty, err := project.LaneDirt(cfg, dir, project.Git)
		if err != nil {
			return nil, fmt.Errorf("git status in lane %s: %v", lane, err)
		}
		if len(dirty) > 0 {
			return nil, fmt.Errorf("lane %s has uncommitted changes (obor land commits nothing: commit or discard them first): %s",
				lane, strings.Join(dirty, ", "))
		}
		summary := ""
		if len(args) == 2 {
			summary = strings.TrimSpace(args[1])
		}
		if summary == "" {
			log, err := project.Git(dir, "log", "--reverse", "--format=%s", cfg.MainBranch()+"..HEAD")
			if err != nil {
				return nil, fmt.Errorf("the commits of lane %s: %v", lane, err)
			}
			if summary = strings.TrimSpace(log); summary == "" {
				return nil, fmt.Errorf("%s has no commits on %s beyond %s: nothing to land", task, branchOf(cfg, task),
					cfg.MainBranch())
			}
		}
		return &wire.LandReq{Envelope: id, Task: task, Report: summary}, nil
	})
}

// landTask is the driving task obor land names (its id, or a drive's name) and its lane, from serve's status.
func landTask(ctx context.Context, cfg *config.Config, arg string) (task, lane string, err error) {
	ss, err := wire.Ask(ctx, wire.SocketPath(cfg.Root))
	if err != nil {
		return "", "", err
	}
	var driving []wire.TaskStatus
	if p := ss.Project; p != nil {
		for _, t := range p.Tasks {
			if t.State == wire.TaskDriving {
				driving = append(driving, t)
			}
		}
	}
	for _, t := range driving {
		if t.Task == arg {
			return t.Task, t.Lane, nil
		}
		if row, ok, err := store.ReadSpecAt(cfg.Root, t.Task); err == nil && ok && row.Spec.Title == driveTitle+arg {
			return t.Task, t.Lane, nil
		}
	}
	return "", "", fmt.Errorf("no open drive %s in obor serve (obor status: a drive is driving until it lands)", arg)
}

// tellLeadCmd is `obor tell-lead "<text>"` (obor lead starts the lead, so the drive's way to it has this name):
// run in a drive agent's pane (role driver, its lane from its cwd), a request to obor serve that posts
// "[obor <T-xx> drive] <text> (reply: obor resume <T-xx> "…")" to this project's lead as a lead line; the lead's obor
// resume comes back to the drive agent as an "[obor answer] …" line. Serve's reply (TOLD …), or FAIL: serve down, the
// caller no drive agent (serve names who it is for), no open drive in the lane, a drive the lead cannot answer (landed,
// not driving).
func tellLeadCmd(args []string) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("tell-lead"))
		return 1
	}
	env := project.OSEnv
	return request(context.Background(), env, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		cwd, _ := os.Getwd()
		repo, _ := project.Find(cwd)
		return &wire.TellLeadReq{Envelope: id, Lane: project.DetectRole(repo, cfg, env).Lane, Text: args[0]}, nil
	})
}
