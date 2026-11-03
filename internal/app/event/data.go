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
	Why    string `json:"why,omitempty"`
	Until  int64  `json:"until,omitempty"`
	Caller string `json:"caller,omitempty"`
	Since  string `json:"since,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
}

// ConfigSet is a setup edit's fact data (T-753): the key, its value before and after (one line of JSON each, "" unset),
// who made it and where (owner, web), the commit on the main branch, and the call that recorded it with its reply.
type ConfigSet struct {
	Key    string `json:"key"`
	Old    string `json:"old"`
	New    string `json:"new"`
	By     string `json:"by"`
	Source string `json:"source,omitempty"`
	Commit string `json:"commit"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
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
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply,omitempty"`
	// Supersedes: briefed's, the seqs of the agent's undelivered brief lines this brief ends (T-580)
	Supersedes []int64 `json:"supersedes,omitempty"`
}

// MergedData is a merged fact's data.
type MergedData struct {
	Head    string `json:"head,omitempty"`
	Outside bool   `json:"outside,omitempty"` // a merge outside the app landed it (by hand in git)
	Full    bool   `json:"full,omitempty"`    // the gate's candidate ran the full check (Candidate.Full)
}

// RecordTime is r's time stamp (zero when unreadable).
func RecordTime(r Record) time.Time {
	t, _ := time.Parse(time.RFC3339, r.TS)
	return t
}

// Merge is one merged fact of the merge loop's (a merge outside the app has no sha and is none): its task, sha, time
// and whether its gate ran the full check (false in a log before T-525: partial).
type Merge struct {
	Task, SHA string
	At        time.Time
	Full      bool
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
	return Merge{Task: r.Task, SHA: d.Head, At: RecordTime(r), Full: d.Full}, true
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
