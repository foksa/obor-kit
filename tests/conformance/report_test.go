package main

import (
	"bytes"
	"testing"
)

func TestReportPrintsOneLinePerCheckAndCounts(t *testing.T) {
	var out bytes.Buffer
	r := newRun(&out)
	r.report("SKIP", "tier", "before any kind", "x")
	r.report("PASS", "H-S1", "method ping takes no params", "internal/herdr/contract.go:9, CAP §5.1")
	r.report("WARN", "H-S2", "newer protocol", "socket.go:1")
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
	if got := len(r.counts); got != 4 {
		t.Errorf("counts = %v, want one entry per status line", r.counts)
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
