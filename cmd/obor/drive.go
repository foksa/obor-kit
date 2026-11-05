package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/herdr"
	"obor/internal/mcp"
	"obor/internal/project"
	"obor/internal/setup"
	"obor/internal/spec"
	"obor/internal/store"
)

// driveTitle is a drive's spec title before its name (internal/app's "Owner drive: ").
const driveTitle = "Owner drive: "

// driveWait bounds how long obor drive waits for serve to prepare a new drive's lane before it starts the agent.
var driveWait = 60 * time.Second

// driveCmd is `obor drive <name> [--model <ref>]` and `obor drive <T-xx>` (T-751): the owner iterates by hand on a
// branch in a plain interactive agent (role driver). Serve records a new drive (a new task on the first free lane) and
// answers DRIVE <task> → <lane> (<agent>, <model>); this command then waits for the lane to be prepared and starts the
// drive agent in a pane split in the caller's tab, labelled like a lane pane, its first prompt one line saying what it
// is. On a drive already (by task id or name; T-755) serve records nothing: its agent not running, this command starts
// it again on its recorded lane and model (a failed start's retry; --model ignored); running, serve's DRIVING reply
// and OK, nothing started. A task id that is not a drive is serve's refusal (a drive is new work only, T-912). Its
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
	c := caller(project.OSEnv, cwd, cfg)
	if cfg.TaskIDOK(name) {
		c.Task = name
	} else {
		c.Note = name
	}
	if ref != "" {
		c.Types = []string{ref}
	}
	ctx := context.Background()
	reply, err := wire.Verdict(ctx, wire.SocketPath(c.Root), wire.MethodDrive, c)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(out, reply)
	d, ok := parseDrive(reply)
	if !ok && strings.HasPrefix(reply, drivingPrefix) { // its drive agent runs already: nothing to start
		fmt.Fprintln(out, "OK "+strings.TrimPrefix(reply, drivingPrefix))
		return 0
	}
	if !ok {
		fmt.Fprintf(out, "FAIL cannot read serve's reply %q\n", reply)
		return 1
	}
	if err := startDrive(ctx, cfg, d, project.OSEnv); err != nil {
		fmt.Fprintf(out, "FAIL %v (the drive is recorded: retry with obor drive %s, which starts its agent again; obor drop %s ends it)\n",
			err, d.task, d.task)
		return 1
	}
	fmt.Fprintf(out, "OK %s: %s drives in lane %s on %s; obor land %s when done\n", d.task, d.agent, d.lane,
		branchOf(cfg, d.task), d.task)
	return 0
}

// drivingPrefix starts serve's reply to obor drive on a drive whose agent runs already (DRIVING <task> → <lane>: …).
const drivingPrefix = "DRIVING "

// drive is serve's DRIVE reply: the task, its lane, the drive agent and its type reference.
type drive struct{ task, lane, agent, ref string }

var driveReplyRE = regexp.MustCompile(`^DRIVE (\S+) → (\S+) \((\S+), (\S+)\)`)

// parseDrive reads serve's DRIVE reply to a drive request (its tail, a retry's " again: …", not read).
func parseDrive(reply string) (drive, bool) {
	m := driveReplyRE.FindStringSubmatch(strings.TrimSpace(reply))
	if m == nil {
		return drive{}, false
	}
	return drive{task: m[1], lane: m[2], agent: m[3], ref: m[4]}, true
}

// branchOf is task's branch (branches.task).
func branchOf(cfg *config.Config, task string) string {
	return cfg.TaskBranch(task)
}

// startDrive starts d's agent: the lane prepared (serve's lane.prepare puts it on the branch; waited for up to
// driveWait), then the drive agent started in the caller's tab. A drive agent already running there is left as it is.
func startDrive(ctx context.Context, cfg *config.Config, d drive, env project.Env) error {
	var dir string
	for _, l := range cfg.Lanes() {
		if l.Name == d.lane {
			dir = l.Dir
		}
	}
	if dir == "" {
		return fmt.Errorf("unknown lane %s (not in the config's lanes)", d.lane)
	}
	if err := waitPrepared(ctx, cfg, d); err != nil {
		return err
	}
	kit := project.KitRoot(env)
	h := &herdr.Socket{Path: herdr.SocketPath(env)}
	ref := d.ref
	if ref == "-" {
		ref = ""
	}
	typ, effort := config.ParseModelRef(ref)
	if typ == "" {
		return fmt.Errorf("lane %s runs no model: obor drop %s, then obor drive <name> --model <model>", d.lane, d.task)
	}
	cwd, _ := os.Getwd()
	s := &setup.Setup{Kit: kit, Dir: cwd, Env: env, H: h, Out: out, McpCheck: mcp.Check,
		Starter: launcherFor(env, kit, h, out, nil)}
	return s.StartDrive(ctx, setup.Drive{Agent: d.agent, Lane: d.lane, Cwd: dir, Type: typ, Effort: effort,
		Prompt: drivePrompt(cfg, d)})
}

