package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

var laneRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (s *Setup) lane(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		return errUsage
	}
	lane, typ := args[0], args[1]
	if !laneRE.MatchString(lane) {
		return project.Failf("bad lane name %s", lane)
	}
	if err := s.load(); err != nil {
		return err
	}
	if err := refuseLaneCollisions(s.cfg, lane); err != nil {
		return err
	}
	if lt, _ := s.cfg.LaneType(typ); lt.Kind == "" {
		return project.Failf("unknown lane type %s (not in lane_types with a kind)", typ)
	}
	have := s.cfg.Data.Lanes.M[lane].Type
	if have != "" && have != typ {
		return project.Failf("lane %s is already configured with type %s", lane, have)
	}
	dir := filepath.Join(s.cfg.LanesDir(), lane)
	if err := s.ensureWorktree(dir); err != nil {
		return err
	}
	for _, link := range s.cfg.Data.LaneLinks {
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
	if have == typ {
		s.say("skip config lanes.%s (already %s)", lane, typ)
	} else if err := s.addLane(lane, typ); err != nil {
		return err
	}
	if c, ok := run.Command(s.cfg, "lane_setup"); ok {
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
		s.say("lane_setup ok: %s", c.Cmd)
	}
	s.say("OK lane %s (%s) at %s", lane, typ, dir)
	return nil
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
	s.say("config lanes.%s = %s (uncommitted)", lane, typ)
	return nil
}
