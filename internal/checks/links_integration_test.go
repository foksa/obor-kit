//go:build integration

package checks

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/project"
)

// An upgraded project's untracked, non-ignored .claude/settings.json, linked into a lane the way obor setup and the
// launcher link it (project.LinkInto), is excluded through git's own info/exclude: the lane is free in the lane table,
// its lane test runs and the test's cleanup (reset --hard, clean -fd) keeps the link, and Lane.Free lets a drop
// through with the link still there.
func TestLinkedSettingsKeepTheLaneClean(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", probe))
	// The upgraded project: the fixture's lane copy of the settings and its .claude/ exclude lines go, so the main
	// checkout's settings file is untracked and ignored nowhere, and the lane has none of its own.
	exclude := filepath.Join(y.Root, ".git", "info", "exclude")
	var kept []string
	for _, line := range strings.Split(y.Read(".git/info/exclude"), "\n") {
		if !strings.Contains(line, ".claude") {
			kept = append(kept, line)
		}
	}
	y.Write(exclude, strings.Join(kept, "\n"))
	if err := os.Remove(filepath.Join(y.lane, project.PluginSettings)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	y.Write(project.PluginSettings, "{}\n")
	if _, err := project.Git(y.Root, "ls-files", "--error-unmatch", "--", project.PluginSettings); err == nil {
		t.Fatalf("precondition: git tracks %s", project.PluginSettings)
	}
	if out, err := project.Git(y.Root, "check-ignore", "-v", project.PluginSettings); err == nil {
		t.Fatalf("precondition: %s is ignored before any link is made: %s", project.PluginSettings, out)
	}
	cfg := y.s.Cfg
	linked, err := project.LinkInto(y.Root, y.lane, project.WorktreeLinks(cfg))
	if err != nil || !slices.Contains(linked, project.PluginSettings) {
		t.Fatalf("precondition: LinkInto linked %v, %v; want %s", linked, err, project.PluginSettings)
	}
	link := filepath.Join(y.lane, project.PluginSettings)
	isLink := func(when string) {
		t.Helper()
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s: %s is no link (%v)", when, link, err)
		}
	}
	isLink("after LinkInto")

	// The worker's submission commits the lane's work (the fixture's feature.txt), never the link.
	sha := y.submit()
	if out := y.Git(y.lane, "ls-tree", "-r", "--name-only", sha, "--", ".claude"); out != "" {
		t.Errorf("the submission committed the link: %q", out)
	}
	y.eq(y.Git(y.lane, "status", "--porcelain", "--untracked-files=all"), "", "raw git status in the lane")
	s := stateOf(NewLaneTable(context.Background(), cfg), "L-01")
	if s.State != port.LaneFree || s.Dirty || len(s.Paths) != 0 {
		t.Fatalf("lane table: %+v; want free with no dirty paths", s)
	}

	c := y.green(lane(sha))
	o, err := y.test(ctx, c)
	if err != nil || o.Kind != port.OutcomeGreen {
		t.Fatalf("lane test: %+v, %v; want green", o, err)
	}
	isLink("after the lane test's cleanup")

	l := Lane{Cfg: cfg, Out: io.Discard}
	line, err := l.Free(ctx, port.FreeInput{Lane: "L-01", Dir: y.lane, Branch: "task/T-05", Target: c.SHA})
	if err != nil {
		t.Fatalf("free: %v; want the drop let through", err)
	}
	if line != "" {
		t.Errorf("free discarded %q; want nothing (the link is obor's)", line)
	}
	isLink("after the drop")
}
