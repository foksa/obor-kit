//go:build conformance

// Command agy is the agy kind as an external command for the conformance suite (P-S8): the kind protocol's
// command path, which a project's own kind takes. The kit's kind is linked into obor (plugin.Local), and this test main
// keeps the external path tested without a shipped kind binary. Its name is no plugin.Local name, so obor runs it.
package main

import (
	"os"

	"obor/kinds/agy/kind"
)

func main() { os.Exit(kind.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
