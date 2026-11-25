package setup

import (
	"context"

	"obor/internal/project"
)

// check is `obor setup check`: the project's terminal's own check (Check), read-only. On hc it reads the core's
// environment and fails when it holds another terminal's identity (a core started from an obor pane or another
// terminal gives every terminal that one's identity); on Rex it finds the rex CLI on PATH and fails naming the app
// bundle's directory to put there (obor never runs it from the bundle); herdr has no check of its own.
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
