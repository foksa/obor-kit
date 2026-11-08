package identity

import (
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
)

func pane(id, term, label, session string) port.Pane {
	return port.Pane{ID: id, Terminal: term, Label: label, Session: session, Agent: "claude"}
}

// table is the lookup of each agent, "-" for none: "w1:p1 session", "ambiguous w1:p1 w1:p2".
func table(r *Registry, agents ...string) string {
	var out []string
	for _, agent := range agents {
		e := r.Lookup(agent)
		switch {
		case len(e.Panes) > 0:
			out = append(out, agent+"=ambiguous "+strings.Join(e.Panes, " "))
		case e.Term != "":
			out = append(out, agent+"="+e.Pane+" "+e.Source)
		default:
			out = append(out, agent+"=-")
		}
	}
	return strings.Join(out, ", ")
}

type proofOf struct {
	agent string
	pane  port.Pane
	src   string
}

func all(string) bool { return true }

// The reconcile tie rules (RV-53-verify V1, T-504, T-505) as a table: each row's claims are accepted in one pass;
// the result is the same for every order the claims are added in.
func TestReconcileRules(t *testing.T) {
	t.Parallel()
	p1, p2, p3 := pane("w1:p1", "t1", "", ""), pane("w1:p2", "t2", "", ""), pane("w1:p3", "t3", "", "")
	rows := []struct {
		name   string
		claims []proofOf
		want   string // table(a, b, c)
	}{
		{"one claim owns its terminal", []proofOf{{"a", p1, srcLabel}}, "a=w1:p1 label, b=-, c=-"},
		{"an agent's equal claims on two terminals: ambiguous, owns none",
			[]proofOf{{"a", p2, srcLabel}, {"a", p1, srcLabel}}, "a=ambiguous w1:p1 w1:p2, b=-, c=-"},
		{"an agent's stronger claim wins over its weaker one: not ambiguous",
			[]proofOf{{"a", p1, srcSession}, {"a", p2, srcLabel}}, "a=w1:p1 session, b=-, c=-"},
		{"a stronger claim of another agent takes the terminal",
			[]proofOf{{"a", p1, srcLabel}, {"b", p1, srcStart}}, "a=-, b=w1:p1 start, c=-"},
		{"two agents' equal proof of one terminal: neither owns it",
			[]proofOf{{"a", p1, srcSession}, {"b", p1, srcSession}}, "a=-, b=-, c=-"},
		{"a tied terminal is not taken by a weaker claim in the same pass (V1)",
			[]proofOf{{"a", p1, srcSession}, {"b", p1, srcSession}, {"c", p1, srcStart}}, "a=-, b=-, c=-"},
		{"V1 control: without the tie the weaker claim owns it",
			[]proofOf{{"a", p1, srcSession}, {"c", p1, srcStart}}, "a=w1:p1 session, b=-, c=-"},
		{"V1 control: the start claim owns a free terminal",
			[]proofOf{{"c", p1, srcStart}}, "a=-, b=-, c=w1:p1 start"},
		{"a tie on one terminal leaves the tied agents' other claims free",
			[]proofOf{{"a", p1, srcSession}, {"b", p1, srcSession}, {"a", p2, srcLabel}}, "a=w1:p2 label, b=-, c=-"},
		{"an agent taken at a higher rank frees its weaker terminal for another",
			[]proofOf{{"a", p1, srcSession}, {"a", p3, srcLabel}, {"b", p3, srcLabel}}, "a=w1:p1 session, b=w1:p3 label, c=-"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if len(row.claims) == 0 {
				t.Fatal("precondition: a row has claims")
			}
			for _, order := range [][]proofOf{row.claims, reversed(row.claims)} {
				r := New()
				best := Claims{}
				for _, c := range order {
					best.Add(c.agent, c.pane, c.src)
				}
				r.Reconcile(best, all, func(string) {})
				if got := table(&r, "a", "b", "c"); got != row.want {
					t.Errorf("got %q, want %q", got, row.want)
				}
			}
		})
	}
}

