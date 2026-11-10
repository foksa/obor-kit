package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
)

// The versions of the tools obor drives. The pass reads them every versionsEvery, records a change as fact
// version and the rule version.changed lines the lead once per change. On the rule harness (a lead, a clock the test
// moves, the rule pass run by hand): no process, no socket, no sleep.

// versionRig is the rule harness with a scripted reader: the map the test changes, each call counted.
type versionRig struct {
	*harness
	fx    *Loop
	read  map[string]string
	calls int
}

func newVersionRig(t *testing.T) *versionRig {
	t.Helper()
	v := &versionRig{harness: ruleHarness(t), read: map[string]string{}}
	v.fx = &Loop{Versions: func(context.Context) map[string]string {
		v.calls++
		return v.read
	}}
	v.a.mu.Lock()
	ver := v.a.herdrVer
	v.a.mu.Unlock()
	if ver != "" {
		t.Fatalf("precondition: the harness's herdr names version %q, want none", ver)
	}
	return v
}

// tick moves the clock by d and runs one versionTick.
func (v *versionRig) tick(d time.Duration) {
	v.now = v.now.Add(d)
	v.a.versionTick(context.Background(), v.proj(), v.fx)
}

// facts is the log's version facts, in order: each "<what> <version> <was>".
func (v *versionRig) facts() []string {
	v.t.Helper()
	var out []string
	for _, r := range readLog(v.t, v.root) {
		if r.Task != event.VersionTask {
			continue
		}
		var d versionData
		if r.Kind != event.KindFact || r.Step != event.StepVersion || json.Unmarshal(r.Data, &d) != nil {
			v.t.Fatalf("a record of task version that is no version fact: %+v", r)
		}
		out = append(out, strings.TrimSpace(d.What+" "+d.Version+" <- "+d.Was))
	}
	return out
}

// want fails unless the log's version facts are exactly these.
func (v *versionRig) want(when string, facts ...string) {
	v.t.Helper()
	if got := v.facts(); strings.Join(got, "\n") != strings.Join(facts, "\n") {
		v.t.Fatalf("%s: version facts %q, want %q", when, got, facts)
	}
}

// rule is versionRule's answer now.
func (v *versionRig) rule() (int64, action, bool) {
	return versionRule(context.Background(), v.a, v.proj(), v.fx, v.now)
}

// seq is the seq of what's folded version fact.
func (v *versionRig) seq(what string) int64 {
	v.a.mu.Lock()
	defer v.a.mu.Unlock()
	return v.proj().folded.versions[what].seq
}

// herdr sets what serve knows of the terminal, as a snapshot does.
func (v *versionRig) herdr(ver string, connected bool) {
	v.a.mu.Lock()
	v.a.herdrVer, v.a.connected = ver, connected
	v.a.mu.Unlock()
}

const (
	claude322 = "2.1.322 (Claude Code)"
	claude400 = "2.1.400 (Claude Code)"
)

// A first sighting is a fact with no was and no line; the same reading again is no fact; a read is made only once
// versionsEvery has passed; a changed reading is a fact naming the old line, and the rule holds with its seq.
func TestVersionTickRecordsAChange(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	v.read = map[string]string{"claude": claude322}

	v.tick(0)
	v.want("the first read", "claude "+claude322+" <-")
	if v.calls != 1 {
		t.Fatalf("the first pass read %d times, want 1", v.calls)
	}
	if key, _, ok := v.rule(); ok {
		t.Errorf("the rule holds (key %d) on a first sighting, want not", key)
	}

	v.tick(versionsEvery - time.Second)
	if v.calls != 1 {
		t.Errorf("not due: the reader was called %d times, want still 1", v.calls)
	}
	v.tick(time.Second)
	if v.calls != 2 {
		t.Fatalf("due again: the reader was called %d times, want 2", v.calls)
	}
	v.want("the same reading", "claude "+claude322+" <-")

	v.read = map[string]string{"claude": claude400}
	v.tick(versionsEvery)
	v.want("a changed reading", "claude "+claude322+" <-", "claude "+claude400+" <- "+claude322)
	key, act, ok := v.rule()
	if !ok || key == 0 || key != v.seq("claude") {
		t.Fatalf("the rule: key %d ok %v, want the change fact's seq %d", key, ok, v.seq("claude"))
	}
	want := "claude changed version: " + claude400 + " (was " + claude322 + "). obor drives it and was not tested with " +
		"this one: watch its next start and stop, and suspect it first if an agent stalls."
	if act.line != want || act.call != nil || act.after != nil {
		t.Errorf("the rule's action: line %q (call %v, after %v), want the line alone:\n%q", act.line, act.call != nil,
			act.after != nil, want)
	}
}

