package launch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// A plugin kind's start reads its override files from the manifest it starts with. An unchanged command starts on the
// cached manifest: one start call, no describe. The first start after the command changed describes it once, and
// that manifest's override file reaches that same start (the cached manifest named none); a start after it makes no
// describe call. A changed command whose manifest names a file that does not parse: that first start is refused,
// naming the file, with no start call.
func TestPluginKindStartReadsTheOverrideFilesOfTheManifestItStartsWith(t *testing.T) {
	dir := t.TempDir()
	cmd, describe := filepath.Join(dir, "obor-kind-stub"), filepath.Join(dir, "describe")
	callLog, reqFile := filepath.Join(dir, "calls"), filepath.Join(dir, "request")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >>'" + callLog + "'\ncase \"$1\" in\n" +
		"describe) cat >/dev/null; cat '" + describe + "' ;;\n" +
		"start) cat >'" + reqFile + "'; echo '{\"version\":1,\"args\":[\"--go\"]}' ;;\n" +
		"*) echo \"no call $1\" >&2; exit 2 ;;\nesac\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// upgrade is a new command: its manifest now names files as its override files, and its mtime moves on.
	mtime := time.Now().Truncate(time.Second)
	upgrade := func(files string) {
		t.Helper()
		m := `{"version":1,"name":"stub","status":"terminal","caps":{}` + files + `}`
		if err := os.WriteFile(describe, []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
		mtime = mtime.Add(time.Second)
		if err := os.Chtimes(cmd, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	upgrade("")
	x := setup(t, testkit.Set("agents.kinds", `{"stub":{"plugin":"`+cmd+`"}}`),
		testkit.Set("agents.models", `{"st":{"kind":"stub","id":""}}`))
	if _, err := plugin.Cache("stub", cmd, filepath.Join(x.p.Root, ".obor", "state")); err != nil { // obor setup agents' cache
		t.Fatal(err)
	}
	cfg, err := config.Load(x.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Kinds().IsPlugin("stub") {
		t.Fatal("precondition: the config has kind stub as a plugin kind")
	}
	const theme, themeFile = `{"theme": "dark"}`, ".obor/stub/stub_override.json"
	x.p.Write(themeFile, theme)

	// start is one build of a worker's start: its error, the calls the command got for it, and the overrides of
	// its start request (nil: no start call).
	start := func() (error, string, map[string]string) {
		t.Helper()
		os.Remove(callLog)
		os.Remove(reqFile)
		l := &launch.Launcher{Cfg: cfg, Env: x.p.Env(), Kit: "/kit"}
		_, err := l.Build(launch.Build{Agent: "w", Kind: "stub", Role: "worker", Args: []string{"--base"}})
		calls, _ := os.ReadFile(callLog)
		var req struct {
			Overrides map[string]string `json:"overrides"`
		}
		if b, rerr := os.ReadFile(reqFile); rerr == nil {
			if err := json.Unmarshal(b, &req); err != nil {
				t.Fatalf("start request %q: %v", b, err)
			}
		}
		return err, strings.Join(strings.Fields(string(calls)), " "), req.Overrides
	}

	if err, calls, over := start(); err != nil || calls != "start" || len(over) != 0 {
		t.Fatalf("an unchanged command: %v, calls %q, overrides %v; want one start call, no describe, no override "+
			"(the cached manifest names none)", err, calls, over)
	}

	upgrade(`,"override_files":["stub_override.json"]`)
	if err, calls, over := start(); err != nil || calls != "describe start" || over["stub_override.json"] != theme {
		t.Errorf("the first start after the command changed: %v, calls %q, overrides %v; want one describe, then the "+
			"start with %s", err, calls, over, themeFile)
	}
	if err, calls, over := start(); err != nil || calls != "start" || over["stub_override.json"] != theme {
		t.Errorf("the start after it: %v, calls %q, overrides %v; want one start call with %s, no describe", err, calls,
			over, themeFile)
	}

	const otherFile = ".obor/stub/other_override.json"
	x.p.Write(otherFile, `{"theme": `)
	if err, calls, over := start(); err != nil || calls != "start" || len(over) != 1 {
		t.Fatalf("precondition: %s is no override file of the manifest yet: %v, calls %q, overrides %v", otherFile, err,
			calls, over)
	}
	upgrade(`,"override_files":["other_override.json"]`)
	if err, calls, _ := start(); err == nil || !strings.Contains(err.Error(), otherFile+": does not parse") || calls != "describe" {
		t.Errorf("the first start after the command changed, its new override file not parsing: %v, calls %q; want it "+
			"refused naming %s, after one describe and with no start call", err, calls, otherFile)
	}
}
