package main

import (
	"context"
	"fmt"
	"os"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/spec"
	"obor/internal/store"
)

const specUsage = "FAIL usage: obor spec set <T-xx> <path> | obor spec show <T-xx> | obor spec list"

// specCmd is `obor spec set|show|list` (T-440): the tasks' specs live in obor serve's store. set is a request to
// serve (the lead role only); show and list read the store read-only, serve up or down.
func specCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(out, specUsage)
		return 1
	}
	switch {
	case args[0] == "set" && len(args) == 3:
		return specSet(args[1], args[2])
	case args[0] == "show" && len(args) == 2:
		return specShow(args[1])
	case args[0] == "list" && len(args) == 1:
		return specList()
	}
	fmt.Fprintln(out, specUsage)
	return 1
}

// specSet is `obor spec set <T-xx> <path>`: path (any file, read once) stored as the task's spec, a request to obor
// serve, the store's one writer. A later set with another text is the next rev (a running task's guard reads its
// protected unlocks at its next call). SET <task> rev <n> | UNCHANGED <task> rev <n>, or FAIL … exit 1 (serve down too).
// The caller is the session's (caller: a lane's cwd is a worker's whatever OBOR_ROLE says; none, the owner in a plain
// terminal, is sent as no role), and serve refuses every role but the lead and the owner.
func specSet(task, path string) int {
	return request(context.Background(), project.OSEnv, wire.MethodSpecSet, func(cfg *config.Config, c *wire.Call) error {
		if err := taskIDs(cfg, task); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read %s: %v", path, err)
		}
		c.Task, c.Spec = task, string(b)
		return nil
	})
}

// specShow is `obor spec show <T-xx>`: the task's stored spec, as set (exit 0); FAIL no spec … exit 1; exit 2 when
// the store cannot be read.
func specShow(task string) int {
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 2
	}
	row, ok, err := store.ReadSpecAt(cfg.Root, task)
	switch {
	case err != nil:
		fmt.Fprintln(out, "FAIL "+err.Error())
		return 2
	case !ok:
		fmt.Fprintf(out, "FAIL no spec for %s in obor serve's store (obor spec set %s <path>)\n", task, task)
		return 1
	}
	fmt.Fprint(out, row.Text)
	return 0
}

// specList is `obor spec list`: one line per stored spec, by task: "<task> rev <n> <set_at> <set_by> <title>".
func specList() int {
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 2
	}
	rows, err := store.ReadSpecsAt(cfg.Root)
	if err != nil {
		fmt.Fprintln(out, "FAIL "+err.Error())
		return 2
	}
	for _, r := range rows {
		fmt.Fprintf(out, "%s rev %d %s %s %s\n", r.Task, r.Rev, r.SetAt, r.SetBy, spec.Title(r.Text))
	}
	return 0
}
