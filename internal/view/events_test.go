package view

// T-688: the view says which events are bad, so a renderer never parses the wording: a gate that is not green and a
// hold are bad; a green gate is not, even when its summary counts a failure.

import (
	"encoding/json"
	"testing"

	"obor/internal/app"
)

// TestEventLineBad: the wording and the bad flag of the records a gate, a hold, a question and a merge word.
func TestEventLineBad(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		kind     string
		step     string
		data     string
		wantLine string
		wantBad  bool
	}{
		{"a green gate whose summary counts a fail", app.KindResult, app.StepGate,
			`{"outcome":"green","line":"check 25 pass, 0 fail"}`,
			"gate T-1 pass (check 25 pass, 0 fail)", false},
		{"a red gate", app.KindResult, app.StepGate,
			`{"outcome":"red","line":"RED T-1: e2e failed"}`,
			"gate T-1 fail (RED T-1: e2e failed)", true},
		{"a hold", app.KindFact, app.StepNeedsYou,
			`{"cause":"held","reason":"held: decide"}`,
			"held T-1: decide", true},
		{"a question", app.KindFact, app.StepNeedsYou,
			`{"reason":"asks: may I?"}`, "", false},
		{"a merge", app.KindFact, app.StepMerged,
			`{"head":"abcdef0123"}`, "merged T-1 abcdef0", false},
	} {
		line, bad := eventLine(app.Record{Task: "T-1", Kind: c.kind, Step: c.step, Data: json.RawMessage(c.data)})
		if line != c.wantLine || bad != c.wantBad {
			t.Errorf("%s: line %q bad %v, want %q bad %v", c.name, line, bad, c.wantLine, c.wantBad)
		}
	}
}
