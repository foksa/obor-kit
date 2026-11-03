package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
)

// The setup edit (T-753): the owner changes one setup value from obor web's Setup page (config.Edit names the keys),
// and serve, the one writer, applies it to the current .obor/config.json, validates the result, writes the file in its
// own layout and commits that one file on the main branch ("config: <key> <old> → <new> (owner, web)"). The write and
// its commit pass the publish admission (admit): never while a publish moves the main branch, and no publish starts
// until the commit is in. A config file with uncommitted changes is refused, so a hand edit is never committed under
// the owner's change. Serve reads the config per call, so the change counts from the next dispatch or start. Each
// change is fact config.set{key, old, new, by, source, commit, call id} and one line to the lead.

// ConfigFile is the main checkout's .obor/config.json and its git (the config_set call; nil: no setup edit).
type ConfigFile interface {
	// Read is the file's bytes, and why it cannot be changed now ("": it can): it has uncommitted changes, or the
	// main checkout is not on the main branch.
	Read(ctx context.Context) (doc []byte, refused string, err error)
	// Commit writes doc as the file (atomically) and commits that one file on the main branch with msg: the commit's
	// sha. A failed commit puts the file's old bytes back.
	Commit(ctx context.Context, doc []byte, msg string) (string, error)
}

// configCall answers the socket's config_set request (the owner's, or the lead's): a call id and a key are required,
// a repeated call id gets the stored reply. The reply is "SET <key> <old> → <new>", with a role's "(at its next
// start)".
func (a *App) configCall(c wire.Call) (string, error) {
	if c.CallID == "" || c.Key == "" {
		return "", errors.New(wire.MethodConfig + ": a call id and a key are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	reply, stored := "", false
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(wire.MethodConfig, c.CallID)]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodConfig, c.Root)
	case stored:
		return reply, nil
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("config: the owner or the lead changes the setup, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	fx, err := a.loopOf(p)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	if fx.Config == nil {
		return "", fmt.Errorf("config: no config writer for %s", p.name)
	}
	a.configMu.Lock() // one setup edit at a time: each reads the file the last one wrote
	defer a.configMu.Unlock()
	wait, cancel := context.WithTimeout(a.life, admitWait)
	release, err := a.admit(wait, p) // a publish running ends first; none starts until the commit is in
	cancel()
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(a.life, requestTimeout)
	defer cancel()
	return a.configSet(ctx, p, fx.Config, c)
}

// configSet applies, validates, writes and commits c's change, then records it and the lead's line (admitted).
func (a *App) configSet(ctx context.Context, p *proj, file ConfigFile, c wire.Call) (string, error) {
	doc, refused, err := file.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	if refused != "" {
		return "", fmt.Errorf("config: %s", refused)
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	ch, err := cfg.Edit(doc, c.Key, c.Value)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	if ch.Lane != "" {
		a.mu.Lock()
		task := p.laneHolds(ch.Lane)
		a.mu.Unlock()
		if task != "" {
			return "", fmt.Errorf("config: lane %s has task %s: change its type once the lane is free", ch.Lane, task)
		}
	}
	if string(ch.Doc) == string(doc) {
		return "UNCHANGED " + c.Key, nil
	}
	next, err := config.LoadWith(p.root, ch.Doc)
	if err != nil {
		return "", fmt.Errorf("config: %s would not load: %v", c.Key, err)
	}
	opt := config.ValidateOptions{Overlap: spec.Overlap}
	_, before := cfg.Validate(opt)
	_, after := next.Validate(opt)
	if fails := config.NewFails(before, after); len(fails) > 0 {
		return "", fmt.Errorf("config: %s refused: %s", c.Key, strings.Join(fails, "; "))
	}
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	where := by
	if c.Source != "" {
		where += ", " + c.Source
	}
	line := fmt.Sprintf("config: %s %s → %s (%s)", c.Key, unsetText(ch.Old), unsetText(ch.New), where)
	sha, err := file.Commit(ctx, ch.Doc, line)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	reply := fmt.Sprintf("SET %s %s → %s", c.Key, unsetText(ch.Old), unsetText(ch.New))
	if strings.HasPrefix(c.Key, "roles.") {
		reply += " (at its next start)"
	}
	d := event.ConfigSet{Key: c.Key, Old: ch.Old, New: ch.New, By: by, Source: c.Source, Commit: sha, Call: c.CallID, Reply: reply}
	if !a.record(p, event.Record{Task: event.ConfigTask, Step: event.StepConfigSet, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("config: committed %s, but the event log of %s refused its record", short(sha), p.name)
	}
	a.mu.Lock()
	lead := p.out.lead()
	a.mu.Unlock()
	if lead != "" {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
			event.Message{To: lead, From: "obor", Line: "[obor] " + line})
	}
	a.wake() // a waiting task may dispatch by the new setup
	return reply, nil
}

// unsetText is a change's value for its line: "unset" for none.
func unsetText(v string) string {
	if v == "" {
		return "unset"
	}
	return v
}

// short is a sha's first 7 characters.
func short(sha string) string { return sha[:min(7, len(sha))] }

// foldConfig folds a setup edit's fact: its call's stored reply.
func (p *proj) foldConfig(r event.Record) {
	if r.Task != event.ConfigTask || r.Kind != event.KindFact || r.Step != event.StepConfigSet {
		return
	}
	var d event.ConfigSet
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodConfig, d.Call)] = d.Reply
	}
}

// laneHolds is the task lane has now ("": none; under mu): an open task of the merge loop there, or a task the lanes
// read or the lane row puts there that is not ended.
func (p *proj) laneHolds(lane string) string {
	for _, t := range p.loopTasks() {
		if m := p.folded.loop[t]; m.lane == lane && !states[m.state].ended {
			return t
		}
	}
	ended := func(task string) bool {
		m := p.folded.loop[task]
		return m != nil && states[m.state].ended
	}
	for task, w := range p.tasks {
		if w.lane.Name == lane && !ended(task) {
			return task
		}
	}
	if task := p.laneTask(lane); task != "" && !ended(task) {
		return task
	}
	return ""
}
