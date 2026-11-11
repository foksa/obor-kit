package guard

import (
	"testing"

	"obor/internal/agents"
)

// xKind is a fourth kind whose executable is not its name (as cursor's is cursor-agent): Claude's bridge but for its
// name and headless forms. Registered for every test of this package: the hooks line lists it.
type xKind struct{ agents.Kind }

func (xKind) Name() string { return "xkind" }

func (xKind) Headless() agents.Headless {
	return agents.Headless{Programs: []string{"x-agent"}, PrintFlag: true}
}

func init() {
	k, ok := agents.Lookup(agents.Default)
	if !ok {
		panic("guard tests: no " + agents.Default + " kind registered")
	}
	agents.Register(xKind{k})
}

// The guard matches the command word against the kinds' programs, not their
// names: x-agent -p is denied (bare, by path, wrapped, nested), xkind -p is no agent run.
func TestHeadlessByProgramNotName(t *testing.T) {
	t.Parallel()
	if k, ok := agents.Lookup("xkind"); !ok || agents.Programs(k)[0] != "x-agent" {
		t.Fatalf("precondition: xkind registered with program x-agent")
	}
	f := fixture(t)
	P := f.P
	const why = "runs an agent headless; agents run in terminal panes"
	check(t, f.Project, []tc{
		bash("lead", P, `x-agent -p "summarise"`, "x-agent -p "+why),
		bash("lead", P, `x-agent --print hi`, "x-agent --print "+why),
		bash("lead", P, `/opt/bin/x-agent -cp hi`, "x-agent -cp"),
		bash("lead", P, `timeout 60 x-agent -p hi`, "x-agent -p"),
		bash("lead", P, `sh -c 'x-agent -p hi'`, "x-agent -p"),
		bash("lead", P, `env -S 'x-agent --print hi'`, "x-agent --print"),
		bash("lead", P, `x-agent --resume abc`, ""),
		bash("lead", P, `xkind -p hi`, ""),
	})
}
