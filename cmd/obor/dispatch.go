package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

const noConfig = "FAIL no usable .obor/config.json (obor config validate)"

const dispatchUsage = "usage: obor dispatch <T-xx> [--types <type[:effort],…>] [--after <T-yy,T-zz>] [--force]"

// dispatchCmd is `obor dispatch <T-xx> [--types a,b] [--after …] [--force]`: a socket request to obor serve, the only
// dispatcher (T-408). The lane types come from the level of the spec's Difficulty line (config difficulty); --types
// overrides them; each may name an effort (`opus:high`: an opus lane, its agent started at effort high, T-695). It answers DISPATCHED … (queued); WAITING … (queued) while a task of --after is not merged or no lane
// of the level is free (T-538), either followed by "; idle: <LANE> (<type>), …" while lanes stay free (T-483); or its
// refusal (exit 1: FAIL). Serve down: FAIL obor serve is not running.
func dispatchCmd(args []string) int {
	return request(context.Background(), project.OSEnv, wire.MethodDispatch, func(cfg *config.Config, c *wire.Call) error {
		return dispatchArgs(args, cfg, c)
	})
}

// dispatchArgs is obor dispatch's arguments as the request's fields.
func dispatchArgs(args []string, cfg *config.Config, c *wire.Call) error {
	usage := errors.New(dispatchUsage)
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--force":
			c.Force = true
		case "--types", "--after":
			list := &c.Types
			if a == "--after" {
				list = &c.After
			}
			if i+1 >= len(args) || args[i+1] == "" || *list != nil {
				return usage
			}
			i++
			*list = strings.Split(args[i], ",")
			if slices.Contains(*list, "") {
				return usage
			}
		default:
			if a == "" || a[0] == '-' {
				return usage
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usage
	}
	c.Task = pos[0]
	return taskIDs(cfg, append([]string{c.Task}, c.After...)...)
}

// dropCmd is `obor drop <LANE> "<why>" [--discard] [--force]`, the lead's: a request to obor serve (T-439), which
// stops the lane's worker (a working one with --force), frees the lane (refused when it is dirty or its branch has
// commits not in the target, unless --discard), records the dropped fact and answers DROPPED <task> from <LANE>
// (<why>), or its refusal (FAIL). `obor drop <T-xx> "<why>"` drops a task that holds no lane (a waiting one, T-462):
// DROPPED <task>. Serve down: FAIL, nothing touched.
func dropCmd(args []string) int {
	var c wire.Call
	usage := func() int {
		fmt.Fprintln(out, `FAIL usage: obor drop <LANE> "<why>" [--discard] [--force] | obor drop <T-xx> "<why>"`)
		return 1
	}
	for _, a := range args {
		switch {
		case a == "--discard":
			c.Discard = true
		case a == "--force":
			c.Force = true
		case len(a) > 0 && a[0] == '-':
			return usage()
		case c.Lane == "":
			c.Lane = a
		case c.Note == "":
			c.Note = a
		default:
			return usage()
		}
	}
	if c.Lane == "" || c.Note == "" {
		return usage()
	}
	return request(context.Background(), project.OSEnv, wire.MethodDrop, func(cfg *config.Config, id *wire.Call) error {
		switch {
		case cfg.HasLane(c.Lane):
		case cfg.TaskIDOK(c.Lane) && !c.Discard: // obor drop <T-xx>: no lane to free
			c.Task, c.Lane = c.Lane, ""
		default:
			return fmt.Errorf("unknown lane %s (not in the config's lanes)", c.Lane)
		}
		c.Agent, c.Role, c.CallID, c.Root = id.Agent, id.Role, id.CallID, id.Root
		*id = c
		return nil
	})
}

// sendbackCmd is `obor sendback <LANE> "<why>"`, the lead's: the lane's open task in obor serve sent back to its worker
// by serve's own sendback transition (RV-45 #1; the job and the candidate end, serve sends the fix job). Serve's
// reply, or FAIL: serve down, no open task in the lane, or serve's refusal; nothing is touched here.
func sendbackCmd(args []string) int {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		fmt.Fprintln(out, `FAIL usage: obor sendback <LANE> "<why>"`)
		return 1
	}
	lane, ctx := args[0], context.Background()
	return request(ctx, project.OSEnv, wire.MethodSendback, func(cfg *config.Config, c *wire.Call) error {
		if !cfg.HasLane(lane) {
			return fmt.Errorf("unknown lane %s (not in the config's lanes)", lane)
		}
		task, err := serveTask(ctx, cfg, lane)
		if err != nil {
			return err
		}
		if task == "" {
			return fmt.Errorf("lane %s has no task open in obor serve", lane)
		}
		c.Task, c.Note = task, args[1]
		return nil
	})
}

// serveTask is the task obor serve's loop has open in lane ("" when none); an error when serve does not answer.
func serveTask(ctx context.Context, cfg *config.Config, lane string) (string, error) {
	ss, err := wire.Ask(ctx, wire.SocketPath(cfg.Root))
	if err != nil {
		return "", err
	}
	p := ss.Project
	if p == nil || filepath.Clean(p.Root) != filepath.Clean(cfg.Root) {
		return "", nil
	}
	for _, t := range p.Tasks {
		if t.Lane == lane && t.State != "rejected" && t.State != "dropped" { // the loop's terminal states besides merged (not listed)
			return t.Task, nil
		}
	}
	return "", nil
}