func reversed(cs []proofOf) []proofOf {
	out := slices.Clone(cs)
	slices.Reverse(out)
	return out
}

// Claims.Add keeps the strongest proof of an agent and terminal, whatever the order.
func TestClaimsKeepTheStrongest(t *testing.T) {
	t.Parallel()
	p1 := pane("w1:p1", "t1", "", "")
	for _, order := range [][]string{{srcLabel, srcSession, srcStart}, {srcSession, srcLabel}, {srcStart, srcSession}} {
		best := Claims{}
		for _, src := range order {
			best.Add("a", p1, src)
		}
		if len(best) != 1 || best[[2]string{"a", "t1"}].src != srcSession {
			t.Errorf("order %v: %v, want one claim, session", order, best)
		}
	}
}

// WhoIs is the inverse of the table, keyed by terminal: a pane moved to another id keeps its agent.
func TestWhoIsByTerminal(t *testing.T) {
	t.Parallel()
	r := New()
	best := Claims{}
	best.Add("a", pane("w1:p1", "t1", "a", ""), srcLabel)
	r.Reconcile(best, all, func(string) {})
	if agent, ok := r.WhoIs(pane("w2:p9", "t1", "", "")); !ok || agent != "a" {
		t.Errorf("moved pane: %q %v, want a", agent, ok)
	}
	if agent, ok := r.WhoIs(pane("w1:p1", "t2", "", "")); ok {
		t.Errorf("another terminal at the same id: %q, want none", agent)
	}
	if agent, ok := r.WhoIs(port.Pane{ID: "w1:p1"}); ok {
		t.Errorf("no terminal: keyed by pane id %q, got %q, want none", TermOf(port.Pane{ID: "w1:p1"}), agent)
	}
}

// Proof ranks session over start over label; an agent the config does not name has only its label.
func TestProof(t *testing.T) {
	t.Parallel()
	r := New()
	r.RecordStart("a", StartRow{Pane: "w1:p1", Terminal: "t1", Session: "s1"})
	rows := []struct {
		name  string
		sid   string
		pane  port.Pane
		named bool
		want  string
	}{
		{"recorded session", "s9", pane("w1:p5", "t5", "", "s9"), true, srcSession},
		{"recorded session as a transcript path", "s9", pane("w1:p5", "t5", "", "/x/s9.jsonl"), true, srcSession},
		{"the start's session", "", pane("w1:p5", "t5", "", "s1"), true, srcSession},
		{"the start's terminal, moved", "", pane("w2:p2", "t1", "", ""), true, srcStart},
		{"the start's pane id, another terminal", "", pane("w1:p1", "t2", "", ""), true, ""},
		{"the label", "", pane("w1:p5", "t5", "a", ""), true, srcLabel},
		{"unnamed: session and start are no proof", "s9", pane("w1:p1", "t1", "", "s9"), false, ""},
		{"unnamed: the label still is", "s9", pane("w1:p1", "t1", "a", "s9"), false, srcLabel},
	}
	for _, row := range rows {
		if got := r.Proof("a", row.sid, row.pane, row.named); got != row.want {
			t.Errorf("%s: %q, want %q", row.name, got, row.want)
		}
	}
}

