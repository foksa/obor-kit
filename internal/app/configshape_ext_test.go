package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"obor/internal/app/event"
	"obor/internal/config"
	"obor/internal/version"
)

// T-1125 (docs/research/config-shape.md §4.4 P3): serve's one rewrite of a config.json of shape 1, on its pass.

// shape1Config is the loop rig's config (newLoopRig's) in shape 1, with done_file_sections, a key shape 2 drops.
const shape1Config = `{
  "version": 1,
  "name": "proj",
  "prefix": "pj",
  "lanes_dir": "../proj-wt",
  "branches": {"task": "task/{id}", "main": "main"},
  "task_id_pattern": "T-[0-9]+[a-z]?",
  "paths": {"reviews": "docs/reviews"},
  "commands": {"check": "echo 1 pass"},
  "lanes": 1,
  "roles": {"integrator": ""},
  "done_file_sections": ["Summary"]
}
`

// shape1Local is a .obor/local.json beside shape1Config: shape 1's handoff_tokens, shape 2's agents.handoff_tokens.
const shape1Local = `{"handoff_tokens": 400000}
`

// shapeWriter is a ConfigFile on the rig's config.json: Read answers refused while it is set; Commit writes the file
// and records the message.
type shapeWriter struct {
	mu      sync.Mutex
	path    string
	refused string
	commits []string
}

func (w *shapeWriter) Read(context.Context) ([]byte, string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refused != "" {
		return nil, w.refused, nil
	}
	b, err := os.ReadFile(w.path)
	return b, "", err
}

func (w *shapeWriter) Commit(_ context.Context, doc []byte, msg string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commits = append(w.commits, msg)
	return "c0ffee1234567", os.WriteFile(w.path, doc, 0o644)
}

func (w *shapeWriter) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.commits...)
}

func (w *shapeWriter) refuse(why string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refused = why
}

// shapeRig is a loop rig whose config.json is shape1Config, local.json shape1Local, and whose loop's ConfigFile is a
// shapeWriter (refusing with refused when set); not started.
func shapeRig(t *testing.T, refused string) (*loopRig, *shapeWriter) {
	t.Helper()
	r := newLoopRig(t)
	r.p.Write(".obor/config.json", shape1Config)
	r.p.Write(".obor/local.json", shape1Local)
	if cfg, err := config.Load(r.p.Root); err != nil || !cfg.Shape1() {
		t.Fatalf("precondition: a config of shape 1 that loads: %v", err)
	}
	w := &shapeWriter{path: filepath.Join(r.p.Root, ".obor", "config.json"), refused: refused}
	r.config = w
	return r, w
}

// shapeLines is the log's lines to the lead (pj-lead) of the rewrite.
func shapeLines(r *loopRig) []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Task != event.MsgTask || rec.Kind != event.KindFact || rec.Step != event.StepMessage {
			continue
		}
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if m.To == "pj-lead" && strings.HasPrefix(m.Line, "[obor] config: .obor/config.json rewritten") {
			out = append(out, m.Line)
		}
	}
	return out
}

// configNeeds is the log's needs-you facts of the config rewrite (task config).
func configNeeds(r *loopRig) []event.Record {
	var out []event.Record
	for _, rec := range r.records() {
		if rec.Task == event.ConfigTask && rec.Kind == event.KindFact && rec.Step == event.StepNeedsYou {
			out = append(out, rec)
		}
	}
	return out
}