// drivePrompt is the drive agent's first prompt: one line saying what it is, that it commits on the branch as it goes,
// how it lands the branch and how it reaches the lead and the lead's answers arrive (T-761, T-762; no backticks: typed
// by herdr).
func drivePrompt(cfg *config.Config, d drive) string {
	return fmt.Sprintf("[obor drive] You are the owner's drive of %s in lane %s, on branch %s: work with the owner here "+
		"and commit on that branch as you go (no push). When the owner is done, obor land %s sends it through the gate, "+
		"the integrator and the merge: it commits nothing and refuses uncommitted changes. To reach this project's lead "+
		"(direction, contracts, protected docs, queued work): obor tell-lead \"<text>\"; the lead's answers arrive here "+
		"as [obor answer] lines.", d.task, d.lane, branchOf(cfg, d.task), d.task)
}

// waitPrepared waits until d's lane row holds d.task prepared (its base recorded): serve's lane.prepare ran.
func waitPrepared(ctx context.Context, cfg *config.Config, d drive) error {
	deadline := time.Now().Add(driveWait)
	for {
		row, ok, err := store.ReadLaneAt(cfg.Root, d.lane)
		if err == nil && ok && row.Task == d.task && row.Base != "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lane %s is not prepared for %s after %s (obor status says why)", d.lane, d.task, driveWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// landCmd is `obor land <T-xx|name> ["<summary>"]` (T-751): the drive's branch submitted on the owner's behalf. It
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
	return request(ctx, project.OSEnv, wire.MethodLand, func(cfg *config.Config, c *wire.Call) error {
		task, lane, err := landTask(ctx, cfg, args[0])
		if err != nil {
			return err
		}
		dir := ""
		for _, l := range cfg.Lanes() {
			if l.Name == lane {
				dir = l.Dir
			}
		}
		if dir == "" {
			return fmt.Errorf("%s's lane %s is not in the config's lanes", task, lane)
		}
		if dirty := dirtyPaths(dir); len(dirty) > 0 {
			return fmt.Errorf("lane %s has uncommitted changes (obor land commits nothing: commit or discard them first): %s",
				lane, strings.Join(dirty, ", "))
		}
		summary := ""
		if len(args) == 2 {
			summary = strings.TrimSpace(args[1])
		}
		if summary == "" {
			log, err := project.Git(dir, "log", "--reverse", "--format=%s", cfg.Data.Branches.Main+"..HEAD")
			if err != nil {
				return fmt.Errorf("the commits of lane %s: %v", lane, err)
			}
			if summary = strings.TrimSpace(log); summary == "" {
				return fmt.Errorf("%s has no commits on %s beyond %s: nothing to land", task, branchOf(cfg, task),
					cfg.Data.Branches.Main)
			}
		}
		c.Task, c.Report = task, summary
		return nil
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
		if row, ok, err := store.ReadSpecAt(cfg.Root, t.Task); err == nil && ok && spec.Title(row.Text) == driveTitle+arg {
			return t.Task, t.Lane, nil
		}
	}
	return "", "", fmt.Errorf("no open drive %s in obor serve (obor status: a drive is driving until it lands)", arg)
}

// dirtyPaths is the lane worktree's uncommitted paths (git status --porcelain), untracked ones too.
func dirtyPaths(dir string) []string {
	status, err := project.Git(dir, "status", "--porcelain")
	if err != nil {
		return []string{"(git status failed: " + err.Error() + ")"}
	}
	var paths []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 {
			paths = append(paths, line[3:])
		}
	}
	return paths
}

// tellLeadCmd is `obor tell-lead "<text>"` (T-761; obor lead starts the lead, so the drive's way to it has this name):
// run in a drive agent's pane (role driver, its lane from its cwd), a request to obor serve that posts
// "[obor <T-xx> drive] <text> (reply: obor resume <T-xx> "…")" to this project's lead as a lead line; the lead's obor
// resume comes back to the drive agent as an "[obor answer] …" line. Serve's reply (TOLD …), or FAIL: serve down, the
// caller no drive agent (serve names who it is for), no open drive in the lane, a drive the lead cannot answer (T-764:
// landed, not driving).
func tellLeadCmd(args []string) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("tell-lead"))
		return 1
	}
	env := project.OSEnv
	return request(context.Background(), env, wire.MethodTellLead, func(cfg *config.Config, c *wire.Call) error {
		cwd, _ := os.Getwd()
		repo, _ := project.Find(cwd)
		c.Lane, c.Note = project.DetectRole(repo, cfg, env).Lane, args[0]
		return nil
	})
}
