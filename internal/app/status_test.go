package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// RV-49 #4: a waiting task has no lane, so the drop its needs-you advertises names the task (obor drop <T-xx>, T-462),
// whichever dependency ended the wait.
func TestAnswerPathWaitingTaskNamesTheTask(t *testing.T) {
	for _, reason := range []string{
		"after: T-8 waits for T-1, which is dropped",
		"after: T-8 waits for T-1, which is rejected",
	} {
		raw, _ := json.Marshal(FactData{Merge: true, Cause: causeAfter, Reason: reason})
		r := Record{Seq: 1, Task: "T-8", Step: StepNeedsYou, Data: raw}
		path := answerPath(r, mtask{task: "T-8", state: stNeedsYou, left: stWaiting, needs: r})
		if strings.Contains(path, "<LANE>") {
			t.Errorf("%q: path %q names <LANE>, want the task", reason, path)
		}
		if !strings.Contains(path, `obor drop T-8 "<why>"`) {
			t.Errorf("%q: path %q, want obor drop T-8", reason, path)
		}
	}
}

// A stalled worker's needs-you keeps its lane in the drop command.
func TestAnswerPathLaneKeepsTheLane(t *testing.T) {
	raw, _ := json.Marshal(FactData{Lane: "O-02", Reason: "stalled"})
	path := answerPath(Record{Task: "T-8", Step: StepNeedsYou, Data: raw}, mtask{})
	if !strings.Contains(path, `obor drop O-02 "<why>"`) {
		t.Errorf("path %q, want obor drop O-02", path)
	}
}
