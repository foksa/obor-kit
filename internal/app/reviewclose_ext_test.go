package app_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-926: obor review close reaches serve through the socket (wire.MethodReviewClose): with RV-5 started and no
// docs/reviews/RV-5.md written, serve refuses it as no open review, nothing committed.
func TestReviewCloseOnTheSocket(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), withConfig(testkit.Set("agents.roles.reviewer", ""), testkit.Set("reviews.dir", "docs/reviews")))
	r.review = lastClose{}
	r.reviewsLog("RV-5")
	r.start()
	_, err := wire.Verdict(context.Background(), r.sock, &wire.ReviewCloseReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: "c1"}, Summary: "fixed"})
	if err == nil || !strings.HasPrefix(err.Error(), "no open review: no docs/reviews/RV-5.md in ") {
		t.Errorf("review_close: %v; want serve's no-open-review refusal", err)
	}
}
