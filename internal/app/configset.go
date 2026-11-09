package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/spec"
	"obor/internal/version"
)

// The setup edit (T-753, T-760): the owner changes setup values from obor web's Setup page (config.Edit names the
// keys), and serve, the one writer, applies them in order to the current .obor/config.json, validates the result, writes
// the file in its own layout and commits that one file on the main branch, all of them in one commit or none. The
// write and its commit pass the publish admission (admit): never while a publish moves the main branch, and no publish
// starts until the commit is in. A config file with uncommitted changes is refused, so a hand edit is never committed
// under the owner's change; so is one whose revision is not the call's base (config.Rev): someone changed it since the
// page read it. Serve reads the config per call, so the changes count from the next dispatch or start. They are fact
// config.set{changes, by, source, commit, call id} and one line to the lead listing them all.

// ConfigFile is the main checkout's .obor/config.json and its git (the config_set call; nil: no setup edit).
type ConfigFile interface {
	// Read is the file's bytes, and why it cannot be changed now ("": it can): it has uncommitted changes, or the
	// main checkout is not on the main branch.
	Read(ctx context.Context) (doc []byte, refused string, err error)
	// Commit writes doc as the file (atomically) and commits that one file on the main branch with msg: the commit's
	// sha. A failed commit puts the file's old bytes back.
	Commit(ctx context.Context, doc []byte, msg string) (string, error)
}

