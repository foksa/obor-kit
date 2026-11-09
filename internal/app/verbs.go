package app

import (
	"fmt"

	"obor/internal/app/wire"
	"obor/internal/config"
)

// leadOr is the admission of a lead's and the owner's method (spec_set, dispatch, watch, send, brief: roles.Verbs): nil
// when c's caller is the owner (no role), the lead, or a custom role whose roles.<name>.verbs names method (T-1039: an
// integration's role; the verbs add rights, never remove one). Else refused, the method's own refusal line: for a
// custom role followed by the verb it lacks and the config key that grants it. The config is read only for a custom
// role's call.
func leadOr(root, method string, c wire.Envelope, refused string) error {
	if c.Role == "" || c.Role == "lead" {
		return nil
	}
	cfg, err := config.Load(root)
	if err != nil {
		return fmt.Errorf("%s (the config, for its verbs: %v)", refused, err)
	}
	rc, ok := cfg.Role(c.Role)
	switch {
	case !ok || !rc.Role.Custom:
		return fmt.Errorf("%s", refused)
	case rc.Role.HasVerb(method):
		return nil
	}
	return fmt.Errorf("%s: role %s has no verb %s (add it to roles.%s.verbs)", refused, c.Role, method, c.Role)
}
