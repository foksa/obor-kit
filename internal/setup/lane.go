package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

var laneRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (s *Setup) lane(ctx context.Context, args []string) error {
	if len(args) < 1 || len(args) > 2 || args[0] == "" || (len(args) == 2 && args[1] == "") {
		return errUsage
	}
	lane, typ := args[0], "" // no type: an untyped pool lane, whose dispatches name the type
	if len(args) == 2 {
		typ = args[1]
	}
	if !laneRE.MatchString(lane) {
		return project.Failf("bad lane name %s", lane)
	}
	if err := s.load(); err != nil {
		return err
	}
	if err := refuseLaneCollisions(s.cfg, lane); err != nil {
		return err
	}
	if lt, _ := s.cfg.LaneType(typ); typ != "" && lt.Kind == "" {
		return project.Failf("unknown lane type %s (not in lane_types with a kind)", typ)
	}
	have := s.cfg.Data.Lanes.M[lane].Type
	configured := s.cfg.HasLane(lane)
	if configured && have != typ {
		if have == "" {
			return project.Failf("lane %s is already configured untyped (edit .obor/config.json to pin it: \"lanes\": {\"%s\": \"%s\"}; for an object-form lane change only type)", lane, lane, typ)
		}
		return project.Failf("lane %s is already configured with type %s", lane, have)
	}
	dir := filepath.Join(s.cfg.LanesDir(), lane)
	if err := s.ensureWorktree(dir); err != nil {
		return err
	}
	for _, link := range s.laneLinks() {
		src, dst := filepath.Join(s.root, link), filepath.Join(dir, link)
		if _, err := os.Lstat(dst); err == nil {
			s.say("skip link %s (exists)", link)
			continue
		}
		if _, err := os.Stat(src); err != nil {
			s.say("warning: lane_links %s: no %s, not linked", link, src)
			continue
		}
		if os.MkdirAll(filepath.Dir(dst), 0o755) != nil || os.Symlink(src, dst) != nil {
			return project.Failf("cannot link %s", dst)
		}
		s.say("link %s → %s", link, src)
	}
	if configured {
		s.say("skip config lanes.%s (already %s)", lane, typeWord(typ))
	} else if err := s.addLane(lane, typ); err != nil {
		return err
	}
	if c, ok := run.Command(s.cfg, "lane_setup"); ok {
		marker := laneSetupMarker(s.root, lane)
		_ = os.Remove(marker) // a run that fails leaves nothing for a rerun to skip (T-613)
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
	s.say("OK lane %s (%s) at %s", lane, typeWord(typ), dir)
	return nil
}

// typeWord is a lane type for a message: "untyped" for a pool lane.
func typeWord(typ string) string {
	if typ == "" {
		return "untyped"
	}
	return typ
}

// laneSetupMarker is <root>/.obor/state/lanes/<lane>.setup: the commands.lane_setup that finished for the lane.
// setup.lane writes the config and the worktree before running it, so on a rerun only this marker tells a finished
// setup from one that failed (T-613); it holds the command, so an edit to lane_setup does not match it (T-618).
func laneSetupMarker(root, lane string) string {
	return filepath.Join(project.StateDir(root), "lanes", lane+".setup")
}

// laneReady runs commands.lane_ready in dir (ok with none configured) and reads its output as a FAIL line can carry
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

// laneDone: this lane of the config needs no `obor setup lane` (obor init's rerun, T-613): dir is a worktree of this
// repo and commands.lane_ready passes there, or (with none configured) its lane_setup finished the last time with the
// command configured now (an edited lane_setup runs again, T-618). A dir that exists and is not a worktree of this repo
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
			return false, project.Failf("lane %s: lane_ready fails (%s) and commands.lane_setup is not set", lane, c.Cmd)
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

// addLane adds lanes.<lane> = type to .obor/config.json (not local.json), uncommitted.
func (s *Setup) addLane(lane, typ string) error {
	path := filepath.Join(s.root, ".obor", "config.json")
	doc, err := os.ReadFile(path)
	if err != nil {
		return project.Failf("cannot read %s", path)
	}
	doc, err = config.EditJSON(doc, []string{"lanes", lane}, typ)
	if err != nil || writeJSON(path, doc) != nil {
		return project.Failf("cannot update %s", path)
	}
	s.say("config lanes.%s = %s (uncommitted)", lane, config.Compact(typ))
	return nil
}

// pluginSettings is the project settings file that enables obor's plugin; lanes are worktrees, so an untracked one
// reaches them only as a link (T-705).
const pluginSettings = ".claude/settings.json"

// laneLinks are lane_links plus pluginSettings while it is untracked in the main checkout and not listed.
func (s *Setup) laneLinks() []string {
	links := s.cfg.Data.LaneLinks
	if slices.Contains(links, pluginSettings) || !s.settingsUntracked() {
		return links
	}
	return append(slices.Clone(links), pluginSettings)
}

// settingsUntracked: the main checkout has pluginSettings and git does not track it.
func (s *Setup) settingsUntracked() bool {
	if _, err := os.Stat(filepath.Join(s.root, pluginSettings)); err != nil {
		return false
	}
	_, err := project.Git(s.root, "ls-files", "--error-unmatch", "--", pluginSettings)
	return err != nil
}
