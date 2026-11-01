package view

import (
	"context"
	"time"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
)

// Load is Build for the project at root, built as `obor status --json` builds it: serve is asked (down: its error, not
// an error here), the lane table is read from git and the store read-only (closed before it returns). `obor web` builds
// every project of the machine this way; the CLI builds the same Input itself, since it also needs the config for
// serve's launchd line. An error is the config's: the project cannot be read at all.
func Load(ctx context.Context, root string, now time.Time, events int) (Project, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return Project{}, err
	}
	ss, err := app.Ask(ctx, app.SocketPath(cfg.Root))
	db, _ := store.OpenAt(cfg.Root)
	if db != nil {
		defer db.Close()
	}
	return Build(Input{Status: ss, Err: err, Lanes: checks.NewLaneTable(cfg), DB: db, Now: now, Events: events}), nil
}
