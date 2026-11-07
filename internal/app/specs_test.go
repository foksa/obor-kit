package app_test

import (
	"context"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"sync"
	"testing"

	"obor/internal/store"
)

// T-440: obor spec set is serve's request: the lead stores a spec (rev 1), the same text again changes nothing, another
// text is the next rev; a repeated call id gets its stored reply; the owner (no role) sets one too (DD-v0.11 A3); every
// other role is refused and stores nothing. A set on a dispatched task sends no line (T-824); the reply is the plain
// set line.
func TestSpecSet(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	if _, ok, err := store.ReadSpecAt(r.p.Root, "T-1"); err != nil || ok {
		t.Fatalf("precondition: a spec before any set: %v, %v", ok, err)
	}
	for _, c := range []struct{ call, text, want string }{
		{"s1", "# T-1 — A\n\nDifficulty: medium\n", "SET T-1 rev 1"},
		{"s2", "# T-1 — A\n\nDifficulty: medium\n", "UNCHANGED T-1 rev 1"},
		{"s3", "# T-1 — B\n\nDifficulty: medium\n", "SET T-1 rev 2"},
		{"s1", "# T-1 — C\n\nDifficulty: medium\n", "SET T-1 rev 1"}, // s1's stored reply: nothing stored
	} {
		if got := r.setSpec(c.call, c.text); got != c.want {
			t.Errorf("spec set %s: %q, want %q", c.call, got, c.want)
		}
	}
	for _, role := range []string{"worker", "integrator"} {
		reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root,
			Agent: "pj-l-01", Role: role, CallID: "w-" + role, Task: "T-1", Spec: "# T-1 — widened by " + role + "\n"})
		if err == nil || !strings.Contains(err.Error(), "only the lead or the owner sets a spec") {
			t.Errorf("spec set by role %q: %q, %v; want refused", role, reply, err)
		}
	}
	row, ok, err := store.ReadSpecAt(r.p.Root, "T-1")
	if err != nil || !ok || row.Rev != 2 || row.Text != "# T-1 — B\n\nDifficulty: medium\n" || row.SetBy != "lead" {
		t.Errorf("stored spec %+v, %v, %v; want rev 2 \"B\" set by lead", row, ok, err)
	}
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root,
		CallID: "o1", Task: "T-1", Spec: "# T-1 — by the owner\n\nDifficulty: medium\n"})
	if err != nil || reply != "SET T-1 rev 3" {
		t.Fatalf("spec set by the owner: %q, %v; want SET T-1 rev 3", reply, err)
	}
	if row, ok, err := store.ReadSpecAt(r.p.Root, "T-1"); err != nil || !ok || row.Rev != 3 || row.SetBy != "owner" {
		t.Errorf("stored spec %+v, %v, %v; want rev 3 set by owner", row, ok, err)
	}
}

// T-790: a spec with no title is refused beside the Difficulty refusal, and nothing is stored.
func TestSpecSetRefusesASpecWithoutATitle(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	for _, c := range []struct{ call, text string }{
		{"u1", "# T-1\n\nDifficulty: medium\n"},
		{"u2", "# T-1:\n\nDifficulty: medium\n"},
		{"u3", "Difficulty: medium\n"},
	} {
		_, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: c.call, Task: "T-1", Spec: c.text})
		if err == nil || !strings.Contains(err.Error(), "the spec needs a title: `# T-1 — <title>`") {
			t.Errorf("spec set of %q: %v; want a refusal naming the title", c.text, err)
		}
	}
	if _, ok, err := store.ReadSpecAt(r.p.Root, "T-1"); err != nil || ok {
		t.Errorf("a refused spec was stored: %v, %v", ok, err)
	}
}

// T-878, T-932: obor spec set accepts the one "Review:" value and refuses any other, naming it; and it refuses a spec
// that has both `Fixes:` and `Review: on merge` (the fix's verify is its review). A refusal stores nothing.
func TestSpecSetReviewOnMerge(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	good := "# T-1 — A\n\nDifficulty: medium\nReview: on merge\n\n## Do\n"
	if got := r.setSpec("r1", good); got != "SET T-1 rev 1" {
		t.Fatalf("spec set with \"Review: on merge\": %q, want SET T-1 rev 1", got)
	}
	_, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
		Agent: "lead", CallID: "r2", Task: "T-1", Spec: "# T-1 — B\n\nDifficulty: medium\nReview: now\n"})
	if err == nil || !strings.Contains(err.Error(), `the "Review:" line takes one value: "on merge"`) {
		t.Errorf("spec set with \"Review: now\": %v; want the refusal naming the one value", err)
	}
	// T-932: a fix spec carries Fixes: in the Review line's place; both is refused, and nothing is stored.
	_, err = wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
		Agent: "lead", CallID: "r3", Task: "T-1",
		Spec: "# T-1 — C\n\nDifficulty: hard\nFixes: RV-12\nReview: on merge\n"})
	if err == nil || !strings.Contains(err.Error(), "a fix (Fixes:) is reviewed by its verify; drop the Review: on merge line") {
		t.Errorf("spec set with \"Fixes:\" and \"Review: on merge\": %v; want the refusal naming the pair", err)
	}
	if row, ok, err := store.ReadSpecAt(r.p.Root, "T-1"); err != nil || !ok || row.Rev != 1 || row.Text != good {
		t.Errorf("stored spec %+v, %v, %v; want the accepted text at rev 1", row, ok, err)
	}
	// A fix spec alone (no Review line) is accepted, whatever its difficulty (T-932).
	fix := "# T-1 — D\n\nDifficulty: hard\nFixes: RV-12\n\n## Do\n"
	if got := r.setSpec("r4", fix); got != "SET T-1 rev 2" {
		t.Errorf("spec set of a fix spec: %q, want SET T-1 rev 2", got)
	}
}

