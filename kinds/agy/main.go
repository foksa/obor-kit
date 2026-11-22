// Command obor-kind-agy is the Antigravity CLI (agy) agent kind as a plugin kind (docs/agents/kind-protocol.md): obor
// runs `obor-kind-agy <call>` with one JSON request on stdin and reads one JSON reply from stdout. `make build`
// builds it beside dist/obor; a release ships it beside obor's own binary. The kind itself is package kind.
package main

import (
	"os"

	"obor/kinds/agy/kind"
)

func main() { os.Exit(kind.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
