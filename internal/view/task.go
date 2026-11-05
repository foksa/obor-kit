package view

import (
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/spec"
	"obor/internal/store"
)

// Task is one task the view knows (T-883): dispatched, waiting, merging, needing you, on a lane's branch, or only a
// stored spec. Build makes one per task (tasks), in Project.Tasks by id; the sections (Lane, MergeItem, WaitItem,
// NeedsYou) name it by id and keep only their own fields, so a task's title, phase and lane are the same everywhere.
// Its JSON names are the ones a section embedding it may use (task_state: a lane's own state is "state").
type Task struct {
	Task    string `json:"task"`
	Title   string `json:"title,omitempty"`    // its spec's title
	SpecRev int    `json:"spec_rev,omitempty"` // the store's current rev of its spec (0: none stored)
	// Lane is serve's lane of it, else the lane whose branch is its own, else its needs-you's; Agent the agent there
	// (serve's in the lane, else the lane row's).
	Lane      string `json:"lane,omitempty"`
	Agent     string `json:"agent,omitempty"`
	TaskState string `json:"task_state,omitempty"` // serve's state of it ("": serve has no such task)
	Open      bool   `json:"open,omitempty"`       // serve still has it in its merge loop (not ended; T-896)
	Phase     string `json:"phase,omitempty"`      // serve's phase of it (wire.Phase*, T-788)
	// Step is the step its phase places it on (wire.PhaseStep; nil: needs-you, held, ended or no phase, which its
	// records place).
	Step    *int   `json:"step,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Running string `json:"running,omitempty"` // its step running now (its gate, its publish …)
	Effort  string `json:"effort,omitempty"`  // the effort its dispatch started its agent with (T-695)
	Job     *Job   `json:"job,omitempty"`     // its open job (nil: none)
}

// tasks is every task the view knows, by id, built once: the stored specs (rev and title), the lanes' branches (their
// lane and agent), serve's tasks (state, phase, attempt, running step, effort, job and lane) and the needs-you facts in
// force (their lane, when nothing else names one). A project's loop error and its lines are no task.
func tasks(ps wire.ProjectStatus, lanes []port.Lane, rows map[string]store.LaneRow,
	specs map[string]store.SpecRow) map[string]Task {
	out := map[string]Task{}
	get := func(id string) Task {
		t := out[id]
		t.Task = id
		return t
	}
	for id, s := range specs {
		t := get(id)
		t.SpecRev = s.Rev
		out[id] = t
	}
	agents := map[string]string{}
	for _, s := range lanes {
		agents[s.Name], _ = laneAgent(ps, rows[s.Name], s.Name)
		if s.Task != "" {
			t := get(s.Task)
			t.Lane = s.Name
			out[s.Task] = t
		}
	}
	for _, st := range ps.Tasks {
		t := get(st.Task)
		t.TaskState, t.Phase, t.Attempt, t.Running, t.Effort = st.State, st.Phase, st.Attempt, st.Running, st.Effort
		t.Open = st.Open
		if st.Lane != "" {
			t.Lane = st.Lane
		}
		if st.Job != "" {
			t.Job = &Job{ID: st.Job, Delivered: st.Delivered}
		}
		out[st.Task] = t
	}
	for _, f := range ps.Facts {
		if f.Step != event.StepNeedsYou || f.Task == event.LoopTask || f.Task == event.MsgTask {
			continue
		}
		t := get(f.Task)
		if t.Lane == "" {
			t.Lane = f.Fact().Lane
		}
		out[f.Task] = t
	}
	for id, t := range out {
		t.Title, t.Agent = spec.Title(specs[id].Text), agents[t.Lane]
		if step, ok := wire.PhaseStep(t.Phase); ok {
			t.Step = &step
		}
		out[id] = t
	}
	return out
}

// laneAgent is the agent in lane and its pane's status: serve's agent there, else the lane row's (no status).
func laneAgent(ps wire.ProjectStatus, row store.LaneRow, lane string) (string, string) {
	name, status := row.Agent, ""
	for _, a := range ps.Agents {
		if a.Lane == lane {
			name, status = a.Name, a.Status
		}
	}
	return name, status
}

// TaskOn is the task serve has on l (zero: none): l's branch's task when serve puts it on l.
func (p Project) TaskOn(l Lane) Task {
	if l.Task == "" {
		return Task{}
	}
	if t := p.Tasks[l.Task]; t.TaskState != "" && t.Lane == l.Name {
		return t
	}
	return Task{}
}

// TaskIDs is Project.Tasks' ids in order (sortTasks: T-9 before T-10).
func (p Project) TaskIDs() []string {
	ids := make([]string, 0, len(p.Tasks))
	for id := range p.Tasks {
		ids = append(ids, id)
	}
	sortTasks(ids, func(id string) string { return id })
	return ids
}
