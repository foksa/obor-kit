package launch_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/testkit"
	agykind "obor/kinds/agy/kind"
)

// staleHooks is agy's hooks file of an obor before T-1502: its PreToolUse matched three tools.
const staleHooks = `{"obor": {"PreInvocation": [{"command": "obor kind agy hook", "timeout": 30, "type": "command"}], ` +
	`"PreToolUse": [{"hooks": [{"command": "obor kind agy hook", "timeout": 30, "type": "command"}], "matcher": "run_command"}, ` +
	`{"hooks": [{"command": "obor kind agy hook", "timeout": 30, "type": "command"}], "matcher": "write_file"}, ` +
	`{"hooks": [{"command": "obor kind agy hook", "timeout": 30, "type": "command"}], "matcher": "edit_file"}], ` +
	`"SessionStart": [{"command": "obor kind agy hook", "timeout": 30, "type": "command"}], ` +
	`"Stop": [{"command": "obor kind agy hook", "timeout": 30, "type": "command"}]}}`

// currentHooks is agy's hooks file with this kit's obor entry.
func currentHooks() string {
	b, _ := json.Marshal(map[string]any{"obor": agykind.HooksEntry()})
	return string(b)
}

// agyOn is the launcher's fixture on fx (a plain project, or a git one) with the lead, the reviewer, the integrator, the
// deep dive and the custom role advisor (its cwd the project) on agy (neon), the lane's worker dispatched on it (Request.Type), and
// the main checkout's agy hooks file root ("": none).
func agyOn(t *testing.T, mk func(t *testing.T, edits ...func(map[string]any)) *fx, root string) *fx {
	t.Helper()
	x := mk(t, testkit.AddModels(`{"neon":{"kind":"agy","id":"gemini-4-neon-high"}}`),
		testkit.Set("agents.roles", `{"integrator":"neon","lead":"neon","reviewer":"neon","deep_dive":"neon","advisor":"neon"}`))
	if root != "" {
		x.p.Write(agykind.HooksFile, root)
	}
	return x
}

// laneHead lays the plain lane L-01 out on branch (its worktree's HEAD file, as worktreeHead reads it).
func laneHead(x *fx, ref string) {
	x.p.Write(filepath.Join(x.p.Root, ".git", "worktrees", "L-01", "HEAD"), ref+"\n")
}

// refusedFor fails unless the start's result out is a FAIL holding each of want, with nothing typed or started and
// nothing stopped.
func refusedFor(t *testing.T, x *fx, out string, ok bool, want ...string) {
	t.Helper()
	if ok {
		t.Fatalf("the start succeeded, want a refusal:\n%s", out)
	}
	for _, w := range want {
		contains(t, failLine(out), w, "the refusal")
	}
	if x.startCall() != "" || len(x.calls("Write")) != 0 || len(x.calls("SendKeys")) != 0 {
		t.Errorf("a refused start typed, stopped or started something: %q", x.f.Calls())
	}
}

// A lane worker's agy start reads the hooks file of the lane it starts in (the main checkout's is current in every
// row): current → started; the three-matcher file → refused, the refusal naming the lane's file and the lane's fix
// (its branch task/T-1 rebased or the task redispatched); the lane dir there with no file → refused naming it.
func TestAgyLaneWorkerStartReadsTheLaneHooksFile(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		file string   // the lane's hooks file ("": none)
		want []string // the refusal's text, "{path}" the lane's hooks file (nil: a start)
	}{
		{"current hooks file: starts", currentHooks(), nil},
		{"stale (three matchers): refused, names the file and the lane's fix", staleHooks,
			[]string{"agy's hooks: {path}'s obor entry is not this kit's",
				"bring the lane's branch task/T-1 onto the integration branch's head", "obor dispatch T-1"}},
		{"dir exists, no file: refused", "", []string{"agy's hooks: no {path} ("}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := agyOn(t, setup, currentHooks())
			laneHead(x, "ref: refs/heads/task/T-1")
			path := filepath.Join(x.lane, agykind.HooksFile)
			if c.file != "" {
				x.p.Write(path, c.file)
			}
			x.shell("w1:p3", "pj-l-01", "")
			out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Type: "neon"})
			if c.want == nil {
				if !ok {
					t.Fatalf("restart failed:\n%s", out)
				}
				contains(t, x.startCall(), "kind=agy", "the start")
				return
			}
			for i := range c.want {
				c.want[i] = strings.ReplaceAll(c.want[i], "{path}", path)
			}
			refusedFor(t, x, out, ok, c.want...)
		})
	}
}

