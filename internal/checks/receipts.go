package checks

import (
	"fmt"
	"os"
	"time"

	"obor/internal/run"
)

// checkedTree is the tree the checks run on: the index's (written from it: the candidate checked out, plus what a step
// staged since, RV-40 #1). "" when no receipt exists to compare it with (nothing is read then); git failing is an
// error, like any git I/O of a run.
func (r *runner) checkedTree() (string, error) {
	if _, err := os.Stat(run.ReceiptDir(r.state)); err != nil {
		return "", nil
	}
	tree, err := r.git(r.dir, "write-tree")
	if err != nil {
		return "", fmt.Errorf("cannot read the tree to check in %s", r.dir)
	}
	return tree, nil
}

// reuse is the worker's green run of commands.<name> on this very tree (a receipt written by obor run, matching tree,
// check and command text, younger than merge.receipt_max_age): the check is not run, and its place in the result line
// reads "reused <lane> <tree7>". A reused merge stays partial (fact merged, T-525), so a full check still comes due.
func (r *runner) reuse(name string) (string, bool) {
	c, ok := r.command(name)
	if !ok || !c.Heavy || r.noReuse {
		return "", false
	}
	// The tree as of now: an earlier command (the guard, a check, the baseline) may have changed the index.
	tree, err := r.checkedTree()
	if err != nil || tree == "" {
		return "", false
	}
	maxAge := time.Duration(r.Cfg.Data.Merge.ReceiptMaxAge) * time.Second
	lane, ok := run.FindReceipt(r.state, tree, c, maxAge, time.Now())
	if !ok {
		return "", false
	}
	r.class.Partial = true
	r.reused = append(r.reused, tree)
	return "reused " + lane + " " + tree[:min(7, len(tree))], true
}
