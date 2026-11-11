package project

import "fmt"

// MaxSocketPath is the longest unix socket path macOS accepts (sun_path); Linux allows 108.
const MaxSocketPath = 104

// CheckSocketPath fails when path is too long to be a unix socket address.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf("socket path is %d bytes, longer than the %d a unix socket allows: %s", len(path), MaxSocketPath, path)
	}
	return nil
}