// configCall answers the socket's config_set request (the owner's, or the lead's): a call id and at least one change
// are required, a repeated call id waits for the call in flight and gets its stored reply. The call takes configMu (one
// main-checkout writer at a time) before its stored-reply lookup. The reply is "SET <key> <old> → <new>" per change,
// joined by "; ", a role's with "(at its next start)".
func (a *App) configCall(c wire.ConfigSetReq) (string, error) {
	if c.CallID == "" || len(c.Changes) == 0 {
		return "", errors.New(wire.MethodConfig + ": a call id and a change are required")
	}
	for _, ch := range c.Changes {
		if ch.Key == "" {
			return "", errors.New(wire.MethodConfig + ": a change without a key")
		}
	}
	a.configMu.Lock() // one main-checkout writer at a time: each reads the file the last one wrote, and a repeated call
	// id waits for the call in flight before its stored-reply lookup (T-930)
	defer a.configMu.Unlock()
	p, reply, stored := a.callProj(wire.MethodConfig, c.Envelope)
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

// configSet applies c's changes in order, each to the file the one before it wrote, validates the result, writes and
// commits it once, then records the fact and the lead's line (admitted). Any change refused refuses them all: nothing
// is written.
func (a *App) configSet(ctx context.Context, p *proj, file ConfigFile, c wire.ConfigSetReq) (string, error) {
	doc, refused, err := file.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	if refused != "" {
		return "", fmt.Errorf("config: %s", refused)
	}
	if config.ShapeOf(doc) < config.ShapeVersion { // an edit would mix the shapes: the rewrite comes first (T-1125)
		return "", fmt.Errorf("config: %s", config.Shape1Refusal(p.root))
	}
	if c.Base != "" && config.Rev(doc) != c.Base {
		return "", errors.New("config: .obor/config.json changed since this page read it (another tab, the lead or a " +
			"hand edit): nothing was saved; read the setup again and redo the changes")
	}
	cfg, err := config.Load(p.root)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	cur, next := doc, cfg
	var done []event.ConfigSetChange
	for _, w := range c.Changes {
		ch, err := next.Edit(cur, w.Key, w.Value)
		if err != nil {
			return "", fmt.Errorf("config: %v", err)
		}
		for _, lane := range ch.Dropped {
			a.mu.Lock()
			task := p.laneHolds(lane)
			a.mu.Unlock()
			if task != "" {
				return "", fmt.Errorf("config: lane %s has task %s: lower lanes once it is free", lane, task)
			}
		}
		if string(ch.Doc) == string(cur) {
			continue
		}
		if next, err = config.LoadWith(p.root, ch.Doc); err != nil {
			return "", fmt.Errorf("config: %s would not load: %v", w.Key, err)
		}
		cur = ch.Doc
		done = append(done, event.ConfigSetChange{Key: w.Key, Old: ch.Old, New: ch.New})
	}
	if len(done) == 0 {
		keys := make([]string, 0, len(c.Changes))
		for _, w := range c.Changes {
			keys = append(keys, w.Key)
		}
		return "UNCHANGED " + strings.Join(keys, ", "), nil
	}
	opt := config.ValidateOptions{Overlap: spec.Overlap}
	_, before := cfg.Validate(opt)
	_, after := next.Validate(opt)
	if fails := config.NewFails(before, after); len(fails) > 0 {
		return "", fmt.Errorf("config: %s refused: %s", changedKeys(done), strings.Join(fails, "; "))
	}
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	where := by
	if c.Source != "" {
		where += ", " + c.Source
	}
	line := fmt.Sprintf("config: %s (%s)", changeText(done, false), where)
	msg := line
	if len(done) > 1 { // the subject names the keys; the body has one change a line
		msg = fmt.Sprintf("config: %s (%s)\n\n%s", changedKeys(done), where, changeText(done, true))
	}
	sha, err := file.Commit(ctx, cur, msg)
	if err != nil {
		return "", fmt.Errorf("config: %v", err)
	}
	parts := make([]string, 0, len(done))
	for _, d := range done {
		r := fmt.Sprintf("SET %s %s → %s", d.Key, unsetText(d.Old), unsetText(d.New))
		if strings.HasPrefix(d.Key, "agents.roles.") {
			r += " (at its next start)"
		}
		parts = append(parts, r)
	}
	reply := strings.Join(parts, "; ")
	d := event.ConfigSet{Changes: done, By: by, Source: c.Source, Commit: sha, Call: c.CallID, Reply: reply}
	if !a.record(p, event.Record{Task: event.ConfigTask, Step: event.StepConfigSet, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("config: committed %s, but the event log of %s refused its record", short(sha), p.name)
	}
	a.mu.Lock()
	lead := p.out.Lead()
	a.mu.Unlock()
	if lead != "" {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
			event.Message{To: lead, From: "obor", Line: "[obor] " + line})
	}
	a.wake() // a waiting task may dispatch by the new setup
	return reply, nil
}

// changeText is the changes as "<key> <old> → <new>", joined by "; " (lines: one "- " line each).
func changeText(done []event.ConfigSetChange, lines bool) string {
	out := make([]string, 0, len(done))
	for _, d := range done {
		t := fmt.Sprintf("%s %s → %s", d.Key, unsetText(d.Old), unsetText(d.New))
		if lines {
			t = "- " + t
		}
		out = append(out, t)
	}
	if lines {
		return strings.Join(out, "\n")
	}
	return strings.Join(out, "; ")
}

// changedKeys is the changes' keys, joined by ", ".
func changedKeys(done []event.ConfigSetChange) string {
	keys := make([]string, 0, len(done))
	for _, d := range done {
		keys = append(keys, d.Key)
	}
	return strings.Join(keys, ", ")
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

// Config shape 2's one rewrite (T-1125, docs/research/config-shape.md §4.2 steps 2-3): serve's first pass on a
// config.json of shape 1 (configShape) and obor config migrate (RewriteShape) reshape it (config.Reshape), commit it on
// the main branch through the setup edit's ConfigFile (subject "config: shape 2 (obor <version>)", the dropped keys in
// the body) and rewrite .obor/local.json in place (untracked). A refusal of ConfigFile.Read (uncommitted changes, the
// main checkout on another branch) writes nothing: the project runs on the load's in-memory reshape, and serve's
// refusal (or failure: a commit a hook refused) is a needs-you (task ConfigTask) until a pass rewrites the file.

// RewriteShape rewrites the config.json of the project at root, of shape 1, as shape 2 through file; by is who does it
// (the commit and the line name it). line is the [obor] line's text without its prefix; "" with no refusal when the
// file is shape 2 already (nothing written). refused is ConfigFile.Read's refusal (nothing written). A failed commit
// puts local.json back.
func RewriteShape(ctx context.Context, root string, file ConfigFile, by string) (line, refused string, err error) {
	disk, err := os.ReadFile(filepath.Join(root, ".obor", "config.json"))
	if err != nil {
		return "", "", err
	}
	if config.ShapeOf(disk) >= config.ShapeVersion { // read without git: a pass of a rewritten project stops here
		return "", "", nil
	}
	doc, refused, err := file.Read(ctx)
	if err != nil || refused != "" || config.ShapeOf(doc) >= config.ShapeVersion {
		return "", refused, err
	}
	next, err := config.Reshape(doc)
	if err != nil {
		return "", "", fmt.Errorf(".obor/config.json: cannot reshape it: %v", err)
	}
	localPath := filepath.Join(root, ".obor", "local.json")
	local, err := os.ReadFile(localPath)
	var nextLocal []byte
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return "", "", err
	default:
		r, err := config.Reshape(local)
		if err != nil {
			return "", "", fmt.Errorf(".obor/local.json: cannot reshape it: %v", err)
		}
		if !sameCompact(r, local) {
			nextLocal = []byte(config.Pretty(json.RawMessage(r)))
		}
	}
	dropped := "none"
	if keys := config.ShapeDropped(doc); len(keys) > 0 {
		dropped = strings.Join(keys, ", ")
	}
	if nextLocal != nil {
		if err := project.WriteAtomic(localPath, nextLocal); err != nil {
			return "", "", err
		}
	}
	msg := fmt.Sprintf("config: shape 2 (obor %s)\n\nRewritten from config shape 1 by %s.\nDropped (no home in shape 2): %s",
		version.Version, by, dropped)
	sha, err := file.Commit(ctx, []byte(config.Pretty(json.RawMessage(next))), msg)
	if err != nil {
		if nextLocal != nil {
			_ = project.WriteAtomic(localPath, local) // nothing half done
		}
		return "", "", err
	}
	line = fmt.Sprintf("config: .obor/config.json rewritten as config shape 2 by %s (commit %s; dropped: %s)", by,
		short(sha), dropped)
	if nextLocal != nil {
		line += "; .obor/local.json rewritten in place"
	}
	return line, "", nil
}

// sameCompact: a and b are the same JSON text but for white space.
func sameCompact(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

// configShape is serve's rewrite on a pass (fx.Config set), until this process finds the file of shape 2 or rewrote
// it (p.shaped): one commit and one line to the lead; a refusal is the project's needs-you (settled once a pass
// rewrites it). It takes configMu (a setup edit in flight: the next pass) and an admission (a publish running: the next
// pass), as a setup edit does.
func (a *App) configShape(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	done := p.shaped
	a.mu.Unlock()
	if done || fx == nil || fx.Config == nil || !a.configMu.TryLock() {
		return
	}
	defer a.configMu.Unlock()
	now, cancel := context.WithCancel(ctx)
	cancel() // no wait: a publish running defers the rewrite to a later pass
	release, err := a.admit(now, p)
	if err != nil {
		return
	}
	defer release()
	line, refused, err := RewriteShape(ctx, p.root, fx.Config, "obor serve")
	switch {
	case ctx.Err() != nil: // serve stops: the next start tries again
		return
	case err != nil: // a commit a hook refused, say: the owner's, as a refusal is
		refused = err.Error()
		fallthrough
	case refused != "":
		a.settleNeeds(p, event.ConfigTask, "config shape 1 not rewritten: "+refused+
			" (serve tries again on its next pass; it runs reshaped meanwhile)")
		return
	}
	a.settleNeeds(p, event.ConfigTask, "")
	a.mu.Lock()
	p.shaped = true
	lead := p.out.Lead()
	a.mu.Unlock()
	if line == "" {
		return
	}
	a.logf("%s: %s", p.name, line)
	if lead != "" {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
			event.Message{To: lead, From: "obor", Line: "[obor] " + line})
	}
}
