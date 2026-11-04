package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
)

const restartUsage = "usage: obor restart <agent> [--pane <id>] [--fresh | --sid <id>] [--force] [--type <type>] | " +
	"obor restart <agent> --stop [--pane <id>] [--force] | " +
	"obor restart <advisor|researcher agent> --fresh --prompt \"<brief>\""

// restart is `obor restart` (internal/launch): progress lines, then the result line last, RESTARTED / STOPPED,
// SCHEDULED (the caller's own pane: a detached run does it once the agent is idle) or FAIL (exit 1).
// A role agent restarted --fresh with a
// --prompt is a brief (T-466; not the merge-hold roles', T-734: refused, their first prompt goes by obor brief): serve records it (app.MethodBrief, no line: the start carried it) and tells the lead
// when that agent has finished; serve down is a note before the result line, not a failure.
func restart(args []string) int {
	usage := func() int { fmt.Fprintln(out, "FAIL "+restartUsage); return 1 }
	// --stop is restart's own flag, not a Request field: take it out, then let launch.ParseRequest own every flag
	// that maps to one (T-303: its write side is Request.Args, so the two cannot drift).
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
	case stopIt && (req.Fresh || req.Sid != ""):
		fmt.Fprintln(out, "FAIL --stop takes no --sid or --fresh")
		return 1
	}

	l := &launch.Launcher{Env: project.OSEnv, H: herdrSocket(project.OSEnv), Out: out}
	if _, cfg, rc := load(); rc == 0 {
		l.Cfg = cfg
		l.Job = func(ctx context.Context, lane string) (string, error) { return serveLaneJob(ctx, cfg, lane) }
	} else if rc != 3 {
		fmt.Fprintln(out, "FAIL the config does not load (obor config validate)")
		return 1
	}
	if req.Prompt != "" && l.Cfg != nil && holdsMerges(l.Cfg, req.Agent) {
		fmt.Fprintf(out, "FAIL %s takes its first prompt through obor brief %s \"<text>\" (a brief to a stopped role agent starts it fresh, after serve holds merges)\n", req.Agent, req.Agent)
		return 1
	}
	ctx := context.Background()
	var line string
	var err error
	if stopIt {
		line, err = l.Stop(ctx, req.Agent, req.Pane, req.Force)
	} else {
		line, err = l.Restart(ctx, req)
	}
	if err == nil && req.Fresh && req.Prompt != "" && l.Cfg != nil && strings.HasPrefix(line, "RESTARTED ") &&
		roleAgent(l.Cfg, req.Agent) {
		if _, berr := brief(ctx, l.Cfg, req.Agent, "", req.Prompt, project.OSEnv); berr != nil {
			fmt.Fprintf(out, "note: brief not recorded (the lead hears nothing when %s finishes): %v\n", req.Agent, berr)
		}
	}
	return launchResult(out, line, err)
}

// holdsMerges: agent is the reviewer's or the deep dive's, whose work serve holds merges for (T-734).
func holdsMerges(cfg *config.Config, agent string) bool {
	info, ok := project.AgentLookup(cfg, agent, "")
	return ok && (info.Role == "reviewer" || info.Role == "deep_dive")
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

// serveLaneJob is the open worker job of the task obor serve holds on lane ("": none): obor restart of the lane's
// worker sends its wake (T-773). serve not answering within a few seconds (no socket, an error, a timeout) or serving
// another project is no job only on a lane whose row holds no task (the store's lanes table, T-778); a lane with a task
// is an error naming serve's state, so the restart refuses before it stops anything.
func serveLaneJob(ctx context.Context, cfg *config.Config, lane string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return resolveLaneJob(cfg.Root, lane, func() (wire.Status, error) { return wire.Ask(ctx, wire.SocketPath(cfg.Root)) })
}

// resolveLaneJob is serveLaneJob over ask (serve's status).
func resolveLaneJob(root, lane string, ask func() (wire.Status, error)) (string, error) {
	st, err := ask()
	if err == nil && (st.Project == nil || filepath.Clean(st.Project.Root) != filepath.Clean(root)) {
		err = errors.New("it serves no project at " + root)
	}
	if err == nil {
		return laneJob(st, root, lane), nil
	}
	row, ok, _ := store.ReadLaneAt(root, lane)
	if !ok || row.Task == "" {
		return "", nil // no task on the lane: the plain restart
	}
	return "", fmt.Errorf("%s holds %s and obor serve cannot tell its job (%v): start obor serve (obor serve restart "+
		"for the installed service, obor serve in a pane; obor status --facts checks it), then retry, or pass "+
		"--prompt to restart without its job", lane, row.Task, err)
}

// laneJob is the open worker job (a work or fix job, never the integrator's review or resolve job) of the task st's
// project at root holds on lane; "": none.
func laneJob(st wire.Status, root, lane string) string {
	p := st.Project
	if p == nil || filepath.Clean(p.Root) != filepath.Clean(root) {
		return ""
	}
	for _, t := range p.Tasks {
		if t.Lane != lane || t.Job == "" {
			continue
		}
		step := t.Job[strings.LastIndex(t.Job, "/")+1:]
		if base, _, _ := strings.Cut(step, "."); base == event.StepWork || base == event.StepFix {
			return t.Job
		}
	}
	return ""
}
