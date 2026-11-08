package app_test

import (
	"os"
	"testing"

	"obor/internal/testkit"
)

// TestMain is the hook testkit.New requires (T-1019): it removes the project template the run copies. Run, not Main:
// internal/app's tests re-exec their own binary carrying OBOR_TEST_DO_CRASH and rewrite goldens under OBOR_UPDATE_GOLDEN,
// and Main's Isolate clears every OBOR_* variable.
func TestMain(m *testing.M) { os.Exit(testkit.Run(m)) }