// RecordStart: a row without a terminal keeps the pane's known one; another agent's row on that pane or terminal goes.
// Reconcile retires a row whose terminal another agent owns, and one of an agent the config does not name.
func TestStartRows(t *testing.T) {
	t.Parallel()
	r := New()
	r.RecordStart("a", StartRow{Pane: "w1:p1", Terminal: "t1"})
	r.RecordStart("a", StartRow{Pane: "w1:p1"})
	if row, ok := r.Start("a"); !ok || row.Terminal != "t1" {
		t.Fatalf("a's row %+v %v, want terminal t1 kept", row, ok)
	}
	r.RecordStart("b", StartRow{Pane: "w1:p7", Terminal: "t1"})
	if _, ok := r.Start("a"); ok || !slices.Equal(r.StartedAgents(), []string{"b"}) {
		t.Fatalf("started %v, want b only (a's terminal taken)", r.StartedAgents())
	}
	r.RecordStart("c", StartRow{Pane: "w1:p8", Terminal: "t8"})
	best := Claims{}
	best.Add("a", pane("w1:p7", "t1", "", "s1"), srcSession)
	var lines []string
	r.Reconcile(best, func(agent string) bool { return agent != "c" }, func(l string) { lines = append(lines, l) })
	if got := r.StartedAgents(); len(got) != 0 {
		t.Errorf("started %v, want none: b's terminal is a's, c is not named", got)
	}
	log := strings.Join(lines, "\n")
	for _, want := range []string{"b's start row retired: pane w1:p7 runs a now (session)",
		"c's start row retired: the config names no such agent", "agent a identified at pane w1:p7 (session)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q lacks %q", log, want)
		}
	}
}

// Reconcile logs each change once: identified, ambiguous, a stray label, no longer identified.
func TestReconcileLogsOnce(t *testing.T) {
	t.Parallel()
	r := New()
	var lines []string
	pass := func(cs ...proofOf) {
		best := Claims{}
		for _, c := range cs {
			best.Add(c.agent, c.pane, c.src)
		}
		r.Reconcile(best, all, func(l string) { lines = append(lines, l) })
	}
	p1, p2 := pane("w1:p1", "t1", "a", "s1"), pane("w1:p2", "t2", "a", "")
	pass(proofOf{"a", p1, srcSession}, proofOf{"a", p2, srcLabel})
	pass(proofOf{"a", p1, srcSession}, proofOf{"a", p2, srcLabel})
	want := []string{"agent a identified at pane w1:p1 (session)", "pane w1:p2 labelled a too, not its session"}
	if !slices.Equal(lines, want) {
		t.Fatalf("two equal passes logged %q, want %q", lines, want)
	}
	lines = nil
	pass(proofOf{"a", p1, srcLabel}, proofOf{"a", p2, srcLabel})
	pass(proofOf{"a", p1, srcLabel}, proofOf{"a", p2, srcLabel})
	if want := []string{"agent a " + NotIdentified("a", []string{"w1:p1", "w1:p2"})}; !slices.Equal(lines, want) {
		t.Fatalf("ambiguity logged %q, want %q", lines, want)
	}
	lines = nil
	pass()
	if want := []string{}; len(lines) != 0 {
		t.Fatalf("an ambiguity ending in nothing logged %q, want %q (it was not identified)", lines, want)
	}
	pass(proofOf{"a", p1, srcLabel})
	lines = nil
	pass()
	if want := []string{"agent a no longer identified"}; !slices.Equal(lines, want) {
		t.Fatalf("logged %q, want %q", lines, want)
	}
}

// CrossCheck logs herdr's differing session of agent's own pane once per value, never for another agent's pane.
func TestCrossCheckOncePerValue(t *testing.T) {
	t.Parallel()
	r := New()
	var lines []string
	log := func(l string) { lines = append(lines, l) }
	r.CrossCheck("a", "s1", pane("w1:p1", "t1", "b", "s2"), true, log)
	if len(lines) != 0 {
		t.Fatalf("another agent's pane logged %q", lines)
	}
	for range 2 {
		r.CrossCheck("a", "s1", pane("w1:p1", "t1", "a", "s2"), true, log)
	}
	r.CrossCheck("a", "s1", pane("w1:p1", "t1", "a", "s1"), true, log)
	if len(lines) != 1 || !strings.Contains(lines[0], "herdr reports session s2 for a at pane w1:p1") {
		t.Fatalf("logged %q, want the s2 line once", lines)
	}
}
