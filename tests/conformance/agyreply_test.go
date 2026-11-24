package main

import "testing"

// A marker in the prompt alone (the model asked back) is no reply; the model's step carrying it is.
func TestAgyReplied(t *testing.T) {
	prompt := `{"type":"USER_INPUT","content":"<USER_REQUEST>\n[obor conf] Reply with exactly AGY-FIRST-OK"}`
	askedBack := `{"type":"PLANNER_RESPONSE","content":"I am worker cf-l-01 on (no task). Did you mean to type here?"}`
	reply := `{"type":"PLANNER_RESPONSE","content":"AGY-FIRST-OK"}`
	cases := []struct {
		name, transcript string
		want             bool
	}{
		{"the prompt alone", prompt, false},
		{"the model asked back", prompt + "\n" + askedBack, false},
		{"the model replied", prompt + "\n" + askedBack + "\n" + reply, true},
		{"a line that does not parse", prompt + "\nAGY-FIRST-OK", false},
	}
	for _, c := range cases {
		got := agyReplied(c.transcript, "AGY-FIRST-OK")
		if got != c.want {
			t.Errorf("%s: agyReplied = %v, want %v", c.name, got, c.want)
		}
	}
}
