package main

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/project"
)

const watchUsage = `FAIL usage: obor watch [<T-xx>[,<T-yy>…] …] | obor watch --rm <T-xx>[,<T-yy>…]`

// watchCmd is `obor watch [<set> …] | --rm <set>`, the lead's or the owner's (T-523, T-528): a set is a task or a
// comma list of them (as dispatch's --after), one watch; one request to obor serve per argument (app.MethodWatch).
// Serve's reply per set: WATCHING <set> (the lead gets "[obor] T-xx merged <short sha>", "[obor] T-a,T-b merged
// (<last short sha>)" or "[obor] T-b dropped: <why>" once it ends), its outcome at once when it ended already,
// UNWATCHED / NOT WATCHING for --rm; with no argument the open watches (WATCH <set> since <age>[, waiting <tasks>]).
// FAIL: serve down, its refusal (an unknown task) or a usage error; exit 1 when any request failed.
func watchCmd(args []string) int {
	rm := len(args) > 0 && args[0] == "--rm"
	if rm {
		args = args[1:]
		if len(args) != 1 {
			fmt.Fprintln(out, watchUsage)
			return 1
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			fmt.Fprintln(out, watchUsage)
			return 1
		}
	}
	if len(args) == 0 {
		return watchRequest("", false)
	}
	rc := 0
	for _, set := range args {
		rc = max(rc, watchRequest(set, rm))
	}
	return rc
}

// watchRequest is one watch request: set's, a task or a comma list (none: the list), ended by --rm when rm.
func watchRequest(set string, rm bool) int {
	return request(context.Background(), project.OSEnv, app.MethodWatch, func(cfg *config.Config, call *app.Call) error {
		if set != "" {
			if err := taskIDs(cfg, strings.Split(set, ",")...); err != nil {
				return err
			}
		}
		call.Task, call.Clear = set, rm
		return nil
	})
}
