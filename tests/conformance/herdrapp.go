//go:build conformance

// herdrappStatic is the herdr-app window's static tier: the installed app's `herdr-app commands` against obor's
// committed copy (internal/herdrapp/testdata), and its version against the oldest obor drives. Both are non-gating
// facts: a drift is a WARN, because the nightly runs this tier as its conformance line (merge.nightly_commands).
package main

import (
	"os/exec"
	"strings"
	"time"

	"obor/internal/herdrapp"
	"obor/internal/version"
)

// herdrappStatic checks the installed window against the contract herdrapp's client is written to.
func herdrappStatic() {
	contractWhere := at("internal/herdrapp/contract.go", "var Commands")
	versionWhere := at("internal/herdrapp/client.go", "MinVersion =")
	path, err := exec.LookPath("herdr-app")
	if err != nil {
		report("SKIP", "HA-S1", "herdr-app is not installed", contractWhere)
		report("SKIP", "HA-S2", "herdr-app is not installed", versionWhere)
		return
	}
	out, err := output(30*time.Second, path, "--version")
	switch v := herdrapp.VersionOf(out); {
	case err != nil:
		report("WARN", "HA-S2", "herdr-app --version failed: "+firstLine(out+err.Error()), versionWhere)
	case v == "":
		report("WARN", "HA-S2", "herdr-app --version printed no version obor reads: "+firstLine(out), versionWhere)
	default:
		switch older, ok := version.Older(v, herdrapp.MinVersion); {
		case !ok:
			report("WARN", "HA-S2", "herdr-app --version printed "+firstLine(out)+", no version obor reads", versionWhere)
		case older:
			report("WARN", "HA-S2", "herdr-app "+v+" is older than "+herdrapp.MinVersion+
				", the oldest obor drives: it refuses the window and places by labels", versionWhere)
		default:
			report("PASS", "HA-S2", "herdr-app "+v+" is at least "+herdrapp.MinVersion, versionWhere)
		}
	}
	cmds, err := output(30*time.Second, path, "commands")
	if err != nil {
		report("WARN", "HA-S1", "herdr-app commands failed: "+firstLine(cmds+err.Error()), contractWhere)
		return
	}
	drift, err := herdrapp.Drift([]byte(cmds))
	switch {
	case err != nil:
		report("WARN", "HA-S1", err.Error(), contractWhere)
	case len(drift) > 0:
		report("WARN", "HA-S1", "the installed app's commands drift from obor's copy of "+herdrapp.MinVersion+"'s: "+
			strings.Join(drift, ", "), contractWhere)
	default:
		report("PASS", "HA-S1", "the installed app's commands and their arguments match obor's copy of "+
			herdrapp.MinVersion+"'s", contractWhere)
	}
}
