// toast.go: the owner's herdr toast, one best-effort rule action's call (T-949; rules.go).
package app

import (
	"context"
	"errors"

	"obor/internal/app/port"
)

// toast shows the owner title and body as herdr's desktop notification. herdr's answer decides what its rule does
// next: busy or rate_limited is an error, so the rule's next pass calls it again (the rule pass logs it); disabled or
// no foreground client is done, as a shown toast is, its reason logged once here. Any other error is tried again too.
func (a *App) toast(ctx context.Context, title, body string) error {
	err := a.panes.Notify(ctx, title, body)
	var ns *port.NotShown
	if errors.As(err, &ns) && !ns.Retry() {
		a.logf("notification %q: %s", title, ns.Reason)
		return nil
	}
	return err
}
