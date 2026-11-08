package main

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// watchCmd is `obor watch [<set> …] [--note "<text>"] | --rm <set>`, the lead's or the owner's (T-523, T-528,
// T-666): a set is a task or a comma list of them (as dispatch's --after), one watch; one request to obor serve per
// set argument (wire.MethodWatch). --note gives those watches the lead's note: what it meant to do when a set ends, on
// the line and in `obor watch` and `obor status`. Serve's reply per set: WATCHING <set> (the lead gets
// "[obor] T-xx merged <short sha>", "[obor] T-a,T-b merged (<last short sha>)" or "[obor] T-b dropped: <why>" once it
// ends, a note after it), its outcome at once when it ended already, UNWATCHED / NOT WATCHING for --rm; with no
// argument the open watches (WATCH <set> since <age>[, waiting <tasks>][ — <note>]).
// FAIL: serve down, its refusal (an unknown task) or a usage error; exit 1 when any request failed.
func watchCmd(args []string) int {
	rm := len(args) > 0 && args[0] == "--rm"
	if rm {
		args = args[1:]
	}
	var sets []string
	var note string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--note":
			if rm || i+1 == len(args) {
				fmt.Fprintln(out, "FAIL "+help.Usage("watch"))
				return 1
			}
			i++
			note = args[i]
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(out, "FAIL "+help.Usage("watch"))
			return 1
		default:
			sets = append(sets, a)
		}
	}
	if rm && len(sets) != 1 || note != "" && len(sets) == 0 {
		fmt.Fprintln(out, "FAIL "+help.Usage("watch"))
		return 1
	}
	if len(sets) == 0 {
		return watchRequest("", false, "")
	}
	rc := 0
	for _, set := range sets {
		rc = max(rc, watchRequest(set, rm, note))
	}
	return rc
}

// watchRequest is one watch request: set's, a task or a comma list (none: the list), ended by --rm when rm, with the
// lead's note when note is not "".
func watchRequest(set string, rm bool, note string) int {
	return request(context.Background(), project.OSEnv, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		if set != "" {
			if err := taskIDs(cfg, strings.Split(set, ",")...); err != nil {
				return nil, err
			}
		}
		return &wire.WatchReq{Envelope: id, Task: set, Clear: rm, Note: note}, nil
	})
}
