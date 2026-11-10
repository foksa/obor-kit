package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

func (s *Setup) lane(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errUsage
	}
	lane := args[0]
	if err := s.load(); err != nil {
		return err
	}
	configured := s.cfg.HasLane(lane)
	if !configured && s.cfg.LanesListed() { // a list is the owner's, a count edit would replace it
		return project.Failf("lanes is a list in .obor/config.json: add {\"name\": \"%s\"} to it, then obor setup lane %s", lane, lane)
	}
	if next := config.LaneName(s.cfg.LaneCount() + 1); !configured && lane != next {
		return project.Failf("lanes are L-01 … L-NN in order (lanes: %d): the next one is %s", s.cfg.LaneCount(), next)
	}
	dir := filepath.Join(s.cfg.LanesDir(), lane)
	if err := s.ensureWorktree(dir); err != nil {
		return err
	}
	if err := s.linkWorktree(dir); err != nil {
		return err
	}
	if configured {
		s.say("skip config lanes (%s is lane %d of %d)", lane, s.laneIndex(lane), s.cfg.LaneCount())
	} else if err := s.addLane(s.cfg.LaneCount() + 1); err != nil {
		return err
	}
	if c, ok := run.Command(s.cfg, "lane_setup"); ok {
		marker := laneSetupMarker(s.root, lane)
		_ = os.Remove(marker) // a run that fails leaves nothing for a rerun to skip
		var out bytes.Buffer
		rc, timedOut := run.Execute(ctx, c, dir, nil, nil, &out, &out)
		if rc != 0 || timedOut {
			lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
			s.say("%s", strings.Join(lines[max(0, len(lines)-15):], "\n"))
			if timedOut {
				return project.Failf("lane_setup timed out after %ds in %s: %s", c.Timeout, dir, c.Cmd)
			}
			return project.Failf("lane_setup exited %d in %s: %s", rc, dir, c.Cmd)
		}
		if os.MkdirAll(filepath.Dir(marker), 0o755) == nil {
			_ = os.WriteFile(marker, []byte(c.Cmd+"\n"), 0o644)
		}
		s.say("lane_setup ok: %s", c.Cmd)
	}
	s.say("OK lane %s at %s", lane, dir)
	return nil
}

// laneIndex is lane's place among the configured lanes (from 1; 0: none).
func (s *Setup) laneIndex(lane string) int {
	for i, l := range s.cfg.Lanes() {
		if l.Name == lane {
			return i + 1
		}
	}
	return 0
}

// laneSetupMarker is <root>/.obor/state/lanes/<lane>.setup: the lanes.setup command that finished for the lane.
// setup.lane writes the config and the worktree before running it, so on a rerun only this marker tells a finished
// setup from one that failed; it holds the command, so an edit to lane_setup does not match it.
func laneSetupMarker(root, lane string) string {
	return filepath.Join(project.StateDir(root), "lanes", lane+".setup")
}

// laneReady runs lanes.ready in dir (ok with none configured) and reads its output as a FAIL line can carry
// it: its last three lines on one line (checks.LaneSetup's shape).
func (s *Setup) laneReady(ctx context.Context, dir string) (c config.Command, out string, ok bool) {
	c, configured := run.Command(s.cfg, "lane_ready")
	if !configured {
		return c, "", true
	}
	var b bytes.Buffer
	rc, timedOut := run.Execute(ctx, c, dir, nil, nil, &b, &b)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	return c, strings.Join(lines[max(0, len(lines)-3):], " "), rc == 0 && !timedOut
}

// laneDone: this lane of the config needs no `obor setup lane` (obor init's rerun): dir is a worktree of this
// repo and lanes.ready passes there, or (with none configured) its lane_setup finished the last time with the
// command configured now (an edited lane_setup runs again). A dir that exists and is not a worktree of this repo
// is a FAIL, never a skip; no dir at all is false: setup.lane makes one.
func (s *Setup) laneDone(ctx context.Context, lane, dir string) (bool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false, nil
	}
	repo, err := project.Find(dir)
	if err != nil || repo.Root != s.root || repo.Top != project.Physical(dir) {
		return false, project.Failf("%s exists and is not a worktree of %s", dir, s.root)
	}
	if c, _, ok := s.laneReady(ctx, dir); !ok {
		if _, has := run.Command(s.cfg, "lane_setup"); !has {
			return false, project.Failf("lane %s: lane_ready fails (%s) and lanes.setup is not set", lane, c.Cmd)
		}
		s.say("lane %s: lane_ready fails (%s): running its lane_setup again", lane, c.Cmd)
		return false, nil
	}
	c, ok := run.Command(s.cfg, "lane_setup")
	if !ok {
		return true, nil
	}
	b, err := os.ReadFile(laneSetupMarker(s.root, lane))
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(b)) == c.Cmd, nil
}

// addLane sets lanes to n in .obor/config.json (not local.json), uncommitted.
func (s *Setup) addLane(n int) error {
	return s.editConfig(func(doc []byte) (config.Write, error) { return config.SetLanes(doc, n) })
}

// linkWorktree links project.WorktreeLinks into the worktree dir (a lane, a role worktree) with project.LinkInto and
// says what became of each entry: linked, already there, or not in the main checkout.
func (s *Setup) linkWorktree(dir string) error {
	links := project.WorktreeLinks(s.cfg)
	linked, err := project.LinkInto(s.root, dir, links)
	if err != nil {
		return project.Failf("%v", err)
	}
	for _, link := range links {
		src := filepath.Join(s.root, link)
		_, statErr := os.Lstat(filepath.Join(dir, link))
		switch {
		case slices.Contains(linked, link):
			s.say("link %s → %s", link, src)
		case statErr == nil:
			s.say("skip link %s (exists)", link)
		default:
			s.say("warning: lanes.links %s: no %s, not linked", link, src)
		}
	}
	return nil
}
