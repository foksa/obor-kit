package main

import (
	"fmt"
	"os"
	"strings"

	"obor/internal/affected"
	"obor/internal/help"
)

// affectedCmd is `obor affected go [--direct] [--changed <file>]`: the Go packages the changed files (one repo-relative
// path per line; default $OBOR_CHANGED_FILES) can break, one import path per line (none: nothing affected), or one line
// "full: <why>" when the whole suite must run. Run from the module root; merge.affected_full globs force full. --direct
// asks for the changed packages only, no reverse dependencies (a lane test with promotion on, T-991).
func affectedCmd(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(out, help.Usage("affected"))
		return 0
	}
	if len(args) == 0 || args[0] != "go" {
		errf("%s", help.Usage("affected"))
		return 2
	}
	changed, direct := os.Getenv("OBOR_CHANGED_FILES"), false
	for args = args[1:]; len(args) > 0; {
		switch {
		case args[0] == "--direct":
			direct = true
			args = args[1:]
		case args[0] == "--changed" && len(args) > 1:
			changed = args[1]
			args = args[2:]
		default:
			errf("%s", help.Usage("affected"))
			return 2
		}
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
	res, err := affected.Go(".", files, cfg.Data.Merge.AffectedFull, direct)
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
