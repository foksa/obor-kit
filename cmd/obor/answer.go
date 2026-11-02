package main

import (
	"context"
	"fmt"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

// answerCmd is `obor answer <T-xx> "<text>"`, the lead's or the owner's: a request to obor serve that ends the task's
// question (its Blocked: report's needs-you{asks}, in force while the worker waits) and delivers "[obor answer] <text>"
// to its worker, as its kind takes a line (posted to a Claude session, typed for the others; T-511). Serve's reply
// (ANSWERED …), or FAIL: serve down, no question open, or serve's refusal (exit 1).
func answerCmd(args []string) int {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		fmt.Fprintln(out, `FAIL usage: obor answer <T-xx> "<text>"`)
		return 1
	}
	return request(context.Background(), project.OSEnv, wire.MethodAnswer, func(cfg *config.Config, c *wire.Call) error {
		c.Task, c.Note = args[0], args[1]
		return taskIDs(cfg, args[0])
	})
}

// caller is the one identity of every CLI request to obor serve (DD-v0.11 §Design 2): OBOR_AGENT, the session's role
// (project.DetectRole: a lane's cwd is a worker's whatever OBOR_ROLE says; none, the owner in a plain terminal, is sent
// as no role), a new call id and the project; cwd is the session's. Serve decides which callers each request takes:
// the owner (no role), the lead, the integrator, a worker.
func caller(env project.Env, cwd string, cfg *config.Config) wire.Call {
	repo, _ := project.Find(cwd)
	role := project.DetectRole(repo, cfg, env).Role
	if role == "none" {
		role = ""
	}
	return wire.Call{Agent: env.Get("OBOR_AGENT"), Role: role, CallID: wire.NewCallID(), Root: cfg.Root}
}
