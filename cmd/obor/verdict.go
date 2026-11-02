package main

import (
	"context"
	"fmt"
	"os"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

// resumeCmd is `obor resume <T-xx> [regate|reprompt] ["<decision>"]`: the lead's or the owner's end of the task's
// needs-you, sent to obor serve (its app socket) as the caller (caller: OBOR_AGENT, the session's role); the decision,
// recorded on the resume, goes to the integrator with its next job (T-511). It prints the app's reply, or FAIL with
// its refusal. The integrator's verdicts (accept, sendback, reject, resolved, hold) are its MCP tools only
// (T-437, T-470).
func resumeCmd(args []string) int {
	c, ok := wire.ResumeArgs(args)
	if !ok {
		fmt.Fprintln(out, `FAIL usage: obor resume <T-xx> [regate|reprompt] ["<decision>"]`)
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

// request is every CLI request to obor serve: the config (load), the caller's identity (caller), the command's fields
// (fill: its error is the FAIL line, nothing sent), sent to the serve holding the call's project (obor send's is
// another project's). Serve's reply printed (exit 0), or FAIL: no config, serve down or its refusal (exit 1).
func request(ctx context.Context, env project.Env, method string, fill func(*config.Config, *wire.Call) error) int {
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
	fmt.Fprintln(out, reply)
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
