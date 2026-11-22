package guard

import (
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
)

// Who is `obor whoami`: the session's role and lane, its agent name (config.Self: derived; "": none, e.g. a role the
// config does not know), its lane row, and the protected files its task's spec unlocks.
type Who struct {
	Role      project.Role
	Agent     string
	State     *store.LaneRow // nil: no lane row
	Rev       int            // the stored spec's rev (0: none)
	Protected []string       // the paths the spec names under "Protected files this task may edit"
	Lines     string         // a lead's line delivery (LinesInbox, LinesHooks, LinesStatus); "": not a lead
}

// How lines reach a lead (skills/lead/SKILL.md "Lines to you"), from its kind's caps: a posted message in its inbox,
// the answers of its hooks, or only `obor status`.
const (
	LinesInbox  = "inbox"
	LinesHooks  = "hooks"
	LinesStatus = "status"
)

// leadLines is the delivery of the lines of a lead running as agent in cwd, its kind read as the session start reads
// it (sessionKind; a plugin kind's cached manifest, none cached: not a kind it knows): an inbox kind's lines are
// posted there; else a kind whose hooks act on the answers hands them at a prompt or a stop; else they wait.
func leadLines(cfg *config.Config, agent, cwd string) string {
	k, ok := cfg.Kind(sessionKind(cfg, agent, cwd))
	switch {
	case ok && k.Caps().Inbox:
		return LinesInbox
	case ok && k.Caps().HookAnswers:
		return LinesHooks
	}
	return LinesStatus
}

// Whoami reads the role of repo's directory, its lane row and its task's spec (read-only); a lanes or specs table that
// cannot be read is an error.
func Whoami(repo *project.Repo, cfg *config.Config, env project.Env) (*Who, error) {
	w := &Who{Role: project.DetectRole(repo, cfg, env)}
	w.Agent = project.SelfAgent(cfg, w.Role)
	if w.Role.Role == "lead" {
		w.Lines = leadLines(cfg, w.Agent, repo.Top)
	}
	if w.Role.Lane == "" {
		return w, nil
	}
	st, ok, err := store.ReadLaneAt(cfg.Root, w.Role.Lane)
	if err != nil {
		return nil, err
	}
	if !ok {
		return w, nil
	}
	w.State = &st
	if st.Task != "" {
		sp, ok, err := store.ReadSpecAt(cfg.Root, st.Task)
		if err != nil {
			return nil, err
		}
		if ok {
			w.Rev, w.Protected = sp.Rev, sp.Spec.Protected
		}
	}
	return w, nil
}

// JSON is `obor whoami --json`: role, its agent name when it derives one, lane, source; the lane row's task, branch and base when it has one, the spec's
// rev when stored and the protected files it unlocks when it names any.
func (w *Who) JSON() any {
	type out struct {
		Role      string   `json:"role"`
		Agent     string   `json:"agent,omitempty"`
		Lane      string   `json:"lane"`
		Source    string   `json:"source"`
		Task      *string  `json:"task,omitempty"`
		Branch    *string  `json:"branch,omitempty"`
		Base      *string  `json:"base,omitempty"`
		Rev       int      `json:"rev,omitempty"`
		Protected []string `json:"protected,omitempty"`
	}
	o := out{Role: w.Role.Role, Agent: w.Agent, Lane: w.Role.Lane, Source: w.Role.Source, Rev: w.Rev, Protected: w.Protected}
	if w.State != nil {
		o.Task, o.Branch, o.Base = &w.State.Task, &w.State.Branch, &w.State.Base
	}
	return o
}
