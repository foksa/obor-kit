package app

import (
	"encoding/json"
	"strings"
)

// foldStart folds a start's result into p.starts (registry.go): the agent it placed in its pane, that pane's terminal
// id, the kind and the session id herdr reported for it. agent.start's (the lane's worker) and start.<job>'s (the
// integrator) results carry it; a refused start, the integrator's "running", or a result from before T-418 leaves
// the rows as they are. Another agent's row on the same pane or terminal goes: the start replaced what ran there.
// The log holds it, so a replay has it; the table itself is derived (reconcile), never folded.
func (p *proj) foldStart(r Record) {
	if step, _ := stepN(r.Step); r.Kind != KindResult || step != StepStartWork && !strings.HasPrefix(step, stepStart) {
		return
	}
	var out startOut
	if json.Unmarshal(r.Data, &out) != nil || out.Refused != "" || out.Agent == "" || out.Pane == "" {
		return
	}
	row := startRow{Pane: out.Pane, Terminal: out.Terminal, Kind: out.Kind, Session: out.Session}
	if old := p.starts[out.Agent]; row.Terminal == "" && old.Pane == out.Pane {
		row.Terminal = old.Terminal
	}
	for agent, old := range p.starts {
		if agent != out.Agent && (old.Pane == row.Pane || old.Terminal != "" && old.Terminal == row.Terminal) {
			delete(p.starts, agent)
		}
	}
	p.starts[out.Agent] = row
}
