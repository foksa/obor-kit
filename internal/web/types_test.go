package web

// The page's hand-kept copy of the API's JSON, tied to the Go structs that write it (T-885, docs/research/data-model.md
// task A1): every interface of web/src/lib/types.ts has the same property names as its Go struct's json tags, and every
// string set of web/src/lib is made of Go's constants. A field one side has and the other lacks fails here, before it
// can drift to a page that reads a field Go never sets (Lane.hold) or misses one Go sends (TaskStatus.stage).

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/view"
)

// tsTypes is types.ts, and tsLib/tsPages the page's source, from this package's directory.
var (
	tsTypes = filepath.Join("..", "..", "web", "src", "lib", "types.ts")
	tsLib   = filepath.Join("..", "..", "web", "src", "lib")
	tsPages = filepath.Join("..", "..", "web", "src", "pages")
)

// goStructs is the Go struct each TS interface mirrors, by the interface's name: view's read model, wire's serve answer,
// and web's own answer shapes. An interface with no same-named struct (HelpCommand is help.Command, ProjectView is
// view.Project) is in tsOnly; a new interface must be one or the other, so the test never silently skips one.
var goStructs = map[string]reflect.Type{
	"Background":    reflect.TypeOf(view.Background{}),
	"Running":       reflect.TypeOf(view.Running{}),
	"Serve":         reflect.TypeOf(view.Serve{}),
	"Job":           reflect.TypeOf(view.Job{}),
	"Task":          reflect.TypeOf(view.Task{}),
	"Lane":          reflect.TypeOf(view.Lane{}),
	"BriefStatus":   reflect.TypeOf(wire.BriefStatus{}),
	"Role":          reflect.TypeOf(view.Role{}),
	"NeedsYou":      reflect.TypeOf(view.NeedsYou{}),
	"FullCheck":     reflect.TypeOf(view.FullCheck{}),
	"Pause":         reflect.TypeOf(view.Pause{}),
	"PendingStep":   reflect.TypeOf(wire.PendingStep{}),
	"TaskStatus":    reflect.TypeOf(wire.TaskStatus{}),
	"AgentStatus":   reflect.TypeOf(wire.AgentStatus{}),
	"MessageStatus": reflect.TypeOf(wire.MessageStatus{}),
	"WatchStatus":   reflect.TypeOf(wire.WatchStatus{}),
	"Idle":          reflect.TypeOf(view.Idle{}),
	"MergeItem":     reflect.TypeOf(view.MergeItem{}),
	"WaitItem":      reflect.TypeOf(view.WaitItem{}),
	"ReviewItem":    reflect.TypeOf(view.ReviewItem{}),
	"OutboxItem":    reflect.TypeOf(view.OutboxItem{}),
	"Queue":         reflect.TypeOf(view.Queue{}),
	"Event":         reflect.TypeOf(view.Event{}),
	"EventsPage":    reflect.TypeOf(view.EventsPage{}),
	"LaneDot":       reflect.TypeOf(LaneDot{}),
	"ProjectsEntry": reflect.TypeOf(ProjectsEntry{}),
	"TaskRecord":    reflect.TypeOf(view.TaskRecord{}),
	"Prompt":        reflect.TypeOf(view.Prompt{}),
	"TaskDetail":    reflect.TypeOf(view.TaskDetail{}),
	"AgentSession":  reflect.TypeOf(view.AgentSession{}),
	"AgentLine":     reflect.TypeOf(view.AgentLine{}),
	"AgentDetail":   reflect.TypeOf(view.AgentDetail{}),
	"SetupLevel":    reflect.TypeOf(view.SetupLevel{}),
	"SetupType":     reflect.TypeOf(view.SetupType{}),
	"SetupModel":    reflect.TypeOf(view.SetupModel{}),
	"SetupKind":     reflect.TypeOf(view.SetupKind{}),
	"SetupLane":     reflect.TypeOf(view.SetupLane{}),
	"SetupRole":     reflect.TypeOf(view.SetupRole{}),
	"DispatchCount": reflect.TypeOf(view.DispatchCount{}),
	"Setup":         reflect.TypeOf(view.Setup{}),
	"Help":          reflect.TypeOf(Help{}),
	"PromoteStatus": reflect.TypeOf(wire.PromoteStatus{}),
	"PromoteRun":    reflect.TypeOf(wire.PromoteRun{}),
	"PromoteRed":    reflect.TypeOf(wire.PromoteRed{}),
}

// tsOnly are the interfaces with no same-named Go struct: help's rows are help.Command and help.Section (web.Help ties
// them to /api/help) and ProjectView is the view the page names its own way (view.Project). ProjectsEntry is here for
// the record; a same-named Go struct wins, so it is compared (see TestTypesTSMatchesGoJSON).
var tsOnly = map[string]bool{"HelpCommand": true, "HelpSection": true, "ProjectView": true, "ProjectsEntry": true}

