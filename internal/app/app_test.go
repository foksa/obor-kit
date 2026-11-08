package app

import (
	"testing"
)

func TestReady(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{"idle": true, "done": true, "working": false, "blocked": false, "unknown": false, "": false} {
		if Ready(status) != want {
			t.Errorf("Ready(%q) = %v, want %v", status, !want, want)
		}
	}
}
