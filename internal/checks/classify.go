package checks

import (
	"fmt"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
)

// Class is a merge's check class (design "Change-based merge checks (v2.5)"): partial runs merge.by_path.guard plus
// each class's checks (Names ["neutral"]: the guard only), full runs check plus the checks of every matched class
// marked also_on_full (T-1007).
type Class struct {
	Partial bool
	// Names are every matched class, in config order (one name per class; ["neutral"]: only neutral files; full: those
	// whose with_full checks run after check).
	Names  []string
	Reason string // full: forced, no-guard, no-changes, unmatched <f>, full-class <name> (<f>)
}

// String is the classes' names joined, config order ("go+release"). "" when none matched (a forced, no-guard or
// no-changes full).
func (c Class) String() string { return strings.Join(c.Names, "+") }

// Mode is how result lines name it: partial:<class> or full:<reason>.
func (c Class) Mode() string {
	if c.Partial {
		return "partial:" + c.String()
	}
	return "full:" + c.Reason
}

// Classify classifies a merge by its changed files. Each file gets the first merge.by_path.rules entry with a matching
// paths glob; neutral files (merge.by_path.no_check, default docs/**) are dropped first. Partial: merge.by_path.guard
// is set and every non-neutral file is in a class (told apart by entry, not name) that has checks and is not full; the
// names of every matched class, config order, are the partial's (a class matched by several entries once, by name);
// only neutral files → partial neutral. A class named neutral never counts as partial (it would read as guard-only).
//
// Full keeps the matched class names too (T-1007): runChecks runs the checks of those marked with_full after check. A
// forced or no-guard merge records its reason first and scans the files anyway (T-1008): its matched classes keep their
// with_full checks.
func Classify(m config.ByPath, files []string, forced bool) Class {
	reason := "" // the full reason: forced or no-guard before any file, else the first file that decides full
	switch {
	case forced:
		reason = "forced"
	case m.Guard == "":
		reason = "no-guard"
	case len(files) == 0:
		return Class{Reason: "no-changes"}
	}
	neutral := m.NoCheck
	if neutral == nil {
		neutral = []string{"docs/**"}
	}
	matched := make([]bool, len(m.Rules))
	for _, f := range files {
		if matchAny(f, neutral) {
			continue
		}
		idx := -1
		for i, c := range m.Rules {
			if matchAny(f, c.Paths) {
				idx = i
				break
			}
		}
		if idx < 0 {
			if reason == "" {
				reason = fmt.Sprintf("unmatched %s", f)
			}
			continue
		}
		c := m.Rules[idx]
		if reason == "" && (c.Name == "neutral" || c.Full || len(c.Run) == 0) {
			reason = fmt.Sprintf("full-class %s (%s)", c.Name, f)
		}
		matched[idx] = true
	}
	var names []string
	seen := map[string]bool{}
	for i, c := range m.Rules {
		if matched[i] && !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	if reason != "" {
		return Class{Names: names, Reason: reason}
	}
	if len(names) == 0 {
		return Class{Partial: true, Names: []string{"neutral"}}
	}
	return Class{Partial: true, Names: names}
}

// classChecks is the checks of the partial class name: exactly one class of that name, not full, with non-empty checks
// (a duplicate name must never yield an empty or wrong set); ok false otherwise.
func classChecks(m config.ByPath, name string) ([]string, bool) {
	var found []config.PathRule
	for _, c := range m.Rules {
		if c.Name == name {
			found = append(found, c)
		}
	}
	if len(found) != 1 || found[0].Full || len(found[0].Run) == 0 {
		return nil, false
	}
	for _, c := range found[0].Run {
		if c == "" {
			return nil, false
		}
	}
	return found[0].Run, true
}

// withFullChecks are the checks a full merge runs after check for the class name: exactly one class of that name, marked
// with_full and not full (obor config validate refuses that pair); nil otherwise (T-1007).
func withFullChecks(m config.ByPath, name string) []string {
	var found []config.PathRule
	for _, c := range m.Rules {
		if c.Name == name {
			found = append(found, c)
		}
	}
	if len(found) != 1 || !found[0].AlsoOnFull || found[0].Full {
		return nil
	}
	return found[0].Run
}

func matchAny(path string, globs []string) bool {
	for _, g := range globs {
		if g != "" && spec.Match(path, g) {
			return true
		}
	}
	return false
}
