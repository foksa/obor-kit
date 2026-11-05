package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"strings"
)

// foldStart folds a start's result into p's identity table (registry.recordStart): the agent it placed in its pane, that pane's terminal
// id, the kind and the session id herdr reported for it. agent.start's (the lane's worker) and start.<job>'s (the
// integrator) results carry it; a refused start, the integrator's "running", or a result from before T-418 leaves
// the rows as they are. Another agent's row on the same pane or terminal goes: the start replaced what ran there.
// The log holds it, so a replay has it; the table itself is derived (reconcile), never folded. Every agent.start
// record, intent or result, is also the task's last try to start its lane agent (folded.starts).
func (p *proj) foldStart(r event.Record) {
	if step, _ := event.StepN(r.Step); step == event.StepStartWork && r.Kind != event.KindFact {
		if at := event.RecordTime(r); !at.IsZero() {
			p.folded.starts[r.Task] = at // serve's try to start the lane's agent, whatever its outcome
		}
	}
	if step, _ := event.StepN(r.Step); r.Kind != event.KindResult || step != event.StepStartWork && !strings.HasPrefix(step, stepStart) {
		return
	}
	var out startOut
	if json.Unmarshal(r.Data, &out) != nil || out.Refused != "" || out.Agent == "" || out.Pane == "" {
		return
	}
	p.reg.recordStart(out.Agent, startRow{Pane: out.Pane, Terminal: out.Terminal, Kind: out.Kind, Session: out.Session,
		Verdicts: p.folded.verdicts})
}
