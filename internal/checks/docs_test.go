package checks

import "testing"

// TestHoldsAll: a line is a hit only while it holds every word, case aside, each as a fixed string (a word inside a
// longer token matches too, as git grep -F finds it).
func TestHoldsAll(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		line  string
		words []string
		want  bool
	}{
		{"alpha beta on one line", []string{"alpha", "beta"}, true},
		{"alpha only here", []string{"alpha", "beta"}, false},
		{"MiXeD CaSe both", []string{"mixed", "case"}, true},
		{"MiXeD CaSe both", []string{"MIXED", "CASE"}, true},
		{"obor-kit's docs", []string{"obor"}, true},
		{"alpha", []string{"alpha", "alpha"}, true},
		{"", []string{"alpha"}, false},
	} {
		if got := holdsAll(c.line, c.words); got != c.want {
			t.Errorf("holdsAll(%q, %v) = %v, want %v", c.line, c.words, got, c.want)
		}
	}
}
