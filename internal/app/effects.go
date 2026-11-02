package app

import "strconv"

// JobID is the id of the job (task, attempt, step): <task>/<attempt>/<step> (docs/state-model.md "The log").
func JobID(k Key) string { return k.Task + "/" + strconv.Itoa(k.Attempt) + "/" + k.Step }
