package project

import (
	"strings"
	"testing"
)

// CheckSocketPath: a path at the unix limit is accepted; one byte over is refused, naming both lengths.
func TestCheckSocketPath(t *testing.T) {
	t.Parallel()
	if err := CheckSocketPath("/" + strings.Repeat("a", 103)); err != nil {
		t.Errorf("104-byte path: %v, want ok", err)
	}
	long := "/" + strings.Repeat("a", 104)
	if err := CheckSocketPath(long); err == nil || !strings.Contains(err.Error(), "105 bytes, longer than the 104") {
		t.Errorf("105-byte path: %v, want a clear error", err)
	}
}
