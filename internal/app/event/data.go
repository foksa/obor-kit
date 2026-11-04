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
	// Verify: a verify brief's (obor review verify, T-823): the fix tasks its coverage names, counted by ReviewCovered
	// once its brief was delivered (one append with the brief, so a refused or superseded one covers nothing)
	Verify []string `json:"verify,omitempty"`
	// Idle is the seq of the idle fact this line announces (T-803, messages.go): the first line, or — with IdlePing —
	// the 30-minute ping's.
	Idle     int64 `json:"idle,omitempty"`
	IdlePing bool  `json:"idle_ping,omitempty"`
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
