package herdr

import "context"

// StartAgent is herdr's agent start as sent, with its name-taken retry (startAgent), for the external tests.
func (s *Socket) StartAgent(ctx context.Context, name, kind, pane string, say func(format string, args ...any), args ...string) (Pane, error) {
	return s.startAgent(ctx, name, kind, pane, say, args...)
}

// Read is the pane as herdr reports it now with herdr's own fields (read), for the external tests.
func (s *Socket) Read(ctx context.Context, id string) (Pane, error) { return s.read(ctx, id) }
