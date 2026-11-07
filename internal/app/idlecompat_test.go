package app

import (
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-983 census: the idle epoch compat branch (before T-866) is kept (the store holds its records), so it keeps its
// test: an idle epoch fact folds to nothing, and a keyless idle.ack names an epoch, no key: it acknowledges nothing,
// while its call's reply still folds for the caller.
func TestIdleEpochFactsFoldNothing(t *testing.T) {
	t.Parallel()
	p := &proj{folded: newFolded()}
	p.foldRecord(event.Record{Task: event.IdleTask, Step: event.StepIdle, Kind: event.KindFact,
		Data: []byte(`{"last":"T-1 merged abc1234","end":7}`)})
	p.foldRecord(event.Record{Task: event.IdleTask, Step: event.StepIdleAck, Kind: event.KindFact,
		Data: []byte(`{"epoch":9,"why":"old ack","by":"ob-lead","call":"c1","reply":"ACKED idle: old ack"}`)})
	if p.folded.idleAck.Key != 0 || p.folded.idleAck.Why != "" {
		t.Errorf("an old idle record acknowledged %+v; want nothing", p.folded.idleAck)
	}
	if got := p.folded.calls[verdictStep(wire.MethodIdle, "c1")]; got != "ACKED idle: old ack" {
		t.Errorf("the old ack's call reply %q, want the stored reply", got)
	}
}
