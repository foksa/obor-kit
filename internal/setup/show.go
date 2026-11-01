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

// levelLine is one level of the table: its preferred type (model, effort), the lanes that run it, and the fallback
// when the preferred type has no lane (opus-high → opus): "opus-high (opus, effort high): O-01 | fallback opus".
func (s *Setup) levelLine(level string) string {
	listed := s.cfg.DifficultyList(level)
	if len(listed) == 0 {
		return "no type listed: set difficulty." + level
	}
	pref := listed[0]
	own := s.cfg.DifficultyLevelTypes(level)
	types := own
	if len(types) == 0 {
		types, _ = s.cfg.DifficultyTypes(level) // T-631: a level with no lane of its own uses medium's, then hard's
	}
	detail, lanes := s.typeDetail(pref), s.lanesOfTypes(types)
	switch {
	case len(lanes) == 0:
		return pref + " (" + detail + "): no lane: a " + level + " task waits for one"
	case len(own) == 0:
		return pref + " (" + detail + "): " + lanes + " | no " + level + " lane: fallback " + strings.Join(types, ", ")
	case !hasType(own, pref):
		return pref + " (" + detail + "): " + lanes + " | fallback " + own[0]
	}
	return pref + " (" + detail + "): " + lanes
}

// typeDetail is a lane type's model and effort as its args name them ("opus, effort high"); a type whose args name
// neither is "unknown" (obor setup show never guesses a model), and a listed type the project has no lane_type for
// says so: the level will use its fallback.
func (s *Setup) typeDetail(name string) string {
	lt, ok := s.cfg.LaneType(name)
	if !ok {
		return "no such lane_type"
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
	return "unknown"
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
		parts = append(parts, role+" "+rc.Type+" ("+s.typeDetail(rc.Type)+")")
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
