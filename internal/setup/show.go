package setup

// `obor setup show` (T-647): the project's models, effort and lanes as obor init left them, and whether the owner
// confirmed them. The lead prints it at its first session and asks the owner about it; obor init ends with it too.

import (
	"strconv"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/config"
	"obor/internal/store"
)

// show is `obor setup show`: read-only, the setup table (per difficulty level each of its models with its kind,
// model id and effort, the lanes, the roles' models) and whether the owner confirmed it.
func (s *Setup) show(args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	if err := s.load(); err != nil {
		return err
	}
	s.printTable()
	return nil
}

// printTable is the setup table: one line per difficulty level, the lanes, the roles, and the owner's answer so far.
// obor init ends with it, so a new project's models, effort and lanes are visible instead of living silently in the
// config.
func (s *Setup) printTable() {
	paused := s.modelPauses()
	s.say("models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch its first free model):")
	for _, level := range []string{"hard", "medium", "simple"} {
		s.say("  %-6s → %s", level, s.levelLine(level, paused))
	}
	s.say("lanes: %s", s.lanesLine())
	s.say("roles: %s", s.rolesLine())
	if s.cfg.SetupConfirmed() {
		s.say("confirmed: yes")
	} else {
		s.say("confirmed: no")
		s.say("starting point: the lead goes through it with you at its first session")
	}
}

// modelPauses is the model pauses in force now (obor pause --model, T-765), by model: the table marks them (T-979).
// No store yet, or one that cannot be read, marks none: the table is still worth printing.
func (s *Setup) modelPauses() map[string]*event.Pause {
	db, err := store.OpenAt(s.root)
	if err != nil || db == nil {
		return nil
	}
	defer db.Close()
	paused, err := event.ModelPauses(db, time.Now())
	if err != nil {
		return nil
	}
	return paused
}

// levelLine is one level of the table: each model ref of the level, in order, with what runs it — "opus:high (claude
// opus, effort high) | deepseek (opencode opencode-go/deepseek-v4.1-flash)" — a ref naming no model of the project
// marked so, and a model a pause holds marked "(paused until cleared|HH:MM)" (T-979).
func (s *Setup) levelLine(level string, paused map[string]*event.Pause) string {
	refs := s.cfg.DifficultyList(level)
	if len(refs) == 0 {
		return "no model: a " + level + " task cannot dispatch"
	}
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		line := ref + s.modelDetail(ref)
		if name, _ := config.ParseModelRef(ref); paused[name] != nil {
			line += " (paused until " + pauseUntil(paused[name]) + ")"
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, " | ")
}

// pauseUntil is a pause's end as local HH:MM, "cleared" for none (as obor status words it, T-979).
func pauseUntil(p *event.Pause) string {
	if p.Until == 0 {
		return "cleared"
	}
	return time.Unix(p.Until, 0).Format("15:04")
}

// modelDetail is a model ref as the table shows it after its name: " (<kind> <model id>, effort <effort>)", the
// model id left out when the kind picks its own, the effort when the ref names none; " (no such model)" for an
// unknown one.
func (s *Setup) modelDetail(ref string) string {
	name, effort := config.ParseModelRef(ref)
	m, ok := s.cfg.Model(name)
	if !ok {
		return " (no such model)"
	}
	detail := m.Kind
	if m.ID != "" {
		detail += " " + m.ID
	}
	if effort != "" {
		detail += ", effort " + effort
	}
	return " (" + detail + ")"
}

// lanesLine is the lanes: "2 (L-01, L-02; each runs the model its dispatch picks)", "0" for none.
func (s *Setup) lanesLine() string {
	var names []string
	for _, l := range s.cfg.Lanes() {
		names = append(names, l.Name)
	}
	if len(names) == 0 {
		return "0"
	}
	return strconv.Itoa(len(names)) + " (" + strings.Join(names, ", ") + "; each runs the model its dispatch picks)"
}

// rolesLine is the lead's, the integrator's and the reviewer's model refs, then every other configured role's (deep
// dive, a custom role: config.RoleAgents): "lead opus:high (claude opus, effort high), integrator sonnet (claude
// sonnet), …".
func (s *Setup) rolesLine() string {
	var parts []string
	names := []string{"lead", "integrator", "reviewer"}
	for _, rc := range s.cfg.RoleAgents() {
		names = append(names, rc.Role.Name)
	}
	for _, role := range config.Unique(names...) {
		rc, _ := s.cfg.Role(role)
		part := role + " - (roles." + role + " not set)"
		switch {
		case rc.Type == "" && rc.Role.Custom: // listed only when its row is set (RoleAgents)
			part = role + " - (no model named)"
		case rc.Type != "":
			ref := config.ModelRef(rc.Type, rc.Effort)
			part = role + " " + ref + s.modelDetail(ref)
		}
		parts = append(parts, part+verbsDetail(rc.Role.Verbs))
	}
	return strings.Join(parts, ", ")
}

// verbsDetail is a custom role's verbs as the roles line shows them after its model (T-1039): " [verbs spec_set
// dispatch]", "" for none.
func verbsDetail(verbs []string) string {
	if len(verbs) == 0 {
		return ""
	}
	return " [verbs " + strings.Join(verbs, " ") + "]"
}

// initConfirmed records setup.confirmed: false when the config has no such key (T-647): the lead goes through the
// setup table with the owner at its first session. A rerun keeps the answer. wrote reports whether it added the key.
func (s *Setup) initConfirmed() (wrote bool, err error) {
	if _, ok := s.cfg.Get("setup.confirmed"); ok {
		return false, nil
	}
	if err := s.editConfig([]string{"setup", "confirmed"}, false); err != nil {
		return false, err
	}
	if err := s.load(); err != nil {
		return false, err
	}
	return true, nil
}

// confirmedLine is obor init's setup step line.
func confirmedLine(cfg *config.Config) string {
	if cfg.SetupConfirmed() {
		return "confirmed by the owner"
	}
	return "no answer yet: the lead asks the owner at its first session"
}
