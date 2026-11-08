package checks

import (
	"fmt"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
)

// Class is a merge's check class (design "Change-based merge checks (v2.5)"): partial runs merge.guard plus each class's
// checks (Names ["neutral"]: the guard only), full runs check (and baseline / gate at "merge").
type Class struct {
	Partial bool
	Names   []string // partial: every matched class, in config order (one name per class; ["neutral"]: only neutral files)
	Reason  string   // full: forced, no-guard, no-changes, unmatched <f>, full-class <name> (<f>)
}

// String is the partial classes' names joined, config order ("go+release"). "" when full.
func (c Class) String() string { return strings.Join(c.Names, "+") }

// Mode is how result lines name it: partial:<class> or full:<reason>.
func (c Class) Mode() string {
	if c.Partial {
		return "partial:" + c.String()
	}
	return "full:" + c.Reason
}

// Classify classifies a merge by its changed files. Each file gets the first merge.classes entry with a matching paths
// glob; neutral files (merge.neutral, default docs/**) are dropped first. Partial: merge.guard is set and every
// non-neutral file is in a class (told apart by entry, not name) that has checks and is not full; the names of every
// matched class, config order, are the partial's (a class matched by several entries once, by name); only neutral files
// → partial neutral. A class named neutral never counts as partial (it would read as guard-only).
func Classify(m config.Merge, files []string, forced bool) Class {
	full := func(format string, a ...any) Class { return Class{Reason: fmt.Sprintf(format, a...)} }
	switch {
	case forced:
		return full("forced")
	case m.Guard == "":
		return full("no-guard")
	case len(files) == 0:
		return full("no-changes")
	}
	neutral := m.Neutral
	if neutral == nil {
		neutral = []string{"docs/**"}
	}
	matched := make([]bool, len(m.Classes))
	for _, f := range files {
		if matchAny(f, neutral) {
			continue
		}
		idx := -1
		for i, c := range m.Classes {
			if matchAny(f, c.Paths) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return full("unmatched %s", f)
		}
		c := m.Classes[idx]
		if c.Name == "neutral" || c.Full || len(c.Checks) == 0 {
			return full("full-class %s (%s)", c.Name, f)
		}
		matched[idx] = true
	}
	var names []string
	seen := map[string]bool{}
	for i, c := range m.Classes {
		if matched[i] && !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return Class{Partial: true, Names: []string{"neutral"}}
	}
	return Class{Partial: true, Names: names}
}

// classChecks is the checks of the partial class name: exactly one class of that name, not full, with non-empty checks
// (a duplicate name must never yield an empty or wrong set); ok false otherwise.
func classChecks(m config.Merge, name string) ([]string, bool) {
	var found []config.MergeClass
	for _, c := range m.Classes {
		if c.Name == name {
			found = append(found, c)
		}
	}
	if len(found) != 1 || found[0].Full || len(found[0].Checks) == 0 {
		return nil, false
	}
	for _, c := range found[0].Checks {
		if c == "" {
			return nil, false
		}
	}
	return found[0].Checks, true
}

func matchAny(path string, globs []string) bool {
	for _, g := range globs {
		if g != "" && spec.Match(path, g) {
			return true
		}
	}
	return false
}
