package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"

	"obor/internal/store"
)

// An agent's sessions (T-577): obor records each session an agent starts itself, as fact session{agent, kind, id,
// transcript, pane, lane, task, started} on task key store.SessionTask, one per (agent, id); the history stays (the
// UI's list), the agent's current session is its newest (store.Current). The sources, per kind:
//   - Claude: launch records the id it chose before the start (T-813: --session-id), and the SessionStart hook
//     confirms it with its transcript_path (T-577; a different id is the session's, recorded with one log line; serve
//     down: the session is not recorded, said on the hook's log line);
//   - Codex: launch, after a fresh start, reads the rollout the started agent's process holds open (T-588; its first
//     line's session_meta) and reports it (in serve after the start returned: its own goroutine);
//   - OpenCode: none yet; the session request is kind-neutral, T-578's plugin is to send it.
//
// herdr's agent_session is a cross-check only: the identity (registry.go) and launch's resume read the recorded
// session first, and a herdr value that differs is logged, not adopted; a kind whose sessions obor names itself never
// adopts it, not even with no record (T-813).

// refusedMark is in the error of a session request serve refused because another agent holds the session (T-586).
const refusedMark = "session already held by "

// sessionCall answers the socket's session request: fact session, once per (agent, id) — a repeat (a resume, a second
// report of one session) records nothing and gets the same reply, except the confirmation of a session obor chose
// before its start, which fills in its transcript (T-813). One session id belongs to one agent (T-586): an id that is
// another agent's current session is refused, nothing recorded, the two agents logged.
func (a *App) sessionCall(c wire.Call) (string, error) {
	if c.Session == nil || c.Session.Agent == "" || c.Session.ID == "" {
		return "", errors.New(wire.MethodSession + ": the agent and the session id are required")
	}
	s := *c.Session
	reply := "SESSION " + s.Agent + " " + s.ID
	a.sessions.Lock() // the check and its record are one step: two reports of one session record one fact
	defer a.sessions.Unlock()
	a.mu.Lock()
	p := a.projAt(c.Root)
	var recorded, holder string
	var cur store.Session
	if p != nil {
		recorded = p.recordedTranscript(s.Agent, s.ID)
		holder = p.holder(s.Agent, s.ID)
		cur, _ = p.session(s.Agent)
	}
	a.mu.Unlock()
	// The session obor chose for the agent before its start (no transcript yet) is not what the session runs as: the
	// reporting session's id wins, said once (T-813: a start Claude ran with another id than obor gave it).
	replaced := p != nil && cur.Agent != "" && cur.ID != s.ID && cur.Transcript == ""
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodSession, c.Root)
	case recorded != "":
		return reply, nil
	case holder != "":
		a.logf("%s: %s's session %s refused: it is %s's current session", p.name, s.Agent, s.ID, holder)
		return "", fmt.Errorf("%s: %s%s (%s's report refused)", wire.MethodSession, refusedMark, holder, s.Agent)
	}
	if s.Started == 0 {
		s.Started = a.opt.Now().Unix()
	}
	if !a.record(p, event.Record{Task: store.SessionTask, Step: store.StepSession, Kind: event.KindFact}, s) {
		return "", fmt.Errorf("%s: the event log of %s refused it", wire.MethodSession, p.name)
	}
	if replaced {
		a.logf("%s: %s runs session %s, not %s (the id obor chose): the reported session is recorded", p.name, s.Agent,
			s.ID, cur.ID)
	}
	return reply, nil
}

// foldSession folds r, a session fact, when store.Admits it (T-586: one session id belongs to one agent; a log that
// holds a duplicate has one owner on replay, the earlier — except the confirmation of the id obor chose, which takes
// its record's place with the transcript it reports, T-813).
func (p *proj) foldSession(r event.Record) {
	if r.Task != store.SessionTask || r.Kind != event.KindFact || r.Step != store.StepSession {
		return
	}
	var s store.Session
	if json.Unmarshal(r.Data, &s) != nil || !store.Admits(p.folded.sessions, s) {
		return
	}
	for i := range p.folded.sessions {
		if p.folded.sessions[i].Agent == s.Agent && p.folded.sessions[i].ID == s.ID {
			p.folded.sessions[i] = s
			return
		}
	}
	p.folded.sessions = append(p.folded.sessions, s)
}

// recordedTranscript is the transcript p records for agent's session id ("" when none: a session recorded but not
// confirmed by its SessionStart hook yet).
func (p *proj) recordedTranscript(agent, id string) string {
	for _, s := range p.folded.sessions {
		if s.Agent == agent && s.ID == id {
			return s.Transcript
		}
	}
	return ""
}

// holder is the agent other than agent whose current session is id ("": none; under mu).
func (p *proj) holder(agent, id string) string { return store.Holder(p.folded.sessions, agent, id) }

// session is agent's current session in p (under mu; ok false: none recorded).
func (p *proj) session(agent string) (store.Session, bool) {
	return store.Current(p.folded.sessions, agent)
}

// transcript is the recorded transcript of pane's agent (under mu): its current session's ("": none recorded; the
// reader then takes the one herdr's session names).
func (a *App) transcript(p *proj, pane port.Pane) string {
	agent, ok := p.reg.whoIs(pane)
	if !ok {
		return ""
	}
	s, _ := p.session(agent)
	return s.Transcript
}
