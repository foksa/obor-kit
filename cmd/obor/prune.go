package main

import (
	"context"
	"fmt"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// pruneCmd is `obor prune [--dry-run] [--unmerged]`: a call to serve, which owns the fold and the main repo's git
// writes. Its reply is one line per deletion and `PRUNE …` last.
func pruneCmd(args []string) int {
	req := wire.PruneReq{}
	for _, a := range args {
		switch a {
		case "--dry-run":
			req.DryRun = true
		case "--unmerged":
			req.Unmerged = true
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("prune"))
			return 1
		}
	}
	return requestReply(context.Background(), project.OSEnv, func(_ *config.Config, id wire.Envelope) (wire.Req, error) {
		req.Envelope = id
		return &req, nil
	}, func(reply string) string { return reply })
}
