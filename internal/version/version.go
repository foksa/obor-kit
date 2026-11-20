// Package version holds the build version and formats the `obor version` identity line.
package version

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"obor/internal/project"
)

// Older reports whether a tool's version v is older than min: both dotted numbers, a suffix after "-" or "+" not
// read ("0.1.21-rc1" is 0.1.21). ok false: v is not such a version. The terminal adapters' version rules (hc's and
// herdr-app's) read it.
func Older(v, min string) (older, ok bool) {
	num := func(s string) ([]int, bool) {
		s, _, _ = strings.Cut(s, "-")
		s, _, _ = strings.Cut(s, "+")
		var out []int
		for _, f := range strings.Split(s, ".") {
			n, err := strconv.Atoi(f)
			if err != nil {
				return nil, false
			}
			out = append(out, n)
		}
		return out, len(out) > 0
	}
	a, ok := num(v)
	b, _ := num(min)
	if !ok {
		return false, false
	}
	return slices.Compare(a, b) < 0, true
}

// Version is the build version, stamped by the build (the Makefile): the tag (v0.14.0) when HEAD is exactly at one, else
// the newest release tag plus -dev+<short sha> (v0.14.0-dev+21242f62, -dirty when the tree is modified) — release tags
// sit on release commits off main. A plain `go build` keeps "dev".
var Version = "dev"

// Release is the release a build belongs to: its version less the dev suffix ("v0.34.0-dev+21242f62-dirty" is
// "v0.34.0"). A version without one (a tag, a plain "dev", a bare sha) is its own release. Two builds of one release
// are not a skew worth showing: obor-kit's own dev builds differ after every merge.
func Release(v string) string {
	if i := strings.Index(v, "-dev+"); i > 0 {
		return v[:i]
	}
	return v
}

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
