package main

import (
	"fmt"
	"time"

	"obor/internal/checks"
)

const flakesUsage = "usage: obor flakes"

// flakesCmd is `obor flakes`: one line per flaky test (7 days, .obor/state/flakes.jsonl), read-only.
func flakesCmd(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(out, flakesUsage)
		return 0
	}
	if len(args) != 0 {
		errf("%s", flakesUsage)
		return 2
	}
	_, cfg, rc := load()
	if rc != 0 {
		return rc
	}
	entries, err := checks.Flakes(cfg.Root)
	if err != nil {
		errf("obor flakes: %v", err)
		return 1
	}
	flaky := checks.FlakyTests(cfg, entries, time.Now())
	if len(flaky) == 0 {
		fmt.Fprintln(out, "no flaky tests (7d)")
		return 0
	}
	for _, f := range flaky {
		fmt.Fprintln(out, f.Line())
	}
	return 0
}
