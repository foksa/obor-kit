package main

import (
	"context"
	"fmt"
	"os"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// resumeCmd is `obor resume <T-xx> ["<text>"] [--regate|--sendback]` (T-775), the lead's or the owner's one word to a
// task, sent to obor serve (its app socket) as the caller (caller: the session's derived name and role): serve picks the
// action from the task's state (a question's answer, a stalled or stopped worker's line, a drive's line, a sendback, a
// needs-you's decision; the flags only where two fit). It prints the app's reply, or FAIL with its refusal. The
// integrator's verdicts (accept, sendback, reject, resolved, hold) are its MCP tools only (T-437, T-470).
func resumeCmd(args []string) int {
	c, ok := wire.ResumeArgs(args)
	if !ok {
		fmt.Fprintln(out, "FAIL "+help.Usage("resume"))
		return 1
	}
	return request(context.Background(), project.OSEnv, wire.MethodResume, func(cfg *config.Config, id *wire.Call) error {
		if err := taskIDs(cfg, args[0]); err != nil {
			return err
		}
		c.Agent, c.Role, c.CallID, c.Root = id.Agent, id.Role, id.CallID, id.Root
		*id = c
		return nil
	})
}

// caller is the one identity of every CLI request to obor serve (DD-v0.11 §Design 2): the session's role
// (project.DetectRole: a lane's cwd is a worker's whatever OBOR_ROLE says; none, the owner in a plain terminal, is sent
// as no role) and its name derived from it (project.SelfAgent, T-786: never OBOR_AGENT), a new call id and the
// project; cwd is the session's. Serve decides which callers each request takes:
// the owner (no role), the lead, the integrator, a worker.
func caller(env project.Env, cwd string, cfg *config.Config) wire.Call {
	repo, _ := project.Find(cwd)
	detected := project.DetectRole(repo, cfg, env)
	role := detected.Role
	if role == "none" {
		role = ""
	}
	return wire.Call{Agent: project.SelfAgent(cfg, detected), Role: role, CallID: wire.NewCallID(), Root: cfg.Root}
}

// request is every CLI request to obor serve: the config (load), the caller's identity (caller), the command's fields
// (fill: its error is the FAIL line, nothing sent), sent to the serve holding the call's project (obor send's is
// another project's). Serve's reply printed (exit 0), or FAIL: no config, serve down or its refusal (exit 1).
func request(ctx context.Context, env project.Env, method string, fill func(*config.Config, *wire.Call) error) int {
	return requestReply(ctx, env, method, fill, func(reply string) string { return reply })
}

// requestReply is request with serve's reply printed as line makes it.
func requestReply(ctx context.Context, env project.Env, method string, fill func(*config.Config, *wire.Call) error,
	line func(reply string) string) int {
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 1
	}
	cwd, _ := os.Getwd()
	c := caller(env, cwd, cfg)
	if err := fill(cfg, &c); err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	reply, err := wire.Verdict(ctx, wire.SocketPath(c.Root), method, c)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(out, line(reply))
	return 0
}

// taskIDs is the one task-id check of a request's arguments: the first that does not match task_id_pattern.
func taskIDs(cfg *config.Config, tasks ...string) error {
	for _, task := range tasks {
		if !cfg.TaskIDOK(task) {
			return fmt.Errorf("%s is no task id (task_id_pattern %s)", task, cfg.Data.TaskIDPattern)
		}
	}
	return nil
}
