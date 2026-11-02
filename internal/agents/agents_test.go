package agents_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	_ "obor/internal/agents/opencode"
)

// T-495: the registry is the one list of kinds (sorted), Default is one of them, an unknown kind is no kind (Cursor's
// rows went), and a second registration of a name panics.
func TestRegistry(t *testing.T) {
	if got := agents.Names(); !slices.Equal(got, []string{"claude", "codex", "opencode"}) {
		t.Errorf("Names = %q", got)
	}
	if k, ok := agents.Lookup(agents.Default); !ok || k.Name() != agents.Default {
		t.Errorf("Default %s is no registered kind", agents.Default)
	}
	for _, name := range []string{"cursor", "gemini", ""} {
		if _, ok := agents.Lookup(name); ok {
			t.Errorf("Lookup(%q) ok", name)
		}
	}
	k, _ := agents.Lookup("codex")
	defer func() {
		if recover() == nil {
			t.Error("a second codex registered")
		}
	}()
	agents.Register(k)
}

// DD-v0.13-verify "Design 2 again" gap 1: a kind's programs default to its name; ByProgram finds a kind by an
// executable's base name and no kind by another word.
func TestByProgram(t *testing.T) {
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		if got := agents.Programs(k); !slices.Equal(got, []string{name}) {
			t.Errorf("Programs(%s) = %q, want its name", name, got)
		}
		if b, ok := agents.ByProgram(name); !ok || b.Name() != name {
			t.Errorf("ByProgram(%q) = %v, %v", name, b, ok)
		}
	}
	for _, w := range []string{"git", "/usr/bin/claude", ""} {
		if _, ok := agents.ByProgram(w); ok {
			t.Errorf("ByProgram(%q) ok", w)
		}
	}
}

// T-581: Resumes names the kinds that honour a session id: Claude and Codex yes, OpenCode no (its session is
// recorded but never resumed), an unknown kind no.
func TestResumes(t *testing.T) {
	for _, c := range []struct {
		kind string
		want bool
	}{{"claude", true}, {"codex", true}, {"opencode", false}, {"", false}, {"cursor", false}} {
		if got := agents.Resumes(c.kind); got != c.want {
			t.Errorf("Resumes(%q) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// T-568: ResetIn reads an epoch after '|', else a clock time ("resets 6pm", "resets at 6:30 PM", "try again at
// 11:01 PM.") as its next occurrence after at in at's location (12 am is midnight, 12 pm noon); else none.
func TestResetIn(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := time.Date(2026, 10, 30, 14, 0, 0, 0, loc)
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	for _, c := range []struct {
		text string
		want time.Time
	}{
		{"usage limit reached|1793383200", time.Unix(1793383200, 0)},
		{"resets 6pm", day(30, 18, 0)},
		{"resets at 6:30 PM", day(30, 18, 30)},
		{"or try again at 11:01 PM.", day(30, 23, 1)},
		{"try again at 4:01 AM.", day(31, 4, 1)},
		{"resets 12am", day(31, 0, 0)},
		{"resets 12:15pm", day(31, 12, 15)},
		{"resets 2pm", day(30, 14, 0)},
		{"429 Too Many Requests", time.Time{}},
		{"resets 13pm", time.Time{}},
	} {
		if got := agents.ResetIn(c.text, at); !got.Equal(c.want) {
			t.Errorf("ResetIn(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestSkills(t *testing.T) {
	for _, c := range []struct {
		kind string
		want bool
	}{{"claude", true}, {"codex", false}, {"opencode", false}, {"", false}} {
		if got := agents.Skills(c.kind); got != c.want {
			t.Errorf("Skills(%q) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// noEffort is a kind that takes no effort (no registered kind is one).
type noEffort struct{ agents.Kind }

func (noEffort) Name() string      { return "plain" }
func (noEffort) Efforts() []string { return nil }

// T-695: an effort is checked per kind: each kind's own list, a value outside it refused naming the list, a kind with
// no effort and an unknown kind refused, "" always fine.
func TestCheckEffort(t *testing.T) {
	for _, c := range []struct{ kind, effort, refusal string }{
		{"claude", "xhigh", ""}, {"claude", "max", ""}, {"claude", "minimal", "not one of claude's (low, medium, high, xhigh, max)"},
		{"codex", "minimal", ""}, {"codex", "max", "not one of codex's (minimal, low, medium, high, xhigh)"},
		{"opencode", "max", ""}, {"cursor", "high", "has no bridge"}, {"cursor", "", ""}, {"claude", "", ""},
	} {
		err := agents.CheckEffort(c.kind, c.effort)
		if c.refusal == "" && err != nil || c.refusal != "" && (err == nil || !strings.Contains(err.Error(), c.refusal)) {
			t.Errorf("CheckEffort(%s, %q) = %v, want %q", c.kind, c.effort, err, c.refusal)
		}
	}
	if err := agents.CheckKindEffort(noEffort{}, "high"); err == nil || !strings.Contains(err.Error(), "kind plain takes no effort") {
		t.Errorf("a kind with no effort: %v", err)
	}
}
