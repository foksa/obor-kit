package main

// CC-L2's check. This file carries no build tag, so its tests run in `go test ./...`; the live tier that feeds it the
// records is behind the conformance tag.

import (
	"fmt"
	"os"
	"path/filepath"
)

// ccL2: SessionStart reports the id --session-id gave (obor's choice) and a transcript_path of its own — the
// realpath Claude resolved, under its config dir — and that path is the file of that id: obor records it and derives
// none. Claude creates the transcript at the first prompt (CAP §2.4), so its existence is checked after the turn
// never at SessionStart: with turn it PASSes when the file is there, else FAILs; without it the missing file
// is a SKIP.
func ccL2(r *run, sid string, hooks []record, turn bool, where string) {
	if len(hooks) == 0 {
		r.report("FAIL", "CC-L2", "no SessionStart hook ran in 20s (--settings hooks)", where)
		return
	}
	p := hooks[0].payload()
	fact := fmt.Sprintf("SessionStart: session_id %s, source %s, cwd %s, transcript_path %s", p.SessionID, p.Source, p.Cwd, p.TranscriptPath)
	switch {
	case p.SessionID != sid:
		r.report("FAIL", "CC-L2", fact+": not the --session-id given ("+sid+")", where)
		return
	case p.TranscriptPath == "":
		r.report("FAIL", "CC-L2", fact+": no transcript_path: obor would have none to record", where)
		return
	case filepath.Base(p.TranscriptPath) != sid+".jsonl":
		r.report("FAIL", "CC-L2", fact+": not the file of that id: the path obor records must be the session's own", where)
		return
	case !filepath.IsAbs(p.TranscriptPath):
		r.report("FAIL", "CC-L2", fact+": not an absolute path", where)
		return
	}
	r.report("PASS", "CC-L2", fact+": the id given and its own path, the one obor records", where)
	fi, statErr := os.Stat(p.TranscriptPath)
	switch {
	case statErr == nil && fi.Mode().IsRegular():
		r.report("PASS", "CC-L2", fact+": the transcript exists after the first turn (a restart can resume it)", where)
	case !turn:
		r.report("SKIP", "CC-L2", "transcript created at the first prompt", where)
	default:
		r.report("FAIL", "CC-L2", fact+": no transcript after the first turn: a restart could not resume it", where)
	}
}
