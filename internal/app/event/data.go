package event

import (
	"encoding/json"
	"errors"
	"io/fs"
	"time"
)

// Pause is a pause fact's data, and the pause in force in the status: why, until when (epoch seconds, 0: until
// cleared), who paused and since when.
type Pause struct {
	Model  string `json:"model,omitempty"` // a model's pause (obor pause --model, T-765); "": the project's
	Why    string `json:"why,omitempty"`
	Until  int64  `json:"until,omitempty"`
	Caller string `json:"caller,omitempty"`
	Since  string `json:"since,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
}

// ConfigSet is a setup edit's fact data (T-753, T-760): its changes (each key with its value before and after), who
// made them and where (owner, web), their one commit on the main branch, and the call that recorded it with its reply.
// A record from before T-760 is one change, as key, old and new at the top: only Call and Reply are folded.
type ConfigSet struct {
	Changes []ConfigSetChange `json:"changes"`
	By      string            `json:"by"`
	Source  string            `json:"source,omitempty"`
	Commit  string            `json:"commit"`
	Call    string            `json:"call,omitempty"`
	Reply   string            `json:"reply"`
}

// ConfigSetChange is one change of a ConfigSet: the key and its value before and after (one line of JSON each, ""
// unset).
type ConfigSetChange struct {
	Key string `json:"key"`
	Old string `json:"old"`
	New string `json:"new"`
}

// Message is a line fact's data: the agent it goes to and the line typed, who sent it, the brief it ends, and the
// call that recorded it with its reply.
type Message struct {
	To     string `json:"to"`
	Line   string `json:"line,omitempty"`
	From   string `json:"from,omitempty"`
	Ends   string `json:"ends,omitempty"`
	Needs  int64  `json:"needs,omitempty"`  // the seq of the needs-you fact this line announces
	Task   string `json:"task,omitempty"`   // answered: the task whose question it answers; a watch's line: the task or comma list watched
	Watch  int64  `json:"watch,omitempty"`  // the seq of the watch fact this line ends (mergewatch.go)
	Review int64  `json:"review,omitempty"` // the seq of the review request this line ends (review.go)
	Stop   int64  `json:"stop,omitempty"`   // the seq of the role agent's stop this alarm line is for (stopwake.go)
	Drive  int    `json:"drive,omitempty"`  // the attempt of the drive Task whose come-back this line tells the lead (T-751)
	// Stopped is the job id (<task>/<attempt>/<step>) of the worker.stop whose outcome this line tells the lead (T-852).
	Stopped string `json:"stopped,omitempty"`
	// Driver: a line between a drive and the lead (T-761): message, the drive agent's to the lead (obor tell-lead);
	// answered, the lead's answer to the drive agent (no question ends; each answer is its own line)
	Driver bool   `json:"driver,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply,omitempty"`
	// Supersedes: briefed's, the seqs of the agent's undelivered brief lines this brief ends (T-580)
	Supersedes []int64 `json:"supersedes,omitempty"`
	// Fresh: briefed's (obor brief --fresh, T-794): its line is the first prompt of the agent's fresh start, the
	// running agent stopped first
	Fresh bool `json:"fresh,omitempty"`
	// Verify: a verify brief's (T-823): the fix tasks it verifies; VerifyOf, the seq of its verify's request (T-856:
	// the brief is the verify record's briefed step, none in a log from before T-856), and Reviewed, the head it handed
	// the reviewer (its worktree moved there first; obor review close's sha once the verify is done). The tasks count as
	// verified and covered, and the head as reviewed, once the reviewer finished this brief (its finished line, Ends;
	// T-857: never a stop's Ends), never when another brief replaced it first (reviewverify.go).
	Verify   []string `json:"verify,omitempty"`
	VerifyOf int64    `json:"verify_of,omitempty"`
	Reviewed string   `json:"reviewed,omitempty"`
	// VerifyFail: the seq of the verify request that failed before its brief (its git, its worktree's move), the lead
	// told (a line) unless the call that asked got the error itself (no line).
	VerifyFail int64 `json:"verify_fail,omitempty"`
	// FixDrop: a lead line's (T-850, reviewfix.go): the review's linked fix tasks that ended unmerged, the lead told
	// once; FixFail: the seq of the fix round whose verify failed, the lead told once.
	FixDrop []string `json:"fix_drop,omitempty"`
	FixFail int64    `json:"fix_fail,omitempty"`
	// Of is a lead line's back-reference (T-866, DD M1): what the line reports. A rule's line carries its did key
	// (rules.go): Kind the rule's name, Seq the record it is keyed by.
	Of Of `json:"of,omitzero"`
}

// Of names the record a lead line reports: a kind and its seq (T-866).
type Of struct {
	Kind string `json:"kind"`
	Seq  int64  `json:"seq"`
}

// MergedData is a merged fact's data.
type MergedData struct {
	Head    string `json:"head,omitempty"`
	Outside bool   `json:"outside,omitempty"` // a merge outside the app landed it (by hand in git)
}

// RecordTime is r's time stamp (zero when unreadable).
func RecordTime(r Record) time.Time {
	t, _ := time.Parse(time.RFC3339, r.TS)
	return t
}

// Merge is one merged fact of the merge loop's (a merge outside the app has no sha and is none): its task, sha and
// time (a log's "full", before T-781, is not read).
type Merge struct {
	Task, SHA string
	At        time.Time
}

// MergeOf is r as a Merge (false: no merged fact, or a merge outside the app).
func MergeOf(r Record) (Merge, bool) {
	if r.Kind != KindFact || r.Step != StepMerged {
		return Merge{}, false
	}
	var d MergedData
	if json.Unmarshal(r.Data, &d) != nil || d.Head == "" {
		return Merge{}, false
	}
	return Merge{Task: r.Task, SHA: d.Head, At: RecordTime(r)}, true
}

// ReadMerges is root's merges, read-only in seq order (obor flakes); none when serve never made its store.
func ReadMerges(root string) ([]Merge, error) {
	recs, err := ReadLog(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var ms []Merge
	for _, r := range recs {
		if m, ok := MergeOf(r); ok {
			ms = append(ms, m)
		}
	}
	return ms, err
}
