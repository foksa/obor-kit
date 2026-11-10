package checks

import (
	"testing"

	"obor/internal/app/port"
)

// the merge's body is the worker report's whole Summary section (an opening sentence and its bullets), on every
// path — the gate's own merge, a moved target, the integrator's resolved merge — while the no-title subject stays its
// first line alone.
func TestMergeMessageSummaryBody(t *testing.T) {
	t.Parallel()
	const summary = "Cut the subject at 100."
	const body = "Cut the subject at 100.\n" +
		"- internal/app/steps.go: the whole section into GateInput.Body\n" +
		"- internal/checks/scratch.go: the message"
	for _, c := range []struct {
		name, title, moved, resolver, want string
	}{
		{"lane, a stored title", "Stored", "", "", "Merge T-05: Stored\n\nSummary: " + body},
		{"lane, no title", "", "", "", "Merge T-05: Cut the subject at 100.\n\nSummary: " + body},
		{"moved", "", "Merged onto the moved main at abc", "",
			"Merge T-05: Cut the subject at 100.\n\nMerged onto the moved main at abc\n\nSummary: " + body},
		{"resolved", "Stored", "", "kept both sides", "Merge T-05: Stored\n\nkept both sides\n\nSummary: " + body},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := port.GateInput{Task: "T-05", Title: c.title, Summary: summary, Body: body}
			if got := mergeMessage(in, c.moved, c.resolver); got != c.want {
				t.Errorf("message\n%s\nwant\n%s", got, c.want)
			}
		})
	}
	// No Body (an old record): the summary's first line is the body.
	if got, want := mergeMessage(port.GateInput{Task: "T-05", Title: "Stored", Summary: summary}, "", ""),
		"Merge T-05: Stored\n\nSummary: "+summary; got != want {
		t.Errorf("without a Body: message\n%s\nwant\n%s", got, want)
	}
}
