// toast.go: the owner's herdr toast, a rule action's best-effort after (T-949; rules.go).
package app

import (
	"context"
	"errors"

	"obor/internal/app/port"
)

// toast shows the owner title and body as herdr's desktop notification, fire and forget: the lead's line is recorded
// first, and no answer of herdr's changes that. A toast not shown (busy, rate_limited, disabled, no foreground
// client) or any error is logged once and dropped: herdr's toast is best effort by contract, and the lead is the one
// who acts.
func (a *App) toast(ctx context.Context, title, body string) {
	err := a.panes.Notify(ctx, title, body)
	if err == nil {
		return
	}
	var ns *port.NotShown
	if errors.As(err, &ns) {
		a.logf("notification %q: not shown: %s", title, ns.Reason)
		return
	}
	a.logf("notification %q: %v", title, err)
}
