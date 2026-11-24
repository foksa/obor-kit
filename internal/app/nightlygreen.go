// nightlygreen.go: the lead's line for a GREEN full check, scheduled or by hand, whose non-gating steps' outcome
// differs from the previous full check's: a step that fails and did not, or fails with a different fail count. RED
// keeps its needs-you; the same outcome as the previous check sends nothing.
package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ruleNightly is the GREEN full check's changed-failure rule in the rule pass (rules.go).
const ruleNightly = "nightly.green"

// nightlyGreenRule is the GREEN full check's rule: the last full check (checks' record, a hand run's too, one that ran
// while serve was down read at the next start) is GREEN and a non-gating step's outcome differs from the previous
// check's. Action: one line to the lead naming every such step, its previous outcome and its log; no call, no
// needs-you. Keyed by the check's time (unix seconds), so each check is lined once, a restart included.
func nightlyGreenRule(_ context.Context, _ *App, _ *proj, fx *Loop, _ time.Time) (int64, action, bool) {
	if fx == nil || fx.Nightly == nil || fx.Nightly.Last == nil {
		return 0, action{}, false
	}
	n := fx.Nightly
	c, err := n.Last()
	if err != nil { // handCheck logs it on the same pass
		return 0, action{}, false
	}
	if c.Verdict != "GREEN" || c.At.IsZero() {
		return 0, action{}, false
	}
	line := nightlyGreenLine(c)
	if line == "" {
		return 0, action{}, false
	}
	return c.At.Unix(), action{line: line}, true
}

// nightlyGreenLine is the lead's line for a GREEN check c: one clause per changed non-gating step (changedSteps) with
// its report, its previous outcome (ok: it did not fail, or did not run) and its log, the step's summary line when it
// has none ("" when no step changed).
func nightlyGreenLine(c FullCheck) string {
	shifts := changedSteps(c.Steps, c.Was)
	if len(shifts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(shifts))
	for _, s := range shifts {
		was := "ok"
		if s.was != nil && !s.was.OK {
			was = s.was.Report
		}
		log := "log " + s.now.Log
		if s.now.Log == "" {
			log = "no log: " + s.now.Line
		}
		parts = append(parts, fmt.Sprintf("%s %s (was: %s), %s", s.now.Name, s.now.Report, was, log))
	}
	return fmt.Sprintf("[obor] nightly GREEN %s: %s", sha7(c.SHA), strings.Join(parts, "; "))
}

// stepShift is a non-gating step whose outcome changed: its outcome now and in the previous check (nil: it did not
// run there).
type stepShift struct {
	now CheckStep
	was *CheckStep
}

// changedSteps are the non-gating steps of now that failed while they passed or did not run in was, or failed in both
// with a different fail count, in the order the check ran them.
func changedSteps(now, was []CheckStep) []stepShift {
	var out []stepShift
	for _, c := range now {
		if c.Gating || c.OK {
			continue
		}
		var prev *CheckStep
		for i := range was {
			if was[i].Name == c.Name {
				prev = &was[i]
			}
		}
		if prev == nil || prev.OK || !sameCount(c.Fails, prev.Fails) {
			out = append(out, stepShift{now: c, was: prev})
		}
	}
	return out
}

// sameCount is whether two reported fail counts are the same (none and none are).
func sameCount(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