// The rule pass lines the lead once for a change: a second pass records no second line and the rule no longer holds.
func TestVersionRuleLinesOnce(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	v.read = map[string]string{"claude": claude322}
	v.tick(0)
	v.read = map[string]string{"claude": claude400}
	v.tick(versionsEvery)
	seq := v.seq("claude")
	if _, _, ok := v.rule(); !ok {
		t.Fatalf("precondition: the rule does not hold after a change")
	}

	rs := []rule{{name: ruleVersion, when: versionRule}}
	v.a.rulePass(context.Background(), rs, v.fx)
	v.a.rulePass(context.Background(), rs, v.fx)
	ofs, lines := ruleLines(t, v.harness)
	if len(ofs) != 1 || ofs[0] != (event.Of{Kind: ruleVersion, Seq: seq}) || !strings.HasPrefix(lines[0], "claude changed version: ") {
		t.Fatalf("lines %v %q, want one of {%s %d} naming claude", ofs, lines, ruleVersion, seq)
	}
	var m event.Message
	for _, r := range readLog(t, v.root) {
		if r.Task == event.MsgTask && r.Step == event.StepMessage {
			_ = json.Unmarshal(r.Data, &m)
		}
	}
	if m.To != "pj-lead" || strings.Contains(m.Line, "\n") {
		t.Errorf("the line goes to %q (%q), want pj-lead and one line", m.To, m.Line)
	}
	if key, _, ok := v.rule(); ok {
		t.Errorf("the rule still holds (key %d) once its line is recorded", key)
	}
}

// The rule is in serve's list once, with no settle.
func TestVersionRuleIsInTheList(t *testing.T) {
	t.Parallel()
	n := 0
	for _, r := range rules {
		if r.name == ruleVersion {
			n++
			if r.settle != 0 {
				t.Errorf("the version rule settles for %s, want none", r.settle)
			}
		}
	}
	if n != 1 {
		t.Fatalf("rules names %s %d times, want once", ruleVersion, n)
	}
}

// A tool that was not read has no version: no fact while it is gone, and none when the old line is back.
func TestVersionNotReadIsNoChange(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	v.read = map[string]string{"claude": claude322}
	v.tick(0)
	v.want("precondition: the first read", "claude "+claude322+" <-")

	for _, gone := range []map[string]string{{}, nil, {"claude": ""}} {
		v.read = gone
		v.tick(versionsEvery)
	}
	if v.calls != 4 {
		t.Fatalf("the reader was called %d times, want 4", v.calls)
	}
	v.want("the CLI gone", "claude "+claude322+" <-")
	v.read = map[string]string{"claude": claude322}
	v.tick(versionsEvery)
	if v.calls != 5 {
		t.Fatalf("the reader was called %d times, want 5", v.calls)
	}
	v.want("the old line again", "claude "+claude322+" <-")
	if _, _, ok := v.rule(); ok {
		t.Errorf("the rule holds after the CLI came back unchanged")
	}
}

// The terminal's version is the last snapshot's while serve is connected: a first sighting, then a change; not
// connected, nothing is read of it.
func TestVersionOfTheTerminal(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	v.herdr("0.9.5", false)
	v.tick(0)
	v.want("not connected")
	if v.calls != 1 {
		t.Fatalf("precondition: the read ran %d times, want 1", v.calls)
	}

	v.herdr("0.9.5", true)
	v.tick(versionsEvery)
	v.want("connected", "herdr 0.9.5 <-")
	if _, _, ok := v.rule(); ok {
		t.Errorf("the rule holds on the terminal's first sighting")
	}

	v.herdr("0.9.6", false)
	v.tick(versionsEvery)
	v.want("a new version, not connected", "herdr 0.9.5 <-")

	v.herdr("0.9.6", true)
	v.tick(versionsEvery)
	v.want("a new version", "herdr 0.9.5 <-", "herdr 0.9.6 <- 0.9.5")
	if _, act, ok := v.rule(); !ok || !strings.HasPrefix(act.line, "herdr changed version: 0.9.6 (was 0.9.5). ") {
		t.Errorf("the rule: ok %v line %q, want the terminal's change", ok, act.line)
	}
	if v.read[terminalTool] != "" {
		t.Errorf("the tick wrote the terminal into the reader's map: %v", v.read)
	}
}

