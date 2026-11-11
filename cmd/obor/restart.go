package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
)

// restart is `obor restart` (internal/launch): progress lines, then the result line last, RESTARTED / STOPPED,
// SCHEDULED (the caller's own pane: a detached run does it once the agent is idle) or FAIL (exit 1).
// A first prompt, a model and an effort are serve's (a brief, a dispatch), never a flag here; the flags restart
// does not take are unknown, so they get the usage.
func restart(args []string) int {
	usage := func() int { fmt.Fprintln(out, "FAIL "+help.Usage("restart")); return 1 }
	// --stop is restart's own flag, not a Request field: take it out, then let launch.ParseRequest own every flag
	// that maps to one (its write side is Request.Args, so the two cannot drift).
	stopIt, rest := false, make([]string, 0, len(args))
	for _, a := range args {
		if a == "--stop" {
			stopIt = true
		} else {
			rest = append(rest, a)
		}
	}
	req, perr := launch.ParseRequest(rest)
	if perr != nil {
		return usage()
	}
	switch {
	case req.Agent == "":
		return usage()
	case stopIt && req.Fresh:
		fmt.Fprintln(out, "FAIL --stop takes no --fresh")
		return 1
	}

	_, cfg, rc := load()
	if rc != 0 && rc != 3 {
		fmt.Fprintln(out, "FAIL the config does not load (obor config validate)")
		return 1
	}
	t, err := terminal(cfg, project.OSEnv)
	if err != nil {
		fmt.Fprintln(out, "FAIL "+err.Error())
		return 1
	}
	l := &launch.Launcher{Env: project.OSEnv, H: t.Control, Out: out, Cfg: cfg}
	if rc == 0 {
		l.Job = func(ctx context.Context, role, lane string) (string, error) {
			return serveAgentJob(ctx, cfg, req.Agent, role, lane)
		}
	}
	ctx := context.Background()
	var line string
	if stopIt {
		line, err = l.Stop(ctx, req.Agent, req.Pane, req.Force)
	} else {
		line, err = l.Restart(ctx, req)
	}
	return launchResult(out, line, err)
}

// launchResult prints a launch's result (obor restart): the evidence of a give-up, then the result line
// or FAIL (1).
func launchResult(w io.Writer, line string, err error) int {
	var fail project.Fail
	if ev := launch.Evidence(err); ev != "" {
		fmt.Fprintln(w, ev)
	}
	switch {
	case errors.As(err, &fail):
		fmt.Fprintln(w, "FAIL "+string(fail))
		return 1
	case err != nil:
		fmt.Fprintf(w, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(w, line)
	return 0
}

// serveAgentJob is the job agent's (role's; a lane's worker on lane, the integrator's lane is "") restart carries as its
// wake ("": none): serve's answer, by the rule its job_next hands jobs out (the agent's oldest open job, never a
// held one). serve not answering within a few seconds (no socket, an error, a timeout) refuses the restart where serve
// may hold a job: for the integrator always (no lane row says otherwise), for a lane whose row holds a task.
func serveAgentJob(ctx context.Context, cfg *config.Config, agent, role, lane string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return resolveAgentJob(cfg.Root, agent, role, lane, func(c wire.RestartJobReq) (string, error) {
		return wire.Verdict(ctx, wire.SocketPath(cfg.Root), &c)
	})
}

// resolveAgentJob is serveAgentJob over ask (serve's restart_job).
func resolveAgentJob(root, agent, role, lane string, ask func(wire.RestartJobReq) (string, error)) (string, error) {
	job, err := ask(wire.RestartJobReq{Envelope: wire.Envelope{Root: root, Agent: agent, Role: role}, Lane: lane})
	if err == nil {
		return job, nil
	}
	if role == "integrator" {
		return "", project.Failf("restart %s: obor serve is not running; it may hold an open job for %s: start serve, "+
			"then restart", agent, agent)
	}
	row, ok, rerr := store.ReadLaneAt(root, lane)
	if rerr != nil { // no proof the lane holds nothing: it may hold a task whose job serve would give
		return "", fmt.Errorf("%s: its state row cannot be read (%v), so obor serve may hold a job for it: start obor "+
			"serve, then retry", lane, rerr)
	}
	if !ok || row.Task == "" {
		return "", nil // no task on the lane: the plain restart
	}
	return "", fmt.Errorf("%s holds %s and obor serve cannot tell its job (%v): start obor serve (obor serve restart "+
		"for the installed service, obor serve in a pane; obor status --facts checks it), then retry", lane, row.Task, err)
}
