//go:build integration

package main

import (
	"testing"

	"obor/internal/testkit"
)

// servedGitProject is servedProject in a git project (testkit.New, lane L-01 a real worktree on task/T-01), for the
// tests whose CLI path runs git.
func servedGitProject(t *testing.T, edits ...func(map[string]any)) *testkit.Project {
	t.Helper()
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	p.Config(append([]func(map[string]any){testkit.Set("roles.integrator", "")}, edits...)...)
	return p
}
