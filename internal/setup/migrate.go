package setup

// `obor setup migrate` (T-765): rewrite a config of the shape before T-765 (lane_types, typed lanes, roles.<role>
// objects) once, by config.Migrate's rules, and commit it. Lanes not named L-01 … L-NN in config order are renamed:
// refused unless every lane is free (its worktree clean, its HEAD in the main branch, no agent running in it); then
// each worktree is moved (git worktree move, through a temporary name so a swap cannot collide), its lane_setup marker
// with it, and every pane labelled with a renamed agent (Migration.Agents) is relabelled. It prints the old and the
// new config, what it dropped, the agents renamed, and the commit. A config of the new shape: OK (already).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
)

func (s *Setup) migrate(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	if err := s.findRoot(); err != nil {
		return err
	}
	cfgPath, localPath := filepath.Join(s.root, ".obor", "config.json"), filepath.Join(s.root, ".obor", "local.json")
	doc, err := os.ReadFile(cfgPath)
	if err != nil {
		return project.Failf("cannot read %s", cfgPath)
	}
	local, err := os.ReadFile(localPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		local = nil
	case err != nil:
		return project.Failf("cannot read %s", localPath)
	}
	if config.OldShape(doc) == "" && (local == nil || config.OldShape(local) == "") {
		s.ok("migrate", true, ".obor/config.json has the new shape (models, a lanes count)")
		return nil
	}
	m, err := config.Migrate(doc, local)
	if err != nil {
		return project.Failf("migrate: %v", err)
	}
	where := placesOf(s.root, doc, local)
	moves := laneMoves(m.Lanes)
	if len(moves) > 0 {
		if err := s.lanesFree(ctx, where, m.Lanes); err != nil {
			return err
		}
	}
	for _, mv := range moves { // through a temporary name: L-02 → L-01 while L-01 → L-02 cannot collide
		if err := s.moveLane(where, mv[0], mv[0]+".migrating"); err != nil {
			return err
		}
	}
	for _, mv := range moves {
		if err := s.moveLane(where, mv[0]+".migrating", mv[1]); err != nil {
			return err
		}
		s.renameMarker(mv[0], mv[1])
	}
	oldConfig := config.OldShape(doc) != "" // else only local.json has the old shape: config.json stays as it is
	if oldConfig {
		if err := project.WriteAtomic(cfgPath, m.Config); err != nil {
			return project.Failf("cannot write %s", cfgPath)
		}
	}
	if m.Local != nil {
		if err := project.WriteAtomic(localPath, m.Local); err != nil {
			return project.Failf("cannot write %s", localPath)
		}
	}
	relabelled := s.relabel(ctx, m.Agents)
	if oldConfig {
		s.say("old .obor/config.json:\n%s", strings.TrimRight(string(doc), "\n"))
		s.say("new .obor/config.json:\n%s", strings.TrimRight(string(m.Config), "\n"))
	}
	if m.Local != nil {
		s.say("old .obor/local.json:\n%s", strings.TrimRight(string(local), "\n"))
		s.say("new .obor/local.json (not committed):\n%s", strings.TrimRight(string(m.Local), "\n"))
	}
	for _, n := range m.Notes {
		s.say("dropped: %s", n)
	}
	for _, mv := range moves {
		s.say("lane %s → %s (git worktree move)", mv[0], mv[1])
	}
	for _, a := range m.Agents {
		s.say("agent %s → %s", a.Old, a.New)
	}
	for _, line := range relabelled {
		s.say("%s", line)
	}
	if _, err := config.Load(s.root); err != nil {
		return project.Failf("the migrated config does not load: %v (fix it by hand, then obor config validate)", err)
	}
	if !oldConfig {
		s.ok("migrate", false, ".obor/local.json rewritten (not committed): obor config validate")
		return nil
	}
	if _, err := project.Git(s.root, "add", "--", ".obor/config.json"); err != nil {
		return project.Failf("git add .obor/config.json: %v", err)
	}
	if _, err := project.Git(s.root, "commit", "-q", "-m", "obor setup migrate: models and a lanes count (T-765)", "--",
		".obor/config.json"); err != nil {
		return project.Failf("git commit .obor/config.json: %v", err)
	}
	s.ok("migrate", false, "committed .obor/config.json: obor config validate, then restart the role agents fresh (obor restart <agent> --fresh)")
	return nil
}