// goAliases are the interfaces whose Go name is an alias of another package's struct (view.Idle = wire.IdleStatus, T-883): the
// struct's own name differs, its json names are the same.
var goAliases = map[string]string{"Idle": "IdleStatus"}

// TestTypesTSMatchesGoJSON: every interface of types.ts has exactly its Go struct's json names, no more and no fewer.
func TestTypesTSMatchesGoJSON(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(tsTypes)
	if err != nil {
		t.Fatalf("read %s: %v", tsTypes, err)
	}
	ts := tsInterfaces(string(src))
	if decls := interfaceDecls(string(src)); len(ts) != decls { // every declaration parsed, not half the file
		t.Fatalf("parsed %d of %s's %d interfaces: the parser broke", len(ts), tsTypes, decls)
	}
	checked := 0
	for _, name := range sortedKeys(ts) {
		typ, ok := goStructs[name]
		if !ok {
			if !tsOnly[name] {
				t.Errorf("%s: no Go struct to compare with (add it to goStructs or tsOnly)", name)
			}
			continue
		}
		if typ.Name() != name && goAliases[name] != typ.Name() {
			t.Errorf("%s: goStructs compares %s, not %s", name, typ.Name(), name)
		}
		missing, extra := diffKeys(ts[name], typ)
		for _, k := range missing {
			t.Errorf("%s: Go's json %q is missing from the interface (add it to %s)", name, k, name)
		}
		for _, k := range extra {
			t.Errorf("%s: the interface's %q is no Go json field (drop it)", name, k)
		}
		checked++
	}
	for name := range goStructs {
		if _, ok := ts[name]; !ok {
			t.Errorf("goStructs names %s, which %s has no interface for", name, tsTypes)
		}
	}
	if checked != len(goStructs) {
		t.Errorf("compared %d interfaces, goStructs names %d", checked, len(goStructs))
	}
}

// TestTypesTSCheckRefusesDrift is the check's own proof: a Go field TS lacks, and a TS field Go lacks, are both
// reported. Without it a comparison that always returned nothing would pass the test above.
func TestTypesTSCheckRefusesDrift(t *testing.T) {
	t.Parallel()
	type drifted struct {
		A string `json:"a"`
		B string `json:"b,omitempty"`
	}
	const src = "export interface Drifted {\n  a: string;\n  c?: number;\n}\n"
	keys, ok := tsInterface(src, "Drifted")
	if !ok || !slices.Equal(keys, []string{"a", "c"}) {
		t.Fatalf("precondition: parsed %v (ok %v), want the interface's a and c", keys, ok)
	}
	missing, extra := diffKeys(keys, reflect.TypeOf(drifted{}))
	if !slices.Equal(missing, []string{"b"}) {
		t.Errorf("missing = %v, want [b]: a Go field TS lacks must fail", missing)
	}
	if !slices.Equal(extra, []string{"c"}) {
		t.Errorf("extra = %v, want [c]: a TS field Go lacks must fail", extra)
	}
}

// TestTSStringSetsMatchGo: the strings the page switches on are Go's constants, not retyped copies: the merge loop's
// stages (Project.svelte's pipeline, wire.Stage*), and the stepper's steps (stages.ts TASK_STEPS, one per
// wire.TaskStep*: T-883, the view places a task on one, the page no longer maps phases, lane states or free itself).
func TestTSStringSetsMatchGo(t *testing.T) {
	t.Parallel()
	stages := []string{wire.StageSubmitted, wire.StageGating, wire.StageReview, wire.StagePublish, wire.StageHeld}

	stagesTS := readTS(t, filepath.Join(tsLib, "stages.ts"))
	steps, ok := tsStringSet(stagesTS, "export const TASK_STEPS = [", "]")
	if !ok || len(steps) != wire.TaskStepMerged+1 || steps[wire.TaskStepMerged] != "merged" ||
		steps[wire.TaskStepGate] != "gate" {
		t.Errorf("stages.ts: TASK_STEPS %v (parsed %v), want one name per wire.TaskStep*, gate at %d and merged last",
			steps, ok, wire.TaskStepGate)
	}
	for _, retyped := range []string{"PHASE_STEP", "stageOf(l.column)"} {
		if strings.Contains(stagesTS, retyped) {
			t.Errorf("stages.ts retypes %s: the view ships the task's step (T-883)", retyped)
		}
	}

	project := readTS(t, filepath.Join(tsPages, "Project.svelte"))
	shown := pipelineStages(project)
	if len(shown) != len(stages) {
		t.Errorf("Project.svelte's pipeline shows %v, want wire's stages %v", shown, stages)
	}
	for _, s := range shown {
		if !slices.Contains(stages, s) {
			t.Errorf("Project.svelte: the pipeline has stage %q, not a wire.Stage*", s)
		}
	}
	for _, s := range stages {
		if !slices.Contains(shown, s) {
			t.Errorf("Project.svelte: the pipeline never shows wire stage %q", s)
		}
	}

	format := readTS(t, filepath.Join(tsLib, "format.ts"))
	// T-977: the event kinds serve names and a promotion run's steps are Go's.
	if kinds, ok := tsStringSet(format, "export const SERVE_KINDS = [", "]"); !ok || !slices.Equal(kinds, []string{app.KindPromotion}) {
		t.Errorf("format.ts: SERVE_KINDS %v (parsed %v), want [%s] (app.Kind*)", kinds, ok, app.KindPromotion)
	}
	promotion := readTS(t, filepath.Join(tsLib, "promotion.ts"))
	if steps, ok := tsStringSet(promotion, "export const PROMOTE_STEPS = [", "]"); !ok ||
		!slices.Equal(steps, []string{wire.PromoteSuite, wire.PromoteReview}) {
		t.Errorf("promotion.ts: PROMOTE_STEPS %v (parsed %v), want [%s %s] (wire.Promote*)", steps, ok,
			wire.PromoteSuite, wire.PromoteReview)
	}
	if strings.Contains(format, "export const FREE") {
		t.Errorf("format.ts retypes the free lane states: the view ships Lane.free (T-883)")
	}
	for _, page := range []string{"Project.svelte", "Fleet.svelte"} {
		if src := readTS(t, filepath.Join(tsPages, page)); strings.Contains(src, "stageOf(l.column)") {
			t.Errorf("%s places a lane by its column: a lane git calls working is not the phase (T-883)", page)
		}
	}
}

