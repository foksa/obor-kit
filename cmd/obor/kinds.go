package main

import (
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/help"
	agykind "obor/kinds/agy/kind"
	ockind "obor/kinds/opencode/kind"
)

// kitKind is a kind the kit ships, linked into obor: handle answers the kind protocol in process (plugin.Local), main
// is its binary's Main (`obor kind <name>`).
type kitKind struct {
	handle plugin.Handler
	main   func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

// kitKinds are the kit's kinds by name (config.KitKinds names their binaries).
var kitKinds = map[string]kitKind{
	agykind.Name: {agykind.Handle, agykind.Main},
	ockind.Name:  {ockind.Handle, ockind.Main},
}

// init links each kit kind: plugin.Local under its binary's name, the command a config resolves it to
// (config.KindCommand), so obor answers it in process. A handler already there stays: this package's test binary
// has testkit's (its init runs first), agy's with a fixed model list.
func init() {
	for name, k := range kitKinds {
		if cmd := config.KitKinds[name]; plugin.Local[cmd] == nil {
			plugin.Local[cmd] = k.handle
		}
	}
}

// kindCmd is `obor kind <name> [args…]`: kit kind name's Main (kitKinds) in process with args, this obor's executable in
// agents.ExeVar (agy's hook transport runs obor hook on it; an inherited value is replaced). agy's .agents/hooks.json
// runs obor kind agy hook. An unknown name: exit 2.
func kindCmd(args []string) int {
	if len(args) == 0 {
		errf("%s", help.Usage("kind"))
		return 2
	}
	k, ok := kitKinds[args[0]]
	if !ok {
		errf("obor kind: %q is no kit kind (%s)", args[0], strings.Join(slices.Sorted(maps.Keys(kitKinds)), ", "))
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		errf("obor kind %s: this obor's executable: %v", args[0], err)
		return 2
	}
	if err := os.Setenv(agents.ExeVar, exe); err != nil {
		errf("obor kind %s: %s: %v", args[0], agents.ExeVar, err)
		return 2
	}
	return k.main(args[1:], os.Stdin, os.Stdout, os.Stderr)
}
