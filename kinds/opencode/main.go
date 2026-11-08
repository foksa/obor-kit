// Command obor-kind-opencode is the OpenCode agent kind as a plugin kind (T-1046, docs/agents/kind-protocol.md): obor
// runs `obor-kind-opencode <call>` with one JSON request on stdin and reads one JSON reply from stdout. `make build`
// builds it beside dist/obor; a release ships it beside obor's own binary. The kind itself is package kind.
package main

import (
	"os"

	"obor/kinds/opencode/kind"
)

func main() { os.Exit(kind.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
