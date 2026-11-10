package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"obor/internal/app/event"
	"obor/internal/config"
	"slices"
	"time"
)

// The versions of the tools obor drives (T-1176): the agent CLIs and the terminal update themselves, and a start or
// a stop that worked yesterday may not today (a Codex self-upgrade broke unattended starts, docs/observations.md).
// Every versionsEvery the pass reads each tool's version, records a change as fact version{what, version, was}, and
// the rule version.changed sends the lead one line per change. It gates and refuses nothing. A tool that was not read
// (not installed, no answer) has no version: nothing is recorded and nothing is compared.

// versionsEvery is how often the tools' versions are read on a pass.
const versionsEvery = 10 * time.Minute

// terminalTool is the terminal's name among the tools (the one terminal adapter, T-1044).
const terminalTool = config.DefaultTerminal

// ruleVersion is the version rule's name: its key in the rule pass.
const ruleVersion = "version.changed"

// versionData is fact version's data: the tool (an agent kind's name, or terminalTool), its version as read, and
// the one the log held before ("": the first sighting).
type versionData struct {
	What    string `json:"what"`
	Version string `json:"version"`
	Was     string `json:"was,omitempty"`
}

// versionAt is a tool's newest version fact as folded: its data and the record's seq.
type versionAt struct {
	versionData
	seq int64
}

// versionTick reads the tools' versions once versionsEvery has passed since the last read (and on the first pass
// after the project loads) and records one fact per tool whose version differs from the one the log holds, in
// tool-name order. The agent CLIs' come from fx.Versions, called outside mu (it runs each CLI); the terminal's is the
// last snapshot's while serve is connected. A refused append is not retried here: the next read sees the same
// difference.
func (a *App) versionTick(ctx context.Context, p *proj, fx *Loop) {
	if fx.Versions == nil {
		return
	}
	now := a.opt.Now()
	a.mu.Lock()
	due := p.versionsAt.IsZero() || now.Sub(p.versionsAt) >= versionsEvery
	if due {
		p.versionsAt = now
	}
	a.mu.Unlock()
	if !due {
		return
	}
	read := maps.Clone(fx.Versions(ctx))
	if read == nil {
		read = map[string]string{}
	}
	a.mu.Lock()
	if a.connected && a.herdrVer != "" {
		read[terminalTool] = a.herdrVer
	}
	var changed []versionData
	for _, what := range slices.Sorted(maps.Keys(read)) {
		was := p.folded.versions[what].Version
		if v := read[what]; v != "" && v != was {
			changed = append(changed, versionData{What: what, Version: v, Was: was})
		}
	}
	a.mu.Unlock()
	for _, d := range changed {
		a.record(p, event.Record{Task: event.VersionTask, Step: event.StepVersion, Kind: event.KindFact}, d)
	}
}

// foldVersion folds r, a tool's version fact: the newest per tool.
func (p *proj) foldVersion(r event.Record) {
	if r.Task != event.VersionTask || r.Kind != event.KindFact || r.Step != event.StepVersion {
		return
	}
	var d versionData
	if json.Unmarshal(r.Data, &d) != nil || d.What == "" || d.Version == "" {
		return
	}
	p.folded.versions[d.What] = versionAt{d, r.Seq}
}

// versionRule is the version rule (T-1176, rule tier). Condition: a tool's newest version fact names a version
// before it (a first sighting names none) and its line is not recorded. Action: one line to the lead; no call.
// Keyed by the fact's seq, the oldest unlined change first: two changes are lined on two passes.
func versionRule(_ context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var next *versionAt
	for _, v := range p.folded.versions {
		if _, did := p.folded.did[event.Of{Kind: ruleVersion, Seq: v.seq}]; v.Was == "" || did {
			continue
		}
		if next == nil || v.seq < next.seq {
			next = &v
		}
	}
	if next == nil {
		return 0, action{}, false
	}
	return next.seq, action{line: versionLine(next.versionData)}, true
}

// versionLine is the lead's line for a tool's changed version.
func versionLine(d versionData) string {
	return fmt.Sprintf("%s changed version: %s (was %s). obor drives it and was not tested with this one: watch its "+
		"next start and stop, and suspect it first if an agent stalls.", d.What, d.Version, d.Was)
}
