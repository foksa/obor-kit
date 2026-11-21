package main

import "testing"

// obor events' arguments: --follow, --json and --after <seq> (a non-negative integer), each at most once, in any order;
// anything else is the usage. The binary's row: TestEventsCommand.
func TestEventsArguments(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args         []string
		follow, json bool
		after        int64
	}{
		{nil, false, false, 0},
		{[]string{"--json"}, false, true, 0},
		{[]string{"--follow", "--after", "7"}, true, false, 7},
		{[]string{"--after", "0", "--json", "--follow"}, true, true, 0},
	} {
		follow, asJSON, after, ok := eventsArgs(c.args)
		if !ok || follow != c.follow || asJSON != c.json || after != c.after {
			t.Errorf("eventsArgs(%q) = %v, %v, %d, %v; want %v, %v, %d, true", c.args, follow, asJSON, after, ok, c.follow, c.json, c.after)
		}
	}
	for _, bad := range [][]string{{"--after"}, {"--after", "-1"}, {"--after", "x"}, {"--json", "--json"}, {"nope"},
		{"--follow", "--follow"}, {"--after", "1", "--after", "2"}} {
		if _, _, _, ok := eventsArgs(bad); ok {
			t.Errorf("eventsArgs(%q) is ok, want the usage", bad)
		}
	}
}