// A role agent (the reviewer: a role worktree there, detached) whose worktree's hooks file is stale once launch
// prepared it (launch moves no existing role worktree) is refused, naming that file and the role worktree's fix; the
// agent already running in its pane is not stopped. The main checkout's file is current.
func TestAgyRoleAgentStaleAfterPreparationIsRefusedRunningAgentKept(t *testing.T) {
	t.Parallel()
	x := agyOn(t, setup, currentHooks())
	wt := x.p.Cfg().RoleWorktree("reviewer")
	x.p.Write(filepath.Join(x.p.Root, ".git", "worktrees", filepath.Base(wt), "HEAD"),
		"3e2887a0865ebc86e62ed858ef347b863942ef12\n")
	path := filepath.Join(wt, agykind.HooksFile)
	x.p.Write(path, staleHooks)
	x.agent("w1:p7", "pj-rev", "agy", "idle")
	out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true})
	refusedFor(t, x, out, ok, "agy's hooks: "+path+"'s obor entry is not this kit's",
		"move the role worktree to the integration branch's head: git -C "+wt+" checkout --detach")
	if a := x.f.At("w1:p7"); a == nil || a.Kind != "agy" {
		t.Errorf("the running reviewer was stopped: %+v", a)
	}
	eq(t, x.p.Read(path), staleHooks, "the role worktree's hooks file (launch moves no existing worktree)")
}

// A start in the main checkout (the custom role advisor: its cwd the project root) reads the root's hooks file:
// current → starts; stale → refused (an agent), naming the root's file and its fix.
func TestAgyRootStartReadsTheRootHooksFile(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, root string
		refused    bool
	}{
		{"root's file current: starts", currentHooks(), false},
		{"root's file stale: refused", staleHooks, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := agyOn(t, setup, c.root)
			cfg, err := config.Load(x.p.Root)
			if err != nil {
				t.Fatal(err)
			}
			if info, ok := cfg.AgentLookup("pj-advisor", ""); !ok || info.Role != "advisor" || info.Cwd != x.p.Root {
				t.Fatalf("precondition: pj-advisor is the advisor, its cwd the root: %+v %v", info, ok)
			}
			x.shell("w1:p8", "pj-advisor", "")
			out, ok := x.restart(launch.Request{Agent: "pj-advisor", Fresh: true})
			if !c.refused {
				if !ok {
					t.Fatalf("restart failed:\n%s", out)
				}
				contains(t, x.startCall(), "kind=agy", "the start")
				return
			}
			refusedFor(t, x, out, ok, "agy's hooks: "+filepath.Join(x.p.Root, agykind.HooksFile)+"'s obor entry is not this kit's",
				"run obor setup agents in the project, then commit "+agykind.HooksFile)
		})
	}
}

// The lead's start in a main checkout whose hooks file is stale starts, with the kind's warning naming the file and
// its fix. Not parallel: it captures plugin.Log, the kinds' warning writer (no parallel test runs meanwhile).
func TestAgyLeadStaleHooksWarnsAndStarts(t *testing.T) {
	x := agyOn(t, setup, staleHooks)
	var log lockedBuf
	old := plugin.Log
	plugin.Log = &log
	t.Cleanup(func() { plugin.Log = old })
	x.shell("w1:p1", "pj-lead", "")
	out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true})
	if !ok {
		t.Fatalf("restart failed:\n%s", out)
	}
	contains(t, x.startCall(), "kind=agy", "the start")
	contains(t, log.String(), "WARN agy's hooks: "+filepath.Join(x.p.Root, agykind.HooksFile)+"'s obor entry is not this kit's",
		"the kind's warning")
}
