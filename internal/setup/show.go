package setup

// `obor setup show` (T-647): the project's models, effort and lanes as obor init left them, and whether the owner
// confirmed them. The lead prints it at its first session and asks the owner about it; obor init ends with it too.

import (
	"strings"

	"obor/internal/config"
)

// show is `obor setup show`: read-only, the setup table (per difficulty level each of its lane types with its model,
// effort and lanes, the roles' types) and whether the owner confirmed it.
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

// printTable is the setup table: one line per difficulty level, the roles, and the owner's answer so far. obor init
// ends with it, so a new project's models, effort and lanes are visible instead of living silently in the config.
func (s *Setup) printTable() {
	s.say("models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch the lane):")
	for _, level := range []string{"hard", "medium", "simple"} {
		s.say("  %-6s → %s", level, s.levelLine(level))
	}
	s.say("roles: %s", s.rolesLine())
	if s.cfg.SetupConfirmed() {
		s.say("confirmed: yes")
	} else {
		s.say("confirmed: no")
		s.say("starting point: the lead goes through it with you at its first session")
	}
}

// levelLine is one level of the table: each type the project has for the level, in order, with its own model and
// effort and its own lanes — "deepseek (opencode): D-01, D-02 | sonnet (sonnet, effort medium): S-01" — and "no lane"
// as the lanes of a type no configured lane has: "opus-high (opus, effort high): no lane" (T-653). A type the project
// has no lane_type for is never named (T-647). A level none of whose types has a lane names the T-631 fallback
// ("… | no simple lane: fallback sonnet"), and one with no lane anywhere says the task waits.
func (s *Setup) levelLine(level string) string {
	types := s.existingTypes(s.cfg.DifficultyList(level))
	if len(types) == 0 { // only another level's types can run it, if any
		if fb := s.fallbackTypes(level); len(fb) > 0 {
			return "no lane_type this level lists: " + s.lanesOfTypes(fb) + " | fallback " + strings.Join(fb, ", ")
		}
		return "no lane_type this level lists: a " + level + " task waits for one"
	}
	parts := make([]string, 0, len(types))
	withLane := false
	for _, t := range types {
		lanes := s.lanesOfType(t)
		if lanes == "" {
			lanes = "no lane"
		} else {
			withLane = true
		}
		parts = append(parts, t+s.typeDetail(t)+": "+lanes)
	}
	line := strings.Join(parts, " | ")
	if withLane {
		return line
	}
	if fb := s.fallbackTypes(level); len(fb) > 0 { // T-631: no lane of any type this level lists
		return line + " | no " + level + " lane: fallback " + strings.Join(fb, ", ")
	}
	return line + ": a " + level + " task waits for one"
}

// typeDetail is a lane type as the table shows it after the type's name: its model and effort as its args name them
// ("(opus, effort high)"); nothing for a type whose args name neither.
func (s *Setup) typeDetail(name string) string {
	if detail := s.typeModelEffort(name); detail != "" {
		return " (" + detail + ")"
	}
	return ""
}

// typeModelEffort is a lane type's model and effort as its args name them ("opus, effort high"), the kind in the
// model's place when the args name no model ("opencode"): the table shows what runs and never guesses a model.
func (s *Setup) typeModelEffort(name string) string {
	lt, ok := s.cfg.LaneType(name)
	if !ok {
		return ""
	}
	model, effort := lt.Model(), lt.Effort()
	if model == "" {
		model = lt.Kind
	}
	switch {
	case model != "" && effort != "":
		return model + ", effort " + effort
	case model != "":
		return model
	case effort != "":
		return "effort " + effort
	}
	return ""
}

// existingTypes keeps the types the project has a lane_type for, in order: the table never names one it lacks.
func (s *Setup) existingTypes(types []string) []string {
	var out []string
	for _, t := range types {
		if _, ok := s.cfg.LaneType(t); ok {
			out = append(out, t)
		}
	}
	return out
}

// fallbackTypes is the types of another level that run the level when none of its own types has a lane (T-631),
// keeping only the types the project has.
func (s *Setup) fallbackTypes(level string) []string {
	types, _ := s.cfg.DifficultyTypes(level)
	return s.existingTypes(types)
}

// lanesOfType is one type's configured lanes, by lane name, as "O-01, S-01" ("" when none).
func (s *Setup) lanesOfType(typ string) string {
	var names []string
	for _, l := range s.cfg.Lanes() {
		if l.Type == typ {
			names = append(names, l.Name)
		}
	}
	return strings.Join(names, ", ")
}

// lanesOfTypes is the configured lanes of the types, by lane name, as "O-01, S-01" ("" when none).
func (s *Setup) lanesOfTypes(types []string) string {
	var names []string
	for _, l := range s.cfg.Lanes() {
		if hasType(types, l.Type) {
			names = append(names, l.Name)
		}
	}
	return strings.Join(names, ", ")
}

// rolesLine is the lead's, the integrator's and the reviewer's types: "lead opus (opus, effort medium), …".
func (s *Setup) rolesLine() string {
	var parts []string
	for _, role := range []string{"lead", "integrator", "reviewer"} {
		rc, _ := s.cfg.Role(role)
		if rc.Type == "" {
			parts = append(parts, role+" - (roles."+role+".type not set)")
			continue
		}
		if _, ok := s.cfg.LaneType(rc.Type); !ok {
			parts = append(parts, role+" - (roles."+role+".type is not a lane_type)")
			continue
		}
		parts = append(parts, role+" "+rc.Type+s.typeDetail(rc.Type))
	}
	return strings.Join(parts, ", ")
}

// hasType reports whether types holds t.
func hasType(types []string, t string) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
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
