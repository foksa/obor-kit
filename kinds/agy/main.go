// Command obor-kind-agy is the Antigravity CLI (agy) agent kind as a plugin kind's binary (docs/agents/kind-protocol.md):
// `obor-kind-agy <call>` with one JSON request on stdin, one JSON reply on stdout. obor answers the kind in process
// (cmd/obor/kinds.go) and runs this binary only when a config names it by a path; `make build` builds it beside
// dist/obor and a release ships it. The kind itself is package kind.
package main

import (
	"os"

	"obor/kinds/agy/kind"
)

func main() { os.Exit(kind.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
