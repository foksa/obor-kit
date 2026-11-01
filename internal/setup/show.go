package setup

// `obor setup show` (T-647): the project's models, effort and lanes as obor init left them, and whether the owner
// confirmed them. The lead prints it at its first session and asks the owner about it; obor init ends with it too.

import (
	"strings"

	"obor/internal/config"
)

// show is `obor setup show`: read-only, the setup table (models and effort per difficulty level, the lanes that run
// each, the roles' types) and whether the owner confirmed it.
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

// levelLine is one level of the table: the level's types the project has, in order — the first with its model and
// effort, or "no lane" when no configured lane has it, then the types that do run the level — and the lanes that run
// it: "opus-high (opus, effort high; no lane), opus: O-01", "deepseek (no lane), sonnet: S-01", "sonnet (sonnet,
// effort medium): S-01". A type the project has no lane_type for is never named (T-647).
func (s *Setup) levelLine(level string) string {
	types := s.existingTypes(s.cfg.DifficultyList(level))
	if len(types) == 0 { // only another level's types can run it, if any
		if fb := s.fallbackTypes(level); len(fb) > 0 {
			return "no lane_type this level lists: " + s.lanesOfTypes(fb) + " | fallback " + strings.Join(fb, ", ")
		}
		return "no lane_type this level lists: a " + level + " task waits for one"
	}
	pref := types[0]
	own := s.existingTypes(s.cfg.DifficultyLevelTypes(level)) // the level's types a lane has
	switch {
	case hasType(own, pref):
		return pref + s.typeDetail(pref, true) + ": " + s.lanesOfTypes(own)
	case len(own) > 0:
		return pref + s.typeDetail(pref, false) + ", " + strings.Join(own, ", ") + ": " + s.lanesOfTypes(own)
	}
	fb := s.fallbackTypes(level) // T-631: no lane of any type this level lists
	if len(fb) == 0 {
		return pref + s.typeDetail(pref, false) + ": no lane: a " + level + " task waits for one"
	}
	return pref + s.typeDetail(pref, false) + ": " + s.lanesOfTypes(fb) + " | no " + level + " lane: fallback " +
		strings.Join(fb, ", ")
}

// typeDetail is a lane type as the table shows it after the type's name: its model and effort as its args name them
// ("(opus, effort high)"), nothing when the args name neither; hasLane false adds "; no lane" — the level runs on
// another of its types instead.
func (s *Setup) typeDetail(name string, hasLane bool) string {
	detail := s.typeModelEffort(name)
	if !hasLane {
		if detail != "" {
			detail += "; "
		}
		detail += "no lane"
	}
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

// typeModelEffort is a lane type's model and effort as its args name them ("opus, effort high"); "" for a type whose
// args name neither (obor setup show never guesses a model).
func (s *Setup) typeModelEffort(name string) string {
	lt, ok := s.cfg.LaneType(name)
	if !ok {
		return ""
	}
	model, effort := lt.Model(), lt.Effort()
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
		parts = append(parts, role+" "+rc.Type+s.typeDetail(rc.Type, true))
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
