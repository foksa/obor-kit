package setup

import (
	"context"

	"obor/internal/project"
)

// check is `obor setup check`: the project's terminal's own check (Check), read-only. On hc it reads the core's
// environment and fails when it holds a HERDR_ or OBOR_ name (a core started from an obor pane gives every terminal
// that pane's identity); herdr has no check of its own.
func (s *Setup) check(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	if err := s.load(); err != nil {
		return err
	}
	kind := s.cfg.TerminalKind()
	if s.Check == nil {
		s.ok("check", false, "terminal %s: no check of its own", kind)
		return nil
	}
	line, err := s.Check(ctx)
	if err != nil {
		return project.Failf("terminal %s: %v", kind, err)
	}
	s.ok("check", false, "terminal %s: %s", kind, line)
	return nil
}
