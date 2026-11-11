package herdr_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

// requestEnvelope are the keys of the request line itself (request), not params.
var requestEnvelope = []string{"id", "method", "params"}

// sites is what the package's non-test files send: request methods named by a string literal at a call, the string
// keys of the params maps (map[string]any literals and m["k"] = v assignments), and the subscription types (the
// Event* / event* string constants).
type sites struct{ methods, keys, subs map[string]bool }

func readSites(t *testing.T) sites {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := sites{map[string]bool{}, map[string]bool{}, map[string]bool{}}
	lit := func(e ast.Expr) (string, bool) {
		b, ok := e.(*ast.BasicLit)
		if !ok || b.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(b.Value)
		return v, err == nil
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					// (*Socket).call(ctx, timeout, &r, method, params) and agentResult(ctx, timeout, method, params).
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
						at := map[string]int{"call": 3, "agentResult": 2}[sel.Sel.Name]
						if at > 0 && len(n.Args) > at {
							if m, ok := lit(n.Args[at]); ok {
								s.methods[m] = true
							}
						}
					}
				case *ast.ValueSpec: // const op = "events.subscribe"
					for i, id := range n.Names {
						if id.Name == "op" && i < len(n.Values) {
							if m, ok := lit(n.Values[i]); ok {
								s.methods[m] = true
							}
						}
						if strings.HasPrefix(id.Name, "Event") || strings.HasPrefix(id.Name, "event") {
							if v, ok := lit(n.Values[i]); ok {
								s.subs[v] = true
							}
						}
					}
				case *ast.CompositeLit:
					if mt, ok := n.Type.(*ast.MapType); ok {
						if id, ok := mt.Key.(*ast.Ident); ok && id.Name == "string" {
							if v, ok := mt.Value.(*ast.Ident); ok && v.Name == "any" {
								for _, e := range n.Elts {
									if kv, ok := e.(*ast.KeyValueExpr); ok {
										if k, ok := lit(kv.Key); ok {
											s.keys[k] = true
										}
									}
								}
							}
						}
					}
				case *ast.AssignStmt:
					for _, l := range n.Lhs {
						if ix, ok := l.(*ast.IndexExpr); ok {
							if k, ok := lit(ix.Index); ok {
								s.keys[k] = true
							}
						}
					}
				}
				return true
			})
		}
	}
	return s
}