// A replay rebuilds the newest version per tool, and a change whose line is recorded is not lined again.
func TestVersionReplay(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	v.fact(event.VersionTask, event.StepVersion, versionData{What: "claude", Version: claude322})
	v.fact(event.VersionTask, event.StepVersion, versionData{What: "claude", Version: claude400, Was: claude322})
	v.fact(event.VersionTask, event.StepVersion, versionData{Version: "9"})  // no tool: folds to nothing
	v.fact(event.VersionTask, event.StepVersion, versionData{What: "codex"}) // no version: folds to nothing
	seq := v.seq("claude")
	rs := []rule{{name: ruleVersion, when: versionRule}}

	restartRig(t, v.harness)
	v.a.mu.Lock()
	got := v.proj().folded.versions
	v.a.mu.Unlock()
	if len(got) != 1 || got["claude"] != (versionAt{versionData{What: "claude", Version: claude400, Was: claude322}, seq}) {
		t.Fatalf("replayed versions %+v, want claude's newest alone (seq %d)", got, seq)
	}
	if key, _, ok := v.rule(); !ok || key != seq {
		t.Fatalf("after the replay the rule: key %d ok %v, want the unlined change %d", key, ok, seq)
	}
	v.a.rulePass(context.Background(), rs, v.fx)
	if ofs, _ := ruleLines(t, v.harness); len(ofs) != 1 {
		t.Fatalf("precondition: lines %v, want one", ofs)
	}

	restartRig(t, v.harness)
	v.a.rulePass(context.Background(), rs, v.fx)
	if ofs, _ := ruleLines(t, v.harness); len(ofs) != 1 {
		t.Errorf("after a restart the change is lined again: %v", ofs)
	}
	v.read = map[string]string{"claude": claude400}
	v.tick(0) // a loaded project reads on its first pass
	if v.calls != 1 {
		t.Fatalf("the first pass after a load read %d times, want 1", v.calls)
	}
	if n := len(v.facts()); n != 4 {
		t.Errorf("the same version after a restart recorded a fact: %d version facts, want 4", n)
	}
}

// Two tools change in one read: two facts in name order, lined over two passes, the lower seq first.
func TestVersionTwoChanges(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	v.read = map[string]string{"codex": "codex-cli 0.167.0", "claude": claude322}
	v.tick(0)
	v.read = map[string]string{"codex": "codex-cli 0.168.0", "claude": claude400}
	v.tick(versionsEvery)
	v.want("two changes", "claude "+claude322+" <-", "codex codex-cli 0.167.0 <-",
		"claude "+claude400+" <- "+claude322, "codex codex-cli 0.168.0 <- codex-cli 0.167.0")
	first, second := v.seq("claude"), v.seq("codex")
	if first == 0 || first >= second {
		t.Fatalf("precondition: claude's fact %d, codex's %d, want name order", first, second)
	}

	rs := []rule{{name: ruleVersion, when: versionRule}}
	v.a.rulePass(context.Background(), rs, v.fx)
	if ofs, _ := ruleLines(t, v.harness); len(ofs) != 1 || ofs[0].Seq != first {
		t.Fatalf("after one pass lines %v, want claude's (%d) alone", ofs, first)
	}
	v.a.rulePass(context.Background(), rs, v.fx)
	v.a.rulePass(context.Background(), rs, v.fx)
	ofs, lines := ruleLines(t, v.harness)
	if len(ofs) != 2 || ofs[1].Seq != second || !strings.HasPrefix(lines[1], "codex changed version: codex-cli 0.168.0 (was codex-cli 0.167.0). ") {
		t.Errorf("after three passes lines %v %q, want claude's then codex's (%d)", ofs, lines, second)
	}
}

// A refused append leaves no fact and no folded version; the next read records the same difference.
func TestVersionRefusedAppendIsReadAgain(t *testing.T) {
	t.Parallel()
	v := newVersionRig(t)
	p := v.proj()
	refuse := func(err error) {
		p.log.Mu.Lock()
		defer p.log.Mu.Unlock()
		p.log.Refuse = nil
		if err != nil {
			p.log.Refuse = func(event.Record) error { return err }
		}
	}
	v.read = map[string]string{"claude": claude322}
	refuse(errors.New("database or disk is full"))
	v.tick(0)
	v.want("the append refused")
	refuse(nil)
	v.tick(versionsEvery)
	v.want("the next read", "claude "+claude322+" <-")
}
