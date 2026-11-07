package app_test

import (
	"obor/internal/app/event"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/app"
)

// diskFake is a Loop's Disk: the free bytes scripted.
type diskFake struct {
	free atomic.Uint64
}

func (f *diskFake) disk() *app.Disk {
	return &app.Disk{Volume: "/vol/proj",
		Free: func() (uint64, error) { return f.free.Load(), nil }}
}

// diskFacts is the low-disk needs-you in force in r's status.
func diskFacts(r *loopRig) []event.Record {
	var out []event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == event.DiskTask {
			out = append(out, f)
		}
	}
	return out
}

// T-564: free space below the bound is the project's needs-you naming the volume, above it ends it;
// one record per transition, none per tick. A gate waits while it is low and runs after.
func TestDiskLowIsANeedsYouAndHoldsTheGate(t *testing.T) {
	t.Parallel()
	const gib = 1 << 30
	f := &diskFake{}
	f.free.Store(2 * gib)
	r := newLoopRig(t)
	r.disk = f.disk()
	r.work("feature.txt", "feature\n")
	r.start()
	r.passes()
	r.submit()
	r.waitStep("1 intent submit")
	r.waitStep("1 result submit")

	want := "disk low: 2.0 GiB free on /vol/proj"
	r.waitFor("the disk needs-you", func() bool { return len(diskFacts(r)) == 1 })
	if fs := diskFacts(r); fs[0].Step != event.StepNeedsYou || fs[0].Fact().Reason != want {
		t.Fatalf("disk facts %+v, want needs-you %q", fs, want)
	}
	for i := 0; i < 3; i++ {
		r.later(app.DiskEvery)
	}
	if r.has("1 intent gate") {
		t.Fatalf("a gate started on a low disk: %q", r.steps())
	}
	if n := r.count("0 fact needs-you{" + want + "}"); n != 1 {
		t.Errorf("needs-you records = %d, want 1 (none per tick): %q", n, r.steps())
	}

	f.free.Store(40 * gib)
	r.later(app.DiskEvery)
	r.waitStep("1 result gate")
	if fs := diskFacts(r); len(fs) != 0 {
		t.Errorf("disk facts after space is back: %+v", fs)
	}
	if n := r.count("0 fact resumed"); n != 1 {
		t.Errorf("resumed records = %d, want 1: %q", n, r.steps())
	}

	f.free.Store(1 * gib)
	r.later(app.DiskEvery)
	r.waitFor("low again", func() bool { return len(diskFacts(r)) == 1 })
	if n := r.count("0 fact needs-you{disk low: 1.0 GiB free on /vol/proj}"); n != 1 {
		t.Errorf("second low: needs-you records = %d, want 1: %q", n, strings.Join(r.steps(), "\n"))
	}
}
