package app

import (
	"obor/internal/app/event"
	"strconv"
)

// JobID is the id of the job (task, attempt, step): <task>/<attempt>/<step> (docs/state-model.md "The log").
func JobID(k event.Key) string { return k.Task + "/" + strconv.Itoa(k.Attempt) + "/" + k.Step }
