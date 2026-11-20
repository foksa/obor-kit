package port

import "context"

// Pruner is the main repo's git behind obor prune: the task branches and the registered worktrees, and the deletes.
// It decides nothing: which of them may go is serve's (internal/app/gitprune.go). A Pruner never deletes a branch
// that is checked out in a worktree, and removes a worktree only when git agrees it is clean.
type Pruner interface {
	// Branches is every local branch under task/ with its tip.
	Branches(ctx context.Context) ([]PruneBranch, error)
	// CheckedOut is the names of the branches checked out in any worktree, the main checkout's too.
	CheckedOut(ctx context.Context) (map[string]bool, error)
	// DeleteBranch deletes b when its tip is still b.SHA; git refuses a branch checked out in a worktree.
	DeleteBranch(ctx context.Context, b PruneBranch) error
	// Trees is every registered worktree but the main checkout, with its head.
	Trees(ctx context.Context) ([]PruneTree, error)
	// Dirt is why the worktree at dir must stay ("": it is clean and holds no commit that only it keeps): uncommitted
	// changes (obor's own links aside), or commits no branch or tag holds.
	Dirt(ctx context.Context, dir string) (string, error)
	// RemoveTree removes the clean worktree at dir (never forced).
	RemoveTree(ctx context.Context, dir string) error
	// PruneTrees removes the registration of each worktree in dirs, whose dir is gone, and only those (never a
	// repository-wide git worktree prune); the failures come back by dir.
	PruneTrees(ctx context.Context, dirs []string) map[string]error
}

// PruneBranch is a local branch and its tip.
type PruneBranch struct{ Name, SHA string }

// PruneTree is a registered worktree: its dir (physical), its branch ("": detached), its head and whether its dir
// is gone.
type PruneTree struct {
	Dir, Branch, SHA string
	Gone             bool
}
