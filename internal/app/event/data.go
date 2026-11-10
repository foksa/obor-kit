package event

import (
	"encoding/json"
	"time"

	"obor/internal/store"
)

// Pause is a pause fact's data, and the pause in force in the status: why, until when (epoch seconds, 0: until
// cleared), who paused and since when.
type Pause struct {
	Model  string `json:"model,omitempty"` // a model's pause (obor pause --model); "": the project's
	Why    string `json:"why,omitempty"`
	Until  int64  `json:"until,omitempty"`
	Caller string `json:"caller,omitempty"`
	Since  string `json:"since,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
}

// ConfigSet is a setup edit's fact data: its changes (each key with its value before and after), who
// made them and where (owner, web), their one commit on the main branch, and the call that recorded it with its reply.
// Only Call and Reply are folded.
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
	Task   string `json:"task,omitempty"`   // answered: the task whose question it answers; a verify's brief: the tasks it verifies
	Review int64  `json:"review,omitempty"` // the seq of the review request this line ends (review.go)
	Stop   int64  `json:"stop,omitempty"`   // the seq of the role agent's stop this alarm line is for (stopwake.go)
	Drive  int    `json:"drive,omitempty"`  // the attempt of the drive Task whose come-back this line tells the lead
	// Driver: a line between a drive and the lead: message, the drive agent's to the lead (obor tell-lead);
	// answered, the lead's answer to the drive agent (no question ends; each answer is its own line)
	Driver bool   `json:"driver,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply,omitempty"`
	// Supersedes: briefed's, the seqs of the agent's undelivered brief lines this brief ends
	Supersedes []int64 `json:"supersedes,omitempty"`
	// Fresh: briefed's (obor brief --fresh): its line is the first prompt of the agent's fresh start, the
	// running agent stopped first
	Fresh bool `json:"fresh,omitempty"`
	// Reviewed: a verify brief's (with Task, the tasks it verifies), the head it handed the reviewer (its
	// worktree moved there first; obor review close's sha once the verify is done). Its tasks count as covered, and the
	// head as reviewed, once the reviewer finished this brief (its finished line, Ends; never a stop's Ends),
	// never when another brief replaced it first (reviewverify.go).
	Reviewed string `json:"reviewed,omitempty"`
	// Of is a lead line's back-reference: what the line reports. A rule's line carries its did key
	// (rules.go): Kind the rule's name, Seq the record it is keyed by.
	Of Of `json:"of,omitzero"`
}

// Of names the record a lead line reports: a kind and its seq.
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
// time.
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

// ReadMerges is root's merges, read-only in seq order (obor flakes); none when serve never made its store. The store
// filters them: a reader does not decode the whole log for a handful of merges.
func ReadMerges(root string) ([]Merge, error) {
	db, err := store.OpenAt(root)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	recs, err := Facts(db, StepMerged)
	if err != nil {
		return nil, err
	}
	var ms []Merge
	for _, r := range recs {
		if m, ok := MergeOf(r); ok {
			ms = append(ms, m)
		}
	}
	return ms, nil
}

// SpecSet is a spec_set's fact data: the call id, and the reply it got (the reply carries the rev).
type SpecSet struct {
	Call  string `json:"call"`
	Reply string `json:"reply"`
}
