package port

import "context"

// Workflow is the project's git workflow behind one interface (docs/plan/architecture-target.md §3): how a
// task's work becomes a candidate, is tested and published onto the target, the lanes it runs in, the target a
// dispatch reads, and the git of a review's start and close. Config workflow.kind selects the
// implementation at serve start; scratch-ff (internal/checks' ScratchFF, the default) is the one today.
//
// The plan's steps by today's names: Submit; Candidate is Scratch.Gate (and Resolved, the integrator's merge); Test;
// Publish (PublishPending: not merged yet, the Landed rule looks again next pass); Landed; Prepare and Free (the lane);
// Target; the review's Head, Files, Detach, Close (and Closed, the close's crash cut).
type Workflow interface {
	Scratch
	LaneSetup
	Review
	// Target is the target branch's sha now, and whether task's branch has commits the target lacks.
	Target(ctx context.Context, task string) (sha string, ahead bool, err error)
}

// Review is the git a review's start and close do on the project (scratch-ff: the main checkout, cmd/obor's
// reviewGit). No method decides what a review covered: that is the app's fold (reviewfold.go).
type Review interface {
	// Head is branch's sha.
	Head(ctx context.Context, branch string) (string, error)
	// Files is the files changed from from to to (from "": every file of to).
	Files(ctx context.Context, from, to string) ([]string, error)
	// Detach puts the role worktree at dir on sha, detached, adding it when it is missing. The workflow's
	// implementation then runs the lane setup a lane gets there (lanes.ready, lanes.setup when red, lanes.ready
	// again): the role's tests run in that worktree.
	Detach(ctx context.Context, dir, sha string) error
	// Close commits c in the main checkout (serve calls it admitted, reviewclose.go). Refused, an error ending
	// "nothing changed": the checkout not on c.Branch, dirty outside a BACKLOG.md, docs/accepted.md and c's files, or
	// c.Reviewed no commit. Else c's files deleted, c.Accepts appended to docs/accepted.md (created when missing) and
	// the commit, each dirty BACKLOG.md and docs/accepted.md with it.
	Close(ctx context.Context, c ReviewCommit) error
	// Closed is the subject of review id's close commit on branch since from (its start's head; "": all of branch),
	// newest first ("": none): the close's crash cut, its one read (reviewCloseRead).
	Closed(ctx context.Context, branch, from, id string) (string, error)
}

// ReviewCommit is a review's close as the main checkout takes it: the integration branch, the review's id and its
// review file (absolute; its verify file <id>-verify.md beside it goes too when there), the accepted findings' lines
// for docs/accepted.md, the head the reviewer last finished (full sha) and the commit's subject and body (CloseCommit).
type ReviewCommit struct {
	Branch, ID, File, Reviewed, Subject, Body string
	Accepts                                   []string
}
