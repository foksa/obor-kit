package guard

import (
	"errors"
	"os"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
)

// loadAt loads the project of dir: nil repo when dir is no directory in a git repository; config.ErrNoConfig without
// .obor/config.json. Keys this binary does not know are ignored and returned in unknown (config.LoadHook). A config
// that exists but does not load is an error with config.Fallback as cfg: the hooks fail closed on it (DD-v0.13 A1).
func loadAt(dir string) (repo *project.Repo, cfg *config.Config, unknown []string, err error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, nil, nil, nil
	}
	repo, err = project.Find(dir)
	if err != nil {
		return nil, nil, nil, nil
	}
	cfg, unknown, err = config.LoadHook(repo.Root)
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		cfg = config.Fallback(repo.Root)
	}
	return repo, cfg, unknown, err
}

// unreadable is the line a hook gives for a config that does not load (err, not config.ErrNoConfig), without "obor: ".
func unreadable(err error) string {
	return "config unreadable: " + strings.TrimPrefix(err.Error(), "obor: ") +
		" — run obor config validate and fix it; until then hooks deny repo writes, git writes and pkill for every role but the lead"
}
