package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

const noConfig = "FAIL no usable .obor/config.json (obor config validate)"

const dispatchUsage = "usage: obor dispatch <T-xx> [--model <model[:effort]>,…] [--after <T-yy,T-zz>] [--force]"

// dispatchCmd is `obor dispatch <T-xx> [--model a,b] [--after …] [--force]`: a socket request to obor serve, the only
// dispatcher (T-408). The models come from the level of the spec's Difficulty line (config difficulty), the spec's
// Model: line first; --model overrides them; each may name an effort (`opus:high`: its agent started at effort high,
// T-695). It answers DISPATCHED … (queued); WAITING … (queued) while a task of --after is not merged or no lane is
// free or every model is paused (T-538, T-765), either followed by "; idle: <LANE>, …" while lanes stay free (T-483);
// or its refusal (exit 1: FAIL). --model on a task its worker works on moves it to that model (T-773): MOVING T-xx
// on <LANE>: <old> → <new> (stopping <worker>), at once: serve's merge loop stops the worker, then starts the new model
// (T-852). Serve down: FAIL obor serve is not running.
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
		case "--model", "--after":
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

// dropCmd is `obor drop <T-xx> "<why>" [--discard] [--force]`, the lead's or the owner's: a request to obor serve
// (T-439, T-775: serve knows the task's lane). A task holding a lane: DROPPING <task> from <LANE> (<why>; stopping
// <worker>) at once (T-852); serve's merge loop then stops the task's worker (a working one interrupted with Escape; one
// that keeps working only with --force, T-773), frees its lane (refused when it is dirty or its branch has commits not
// in the target, unless --discard) and records the dropped fact; a stop or a free refused is a needs-you naming the
// retry. A task that holds no lane (a waiting one, T-462): DROPPED <task>. Its refusal: FAIL; serve down: FAIL, nothing
// touched.
func dropCmd(args []string) int {
	var c wire.Call
	usage := func() int {
		fmt.Fprintln(out, `FAIL usage: obor drop <T-xx> "<why>" [--discard] [--force]`)
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
		case c.Task == "":
			c.Task = a
		case c.Note == "":
			c.Note = a
		default:
			return usage()
		}
	}
	if c.Task == "" || c.Note == "" {
		return usage()
	}
	return request(context.Background(), project.OSEnv, wire.MethodDrop, func(cfg *config.Config, id *wire.Call) error {
		if err := taskIDs(cfg, c.Task); err != nil {
			return err
		}
		c.Agent, c.Role, c.CallID, c.Root = id.Agent, id.Role, id.CallID, id.Root
		*id = c
		return nil
	})
}
