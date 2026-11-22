package guard

import (
	"errors"
	"testing"

	"obor/internal/testkit"
)

// askLines is a fake LeadLines: the text serve would hand (and record as delivered), and the asks it got
// ("<root> <agent>" each).
type askLines struct {
	text string
	err  error
	asks []string
}

func (a *askLines) ask(root, agent string) (string, error) {
	a.asks = append(a.asks, root+" "+agent)
	return a.text, a.err
}

const twoLines = "[obor other] release v0.12 is out\n[obor] needs-you T-05 (L-01): stalled → obor resume T-05"

// ocLeadFx: a project whose lead runs as OpenCode (deepseek: a kind with no inbox).
func ocLeadFx(t *testing.T) hfx {
	t.Helper()
	f, _ := promptFx(t, true)
	f.Config(testkit.Set("agents.roles.lead", "deepseek"))
	return f
}

// A lead whose kind has no inbox gets its queued lines as the prompt's context, asked once for its own agent (serve
// records them as delivered); with none queued, no context; a failed ask hands nothing.
func TestPromptLeadLinesNoInbox(t *testing.T) {
	t.Parallel()
	f := ocLeadFx(t)
	a := &askLines{text: twoLines + "\n"}
	want := "obor (lead): lines for you that arrived since your last turn (your session has no inbox: they come with " +
		"your next prompt or at a turn's end), one per row:\n" + twoLines
	if c := f.promptLines(t, f.Root, "what next?", "", a.ask, "OBOR_ROLE=lead"); c != want {
		t.Errorf("context %q, want %q", c, want)
	}
	if len(a.asks) != 1 || a.asks[0] != f.Root+" pj-lead" {
		t.Errorf("asks %q, want one for pj-lead in %s", a.asks, f.Root)
	}
	none := &askLines{}
	if c := f.promptLines(t, f.Root, "what next?", "", none.ask, "OBOR_ROLE=lead"); c != "" || len(none.asks) != 1 {
		t.Errorf("none queued: context %q, asks %d; want none, asked once", c, len(none.asks))
	}
	failed := &askLines{text: twoLines, err: errors.New("obor serve is not running")}
	if c := f.promptLines(t, f.Root, "what next?", "", failed.ask, "OBOR_ROLE=lead"); c != "" || len(failed.asks) != 1 {
		t.Errorf("a failed ask: context %q, asks %d; want none, asked once", c, len(failed.asks))
	}
}

// The lines and the handoff reminder share one context, the lines first.
func TestPromptLeadLinesWithHandoffReminder(t *testing.T) {
	t.Parallel()
	f := ocLeadFx(t)
	tr := transcript(f, f.Base+"/projects/p/s1.jsonl", 600000)
	a := &askLines{text: "[obor other] hi"}
	c := f.promptLines(t, f.Root, "go on", session("s1", tr), a.ask, "OBOR_ROLE=lead")
	want := "obor (lead): lines for you that arrived since your last turn (your session has no inbox: they come with " +
		"your next prompt or at a turn's end), one per row:\n[obor other] hi\n\nobor (lead): context is 600k (> 500k). " + leadWhen
	if c != want {
		t.Errorf("context %q, want %q", c, want)
	}
}

// A lead whose kind has an inbox (Claude, the default) gets its lines posted there: its hooks never ask, at a prompt
// or at a stop; nor does a worker's.
func TestLeadLinesInboxKindNotAsked(t *testing.T) {
	t.Parallel()
	f, o1 := promptFx(t, true)
	a := &askLines{text: twoLines}
	if c := f.promptLines(t, f.Root, "what next?", "", a.ask, "OBOR_ROLE=lead"); c != "" {
		t.Errorf("a Claude lead's prompt: context %q", c)
	}
	f.stopLines(t, "lead", f.Root, "", a.ask).allow("a Claude lead's stop")
	f.Config(testkit.Set("agents.roles.lead", "deepseek"))
	f.promptLines(t, o1, "[obor pj-lead] go", "", a.ask)
	if len(a.asks) != 0 {
		t.Errorf("asks %q, want none", a.asks)
	}
}

// A lead whose kind has no inbox and lines queued is blocked once at its turn's end with them: within the active stop
// loop (stop_hook_active) nothing is asked and it stops; none queued, it stops.
func TestStopLeadLinesBlockOnce(t *testing.T) {
	t.Parallel()
	f := ocLeadFx(t)
	a := &askLines{text: twoLines}
	f.stopLines(t, "lead", f.Root, "", a.ask).block("lines queued").has("reason",
		"obor (lead): lines for you arrived as your turn ended (your session has no inbox: they come with your next "+
			"prompt or at a turn's end); act on them, one per row:\n"+twoLines)
	if len(a.asks) != 1 || a.asks[0] != f.Root+" pj-lead" {
		t.Fatalf("asks %q, want one for pj-lead", a.asks)
	}
	f.stopLines(t, "lead", f.Root, `{"stop_hook_active":true}`, a.ask).allow("the stop loop's second stop")
	if len(a.asks) != 1 {
		t.Errorf("asked within the stop loop: %q", a.asks)
	}
	none := &askLines{}
	f.stopLines(t, "lead", f.Root, "", none.ask).allow("no lines queued")
	if len(none.asks) != 1 {
		t.Errorf("none queued: asks %d, want 1", len(none.asks))
	}
}
