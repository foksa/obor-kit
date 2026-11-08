//go:build conformance

// The static tier of the kind protocol (T-1036, docs/agents/kind-protocol.md): obor's plugin package against a plugin
// kind written as a shell script (kindplugin/obor-kind-shell), every call. No CLI, no session.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
)

const (
	pluginGo     = "internal/agents/plugin/calls.go"
	pluginScript = "tests/conformance/kindplugin/obor-kind-shell"
)

// pluginStatic is the plugin kind's static tier: P-S1 describe and its cache, P-S2 start, P-S3 not-ready, P-S4
// install, P-S5 turn, P-S6 deliver, P-S7 the cached manifest answering with the command gone (hook time).
func pluginStatic() {
	out.begin("plugin", fmt.Sprintf("protocol %d", plugin.Version))
	where := func(needle string) string { return at(pluginGo, needle) + ", kind-protocol.md" }
	check := func(id, fact string, ok bool, why string, w string) {
		if ok {
			report("PASS", id, fact, w)
			return
		}
		report("FAIL", id, fact+": "+why, w)
	}
	tmp, err := os.MkdirTemp("", "obor-conformance-plugin")
	if err != nil {
		report("FAIL", "P-S1", "a temp dir: "+err.Error(), pluginScript)
		return
	}
	defer os.RemoveAll(tmp)
	// a copy of the script, so P-S7 can remove it; the state dir beside it
	cmd, state := filepath.Join(tmp, "obor-kind-shell"), filepath.Join(tmp, "state")
	b, err := os.ReadFile(pluginScript)
	if err == nil {
		err = os.WriteFile(cmd, b, 0o755)
	}
	if err != nil {
		report("FAIL", "P-S1", "the script: "+err.Error(), pluginScript)
		return
	}

	k, err := plugin.Cache("shell", cmd, state)
	if err != nil {
		report("FAIL", "P-S1", "describe: "+err.Error(), where("func (k *Kind) run"))
		return
	}
	c := k.Caps()
	_, cacheErr := os.Stat(plugin.CacheFile(state, "shell"))
	check("P-S1", "describe answers a manifest (status hooks, hooks plugin, resume) and setup caches it",
		c.Status == agents.StatusHooks && c.Hooks == agents.HooksPlugin && c.Resume && cacheErr == nil,
		fmt.Sprintf("caps %+v, cache %v", c, cacheErr), where("func (k *Kind) run"))

	in := agents.Start{Kit: "/kit", Prompt: "hello", Args: []string{"--x"}}
	l, err := agents.StartOf(k, in)
	conf := filepath.Join(state, "shell-kind.json")
	written, _ := os.ReadFile(conf)
	check("P-S2", "start: argv with the written file's path substituted, the file in the state dir",
		err == nil && strings.Join(l.Args, " ") == "--config "+conf+" --prompt hello" && string(written) == `{"mcp":"/kit/bin/obor mcp"}`,
		fmt.Sprintf("args %q, file %q, err %v", l.Args, written, err), where("func (k *Kind) start"))
	check("P-S2", "start: env of the manifest's vars, the file's path substituted, from the same call",
		err == nil && l.Env["SHELL_KIND_CONFIG"] == conf && len(l.Env) == 1, fmt.Sprintf("env %v, err %v", l.Env, err),
		where("func (k *Kind) StartWhole"))

	check("P-S3", "not-ready: the reason on an updating screen, none on a ready one",
		k.NotReady("Updating to 2.0") == "is updating itself" && k.NotReady("> ") == "",
		fmt.Sprintf("%q / %q", k.NotReady("Updating to 2.0"), k.NotReady("> ")), where("func (k *Kind) NotReady"))

	inst, err := agents.InstallOf(k, "/kit", os.Getenv)
	how, registered, static := "", false, false
	if err == nil {
		how, registered, static = inst.MCP.Lane(nil, nil)
	}
	check("P-S4", "install: registered per start, the lane rule the manifest's mcp_lane",
		err == nil && inst.MCP.Where == agents.PerStart && inst.Plugin == nil && registered && !static &&
			strings.HasPrefix(how, "SHELL_KIND_CONFIG"),
		fmt.Sprintf("%+v %q %v %v, err %v", inst.MCP, how, registered, static, err), where("func (k *Kind) InstallErr"))

	turn, okTurn := k.LastTurn([]byte("{}\n"))
	sess, okSess := k.Session([]byte("{}\n"))
	reset, limited := k.Reset("rate_limit", "resets 6pm", time.Now())
	check("P-S5", "turn: the last turn, the session, the reset (what: last, session, reset)",
		okTurn && turn.ID == "t-1" && turn.Ended && turn.Error == "rate_limit" &&
			okSess && sess.ID == "s-1" && sess.Cwd == "/lane" && !sess.Started.IsZero() &&
			limited && reset.Equal(time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)),
		fmt.Sprintf("turn %+v %v, session %+v %v, reset %v %v", turn, okTurn, sess, okSess, reset, limited),
		where("func (k *Kind) LastTurn"))

	d, okDel := k.Deliver("hi", []byte(`{"SHELL_KIND_INBOX":"/s"}`))
	check("P-S6", "deliver: typed", okDel && d.Typed, fmt.Sprintf("%+v %v", d, okDel), where("func (k *Kind) Deliver"))

	// hook time: a kind loaded from the cache answers what the manifest holds with the command gone
	os.Remove(cmd)
	h := plugin.New("shell", cmd, state)
	transcript := h.Transcript(func(string) string { return "/home/u" }, "s-1")
	check("P-S7", "the cached manifest answers with no process (caps, stop, headless, transcript, inbox record)",
		h.Caps() == c && h.Stop().Quit == "/exit" && h.Headless().PrintFlag &&
			transcript == "/home/u/.shell-kind/s-1.jsonl" &&
			string(h.InboxRecord(func(string) string { return "/s" })) == `{"SHELL_KIND_INBOX":"/s"}`,
		fmt.Sprintf("caps %+v, stop %+v, transcript %q", h.Caps(), h.Stop(), transcript), at("internal/agents/plugin/plugin.go", "func New"))
	_, err = agents.StartOf(h, in)
	check("P-S7", "a call that runs the command fails naming the kind and the call once the command is gone",
		err != nil && strings.HasPrefix(err.Error(), "kind shell: describe: "), fmt.Sprint(err),
		at("internal/agents/plugin/plugin.go", "func (k *Kind) manifest"))
}
