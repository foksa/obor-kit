package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPageScript (T-676) runs the page's script in node against a fake browser (testdata/page_harness.js: a DOM, fetch
// and a clock it advances), one scenario a subtest: a transition between the page's first read and its cursor is shown,
// a change with no record shows after the periodic refresh, a failed redraw is retried, each project's cursor is kept
// when navigating back, the task pipeline's step per stage, a stopped role agent's status and the agents' background
// processes (T-714), a busy lane's type (T-721) and the setup page's cards with its 7 days / all switch (T-750). No node
// on PATH: skipped (node is no runtime dependency; cmd/obor's opencode test does the same).
func TestPageScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	m := regexp.MustCompile(`(?s)<script>\n(.*)</script>`).FindSubmatch(pageHTML)
	if m == nil {
		t.Fatal("page.html has no <script>")
	}
	harness, err := os.ReadFile(filepath.Join("testdata", "page_harness.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(harness), "\n//PAGE//\n") {
		t.Fatal("the harness has no //PAGE// line")
	}
	script := strings.Replace(string(harness), "\n//PAGE//\n", "\n"+strings.Replace(string(m[1]), `"use strict";`, "", 1)+"\n", 1)
	file := filepath.Join(t.TempDir(), "page.js")
	if err := os.WriteFile(file, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"transitionAfterFirstRead", "changeWithoutRecord", "failedRedrawRetried",
		"cursorKeptPerProject", "failedFirstDraw", "pipeline", "stoppedRole", "background", "laneType", "setupRoute"} {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command(node, file, name).CombinedOutput()
			if err != nil {
				t.Errorf("%v:\n%s", err, out)
			}
		})
	}
}
