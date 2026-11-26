package project

import (
	"os"
	"path/filepath"

	"obor/internal/config"
)

// ProbeDir is the one dir a role agent probes in: <os.TempDir()>/obor-probe/<project name>/<agent>. It is per
// project and agent and outside every checkout (the guard's write rule is the repository's), so a probe never lands
// in a lane or the main checkout. "" when cfg names no project or agent is empty: there is no dir to name then.
func ProbeDir(cfg *config.Config, agent string) string {
	name := cfg.ProjectName()
	if name == "" || agent == "" {
		return ""
	}
	return filepath.Join(os.TempDir(), "obor-probe", name, agent)
}

// ClearProbeDir removes agent's probe dir and everything a probe left in it (ProbeDir). A caller logs the error and
// goes on: a stale probe costs disk, never the stop. nil when ProbeDir names nothing.
func ClearProbeDir(cfg *config.Config, agent string) error {
	dir := ProbeDir(cfg, agent)
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}
