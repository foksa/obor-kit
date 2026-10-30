package main

import (
	"fmt"
	"os"
	"strings"

	"obor/internal/affected"
)

const affectedUsage = "usage: obor affected go [--changed <file>]"

// affectedCmd is `obor affected go [--changed <file>]`: the Go packages the changed files (one repo-relative path per
// line; default $OBOR_CHANGED_FILES) can break, one import path per line (none: nothing affected), or one line
// "full: <why>" when the whole suite must run. Run from the module root; merge.affected_full globs force full.
func affectedCmd(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(out, affectedUsage)
		return 0
	}
	var changed string
	switch {
	case len(args) == 1 && args[0] == "go":
		changed = os.Getenv("OBOR_CHANGED_FILES")
	case len(args) == 3 && args[0] == "go" && args[1] == "--changed":
		changed = args[2]
	default:
		errf("%s", affectedUsage)
		return 2
	}
	_, cfg, rc := load()
	if rc != 0 {
		return rc
	}
	if changed == "" {
		fmt.Fprintln(out, "full: OBOR_CHANGED_FILES not set")
		return 0
	}
	data, err := os.ReadFile(changed)
	if err != nil {
		fmt.Fprintf(out, "full: OBOR_CHANGED_FILES unreadable (%v)\n", err)
		return 0
	}
	var files []string
	for _, f := range strings.Split(string(data), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	res, err := affected.Go(".", files, cfg.Data.Merge.AffectedFull)
	switch {
	case err != nil:
		errf("obor affected: %v", err)
		return 1
	case res.Full != "":
		fmt.Fprintln(out, "full: "+res.Full)
	default:
		for _, p := range res.Pkgs {
			fmt.Fprintln(out, p)
		}
	}
	return 0
}
