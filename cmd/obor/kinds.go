package main

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
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

// kindCmd is `obor kind <name> [args…]`: kit kind name's Main (kitKinds) in process with args. agy's hook transport
// is answered by agyHook instead: one obor start per tool call after the shim, no second obor. An unknown name: exit 2.
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
	if args[0] == agykind.Name && len(args) == 2 && args[1] == "hook" {
		return agyHook(os.Stdin, os.Stdout, os.Stderr)
	}
	return k.main(args[1:], os.Stdin, os.Stdout, os.Stderr)
}

// agyHook answers `obor kind agy hook` (agy's .agents/hooks.json runs it): agy's event on stdin, agy's answer on
// stdout, exit 0. obor's hook runs in process (hookRun), and the project rule is the guard's (project.InProject), so a
// repository nested in an obor project is its own.
func agyHook(stdin io.Reader, stdout, stderr io.Writer) int {
	run := func(name string, event []byte) ([]byte, []byte, int, error) {
		return inProcessHook([]string{name}, event, agykind.HookTimeout)
	}
	return agykind.RunHook(stdin, stdout, stderr, run, project.InProject)
}

// inProcessHook is `obor hook args…` with event on stdin: its stdout, its stderr and its exit code; err when it did
// not answer within timeout, the transport's deny naming it as a run that failed. A panic is that failed run too (a
// process would exit non-zero), never the transport's own.
func inProcessHook(args []string, event []byte, timeout time.Duration) (out, errOut []byte, code int, err error) {
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		code := 0
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(&stderr, "panic: %v\n", r)
				code = 2
			}
			done <- code
		}()
		code = hookRun(args, bytes.NewReader(event), &stdout, &stderr)
	}()
	select {
	case code = <-done:
		return stdout.Bytes(), stderr.Bytes(), code, nil
	case <-time.After(timeout):
		return nil, nil, -1, fmt.Errorf("no answer within %s", timeout)
	}
}
