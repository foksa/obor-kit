package app_test

import (
	"context"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"

	"obor/internal/store"
)

// T-440: obor spec set is serve's request: the lead stores a spec (rev 1), the same text again changes nothing, another
// text is the next rev; a repeated call id gets its stored reply; the owner (no role) sets one too (DD-v0.11 A3); every
// other role is refused and stores nothing.
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
			Agent: "pj-o1", Role: role, CallID: "w-" + role, Task: "T-1", Spec: "# T-1 — widened by " + role + "\n"})
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

// T-440: a dispatch reads the stored spec for Specs.Check (which refuses "": no stored spec, cmd/obor TestSpecs):
// before the set the request is refused with nothing recorded, after it the same request dispatches.
func TestDispatchNeedsStoredSpec(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.disp.refuse["T-1"] = "no spec for T-1 in obor serve's store" // what the real Check says to ""
	r.start()
	if _, err := r.dispatch("d1", "O-01"); err == nil || !strings.Contains(err.Error(), "no spec for T-1") {
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

// T-440: a fix job carries the stored spec only when its rev is not the one the worker's last job carried (a widening
// since); every gate gets the current rev to write into the candidate.
func TestFixJobCarriesChangedSpec(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		widen   bool
		wantFix string // in the second fix job: the spec's rev line, "" none
	}{
		{"unchanged", false, ""},
		{"widened", true, "The spec of T-1 (rev 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, `if [ -f red.txt ]; then echo "0 pass, 1 fail"; exit 1; fi; echo 1 pass`)
			r.work("red.txt", "red\n")
			r.start()
			r.setSpec("s1", "# T-1 — Red\n\nDifficulty: medium\n\n## Files you may touch\n`red.txt`\n")
			r.submit()
			job := r.waitJob("pj-o1", "T-1/2/fix")
			if !strings.Contains(job, "The spec of T-1 (rev 1") { // the worker's last job carried none
				t.Errorf("first fix job has no spec rev 1:\n%s", job)
			}
			r.waitStep("1 fact sendback")
			if c.widen {
				r.setSpec("s2", "# T-1 — Red\n\nDifficulty: medium\n\n## Files you may touch\n`red.txt`, `more.txt`\n")
			}
			r.submit()
			r.waitStep("2 fact submitted")
			job = r.waitJob("pj-o1", "T-1/3/fix")
			if got := strings.Contains(job, "The spec of T-1"); got != (c.wantFix != "") ||
				c.wantFix != "" && !strings.Contains(job, c.wantFix) {
				t.Errorf("second fix job: spec %v, want %q:\n%s", got, c.wantFix, job)
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
