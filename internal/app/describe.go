package app

import (
	"encoding/json"
	"strings"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// Described is a record as the readers show it: decoded here, with the types serve writes
// it with, so the view never reads a record's data by hand. Line is the event list's wording ("": a record the list
// does not word), Bad whether that line is bad news (a gate that is not green, a hold, an unreadable record);
// the other fields are the facts the pages read, each filled by the kinds that have it.
type Described struct {
	Line    string
	Bad     bool
	Outcome string   // a gate's outcome: green, red, conflict, fail
	Text    string   // the record's own line: a gate's summary, a message's text
	Lane    string   // a dispatch's, a drive's, a submission's or a needs-you's lane
	Agent   string   // a dispatch's or a submission's agent
	Report  string   // a submission's report
	SpecRev int      // a gate's spec rev
	To      string   // a line's agent
	From    string   // a line's sender
	Level   string   // a dispatch's difficulty level
	Types   []string // a dispatch's model refs (--model or the level's)
	Type    string   // the model ref a dispatch chose
	Kind    string   // the event's kind: one of the Kind* constants, always set on a worded record
}

// The event kinds: Described.Kind of every worded record, the web's icons and filters (EventKind).
const (
	KindMerge    = "merge"
	KindGate     = "gate" // a gate or lane test result
	KindAccept   = "accept"
	KindSubmit   = "submit"
	KindDispatch = "dispatch" // dispatched, waiting
	KindDrive    = "drive"
	KindMessage  = "message" // a line to an agent, an outbox line
	KindServe    = "serve"   // serve's own restart, no record
	KindProblem  = "problem" // a needs-you, a sendback, a failed step
	KindOther    = "other"
)

// worder words one record of its kind into d from its data (an error: the data is not the kind's shape).
type worder func(r event.Record, d *Described) error

// worded is every record kind the readers word, by key (describedKey): Describe's one table.
var worded = map[string]worder{
	"result " + event.StepGate: func(r event.Record, d *Described) error {
		var out gateOut
		if err := decode(r, &out); err != nil {
			return err
		}
		d.Outcome, d.Text, d.SpecRev = out.Outcome, out.Line, out.SpecRev
		d.Kind = KindGate
		if out.Outcome == "green" {
			d.Line = "gate " + r.Task + " pass (" + out.Line + ")"
		} else {
			d.Line, d.Bad = "gate "+r.Task+" fail ("+out.Line+")", true
		}
		return nil
	},
	"result " + event.StepTest: func(r event.Record, d *Described) error {
		var out testOut
		if err := decode(r, &out); err != nil {
			return err
		}
		d.Outcome, d.Text = out.Outcome, out.Line
		d.Kind = KindGate
		if out.Outcome == "green" {
			d.Line = "test " + r.Task + " pass (" + out.Line + ")"
		} else {
			d.Line, d.Bad = "test "+r.Task+" fail ("+out.Line+")", true
		}
		return nil
	},
	"result " + wire.MethodAccept: func(r event.Record, d *Described) error {
		d.Line, d.Kind = "accepted "+r.Task, KindAccept
		return nil
	},
	"result " + wire.MethodSendback: func(r event.Record, d *Described) error {
		var v verdictResult
		if err := decode(r, &v); err != nil {
			return err
		}
		d.Line, d.Kind = "sendback "+r.Task+noteIn(v.Note), KindProblem
		return nil
	},
	"fact " + event.StepDispatched: func(r event.Record, d *Described) error {
		var dd dispatchData
		if err := decode(r, &dd); err != nil {
			return err
		}
		d.Lane, d.Agent, d.Level, d.Types, d.Type = dd.Lane, dd.Agent, dd.Level, dd.Types, dd.Type
		if dd.Drive {
			d.Line, d.Kind = "drive "+r.Task+laneIn(dd.Lane), KindDrive
		} else {
			d.Line, d.Kind = "dispatched "+r.Task+laneIn(dd.Lane), KindDispatch
		}
		return nil
	},
	"fact " + event.StepSubmitted: func(r event.Record, d *Described) error {
		var s submitData
		if err := decode(r, &s); err != nil {
			return err
		}
		d.Lane, d.Agent, d.Report = s.Lane, s.Agent, s.Report
		d.Line, d.Kind = "submitted "+r.Task, KindSubmit
		return nil
	},
	"fact " + event.StepSendback: func(r event.Record, d *Described) error {
		var n noteData
		if err := decode(r, &n); err != nil {
			return err
		}
		d.Line, d.Kind = "sendback "+r.Task+noteIn(n.Note), KindProblem
		return nil
	},
	"fact " + event.StepNeedsYou: func(r event.Record, d *Described) error {
		var f event.FactData
		if err := decode(r, &f); err != nil {
			return err
		}
		d.Lane, d.Agent = f.Lane, f.Agent
		if f.Cause == event.CauseHeld { // the hold's reason is "held: " + its note (verdict.go hold)
			d.Line, d.Bad, d.Kind = "held "+r.Task+noteIn(strings.TrimPrefix(f.Reason, "held: ")), true, KindProblem
		}
		return nil
	},
	"fact " + event.StepMerged: func(r event.Record, d *Described) error {
		var m event.MergedData
		if err := decode(r, &m); err != nil {
			return err
		}
		if m.Head == "" {
			d.Line = "merged " + r.Task + " (outside obor serve)"
		} else {
			d.Line = "merged " + r.Task + " " + m.Head[:min(7, len(m.Head))]
		}
		d.Kind = KindMerge
		return nil
	},
	"fact " + event.StepReviewRequest: func(r event.Record, d *Described) error {
		var rd ReviewData
		if err := decode(r, &rd); err != nil {
			return err
		}
		d.Line = "review requested"
		if len(rd.After) > 0 {
			d.Line += " (after " + strings.Join(rd.After, ",") + ")"
		}
		d.Kind = KindOther
		return nil
	},
	"fact " + event.StepReviewStarted: func(r event.Record, d *Described) error {
		var rd ReviewData
		if err := decode(r, &rd); err != nil {
			return err
		}
		d.Line, d.Kind = "review started "+rd.ID, KindOther
		return nil
	},
	"fact " + event.StepReviewRm: func(r event.Record, d *Described) error {
		d.Line, d.Kind = "review request ended", KindOther
		return nil
	},
	"fact " + event.StepVersion: func(r event.Record, d *Described) error { // a tool's version as read
		var v versionData
		if err := decode(r, &v); err != nil {
			return err
		}
		d.Line, d.Kind, d.Text = v.What+" "+v.Version, KindOther, v.Version
		if v.Was != "" {
			d.Line += " (was " + v.Was + ")"
		}
		return nil
	},
	"fact " + event.StepMessage: func(r event.Record, d *Described) error {
		var m event.Message
		if err := decode(r, &m); err != nil {
			return err
		}
		d.To, d.From, d.Text = m.To, m.From, m.Line
		d.Line, d.Kind = "line to "+m.To+": "+m.Line, KindMessage
		return nil
	},
}

// Describe is r as the readers show it: the event list's line and the facts the task and agent pages read. A record
// of a kind no reader words is zero; one whose data is not its kind's shape is shown as such (bad), never dropped.
func Describe(r event.Record) Described {
	var d Described
	w, ok := worded[describedKey(r)]
	if !ok {
		return d
	}
	if err := w(r, &d); err != nil {
		return Described{Line: r.Step + " " + r.Task + ": unreadable record " + err.Error(), Bad: true, Kind: KindProblem}
	}
	return d
}

// describedKey is r's key in worded: "<kind> <step>", a result's step without its .N, a verdict's its method (the
// step is <method>.<call id>).
func describedKey(r event.Record) string {
	if r.Kind != event.KindResult {
		return r.Kind + " " + r.Step
	}
	if base, _ := event.StepN(r.Step); base == event.StepGate || base == event.StepTest {
		return r.Kind + " " + base
	}
	method, _, _ := strings.Cut(r.Step, ".")
	return r.Kind + " " + method
}

// decode is r's data into v (no data: v stays zero).
func decode(r event.Record, v any) error {
	if len(r.Data) == 0 {
		return nil
	}
	return json.Unmarshal(r.Data, v)
}

// laneIn is " (O-01)" for a dispatch's lane, "" without one.
func laneIn(lane string) string {
	if lane == "" {
		return ""
	}
	return " (" + lane + ")"
}

// noteIn is ": note" for a sendback's or hold's why, "" without one.
func noteIn(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}
