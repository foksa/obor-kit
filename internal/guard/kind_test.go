package guard

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/testkit"
)

// xKind is a fourth kind whose executable is not its name (as cursor's is cursor-agent): Claude's bridge but for its
// name and headless forms, with the probe line's context read. Registered for every test of this package: the hooks
// line lists it.
type xKind struct{ agents.Kind }

func (xKind) Name() string { return "xkind" }

func (xKind) Headless() agents.Headless {
	return agents.Headless{Programs: []string{"x-agent"}, PrintFlag: true, Flags: []string{"--prompt"}}
}

// flagKind is a kind whose only headless form is a long flag (a manifest's headless.flags): the hooks line names it.
type flagKind struct{ agents.Kind }

func (flagKind) Name() string { return "flagkind" }

func (flagKind) Headless() agents.Headless {
	return agents.Headless{Programs: []string{"f-agent"}, Flags: []string{"--prompt"}}
}

// Context is Claude's read, and the probe line's: the tests size a session with it where no compiled kind's line
// applies.
func (k xKind) Context(tail []byte) (agents.Context, bool) {
	var l struct {
		Type   string `json:"type"`
		Tokens int    `json:"tokens"`
		At     string `json:"at"`
	}
	if json.Unmarshal(bytes.TrimSpace(tail), &l) == nil && l.Type == "ctx-probe" {
		at, err := time.Parse(time.RFC3339, l.At)
		if err != nil {
			return agents.Context{}, false
		}
		return agents.Context{Tokens: l.Tokens, At: at}, true
	}
	return k.Kind.Context(tail)
}

func init() {
	k, ok := agents.Kinds{}.Lookup(agents.Default)
	if !ok {
		panic("guard tests: no " + agents.Default + " kind registered")
	}
	agents.Register(xKind{k})
	agents.Register(testkit.HookKind{}) // a lead of it takes its lines through its hooks (leadlines_test.go)
}

// The guard matches the command word against the kinds' programs, not their
// names: x-agent -p is denied (bare, by path, wrapped, nested), xkind -p is no agent run; a long flag of the kind's
// manifest (agy's --prompt) is denied either spelling, a longer flag that starts the same is not.
func TestHeadlessByProgramNotName(t *testing.T) {
	t.Parallel()
	if k, ok := (agents.Kinds{}).Lookup("xkind"); !ok || agents.Programs(k)[0] != "x-agent" {
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
		bash("lead", P, `x-agent --prompt "summarise"`, "x-agent --prompt "+why),
		bash("lead", P, `x-agent --prompt=hi`, "x-agent --prompt "+why),
		bash("lead", P, `x-agent --prompt-interactive hi`, ""),
		bash("lead", P, `x-agent --resume abc`, ""),
		bash("lead", P, `xkind -p hi`, ""),
	})
}
