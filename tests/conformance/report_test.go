package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readVersions(t *testing.T, path string) versions {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v versions
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return v
}

func TestReportPrintsOneLinePerCheckAndCountsByKind(t *testing.T) {
	var out bytes.Buffer
	r := newRun(&out)
	r.report("SKIP", "tier", "before any kind", "x")
	r.begin("herdr", "0.9.3")
	r.report("PASS", "H-S1", "method ping takes no params", "internal/herdr/contract.go:9, CAP §5.1")
	r.report("WARN", "H-S2", "newer protocol", "socket.go:1")
	r.begin("claude", "2.1.318")
	r.report("FAIL", "CC-L1", "no record", "claude.go:1")
	r.note("a leftover")

	want := "SKIP tier before any kind (x)\n" +
		"PASS H-S1 method ping takes no params (internal/herdr/contract.go:9, CAP §5.1)\n" +
		"WARN H-S2 newer protocol (socket.go:1)\n" +
		"FAIL CC-L1 no record (claude.go:1)\n" +
		"note: a leftover\n"
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
	if got := r.summary(); got != "conformance: 1 pass, 1 fail, 1 skip, 1 warn" {
		t.Errorf("summary = %q", got)
	}
	if !r.failed() {
		t.Error("failed() = false with a FAIL line")
	}
	if h := r.kinds["herdr"]; h.Pass != 1 || h.Warn != 1 || h.Fail != 0 || h.Skip != 0 {
		t.Errorf("herdr tally = %+v, want 1 pass 1 warn (the SKIP before begin is no kind's)", *h)
	}
	if c := r.kinds["claude"]; c.Fail != 1 || c.Pass != 0 {
		t.Errorf("claude tally = %+v, want 1 fail", *c)
	}
}

func TestSaveWritesPassedVersionPerKindAndTier(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".obor", "state", "conformance.json") // save makes the dirs
	r := newRun(&bytes.Buffer{})
	r.begin("herdr", "0.9.3")
	r.report("PASS", "H-S1", "f", "w")
	r.begin("codex", "") // not installed: nothing to record
	r.report("SKIP", "CX-S1", "codex is not installed", "w")
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if err := r.save(path, "static", at); err != nil {
		t.Fatal(err)
	}
	v := readVersions(t, path)
	got := v.Kinds["herdr"]["static"]
	if got.Version != "0.9.3" || got.Passed != "0.9.3" || got.Pass != 1 || got.At != "2026-11-04T11:00:00Z" {
		t.Errorf("herdr static = %+v, want version and passed 0.9.3, 1 pass, at in UTC", got)
	}
	if _, ok := v.Kinds["codex"]; ok {
		t.Errorf("codex recorded without a version: %+v", v.Kinds["codex"])
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestSaveKeepsOtherKindsTiersAndTheLastPassOnAFail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conformance.json")
	first := newRun(&bytes.Buffer{})
	first.begin("claude", "2.1.318")
	first.report("PASS", "CC-S1", "f", "w")
	first.begin("herdr", "0.9.3")
	first.report("PASS", "H-S1", "f", "w")
	if err := first.save(path, "static", time.Now()); err != nil {
		t.Fatal(err)
	}
	live := newRun(&bytes.Buffer{})
	live.begin("claude", "2.1.318")
	live.report("PASS", "CC-L1", "f", "w")
	if err := live.save(path, "live", time.Now()); err != nil {
		t.Fatal(err)
	}
	failed := newRun(&bytes.Buffer{})
	failed.begin("claude", "2.1.320")
	failed.report("PASS", "CC-S1", "f", "w")
	failed.report("FAIL", "CC-S2", "f", "w")
	if err := failed.save(path, "static", time.Now()); err != nil {
		t.Fatal(err)
	}

	v := readVersions(t, path)
	st := v.Kinds["claude"]["static"]
	if st.Version != "2.1.320" || st.Passed != "2.1.318" || st.Fail != 1 {
		t.Errorf("claude static = %+v, want version 2.1.320 checked, passed still 2.1.318", st)
	}
	if lv := v.Kinds["claude"]["live"]; lv.Passed != "2.1.318" || lv.Pass != 1 {
		t.Errorf("claude live = %+v, want the live run kept", lv)
	}
	if h := v.Kinds["herdr"]["static"]; h.Passed != "0.9.3" {
		t.Errorf("herdr static = %+v, want it kept", h)
	}
}

func TestSaveOnlySkipsIsNoPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conformance.json")
	r := newRun(&bytes.Buffer{})
	r.begin("codex", "0.166.0")
	r.report("SKIP", "CX-L1", "no auth", "w")
	if err := r.save(path, "live", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := readVersions(t, path).Kinds["codex"]["live"]; got.Version != "0.166.0" || got.Passed != "" || got.Skip != 1 {
		t.Errorf("codex live = %+v, want version recorded, no passed", got)
	}
}

// T-816 (RV-104 #4): the versions file says how far each kind's run got — passed, partial (any SKIP) or failed — and
// only a run with no SKIP advances the passed version, so a run whose TURN checks were skipped is not recorded as a
// pass.
func TestSaveRecordsPassedPartialOrFailedPerKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conformance.json")
	r := newRun(&bytes.Buffer{})
	r.begin("herdr", "0.9.3")
	r.report("PASS", "H-L1", "f", "w")
	r.begin("claude", "2.1.318")
	r.report("PASS", "CC-L1", "f", "w")
	r.report("SKIP", "CC-L4", "the plain-text line is not sent: one model call per kind", "w")
	r.begin("codex", "0.166.0")
	r.report("PASS", "CX-L1", "f", "w")
	r.report("FAIL", "CX-L2", "f", "w")
	if err := r.save(path, "live", time.Now()); err != nil {
		t.Fatal(err)
	}
	v := readVersions(t, path)
	if got := v.Kinds["herdr"]["live"]; got.State != StatePassed || got.Passed != "0.9.3" {
		t.Errorf("herdr live = %+v, want state passed and passed 0.9.3", got)
	}
	if got := v.Kinds["claude"]["live"]; got.State != StatePartial || got.Passed != "" {
		t.Errorf("claude live = %+v, want state partial and no passed version (a SKIP was not checked)", got)
	}
	if got := v.Kinds["codex"]["live"]; got.State != StateFailed || got.Passed != "" {
		t.Errorf("codex live = %+v, want state failed and no passed version", got)
	}
}

func TestAPartialRunKeepsTheLastGaplessPassedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conformance.json")
	whole := newRun(&bytes.Buffer{})
	whole.begin("claude", "2.1.318")
	whole.report("PASS", "CC-L1", "f", "w")
	if err := whole.save(path, "live", time.Now()); err != nil {
		t.Fatal(err)
	}
	partial := newRun(&bytes.Buffer{})
	partial.begin("claude", "2.1.320")
	partial.report("PASS", "CC-L1", "f", "w")
	partial.report("SKIP", "CC-L4", "needs TURN=1 (a model call)", "w")
	if err := partial.save(path, "live", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := readVersions(t, path).Kinds["claude"]["live"]
	if got.Version != "2.1.320" || got.State != StatePartial || got.Passed != "2.1.318" {
		t.Errorf("claude live = %+v, want version 2.1.320 partial, passed still 2.1.318", got)
	}
}

func TestSaveRefusesAnUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conformance.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRun(&bytes.Buffer{})
	r.begin("herdr", "0.9.3")
	r.report("PASS", "H-S1", "f", "w")
	err := r.save(path, "static", time.Now())
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("save = %v, want an error naming %s", err, path)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Errorf("file overwritten: %q", b)
	}
}

func TestVersionOf(t *testing.T) {
	for in, want := range map[string]string{
		"2.1.318 (Claude Code)\n": "2.1.318",
		"codex-cli 0.166.0":       "0.166.0",
		"herdr 0.9.3":             "0.9.3",
		"opencode v2.2.0":         "2.2.0",
		"v2.2":                    "2.2",
		"no version here":         "unknown",
	} {
		if got := versionOf(in); got != want {
			t.Errorf("versionOf(%q) = %q, want %q", in, got, want)
		}
	}
}