// A config.json of shape 1 is rewritten once at serve's start: one commit (subject "config: shape 2 (obor
// <version>)", the dropped key in the body) of the reshaped file, local.json rewritten in place, and one line to the
// lead; later passes and the next start (the file is shape 2 now) commit and say nothing.
func TestServeRewritesShape1OnceAtStart(t *testing.T) {
	t.Parallel()
	r, w := shapeRig(t, "")
	r.start()
	r.waitFor("the rewrite's commit", func() bool { return len(w.messages()) > 0 })
	r.passes()
	msgs := w.messages()
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "config: shape 2 (obor "+version.Version+")\n\n") ||
		!strings.Contains(msgs[0], "Dropped (no home in shape 2): done_file_sections") {
		t.Fatalf("commits %q, want one: config: shape 2 (obor %s), done_file_sections in its body", msgs, version.Version)
	}
	doc := []byte(r.p.Read(".obor/config.json"))
	if config.ShapeOf(doc) != config.ShapeVersion || strings.Contains(string(doc), "done_file_sections") ||
		!strings.Contains(string(doc), `"agent_prefix": "pj"`) {
		t.Errorf("config.json after the rewrite is not shape 2 with the values moved:\n%s", doc)
	}
	local := r.p.Read(".obor/local.json")
	if v, _ := config.MemberAt([]byte(local), []string{"agents", "handoff_tokens"}); string(v) != "400000" ||
		strings.HasPrefix(strings.TrimSpace(local), `{"handoff_tokens"`) {
		t.Errorf("local.json after the rewrite:\n%s\nwant agents.handoff_tokens 400000", local)
	}
	if cfg, err := config.Load(r.p.Root); err != nil || cfg.Shape1() || cfg.HandoffTokens() != 400000 {
		t.Errorf("the rewritten files load: %v (shape 1 %v)", err, cfg.Shape1())
	}
	lines := shapeLines(r)
	if len(lines) != 1 || !strings.Contains(lines[0], "by obor serve (commit c0ffee1; dropped: done_file_sections)") ||
		!strings.Contains(lines[0], ".obor/local.json rewritten in place") {
		t.Errorf("lines to the lead %q, want one naming the commit, the dropped key and local.json", lines)
	}

	r.stop()
	r.start()
	r.passes()
	if got, lines := w.messages(), shapeLines(r); len(got) != 1 || len(lines) != 1 {
		t.Errorf("after the next start: commits %q, lines %q; want the first start's only", got, lines)
	}
}

// A refusal (the file has uncommitted changes) writes nothing and is one needs-you of the project, kept while the
// refusal lasts; once the file is clean a pass rewrites it and the needs-you ends.
func TestServeShape1RewriteRefusedIsNeedsYou(t *testing.T) {
	t.Parallel()
	why := ".obor/config.json has uncommitted changes in /x: commit or discard them first"
	r, w := shapeRig(t, why)
	r.start()
	r.waitFor("the refusal's needs-you", func() bool { return len(configNeeds(r)) > 0 })
	r.passes()
	needs := configNeeds(r)
	if len(needs) != 1 || !strings.Contains(needs[0].Fact().Reason, why) {
		t.Fatalf("needs-you facts %+v, want one naming the refusal", needs)
	}
	if got := r.needsYou(); got.Task != event.ConfigTask {
		t.Errorf("the needs-you in force is %s's, want the config's", got.Task)
	}
	if got := r.p.Read(".obor/config.json"); got != shape1Config {
		t.Errorf("config.json was written:\n%s", got)
	}
	if got := r.p.Read(".obor/local.json"); got != shape1Local {
		t.Errorf("local.json was written:\n%s", got)
	}
	if len(w.messages()) != 0 || len(shapeLines(r)) != 0 {
		t.Errorf("a refused rewrite committed %q or told the lead %q", w.messages(), shapeLines(r))
	}

	w.refuse("")
	r.waitFor("the rewrite once the file is clean", func() bool { return len(w.messages()) == 1 })
	r.waitFor("the needs-you to end", func() bool { return configResumed(r) })
	r.passes()
	if len(w.messages()) != 1 || len(shapeLines(r)) != 1 || len(configNeeds(r)) != 1 {
		t.Errorf("after the refusal ended: commits %q, lines %q, needs-you %d; want one each", w.messages(),
			shapeLines(r), len(configNeeds(r)))
	}
}

// configResumed: the log has the fact that ended the config rewrite's needs-you.
func configResumed(r *loopRig) bool {
	for _, rec := range r.records() {
		if rec.Task == event.ConfigTask && rec.Kind == event.KindFact && rec.Step == event.StepResumed {
			return true
		}
	}
	return false
}