func sorted(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diff names what is in a but not in b.
func diff(a, b map[string]bool) []string {
	var out []string
	for _, k := range sorted(a) {
		if !b[k] {
			out = append(out, k)
		}
	}
	return out
}

func TestSentCoversCallSites(t *testing.T) {
	s := readSites(t)
	if len(s.methods) < 10 || len(s.keys) < 10 || len(s.subs) < 5 { // precondition: the scan saw the call sites
		t.Fatalf("scan found %d methods, %d param keys, %d subscription types: too few, the parser misses the call sites",
			len(s.methods), len(s.keys), len(s.subs))
	}
	listed, listedKeys, listedSubs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for m, ps := range herdr.Sent {
		listed[m] = true
		for _, p := range ps {
			listedKeys[p] = true
		}
	}
	for ty, ps := range herdr.SentSubscriptions {
		listedSubs[ty] = true
		for _, p := range ps {
			listedKeys[p] = true
		}
	}
	for _, k := range requestEnvelope {
		listedKeys[k] = true
	}
	listedKeys["type"] = true // the subscription's discriminator

	if d := diff(s.methods, listed); len(d) > 0 {
		t.Errorf("call sites send methods missing from herdr.Sent: %v", d)
	}
	if d := diff(listed, s.methods); len(d) > 0 {
		t.Errorf("herdr.Sent lists methods no call site sends: %v", d)
	}
	if d := diff(s.keys, listedKeys); len(d) > 0 {
		t.Errorf("call sites send params missing from herdr.Sent / SentSubscriptions: %v", d)
	}
	if d := diff(listedKeys, s.keys); len(d) > 0 {
		t.Errorf("herdr.Sent lists params no call site sends: %v", d)
	}
	if d := diff(s.subs, listedSubs); len(d) > 0 {
		t.Errorf("call sites subscribe to types missing from herdr.SentSubscriptions: %v", d)
	}
	if d := diff(listedSubs, s.subs); len(d) > 0 {
		t.Errorf("herdr.SentSubscriptions lists types no call site subscribes to: %v", d)
	}
	if !slices.Contains(herdr.SentSubscriptions["pane.agent_status_changed"], "pane_id") {
		t.Error("pane.agent_status_changed must list pane_id")
	}
}

// TestWireIsOne: herdr's Wire is the one struct of herdr's pane and agent JSON, filled by herdrtest's fake and
// decoded by the client, so neither side can carry a field the other lacks.
func TestWireIsOne(t *testing.T) {
	if f, ok := reflect.TypeOf(herdrtest.Pane{}).FieldByName("Wire"); !ok || f.Type != reflect.TypeOf(herdr.Wire{}) {
		t.Fatalf("herdrtest.Pane does not embed herdr.Wire: the fake restates the protocol's fields")
	}
	want := herdr.Wire{
		Name: "pj-l-01", ID: "w1:p1", Tab: "w1:t1", Terminal: "t1", Workspace: "w1", Label: "pj-l-01",
		Status: "idle", Cwd: "/proj-wt/L-01", Kind: "claude", Seq: 7, LaunchPending: true, InteractiveReady: true,
		AgentSession: &herdr.AgentSession{Agent: "claude", Kind: "id", Value: "5e1d0c2b-aaaa"},
	}
	// Precondition: the literal fills every field, so a field added to Wire cannot go untested here.
	rv := reflect.ValueOf(want)
	for i := 0; i < rv.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Fatalf("Wire.%s is zero in this test: fill every field of the one struct", rv.Type().Field(i).Name)
		}
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != rv.NumField() { // two fields sharing a tag, or one tagged "-", hides one
		t.Errorf("herdr.Wire marshals %d JSON keys for its %d fields: %s", len(keys), rv.NumField(), b)
	}
	var got herdr.Wire
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("herdr.Wire does not survive JSON, so a tag is wrong in one direction: got %+v, want %+v", got, want)
	}

	// The fake fills that struct, and the client reads every field of it but state_change_seq (no decision reads it,
	// the stream compares panes without it).
	f := herdrtest.New()
	p := f.Agent("w1:p1", "pj-l-01", "idle", "/proj-wt/L-01")
	p.Tab, p.Terminal, p.Workspace, p.Label, p.Seq = "w1:t1", "t1", "w1", "pj-l-01", 7
	p.Session = "5e1d0c2b-aaaa"
	p.LaunchPending = true // read: Launched false below (interactive_ready: TestAgentLaunchFact)
	snap, err := herdrtest.Serve(t, f).Client().Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Panes) != 1 {
		t.Fatalf("the fake's snapshot has %d panes, want 1", len(snap.Panes))
	}
	wantPane := herdr.Pane{Pane: port.Pane{ID: "w1:p1", Terminal: "t1", Label: "pj-l-01", Cwd: "/proj-wt/L-01",
		Agent: "claude", Status: "idle", Session: "5e1d0c2b-aaaa"}, Workspace: "w1", Tab: "w1:t1", Name: "pj-l-01"}
	if got := snap.Panes[0]; got != wantPane {
		t.Errorf("the fake's pane = %+v, want the scripted %+v", got, wantPane)
	}
	if len(snap.Agents) != 1 || snap.Agents[0].Name != "pj-l-01" || snap.Agents[0].Session != "5e1d0c2b-aaaa" {
		t.Errorf("the fake's agent = %+v, want the same struct filled", snap.Agents)
	}
}
