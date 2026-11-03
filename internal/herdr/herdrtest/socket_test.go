package herdrtest

import (
	"encoding/json"
	"testing"
)

// T-541 (RV-56 #5): pane.send_input types its text and keeps it pending; only Enter submits it, Escape clears it.
func TestPaneInputSubmitsOnlyOnEnter(t *testing.T) {
	for _, c := range []struct {
		name    string
		keys    []string
		quits   bool
		pending string
	}{
		{"no keys: the text waits", nil, false, "/exit"},
		{"esc: the text is cleared", []string{"esc"}, false, ""},
		{"Enter: the text runs", []string{"Enter"}, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := New()
			f.Agent("w1:p1", "pj-l-01", "idle", "")
			if p := f.Pane("w1:p1"); p == nil || p.Kind == "" {
				t.Fatal("precondition: pane w1:p1 hosts an agent")
			}
			params, err := json.Marshal(map[string]any{"pane_id": "w1:p1", "text": "/exit", "keys": c.keys})
			if err != nil {
				t.Fatal(err)
			}
			answer, _ := (&Server{F: f}).answer(nil, "pane.send_input", params)
			if e, ok := answer["error"]; ok {
				t.Fatalf("pane.send_input failed: %v", e)
			}
			p := f.Pane("w1:p1")
			if quit := p.Kind == ""; quit != c.quits {
				t.Errorf("agent quit = %v, want %v (pane holds %q)", quit, c.quits, p.Kind)
			}
			if p.Pending != c.pending {
				t.Errorf("pending text = %q, want %q", p.Pending, c.pending)
			}
		})
	}
}
