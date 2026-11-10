package app_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/roles"
)

// Admission table: serve admits each lead's and owner's method a role may be granted (roles.Verbs) to the owner and
// the lead, refuses the built-in roles, admits a custom role whose roles.<name>.verbs names the method and refuses one
// without it, naming the verb and the config key. Each admitted call is one a later check refuses, so nothing it does
// depends on the caller.
func TestCustomRoleVerbsAdmission(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := verbsRig(t)
		if !slices.Equal(roles.Verbs, []string{wire.MethodSpecSet, wire.MethodDispatch, wire.MethodWatch, wire.MethodSend,
			wire.MethodBrief}) {
			t.Fatalf("precondition: roles.Verbs %q, want the table's methods", roles.Verbs)
		}
		type method struct {
			call    func() wire.Req // the method's own fields: what a later check refuses
			refused string          // its refusal of a caller of another role: the agent and the role go into it
			later   string          // the later check's refusal an admitted caller gets
		}
		methods := map[string]method{
			wire.MethodSpecSet: {func() wire.Req { return &wire.SpecSetReq{Task: "T-9", Spec: "no title"} },
				"spec set: only the lead or the owner sets a spec, not %s (role %s)",
				"spec set T-9: "},
			wire.MethodDispatch: {func() wire.Req {
				return &wire.DispatchReq{Task: "T-9", Lane: "L-99",
					Types: []string{"sonnet"}}
			},
				"dispatch: the lead or the owner dispatches, not %s (role %s)", "L-99"},
			wire.MethodWatch: {func() wire.Req { return &wire.WatchReq{Clear: true} },
				"watch: the lead or the owner watches, not %s (role %s)",
				"watch: --rm needs a task"},
			wire.MethodSend: {func() wire.Req { return &wire.SendReq{} },
				"send: a lead or the owner sends, not %s (role %s)", "send: a text is required"},
			wire.MethodBrief: {func() wire.Req { return &wire.BriefReq{} },
				"brief: the lead or the owner briefs, not %s (role %s)",
				"brief: the role agent is required"},
		}
		callers := []struct {
			agent, role string
			verbs       []string // the methods it is admitted to; nil: refused, as built-in (custom: with the verb named)
			custom      bool
		}{
			{"", "", roles.Verbs, false},
			{"pj-lead", "lead", roles.Verbs, false},
			{"pj-l-01", "worker", nil, false},
			{"pj-int", "integrator", nil, false},
			{"pj-rev", "reviewer", nil, false},
			{"pj-tracker", "tracker", []string{wire.MethodSpecSet, wire.MethodDispatch}, true},
			{"pj-scout", "scout", nil, true},
		}
		n := 0
		for _, who := range callers {
			for _, verb := range roles.Verbs {
				m := methods[verb]
				c := m.call()
				n++
				*c.Env() = wire.Envelope{Root: r.p.Root, Agent: who.agent, Role: who.role, CallID: fmt.Sprintf("adm-%d", n)}
				if c.Method() != verb {
					t.Fatalf("precondition: the %s row builds a %s request", verb, c.Method())
				}
				_, err := r.ask(c)
				refused := fmt.Sprintf(m.refused, app.Dash(who.agent), who.role)
				switch got := errText(err); {
				case slices.Contains(who.verbs, verb):
					if err == nil || !strings.Contains(got, m.later) {
						t.Errorf("%s (role %q) %s: %q; want admitted, then refused by %q", who.agent, who.role, verb, got, m.later)
					}
				case who.custom:
					want := refused + ": role " + who.role + " has no verb " + verb + " (add it to roles." + who.role + ".verbs)"
					if got != want {
						t.Errorf("custom %s %s: %q; want %q", who.role, verb, got, want)
					}
				default:
					if got != refused {
						t.Errorf("built-in %s %s: %q; want the refusal, %q", who.role, verb, got, refused)
					}
				}
			}
		}
	})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
