package kind

import (
	"slices"
	"strings"
	"testing"
)

// parseModels counts only `slug<TAB>label` lines: a notice before the list, a line whose columns spaces separate, a
// blank line, a line with no slug and a label that wrapped are no models.
func TestParseModels(t *testing.T) {
	out := "Fetching available models...\n" +
		"gemini-4-neon-high\tGemini 4 Neon (High)\n" +
		"\n" +
		"gemini-3.8-flash-low Gemini 3.8 Flash (Low)\n" +
		"\tOrchestrator (Neon)\n" +
		"  gemini-3.8-flash-low \t Gemini 3.8 Flash (Low) \r\n" +
		"Available models: 2"
	got := parseModels([]byte(out))
	want := []model{
		{"gemini-4-neon-high", "Gemini 4 Neon (High)"},
		{"gemini-3.8-flash-low", "Gemini 3.8 Flash (Low)"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parseModels = %+v, want %+v", got, want)
	}
}

// slugLine puts the slugs on one line and cuts a long list after slugsMax bytes at a slug's end with an ellipsis.
func TestSlugLine(t *testing.T) {
	if got := slugLine([]model{{slug: "a"}, {slug: "b"}}); got != "a, b" {
		t.Errorf("slugLine = %q, want %q", got, "a, b")
	}
	long := make([]model, 100)
	for i := range long {
		long[i] = model{slug: "gemini-4-neon-high"}
	}
	got := slugLine(long)
	cut := strings.TrimSuffix(got, ", …")
	if cut == got || len(cut) > slugsMax || strings.Contains(got, "\n") {
		t.Fatalf("a long list's line = %q, want it cut at a slug's end with an ellipsis", got)
	}
	if last := cut[strings.LastIndex(cut, ", ")+2:]; last != "gemini-4-neon-high" {
		t.Errorf("a long list's line ends mid-slug: %q", got)
	}
}
