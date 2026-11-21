//go:build integration

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/testkit"
)

// --json (the binary, serve down): one indented object whose lanes name the one configured lane, needs_you [] and
// messages [] present, no paused (serve down: .obor/pause is not read), no ready field; the text has no ready line.
func TestStatusJSON(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "task/T-01")
	p.Config()
	p.Write(".obor/pause", "-\nhold\n") // no reader; serve (down here) holds the pause
	rc, stdout, stderr := run(t, p.Root, nil, "status", "--json")
	if rc != 0 || !strings.HasPrefix(stdout, "{\n  \"serve\": {") {
		t.Fatalf("--json: exit %d, %s\n%s", rc, stdout, stderr)
	}
	var st struct {
		Lanes []map[string]any `json:"lanes"`
		Ready any              `json:"ready"`
		Pause *struct {
			Reason string `json:"reason"`
		} `json:"paused"`
		NeedsYou []any `json:"needs_you"`
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Lanes) != 1 || st.Lanes[0]["lane"] != "L-01" || st.NeedsYou == nil || st.Messages == nil {
		t.Errorf("lanes %v, needs_you %v, messages %v; want the one lane L-01, needs_you [], messages []", st.Lanes,
			st.NeedsYou, st.Messages)
	}
	if st.Ready != nil || strings.Contains(stdout, `"ready"`) {
		t.Errorf("ready: %v; want no ready field (T-470)", st.Ready)
	}
	if st.Pause != nil {
		t.Errorf("paused %+v; want none (serve down; .obor/pause is not read)", st.Pause)
	}
	if _, text, _ := run(t, p.Root, nil, "status"); strings.Contains(text, "ready:") {
		t.Errorf("a ready line in the text:\n%s", text)
	}
}

// --json carries each model's pause as model_pauses (serve's answer through the client: the binary), and a model serve
// does not name (its pause cleared) has none. The text lines of them: TestStatusListsModelPauses.
func TestStatusJSONModelPauses(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	until := time.Now().Add(2 * time.Hour).Unix()
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true,
		Paused: &event.Pause{Why: "quota", Caller: "pj-lead"},
		ModelPauses: map[string]*event.Pause{
			"sonnet":   {Why: "quota", Caller: "pj-lead"},
			"deepseek": {Why: "down", Until: until, Caller: "pj-lead"},
		}}}
	home, _ := startFakeServe(t, p.Root, ss, "", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	rc, stdout, stderr := run(t, p.Root, env, "status", "--json")
	if rc != 0 {
		t.Fatalf("status --json: exit %d, %s", rc, stderr)
	}
	var got struct {
		ModelPauses map[string]struct {
			Reason string `json:"reason"`
			Until  *int64 `json:"until"`
		} `json:"model_pauses"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ModelPauses) != 2 || got.ModelPauses["sonnet"].Reason != "quota" || got.ModelPauses["sonnet"].Until != nil ||
		got.ModelPauses["deepseek"].Until == nil || *got.ModelPauses["deepseek"].Until != until {
		t.Errorf("--json model_pauses %+v, want sonnet's (quota, until cleared) and deepseek's (down, until %d)",
			got.ModelPauses, until)
	}
	ss.Project.ModelPauses = nil
	startFakeServeAt(t, wire.SocketPath(p.Root), ss, "", "")
	_, stdout, _ = run(t, p.Root, env, "status", "--json")
	if strings.Contains(stdout, "model_pauses") {
		t.Errorf("--json names model_pauses after the clear:\n%s", stdout)
	}
}

// the binary's --short over a lane and a full check prints neither, only serve's line and `running: nothing`
// when nothing runs.
func TestStatusShortBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	rc, stdout, stderr := run(t, p.Root, nil, "status", "--short")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "serve: down (") || lines[1] != "running: nothing" {
		t.Errorf("obor status --short: exit %d, %q, %q; want serve's down line and `running: nothing`", rc, stdout, stderr)
	}
	if strings.Contains(stdout, "L-01") || strings.Contains(stdout, "full check") {
		t.Errorf("--short prints a lane or the full check:\n%s", stdout)
	}
}

// the lane table names each configured lane's worker (config derives it); its states are checks.TestLaneStates'.
func TestLaneTable(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "task/T-01")
	p.Config()
	ls, err := checks.Lanes{}.Lanes(context.Background(), p.Root)
	if err != nil || len(ls) != 1 {
		t.Fatalf("lanes %+v, %v", ls, err)
	}
	if ls[0].Agent != "pj-l-01" {
		t.Errorf("agent %q, want the lane's worker pj-l-01", ls[0].Agent)
	}
}
