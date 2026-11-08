package port

import "context"

// Workflow is the project's git workflow behind one interface (T-1029, docs/plan/architecture-target.md §3): how a
// task's work becomes a candidate, is tested and published onto the target, the lanes it runs in, the target a
// dispatch reads, the git of a review's start and close, and the promotion. Config workflow.kind selects the
// implementation at serve start; scratch-ff (internal/checks' ScratchFF, the default) is the one today.
//
// The plan's steps by today's names: Submit; Candidate is Scratch.Gate (and Resolved, the integrator's merge); Test;
// Publish (PublishPending: not merged yet, the Landed rule looks again next pass); Landed; Prepare and Free (the lane);
// Target; the review's Head, Files, Detach, Close (and Closed, the close's crash cut); Promotion.
type Workflow interface {
	Scratch
	LaneSetup
	Review
	// Target is the target branch's sha now, and whether task's branch has commits the target lacks.
	Target(ctx context.Context, task string) (sha string, ahead bool, err error)
	// Promotion is the workflow's promotion of its target (nil: none; after_merge then runs after each publish).
	Promotion() *Promote
}

// Review is the git a review's start and close do on the project (scratch-ff: the main checkout, cmd/obor's
// reviewGit). No method decides what a review covered: that is the app's fold (reviewfold.go, T-1023).
type Review interface {
	// Head is branch's sha.
	Head(ctx context.Context, branch string) (string, error)
	// Files is the files changed from from to to (from "": every file of to).
	Files(ctx context.Context, from, to string) ([]string, error)
	// Detach puts the role worktree at dir on sha, detached, adding it when it is missing (T-853).
	Detach(ctx context.Context, dir, sha string) error
	// Close commits c in the main checkout (T-926; serve calls it admitted, reviewclose.go). Refused, an error ending
	// "nothing changed": the checkout not on c.Branch, dirty outside BACKLOG.md and c's files, no "## Open" section
	// in BACKLOG.md for c.Accepts, or c.Reviewed no commit. Else c's files deleted, c.Accepts at the top of
	// BACKLOG.md's Open section and the commit, BACKLOG.md with it.
	Close(ctx context.Context, c ReviewCommit) error
	// Closed is the subject of review id's close commit on branch since from (its start's head; "": all of branch),
	// newest first ("": none): the close's crash cut, its one read (reviewCloseRead).
	Closed(ctx context.Context, branch, from, id string) (string, error)
}

// ReviewCommit is a review's close as the main checkout takes it: the integration branch, the review's id and its
// review file (absolute; its verify file <id>-verify.md beside it goes too when there), the accepted findings' lines
// for BACKLOG.md, the head the reviewer last finished (full sha) and the commit's subject and body (CloseCommit).
type ReviewCommit struct {
	Branch, ID, File, Reviewed, Subject, Body string
	Accepts                                   []string
}

// Promote is a workflow's promotion (T-974; scratch-ff: branches.main promoted to branches.promote when that is set).
type Promote struct {
	Branch string                 // branches.promote: the promoted branch (main)
	Target string                 // branches.main: the branch tasks merge into (staging)
	Weight int                    // merge.promote_weight: the merged weight that triggers a promotion
	Weigh  func(level string) int // merge.weights: a difficulty level's weight
	// Run runs the full suite at sha in the promotion worktree: GREEN or RED (the promoted branch at sha or past it:
	// GREEN, nothing run); an error: it ended neither (tried again).
	Run func(ctx context.Context, sha string) (PromoteOut, error)
	// Passed: the promoted branch is at sha or past it (T-984): nothing to promote there.
	Passed func(ctx context.Context, sha string) (bool, error)
	// FF fast-forwards the promoted branch to sha, decided by ancestry (T-984): moved, already (at sha or past it) or
	// refused (diverged); an error: git.
	FF func(ctx context.Context, sha string) (FFOut, error)
	// After runs commands.after_merge in the promotion worktree at sha, OBOR_ROOT set; since is the sha promoted before
	// ("": none), for its when paths. ran | skipped | WARN …; an error: tried again.
	After func(ctx context.Context, sha, since string) (string, error)
}
