package app

import (
	"encoding/json"
	"errors"
	"fmt"

	"obor/internal/store"
)

// An agent's sessions (T-577): obor records each session an agent starts itself, as fact session{agent, kind, id,
// transcript, pane, lane, task, started} on task key store.SessionTask, one per (agent, id); the history stays (the
// UI's list), the agent's current session is its newest (store.Current). The sources, per kind:
//   - Claude: its SessionStart hook sends serve a session request with the id and transcript_path (serve down: the
//     session is not recorded, said on the hook's log line; a restart resumes the agent's last recorded session,
//     else herdr's, T-607);
//   - Codex: launch, after a fresh start, reads the rollout the started agent's process holds open (T-588; its first
//     line's session_meta) and reports it (in serve after the start returned: its own goroutine);
//   - OpenCode: none yet; the session request is kind-neutral, T-578's plugin is to send it.
//
// herdr's agent_session is a cross-check only: the identity (registry.go) and launch's resume read the recorded
// session first, and a herdr value that differs is logged, not adopted.

// refusedMark is in the error of a session request serve refused because another agent holds the session (T-586).
const refusedMark = "session already held by "

// sessionCall answers the socket's session request: fact session, once per (agent, id) — a repeat (a resume, a second
// report of one session) records nothing and gets the same reply. One session id belongs to one agent (T-586): an id
// that is another agent's current session is refused, nothing recorded, the two agents logged.
func (a *App) sessionCall(c Call) (string, error) {
	if c.Agent == "" || c.Session == "" {
		return "", errors.New(MethodSession + ": the agent and the session id are required")
	}
	reply := "SESSION " + c.Agent + " " + c.Session
	a.sessions.Lock() // the check and its record are one step: two reports of one session record one fact
	defer a.sessions.Unlock()
	a.mu.Lock()
	p := a.projAt(c.Root)
	known := p != nil && p.hasSession(c.Agent, c.Session)
	holder := ""
	if p != nil {
		holder = p.holder(c.Agent, c.Session)
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodSession, c.Root)
	case known:
		return reply, nil
	case holder != "":
		a.logf("%s: %s's session %s refused: it is %s's current session", p.name, c.Agent, c.Session, holder)
		return "", fmt.Errorf("%s: %s%s (%s's report refused)", MethodSession, refusedMark, holder, c.Agent)
	}
	s := store.Session{Agent: c.Agent, Kind: c.Kind, ID: c.Session, Transcript: c.Transcript, Pane: c.Pane,
		Lane: c.Lane, Task: c.Task, Started: c.Started, Source: c.Source}
	if s.Started == 0 {
		s.Started = a.opt.Now().Unix()
	}
	if !a.record(p, Record{Task: store.SessionTask, Step: store.StepSession, Kind: KindFact}, s) {
		return "", fmt.Errorf("%s: the event log of %s refused it", MethodSession, p.name)
	}
	return reply, nil
}

// foldSession folds r, a session fact, when store.Admits it (T-586: one session id belongs to one agent; a log that
// holds a duplicate has one owner on replay, the earlier, as the store's readers see it too).
func (p *proj) foldSession(r Record) {
	if r.Task != store.SessionTask || r.Kind != KindFact || r.Step != store.StepSession {
		return
	}
	var s store.Session
	if json.Unmarshal(r.Data, &s) == nil && store.Admits(p.folded.sessions, s) {
		p.folded.sessions = append(p.folded.sessions, s)
	}
}

// hasSession: p records agent's session id (under mu).
func (p *proj) hasSession(agent, id string) bool {
	for _, s := range p.folded.sessions {
		if s.Agent == agent && s.ID == id {
			return true
		}
	}
	return false
}

// holder is the agent other than agent whose current session is id ("": none; under mu).
func (p *proj) holder(agent, id string) string { return store.Holder(p.folded.sessions, agent, id) }

// session is agent's current session in p (under mu; ok false: none recorded).
func (p *proj) session(agent string) (store.Session, bool) {
	return store.Current(p.folded.sessions, agent)
}

// transcript is the recorded transcript of pane's agent (under mu): its current session's ("": none recorded; the
// reader then takes the one herdr's session names).
func (a *App) transcript(p *proj, pane Pane) string {
	agent, ok := p.reg.whoIs(pane)
	if !ok {
		return ""
	}
	s, _ := p.session(agent)
	return s.Transcript
}