// places are what a lane move needs of the config before it is migrated: the lanes dir and the main branch.
type places struct{ lanesDir, main string }

// placesOf reads lanes_dir and branches.main from config with local over it (the defaults when unset).
func placesOf(root string, doc, local []byte) places {
	var d struct {
		LanesDir string `json:"lanes_dir"`
		Branches struct {
			Main string `json:"main"`
		} `json:"branches"`
	}
	_ = json.Unmarshal(doc, &d)
	if local != nil {
		_ = json.Unmarshal(local, &d)
	}
	p := places{lanesDir: d.LanesDir, main: d.Branches.Main}
	if p.lanesDir == "" {
		p.lanesDir = "../" + filepath.Base(root) + "-wt"
	}
	if p.main == "" {
		p.main = "main"
	}
	p.lanesDir = config.AbsUnder(root, p.lanesDir)
	return p
}

// laneMoves are the lanes (old names, config order) not already named L-01 … L-NN in that order: old → new.
func laneMoves(lanes []string) [][2]string {
	var out [][2]string
	for i, old := range lanes {
		if next := config.LaneName(i + 1); old != next {
			out = append(out, [2]string{old, next})
		}
	}
	return out
}

// lanesFree: every lane's worktree is clean with its HEAD in the main branch (a missing one is free too), and no herdr
// agent runs in it; else a FAIL naming the busy lanes.
func (s *Setup) lanesFree(ctx context.Context, where places, lanes []string) error {
	running, err := s.H.Agents(ctx)
	if err != nil && !port.NotRunning(err) {
		return project.Failf("cannot ask herdr for the running agents: %v", err)
	}
	var busy []string
	for _, lane := range lanes {
		dir := filepath.Join(where.lanesDir, lane)
		if !project.IsDir(dir) {
			continue
		}
		why := ""
		if out, err := project.Git(dir, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
			why = "uncommitted changes"
		} else if _, err := project.Git(dir, "merge-base", "--is-ancestor", "HEAD", "refs/heads/"+where.main); err != nil {
			why = "commits not in " + where.main
		}
		for _, a := range running {
			if c := project.Physical(a.Cwd); why == "" && (c == project.Physical(dir) || strings.HasPrefix(c, project.Physical(dir)+"/")) {
				why = "agent " + a.Name + " runs in it"
			}
		}
		if why != "" {
			busy = append(busy, lane+" ("+why+")")
		}
	}
	if len(busy) > 0 {
		return project.Failf("migrate renames the lanes to L-01 … L-%02d, so every lane must be free: %s (finish or "+
			"drop their tasks, quit their agents, then obor setup migrate again)", len(lanes), strings.Join(busy, ", "))
	}
	return nil
}

// moveLane moves lane from's worktree to to's under the lanes dir (git worktree move); a lane with no worktree there
// moves nothing.
func (s *Setup) moveLane(where places, from, to string) error {
	src, dst := filepath.Join(where.lanesDir, from), filepath.Join(where.lanesDir, to)
	if !project.IsDir(src) {
		return nil
	}
	if _, err := project.Git(s.root, "worktree", "move", src, dst); err != nil {
		return project.Failf("git worktree move %s %s: %v", src, dst, err)
	}
	return nil
}

// renameMarker moves lane from's lane_setup marker to lane to's (obor init's rerun skips a lane whose marker matches).
func (s *Setup) renameMarker(from, to string) {
	src := laneSetupMarker(s.root, from)
	if _, err := os.Stat(src); err == nil {
		_ = os.Rename(src, laneSetupMarker(s.root, to))
	}
}

// relabel renames every herdr pane labelled with an old agent name of agents to its new one; the lines say what it
// did (herdr not running: nothing to relabel).
func (s *Setup) relabel(ctx context.Context, agents []config.AgentRename) []string {
	if len(agents) == 0 {
		return nil
	}
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return []string{fmt.Sprintf("WARN herdr pane list: %v: relabel the panes by hand (herdr pane rename)", err)}
	}
	var out []string
	for _, p := range panes {
		for _, a := range agents {
			if p.Label != a.Old {
				continue
			}
			if err := s.H.RenamePane(ctx, p.ID, a.New); err != nil {
				out = append(out, fmt.Sprintf("WARN pane %s: herdr pane rename %s: %v", p.ID, a.New, err))
			} else {
				out = append(out, fmt.Sprintf("pane %s %s → %s", p.ID, a.Old, a.New))
			}
		}
	}
	return out
}