// T-440: a dispatch reads the stored spec for Specs.Check (which refuses "": no stored spec, cmd/obor TestSpecs):
// before the set the request is refused with nothing recorded, after it the same request dispatches.
func TestDispatchNeedsStoredSpec(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.disp.refuse["T-1"] = "no spec for T-1 in obor serve's store" // what the real Check says to ""
	r.start()
	if _, err := r.dispatch("d1", "L-01"); err == nil || !strings.Contains(err.Error(), "no spec for T-1") {
		t.Fatalf("dispatch without a stored spec: %v; want refused", err)
	}
	if n := len(r.records()); n != 0 {
		t.Errorf("a refused dispatch recorded %q", r.steps())
	}
	r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n")
	r.disp.mu.Lock()
	delete(r.disp.refuse, "T-1")
	r.disp.mu.Unlock()
	r.mustDispatch("d2")
	r.disp.mu.Lock()
	texts := slices.Clone(r.disp.texts)
	r.disp.mu.Unlock()
	if !slices.Equal(texts, []string{"", "# T-1 — Stored\n\nDifficulty: medium\n"}) {
		t.Errorf("Specs.Check got %q; want \"\" before the set, the stored spec after", texts)
	}
}

// T-440: a fix job carries the stored spec when its rev is not the one the worker last had (T-819: a change made
// while the task was not working; a change while it works reaches it as the refusal, and the next fix job does not
// repeat it); every gate gets the current rev to write into the candidate.
func TestFixJobCarriesChangedSpec(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		widen bool
	}{
		{"unchanged", false},
		{"widened while working: the refusal carried it", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, `if [ -f red.txt ]; then echo "0 pass, 1 fail"; exit 1; fi; echo 1 pass`)
			r.work("red.txt", "red\n")
			r.start()
			r.setSpec("s1", "# T-1 — Red\n\nDifficulty: medium\n\n## Files you may touch\n`red.txt`\n")
			r.submit()
			job := r.waitJob("pj-l-01", "T-1/2/fix")
			if !strings.Contains(job, "The spec of T-1 (rev 1") { // the worker's last job carried none
				t.Errorf("first fix job has no spec rev 1:\n%s", job)
			}
			r.waitStep("1 fact sendback")
			if c.widen {
				r.setSpec("s2", "# T-1 — Red\n\nDifficulty: medium\n\n## Files you may touch\n`red.txt`, `more.txt`\n")
				// The worker fetched its fix job at the old rev: its task_done is refused once, then submits (T-819).
				if reply, err := r.taskDone(report); err == nil || !strings.Contains(err.Error(), "spec is now rev 2") {
					t.Fatalf("task_done at the old rev: %q, %v; want the refusal naming rev 2", reply, err)
				}
			}
			r.submit()
			r.waitStep("2 fact submitted")
			job = r.waitJob("pj-l-01", "T-1/3/fix")
			if strings.Contains(job, "The spec of T-1") {
				t.Errorf("second fix job repeats a spec the worker has:\n%s", job)
			}
			r.scratch.mu.Lock()
			specs := slices.Clone(r.scratch.specs)
			r.scratch.mu.Unlock()
			if len(specs) != 2 || !strings.Contains(specs[0], "`red.txt`\n") ||
				strings.Contains(specs[1], "more.txt") != c.widen {
				t.Errorf("gate specs %q; want the current rev at each gate", specs)
			}
		})
	}
}

// SM-02 (T-939): spec_set's receipt is a folded fact, so a retry of a call id after a serve restart, with another
// writer's spec in between, answers the original reply and does not write the old text over the newer spec.
func TestSpecSetRetryAfterRestartPreservesNewerSpec(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	a, b := "# T-1 — A\n\nDifficulty: medium\n", "# T-1 — B\n\nDifficulty: medium\n"
	if got := r.setSpec("s1", a); got != "SET T-1 rev 1" {
		t.Fatalf("precondition: s1 replied %q", got)
	}
	if got := r.setSpec("s2", b); got != "SET T-1 rev 2" {
		t.Fatalf("precondition: s2 replied %q", got)
	}
	r.stop()
	r.start() // serve restarts: the live reply cache is gone
	if got := r.setSpec("s1", a); got != "SET T-1 rev 1" {
		t.Errorf("s1 retried after the restart: %q, want its original SET T-1 rev 1", got)
	}
	row, ok, err := store.ReadSpecAt(r.p.Root, "T-1")
	if err != nil || !ok || row.Rev != 2 || row.Text != b {
		t.Errorf("spec after the retry %+v, %v, %v; want B at rev 2", row, ok, err)
	}
}

// SM-02 (T-939): simultaneous callers of one call id all get the first call's reply, and the spec is set once.
func TestSpecSetConcurrentRetriesShareReply(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	text := "# T-1 — A\n\nDifficulty: medium\n"
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root,
				Role: "lead", CallID: "same", Task: "T-1", Spec: text})
			if err != nil || got != "SET T-1 rev 1" {
				t.Errorf("concurrent retry: %q, %v; want the original SET T-1 rev 1", got, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	row, ok, err := store.ReadSpecAt(r.p.Root, "T-1")
	if err != nil || !ok || row.Rev != 1 {
		t.Errorf("spec after the concurrent retries %+v, %v, %v; want rev 1", row, ok, err)
	}
}
