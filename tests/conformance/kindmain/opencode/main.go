//go:build conformance

// Command opencode is the OpenCode kind as an external command, for the tests that prove a kind's command path (launch's
// lane start runs one; the kit's kind itself is linked into obor). Its name is no plugin.Local name, so obor runs it.
package main

import (
	"os"

	"obor/kinds/opencode/kind"
)

func main() { os.Exit(kind.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
