package main

import (
	"fmt"

	"obor/internal/config"
	"obor/internal/guard"
	"obor/internal/project"
	"obor/internal/spec"
)

// configCmd is `obor config [get <path> | validate]`: the effective config (config.json * local.json * defaults), one
// value (a string raw, anything else as JSON, null → nothing; FAIL on stderr and exit 1 when there is no such key), or the checks: WARN lines, then a FAIL line per
// problem (exit 1) or OK. Exit 3 without .obor/config.json.
func configCmd(args []string) int {
	_, cfg, rc := load()
	if rc != 0 {
		return rc
	}
	bad := func() int { errf("usage: obor config [get <path> | validate]"); return 2 }
	switch {
	case len(args) == 0:
		s, _ := cfg.Raw(".")
		fmt.Fprintln(out, s)
	case args[0] == "get":
		if len(args) != 2 {
			return bad()
		}
		if _, ok := cfg.Lookup(args[1]); !ok {
			errf("FAIL no such key: %s", args[1])
			return 1
		}
		if s, ok := cfg.Raw(args[1]); ok {
			fmt.Fprintln(out, s)
		}
	case args[0] == "validate" && len(args) == 1:
		warns, fails := cfg.Validate(config.ValidateOptions{Overlap: spec.Overlap,
			StateAgent: func(lane string) string { return project.LaneAgent(cfg.Root, lane) }})
		for _, line := range append(warns, fails...) {
			fmt.Fprintln(out, line)
		}
		if len(fails) > 0 {
			return 1
		}
		fmt.Fprintln(out, "OK")
	default:
		return bad()
	}
	return 0
}

// whoami is `obor whoami [--json]`: role, lane (only when the role has one) and source; the lane row's task, branch and
// base, the task's stored spec rev and the protected files it unlocks, or "lane row: none".
func whoami(args []string) int {
	asJSON := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--json":
		asJSON = true
	default:
		errf("usage: obor whoami [--json]")
		return 2
	}
	repo, cfg, rc := load()
	if rc != 0 {
		return rc
	}
	w, err := guard.Whoami(repo, cfg, project.OSEnv)
	if err != nil {
		errf("%v", err)
		return 1
	}
	if asJSON {
		fmt.Fprint(out, config.Pretty(w.JSON()))
		return 0
	}
	if project.Stopped(cfg.Root) { // obor stop (T-709): the hooks stand down
		fmt.Fprintln(out, project.StoppedLine)
	}
	fmt.Fprintf(out, "role: %s\n", w.Role.Role)
	if w.Role.Lane != "" {
		fmt.Fprintf(out, "lane: %s\n", w.Role.Lane)
	}
	fmt.Fprintf(out, "source: %s\n", w.Role.Source)
	if w.State == nil {
		fmt.Fprintln(out, "lane row: none")
		return 0
	}
	fmt.Fprintf(out, "task: %s\nbranch: %s\nbase: %s\n", w.State.Task, w.State.Branch, w.State.Base)
	if w.Rev == 0 {
		fmt.Fprintf(out, "spec: none in obor serve's store\n")
		return 0
	}
	fmt.Fprintf(out, "spec: rev %d\n", w.Rev)
	for _, p := range w.Protected {
		fmt.Fprintf(out, "protected file unlocked: %s\n", p)
	}
	return 0
}