// readTS is path's source.
func readTS(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// tsInterfaces is src's exported interfaces by name, each its top-level property names (an inline object's own keys are
// not the interface's).
func tsInterfaces(src string) map[string][]string {
	out := map[string][]string{}
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "export interface ") || !strings.HasSuffix(t, " {") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(t, "export interface "), " {")
		if keys, ok := tsInterface(src, name); ok {
			out[name] = keys
		}
	}
	return out
}

// interfaceDecls is how many "export interface … {" lines src has.
func interfaceDecls(src string) int {
	n := 0
	for _, l := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "export interface ") {
			n++
		}
	}
	return n
}

// tsInterface is the property names of src's "export interface name { … }": a member line's name is the text before its
// first colon, `?` stripped; a line inside an inline object type ({ task: string; … }) is not a member.
func tsInterface(src, name string) ([]string, bool) {
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "export interface "+name+" {" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	var keys []string
	depth := 0
	for _, l := range lines[start:] {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		if depth == 0 {
			if t == "}" {
				return keys, true
			}
			if k, ok := tsKey(t); ok {
				keys = append(keys, k)
			}
		}
		depth += strings.Count(t, "{") - strings.Count(t, "}")
	}
	return nil, false
}

// tsKey is a TS interface member line's property name ("key?: <type>;"): "" for a line that declares none.
func tsKey(line string) (string, bool) {
	before, _, ok := strings.Cut(line, ":")
	if !ok {
		return "", false
	}
	k := strings.TrimSuffix(strings.TrimSpace(before), "?")
	if k == "" || strings.ContainsAny(k, " ;,({[") {
		return "", false
	}
	return k, true
}

// diffKeys is keys (TS) against t's json names (Go): the names each side has and the other lacks, sorted.
func diffKeys(keys []string, t reflect.Type) (missing, extra []string) {
	goNames := jsonNames(t)
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
		if !goNames[k] {
			extra = append(extra, k)
		}
	}
	for k := range goNames {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	return missing, extra
}

// jsonNames is t's json field names: each field's tag, a nested struct's when it is embedded without a tag (encoding/json
// flattens it), and a field with no tag by its name; `json:"-"` is not sent.
func jsonNames(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
		case name != "":
			out[name] = true
		case f.Anonymous && f.Type.Kind() == reflect.Struct:
			for k := range jsonNames(f.Type) {
				out[k] = true
			}
		default:
			out[f.Name] = true
		}
	}
	return out
}

// tsStringSet is the strings of a JS array literal opened by open and closed by close ("export const FREE = new
// Set([", "])").
func tsStringSet(src, open, close string) ([]string, bool) {
	at := strings.Index(src, open)
	if at < 0 {
		return nil, false
	}
	rest := src[at+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return nil, false
	}
	var out []string
	for _, e := range strings.Split(rest[:end], ",") {
		if s := strings.Trim(strings.TrimSpace(e), `"'`); s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

// pipelineStages is the stage names Project.svelte's pipeline asks for (at("gating") and the like), in their order.
func pipelineStages(src string) []string {
	var out []string
	for _, part := range strings.Split(src, `at("`)[1:] {
		s, _, ok := strings.Cut(part, `")`)
		if ok && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// sortedKeys is m's keys, sorted.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
