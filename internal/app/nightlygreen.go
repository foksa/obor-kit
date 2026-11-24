// nightlygreen.go: the lead's line for a GREEN full check whose non-gating steps report a failure the previous full
// check did not: a step that failed and did not before, or the same step with a different fail count. RED keeps its
// needs-you; the same failure as the previous check sends nothing.
package app

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/project"
)

// ruleNightly is the GREEN full check's changed-failure rule in the rule pass (rules.go).
const ruleNightly = "nightly.green"

// previousSummary is the summary of the full check before the nightly's run as last-full-check holds it ("" when there
// is none): the run overwrites the file. A read that fails says so and compares against none.
func previousSummary(a *App, p *proj, n *Nightly) string {
	if n.Last == nil {
		return ""
	}
	c, err := n.Last()
	if err != nil {
		a.logf("nightly of %s: %v", p.name, err)
		return ""
	}
	return c.Summary
}

// nightlyGreenRule is the GREEN full check's rule: the newest result is GREEN and its summary holds a non-gating
// failure that the previous summary did not, or with a different fail count. Action: one line to the lead naming every
// such step, its summary, the previous one and serve's log; no call, no needs-you. Keyed by the result's seq, so a
// restart lines none twice.
func nightlyGreenRule(_ context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := p.night
	if n.last == nil || n.last.Verdict != "GREEN" || n.lastSeq == 0 {
		return 0, action{}, false
	}
	if _, did := p.folded.did[event.Of{Kind: ruleNightly, Seq: n.lastSeq}]; did {
		return 0, action{}, false
	}
	line := nightlyGreenLine(p.root, *n.last)
	if line == "" {
		return 0, action{}, false
	}
	return n.lastSeq, action{line: line}, true
}

// nightlyGreenLine is the lead's line for a GREEN check out: every non-gating step whose failure is new or whose fail
// count changed, one clause each, and serve's log its details are in ("" when none changed).
func nightlyGreenLine(root string, out NightlyOut) string {
	shifts := nonGatingShifts(out.Summary, out.Was)
	if len(shifts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(shifts))
	for _, s := range shifts {
		was := s.was
		if was == "" {
			was = "ok"
		}
		parts = append(parts, fmt.Sprintf("%s %s (was: %s)", s.step, s.summary, was))
	}
	return fmt.Sprintf("[obor] nightly GREEN %s: %s — log: %s", sha7(out.SHA), strings.Join(parts, "; "),
		filepath.Join(project.StateDir(root), "logs", "serve.log"))
}

// nonGatingShift is a non-gating step's failure that changed since the previous summary: the step, the summary it
// reported and the one it did before ("" : it did not fail).
type nonGatingShift struct {
	step, summary, was string
}

// nonGatingShifts are the steps of current that failed and did not in previous, or failed in both with a different
// fail count, in the order the check ran them. A failed step whose summary reports no count of its own (a race
// report, say) counts as changed when its summary is not the same text.
func nonGatingShifts(current, previous string) []nonGatingShift {
	was := map[string]nonGatingStep{}
	for _, s := range nonGatingSteps(previous) {
		was[s.name] = s
	}
	var out []nonGatingShift
	for _, c := range nonGatingSteps(current) {
		if !c.failed {
			continue
		}
		p, seen := was[c.name]
		switch {
		case !seen || !p.failed:
			out = append(out, nonGatingShift{step: c.name, summary: c.summary})
		case c.fails != p.fails:
			out = append(out, nonGatingShift{step: c.name, summary: c.summary, was: p.summary})
		case c.fails == 0 && c.summary != p.summary:
			out = append(out, nonGatingShift{step: c.name, summary: c.summary, was: p.summary})
		}
	}
	return out
}

// nonGatingStep is one non-gating step of a full check's summary: its name, the summary it reported and the fail
// count that summary holds (0 when it names none), and whether it failed (a WARN line, or a nonzero fail count).
type nonGatingStep struct {
	name, summary string
	fails         int
	failed        bool
}

// nonGatingStepRE matches the start of a non-gating step as checks.nonGatingLine writes it ("<name> (non-gating)
// <summary>", "WARN <name> (rc N, non-gating): <summary>", "WARN prune (non-gating): <why>"): group 1 is "WARN" for a
// failed step, group 2 its name, group 3 a passing step's name.
var nonGatingStepRE = regexp.MustCompile(`(?:^|, )(?:(WARN) (\S+) \((?:rc \d+, )?non-gating\): |(\S+) \(non-gating\) )`)

// failCountRE matches a summary's "<n> fail" count ("0 fail", "8 fail"), not "failed" or "failing".
var failCountRE = regexp.MustCompile(`\b(\d+) fail\b`)

// durationRE is the nightly's total time at the end of its summary.
var durationRE = regexp.MustCompile(`, \d+s$`)

// nonGatingSteps are summary's non-gating steps in order: each runs from its marker to the next one's (the nightly
// writes the non-gating steps last, so nothing else follows them but its total time).
func nonGatingSteps(summary string) []nonGatingStep {
	ms := nonGatingStepRE.FindAllStringSubmatchIndex(summary, -1)
	var out []nonGatingStep
	for i, m := range ms {
		end := len(summary)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		s := summary[m[1]:end]
		if i+1 == len(ms) {
			s = durationRE.ReplaceAllString(s, "")
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ","))
		name, failed := "", false
		if m[2] != -1 {
			name, failed = summary[m[4]:m[5]], true
		} else {
			name = summary[m[6]:m[7]]
		}
		fails := 0
		for _, c := range failCountRE.FindAllStringSubmatch(s, -1) {
			n, _ := strconv.Atoi(c[1])
			fails += n
		}
		out = append(out, nonGatingStep{name: name, summary: s, fails: fails, failed: failed || fails > 0})
	}
	return out
}
