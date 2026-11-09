package main

// The run's output. This file carries no build tag, so its tests run in `go test ./...`; the checks themselves
// (main.go and the *_live.go/static files) are behind the conformance tag.

import (
	"fmt"
	"io"
	"regexp"
)

// run is one conformance run: each check line printed as it comes and counted.
type run struct {
	out    io.Writer
	counts map[string]int
}

func newRun(out io.Writer) *run {
	return &run{out: out, counts: map[string]int{}}
}

// report prints one check line, `STATUS ID fact (where)`, and counts it.
func (r *run) report(status, id, fact, where string) {
	r.counts[status]++
	fmt.Fprintf(r.out, "%s %s %s (%s)\n", status, id, fact, where)
}

// note prints a line that is no check (a leftover the run could not avoid, where its scratch files are).
func (r *run) note(text string) {
	fmt.Fprintf(r.out, "note: %s\n", text)
}

func (r *run) summary() string {
	return fmt.Sprintf("conformance: %d pass, %d fail, %d skip, %d warn", r.counts["PASS"], r.counts["FAIL"], r.counts["SKIP"],
		r.counts["WARN"])
}

func (r *run) failed() bool { return r.counts["FAIL"] > 0 }

var versionRE = regexp.MustCompile(`\bv?(\d+\.\d+(?:\.\d+)?)\b`)

// versionOf is the first dotted version in a CLI's --version output ("2.1.318 (Claude Code)", "codex-cli 0.166.0",
// "herdr 0.9.3", "v2.2.0"), without a leading v; "unknown" when there is none.
func versionOf(out string) string {
	if m := versionRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return "unknown"
}
