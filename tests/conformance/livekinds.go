//go:build conformance

// The live tier runs the kit's kinds the way a project does: linked into obor and answered in process (plugin.Local),
// never a binary built from kinds/<name>. testkit registers them for the Go tests with agy's model list fixed (a unit
// test must not run agy); the live tier needs the kind's own `agy models`, so it registers the handlers here.
package main

import (
	"obor/internal/agents/plugin"
	"obor/internal/config"
	agykind "obor/kinds/agy/kind"
	ockind "obor/kinds/opencode/kind"
	pikind "obor/kinds/pi/kind"
)

func init() {
	plugin.Local[config.KitKinds[agykind.Name]] = agykind.Handle
	plugin.Local[config.KitKinds[ockind.Name]] = ockind.Handle
	plugin.Local[config.KitKinds[pikind.Name]] = pikind.Handle
}
