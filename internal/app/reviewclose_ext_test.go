package app_test

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// obor review close reaches serve as a request (wire.MethodReviewClose): with RV-5 started and no
// docs/reviews/RV-5.md written, serve refuses it as no open review, nothing committed.
func TestReviewCloseOnTheSocket(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching(), withConfig(testkit.Set("agents.roles.reviewer", ""), testkit.Set("reviews.dir", "docs/reviews")))
		r.review = lastClose{}
		r.reviewsLog("RV-5")
		r.start()
		_, err := r.ask(&wire.ReviewCloseReq{Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-lead", Role: "lead", CallID: "c1"}, Summary: "fixed"})
		if err == nil || !strings.HasPrefix(err.Error(), "no open review: no docs/reviews/RV-5.md in ") {
			t.Errorf("review_close: %v; want serve's no-open-review refusal", err)
		}
	})
}
