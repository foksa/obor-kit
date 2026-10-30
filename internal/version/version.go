// Package version holds the build version and formats the `obor version` identity line.
package version

import (
	"fmt"
	"os"

	"obor/internal/project"
)

// Version is the build version, stamped by the build (the Makefile): the tag (v0.14.0) when HEAD is exactly at one, else
// the newest release tag plus -dev+<short sha> (v0.14.0-dev+21242f62, -dirty when the tree is modified) — release tags
// sit on release commits off main (T-559). A plain `go build` keeps "dev".
var Version = "dev"

// devBuild is the shim's source text for a kit's own dist/obor (OBOR_VERSION_SOURCE).
const devBuild = "dev build"

// Line reports the binary and kit selected by the shim. A kit's own build is self-describing (its version came from
// that kit and its path is in it), so it prints the binary alone; a fetched release also names the kit and its source.
func Line(binary, kit, source string) string {
	if source == devBuild {
		return fmt.Sprintf("obor %s (%s)", Version, binary)
	}
	return fmt.Sprintf("obor %s (%s, kit %s, %s)", Version, binary, kit, source)
}

// CurrentLine reports this executable, using shim metadata when available.
func CurrentLine() string {
	binary, err := os.Executable()
	if err != nil {
		binary = "obor"
	}
	kit := project.KitRoot(project.OSEnv)
	source := os.Getenv("OBOR_VERSION_SOURCE")
	if source == "" {
		source = devBuild
	}
	return Line(binary, kit, source)
}
