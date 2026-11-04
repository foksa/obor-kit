package main

// T-820: Claude creates a session's transcript at the first prompt (CAP §2.4), so CC-L2 checks the path at
// SessionStart and the file's existence after the turn.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ccL2Record is a SessionStart stub record whose stdin is the payload of sid and path.
func ccL2Record(t *testing.T, sid, path string) record {
	t.Helper()
	b, err := json.Marshal(payload{SessionID: sid, TranscriptPath: path, Cwd: "/lane", Source: "startup"})
	if err != nil {
		t.Fatal(err)
	}
	return record{Stdin: string(b)}
}

func TestCCL2SkipsTheTranscriptBeforeTheFirstPrompt(t *testing.T) {
	sid := "6832327a-5a1a-4c55-9c19-0d1f5d0d1a11"
	path := filepath.Join(t.TempDir(), sid+".jsonl") // not created: only the first prompt does
	var out bytes.Buffer
	r := newRun(&out)
	ccL2(r, sid, []record{ccL2Record(t, sid, path)}, false, "test")
	if r.counts["FAIL"] != 0 {
		t.Errorf("no turn: %d FAIL(s), want none; output:\n%s", r.counts["FAIL"], out.String())
	}
	if got := r.counts["PASS"]; got != 1 {
		t.Errorf("the id and path checks: %d PASS(es), want 1; output:\n%s", got, out.String())
	}
	if r.counts["SKIP"] != 1 || !strings.Contains(out.String(), "SKIP CC-L2 transcript created at the first prompt") {
		t.Errorf("got %d SKIP(s), want one skipping the missing transcript; output:\n%s", r.counts["SKIP"], out.String())
	}
}

func TestCCL2PassesWhenTheTranscriptExistsAfterTheTurn(t *testing.T) {
	sid := "6832327a-5a1a-4c55-9c19-0d1f5d0d1a11"
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := newRun(&out)
	ccL2(r, sid, []record{ccL2Record(t, sid, path)}, true, "test")
	if r.counts["FAIL"] != 0 || r.counts["SKIP"] != 0 {
		t.Errorf("file present after the turn: %d FAIL(s), %d SKIP(s), want none; output:\n%s",
			r.counts["FAIL"], r.counts["SKIP"], out.String())
	}
	if got := r.counts["PASS"]; got != 2 || !strings.Contains(out.String(), "the transcript exists after the first turn") {
		t.Errorf("got %d PASS(es), want the path checks and the transcript's existence; output:\n%s", got, out.String())
	}
}

func TestCCL2FailsWhenTheTurnLeavesNoTranscript(t *testing.T) {
	sid := "6832327a-5a1a-4c55-9c19-0d1f5d0d1a11"
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	var out bytes.Buffer
	r := newRun(&out)
	ccL2(r, sid, []record{ccL2Record(t, sid, path)}, true, "test")
	if r.counts["FAIL"] != 1 || !strings.Contains(out.String(), "FAIL CC-L2") {
		t.Errorf("no transcript after the turn: %d FAIL(s), want one; output:\n%s", r.counts["FAIL"], out.String())
	}
}
