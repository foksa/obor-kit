package help

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	"obor/internal/config"
)

// `obor kind` is a row like any other: it runs a kit kind's binary whatever kind.
func TestKindHasARow(t *testing.T) {
	t.Parallel()
	if !slices.ContainsFunc(Commands, func(c Command) bool { return c.Name == "kind" && c.Usage == "obor kind <kit kind> [args…]" }) {
		t.Errorf("Commands has no kind row, or not its usage line: %v", Commands)
	}
}

// No row's description names one agent CLI where any kind applies: obor's commands reach every kind, so a description
// naming one kind's CLI or hooks file would read as theirs alone.
func TestRowsNameNoOneAgentCLI(t *testing.T) {
	t.Parallel()
	names := append((agents.Kinds{}).Names(), slices.Sorted(maps.Keys(config.KitKinds))...)
	if len(names) < 4 {
		t.Fatalf("precondition: the kind names %v", names)
	}
	for _, c := range Commands {
		for _, name := range names {
			if strings.Contains(strings.ToLower(c.Desc), name) || strings.Contains(strings.ToLower(c.Usage), name) {
				t.Errorf("%s: names the kind %q: %q, %q", c.Name, name, c.Usage, c.Desc)
			}
		}
	}
}
