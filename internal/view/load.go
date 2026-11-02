package view

import (
	"context"
	"time"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
)

// Load is Build for the project at root, built as `obor status --json` builds it: serve is asked (down: its error, not
// an error here), the lane table is read from git and the store read-only (closed before it returns). `obor web` builds
// every project of the machine this way; the CLI builds the same Input itself, since it also needs the config for
// serve's launchd line. A config key this binary does not know is a note, not an error (config.LoadReader, T-709); an
// error is the config's: the project cannot be read at all.
func Load(ctx context.Context, root string, now time.Time, events int) (Project, error) {
	cfg, warn, err := config.LoadReader(root)
	if err != nil {
		return Project{}, err
	}
	ss, err := wire.Ask(ctx, wire.SocketPath(cfg.Root))
	db, _ := store.OpenAt(cfg.Root)
	if db != nil {
		defer db.Close()
	}
	return Build(Input{Status: ss, Err: err, Lanes: checks.NewLaneTable(cfg), DB: db, Now: now, Events: events,
		Notes: Notes(cfg.Root, warn), Background: ScanBackground(ctx, cfg.Root, ss, err, project.OSEnv)}), nil
}
