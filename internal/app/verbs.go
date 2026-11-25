package app

import (
	"fmt"

	"obor/internal/app/wire"
)

// leadOrOwner is the admission of a method only the lead and the owner may call: nil when c's caller is the owner (no
// role) or the lead, else the refusal line every such method shares. method names the request, does what it lets the
// caller do.
func leadOrOwner(method, does string, c wire.Envelope) error {
	if c.Role == "" || c.Role == "lead" {
		return nil
	}
	return fmt.Errorf("%s: the lead or the owner %s, not %s (role %s)", method, does, Dash(c.Agent), c.Role)
}

// leadOr is the admission of a lead's and the owner's method (spec_set, dispatch, watch, send, brief: roles.Verbs): nil
// when c's caller is the owner (no role), the lead, or a custom role whose roles.<name>.verbs names method (an
// integration's role; the verbs add rights, never remove one). Else refused, leadOrOwner's line: for a custom role
// followed by the verb it lacks and the config key that grants it. The config is read only for a custom role's call,
// through load: the call's own read of the config (its entry's), so one call reads one config.
func leadOr(load cfgLoad, method, does string, c wire.Envelope) error {
	refused := leadOrOwner(method, does, c)
	if refused == nil {
		return nil
	}
	cfg, err := load()
	if err != nil {
		return fmt.Errorf("%s (the config, for its verbs: %v)", refused, err)
	}
	rc, ok := cfg.Role(c.Role)
	switch {
	case !ok || !rc.Role.Custom:
		return refused
	case rc.Role.HasVerb(method):
		return nil
	}
	return fmt.Errorf("%s: role %s has no verb %s (add it to roles.%s.verbs)", refused, c.Role, method, c.Role)
}
