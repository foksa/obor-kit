// cfg.go: where serve's core reads the project's config.
package app

import "obor/internal/config"

// cfgNow is p's config as its files read now: the one read of a call that takes no Loop (a socket verb, a watchdog
// look, a status read, the identity reconcile). A call or a pass that has a Loop runs on its Cfg (Loop.Cfg) instead,
// and everything below its entry takes the value it was given; the entries are the only callers
// (TestConfigReadAtEntries). What a failed read means is decided there: a verb refuses, a pass is skipped, a status
// shows what it can.
func (p *proj) cfgNow() (*config.Config, error) { return config.Load(p.root) }

// servedCfg is the config of the project this serve holds as it reads now, for a status read: nil when there is no
// project or its config does not load (what a status then shows is what the fold holds).
func (a *App) servedCfg() *config.Config {
	a.mu.Lock()
	p := a.p
	a.mu.Unlock()
	if p == nil {
		return nil
	}
	cfg, _ := p.cfgNow()
	return cfg
}

// cfgLoad is a call's config as its entry has it (or reads it on demand: p.cfgNow): a loaded one comes back as it is.
type cfgLoad func() (*config.Config, error)

// loaded is the load of a config the call has read already (cfg, err).
func loaded(cfg *config.Config, err error) cfgLoad {
	return func() (*config.Config, error) { return cfg, err }
}

// cfgOf is the config of the pass fx belongs to; nil when no pass ran (fx nil), for the rules that run without one and
// treat a nil config as one that did not load.
func cfgOf(fx *Loop) *config.Config {
	if fx == nil {
		return nil
	}
	return fx.Cfg
}
