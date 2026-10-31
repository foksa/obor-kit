package project

import (
	"strings"

	"obor/internal/config"
)

// LabelOurs: a pane labelled label with herdr's cwd may be cfg's project's (RV-19 #3: labels are machine-wide, another
// project may use the same names): its cwd in dir (when given), else in the main checkout or a lanes_dir worktree. No
// cwd known: yes, except for a bare <lane lower> label in a lane lookup (dir given; axis's o-01), the name other
// projects share, which only a cwd can confirm. Paths compared physically.
func LabelOurs(cfg *config.Config, label, cwd, dir string) bool {
	if cwd == "" {
		if dir != "" {
			for _, lane := range cfg.Lanes() {
				if strings.ToLower(lane.Name) == label {
					return false
				}
			}
		}
		return true
	}
	c := Physical(cwd)
	in := func(d string) bool {
		d = Physical(d)
		return c == d || strings.HasPrefix(c, d+"/")
	}
	if dir != "" {
		return in(dir)
	}
	return in(cfg.Root) || strings.HasPrefix(c, Physical(cfg.LanesDir())+"/")
}
