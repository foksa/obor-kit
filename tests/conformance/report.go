package main

// The run's output and its versions file. This file carries no build tag, so its tests run in `go test ./...`; the
// checks themselves (main.go and the *_live.go/static files) are behind the conformance tag.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// versionsFile is where a run records the versions it checked (deep dive §6: `obor status` can then name an installed
// version no run has passed), relative to the repository root.
const versionsFile = ".obor/state/conformance.json"

// run is one conformance run: each check line printed as it comes, counted, and tallied per kind for the versions file.
type run struct {
	out    io.Writer
	counts map[string]int
	kind   string // the kind the next lines count for ("": none)
	kinds  map[string]*kindResult
}

// kindResult is one kind's run of one tier, as the versions file keeps it.
type kindResult struct {
	Version string `json:"version"` // the version this run checked
	At      string `json:"at"`      // when (RFC 3339, UTC)
	Pass    int    `json:"pass"`
	Fail    int    `json:"fail"`
	Skip    int    `json:"skip"`
	Warn    int    `json:"warn"`
	// Passed is the last version a run of this tier passed: at least one PASS and no FAIL. A failed run keeps the
	// earlier one.
	Passed string `json:"passed,omitempty"`
}

// versions is the versions file: kind → tier → its last run.
type versions struct {
	Kinds map[string]map[string]kindResult `json:"kinds"`
}

func newRun(out io.Writer) *run {
	return &run{out: out, counts: map[string]int{}, kinds: map[string]*kindResult{}}
}

// begin makes the next lines count for kind, whose tool is at version ("": not installed, never recorded).
func (r *run) begin(kind, version string) {
	r.kind = kind
	if _, ok := r.kinds[kind]; !ok {
		r.kinds[kind] = &kindResult{Version: version}
	}
	if version != "" {
		r.kinds[kind].Version = version
	}
}

// report prints one check line, `STATUS ID fact (where)`, and counts it.
func (r *run) report(status, id, fact, where string) {
	r.counts[status]++
	if k := r.kinds[r.kind]; k != nil {
		switch status {
		case "PASS":
			k.Pass++
		case "FAIL":
			k.Fail++
		case "SKIP":
			k.Skip++
		case "WARN":
			k.Warn++
		}
	}
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

// save merges this run's kinds (those with a version) into the versions file at path under tier; other kinds and
// tiers stay as they were.
func (r *run) save(path, tier string, at time.Time) error {
	var v versions
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &v); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
	}
	if v.Kinds == nil {
		v.Kinds = map[string]map[string]kindResult{}
	}
	names := make([]string, 0, len(r.kinds))
	for name := range r.kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		k := *r.kinds[name]
		if k.Version == "" {
			continue
		}
		if v.Kinds[name] == nil {
			v.Kinds[name] = map[string]kindResult{}
		}
		k.At = at.UTC().Format(time.RFC3339)
		k.Passed = v.Kinds[name][tier].Passed
		if k.Fail == 0 && k.Pass > 0 {
			k.Passed = k.Version
		}
		v.Kinds[name][tier] = k
	}
	b, err = json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var versionRE = regexp.MustCompile(`\bv?(\d+\.\d+(?:\.\d+)?)\b`)

// versionOf is the first dotted version in a CLI's --version output ("2.1.318 (Claude Code)", "codex-cli 0.166.0",
// "herdr 0.9.3", "v2.2.0"), without a leading v; "unknown" when there is none.
func versionOf(out string) string {
	if m := versionRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return "unknown"
}
