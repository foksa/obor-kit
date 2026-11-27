package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/view"
)

// specCmd is `obor spec set|show|list`: the tasks' specs live in obor serve's store. set is a request to
// serve (the lead role only); show and list read the store read-only, serve up or down.
func specCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "FAIL "+help.Usage("spec"))
		return 1
	}
	switch {
	case args[0] == "set":
		if task, path, note, ok := specSetArgs(args[1:]); ok {
			return specSet(task, path, note)
		}
	case args[0] == "show":
		if task, report, ok := specShowArgs(args[1:]); ok {
			return specShow(task, report)
		}
	case args[0] == "list" && len(args) == 1:
		return specList()
	}
	fmt.Fprintln(out, "FAIL "+help.Usage("spec"))
	return 1
}

// specSetArgs is obor spec set's arguments, <T-xx> <path> [--note "<what changed>"] (the note is what
// task_done's refusal names). False: not that form.
func specSetArgs(args []string) (task, path, note string, ok bool) {
	switch {
	case len(args) == 2:
		return args[0], args[1], "", true
	case len(args) == 4 && args[2] == "--note" && strings.TrimSpace(args[3]) != "":
		return args[0], args[1], strings.TrimSpace(args[3]), true
	}
	return "", "", "", false
}

// specSet is `obor spec set <T-xx> <path> [--note "<what changed>"]`: path (any file, read once) stored as the task's
// spec, a request to obor serve, the store's one writer. A later set with another text is the next rev (a running
// task's guard reads its protected unlocks at its next call; its worker's next task_done at the older rev is refused).
// SET <task> rev <n> | UNCHANGED <task> rev <n> and the line naming the models a dispatch would try, or FAIL … exit 1
// (serve down too). The caller is the session's (caller: a lane's cwd is a worker's whatever OBOR_ROLE says; none, the
// owner in a plain terminal, is sent as no role), and serve refuses every role but the lead and the owner.
func specSet(task, path, note string) int {
	return request(context.Background(), project.OSEnv, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		if err := taskIDs(cfg, task); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %v", path, err)
		}
		return &wire.SpecSetReq{Envelope: id, Task: task, Spec: string(b), Changed: note}, nil
	})
}

// specShowArgs is spec show's arguments, <T-xx> [--report]. False: not that form.
func specShowArgs(args []string) (task string, report, ok bool) {
	switch {
	case len(args) == 1:
		return args[0], false, true
	case len(args) == 2 && args[1] == "--report":
		return args[0], true, true
	}
	return "", false, false
}

// specShow is `obor spec show <T-xx> [--report]`: the task's stored spec, as set (exit 0) or, with --report, its
// latest task_done report with its attempt and time, else why there is none: none yet (the task has not ended) or
// pruned (it ended, the log kept only its terminal fact; merged names the commit whose body carries the Summary).
// Without --report: FAIL no spec … exit 1, exit 2 when the store cannot be read.
func specShow(task string, report bool) int {
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 2
	}
	if report {
		a, err := view.Detail{Root: cfg.Root}.LatestReport(task)
		switch {
		case err != nil:
			fmt.Fprintln(out, "FAIL "+err.Error())
			return 2
		case a.Has:
			fmt.Fprintf(out, "%s attempt %d submitted %s\n\n%s\n", a.Task, a.Attempt, a.At, a.Text)
		case a.Ended == event.StepMerged && a.Merged != "":
			fmt.Fprintf(out, "%s report: pruned (merged %s on %s; the log keeps 30 days): git log -1 --format=%%B %s carries its Summary\n",
				task, a.Merged, a.EndedOn, a.Merged)
		case a.Ended == event.StepMerged:
			fmt.Fprintf(out, "%s report: pruned (merged outside obor serve on %s; the log keeps 30 days)\n", task, a.EndedOn)
		case a.Ended == event.StepDropped:
			fmt.Fprintf(out, "%s report: pruned (dropped on %s; the log keeps 30 days)\n", task, a.EndedOn)
		default:
			fmt.Fprintf(out, "%s report: none yet\n", task)
		}
		return 0
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
		fmt.Fprintf(out, "%s rev %d %s %s %s\n", r.Task, r.Rev, r.SetAt, r.SetBy, r.Spec.Title)
	}
	return 0
}
