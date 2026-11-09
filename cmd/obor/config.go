package main

import (
	"context"
	"fmt"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/guard"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/spec"
)

// configCmd is `obor config [get <path> | validate | migrate]`: the effective config (config.json * local.json *
// defaults), one value (a string raw, anything else as JSON, null → nothing; FAIL on stderr and exit 1 when there is no such key), or the checks: WARN lines, then a FAIL line per
// problem (exit 1) or OK. Exit 3 without .obor/config.json. validate is the one strict reader (T-1081): a key this obor
// does not know is a FAIL line naming it (config.LoadForValidate), where every other command ignores it with a warning.
// migrate is config shape 2's rewrite by hand (configMigrate).
func configCmd(args []string) int {
	if len(args) == 1 && args[0] == "migrate" {
		return configMigrate()
	}
	validate := len(args) == 1 && args[0] == "validate"
	var unknown []string
	load := load
	if validate {
		load = func() (*project.Repo, *config.Config, int) {
			return loadWith(func(root string) (cfg *config.Config, err error) {
				cfg, unknown, err = config.LoadForValidate(root)
				return cfg, err
			})
		}
	}
	_, cfg, rc := load()
	if rc != 0 {
		return rc
	}
	bad := func() int { errf("%s", help.Usage("config")); return 2 }
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
	case validate:
		warns, fails := cfg.Validate(config.ValidateOptions{Overlap: spec.Overlap})
		fails = append(unknown, fails...)
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

// configMigrate is `obor config migrate` (T-1125, docs/research/config-shape.md §4.2 step 3): serve's one rewrite of a
// config.json of shape 1 by hand, for a project whose serve is not running (app.RewriteShape through configFile): the
// commit on the main branch and local.json in place, then "OK <the line serve posts to the lead>"; "OK … already" for
// a file of shape 2 (nothing written, so a second run changes nothing); FAIL with the refusal (uncommitted changes, the
// main checkout on another branch: nothing written), exit 1. The load is the hooks' (no shape-1 warning: this is the
// rewrite it points to).
func configMigrate() int {
	_, cfg, rc := loadWith(func(root string) (*config.Config, error) {
		c, _, err := config.LoadHook(root)
		return c, err
	})
	if rc != 0 {
		return rc
	}
	file := configFile{root: cfg.Root, main: cfg.MainBranch()}
	line, refused, err := app.RewriteShape(context.Background(), cfg.Root, file, "obor config migrate")
	switch {
	case err != nil:
		errf("FAIL obor config migrate: %v", err)
		return 1
	case refused != "":
		errf("FAIL obor config migrate: %s: nothing written", refused)
		return 1
	case line == "":
		fmt.Fprintf(out, "OK %s is config shape %d already\n", configPath, config.ShapeVersion)
		return 0
	}
	fmt.Fprintf(out, "OK %s\n", line)
	return 0
}

// whoami is `obor whoami [--json]`: role, agent name (when it derives one), lane (only when the role has one) and source; the lane row's task, branch and
// base, the task's stored spec rev and the protected files it unlocks, or "lane row: none".
func whoami(args []string) int {
	asJSON, canPush := false, false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--json":
		asJSON = true
	case len(args) == 1 && args[0] == "--can-push": // hidden: the pre-push hook's question for a role (T-793, T-1116)
		canPush = true
	default:
		errf("%s", help.Usage("whoami"))
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
	if canPush { // exit 0 = may push (config.MayPush), 1 = may not
		if cfg.MayPush(w.Role.Role) {
			return 0
		}
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
	if w.Agent != "" {
		fmt.Fprintf(out, "agent: %s\n", w.Agent)
	}
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
