package app

import (
	"cmp"
	"encoding/json"
	"strings"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// Described is a record as the readers show it (T-893, DD-encapsulation E1): decoded here, with the types serve writes
// it with, so the view never reads a record's data by hand. Line is the event list's wording ("": a record the list
// does not word), Bad whether that line is bad news (T-688: a gate that is not green, a hold, an unreadable record);
// the other fields are the facts the pages read, each filled by the kinds that have it.
type Described struct {
	Line    string
	Bad     bool
	Outcome string   // a gate's outcome: green, red, conflict, fail
	Text    string   // the record's own line: a gate's summary, a message's text
	Lane    string   // a dispatch's, a drive's, a submission's or a needs-you's lane
	Agent   string   // a dispatch's or a submission's agent
	Report  string   // a submission's report
	SpecRev int      // a gate's spec rev (T-521)
	To      string   // a line's agent
	From    string   // a line's sender
	Level   string   // a dispatch's difficulty level
	Types   []string // a dispatch's model refs (--model or the level's)
	Type    string   // the model ref a dispatch chose (T-721)
	Kind    string   // the event's kind when serve names one (KindPromotion; "": the readers word it by its line)
}

// KindPromotion is Described.Kind of a promotion's events (T-977): started, GREEN, RED, promoted.
const KindPromotion = "promotion"

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
		if out.Outcome == "green" {
			d.Line = "test " + r.Task + " pass (" + out.Line + ")"
		} else {
			d.Line, d.Bad = "test "+r.Task+" fail ("+out.Line+")", true
		}
		return nil
	},
	"result " + wire.MethodAccept: func(r event.Record, d *Described) error {
		d.Line = "accepted " + r.Task
		return nil
	},
	"result " + wire.MethodSendback: func(r event.Record, d *Described) error {
		var v verdictResult
		if err := decode(r, &v); err != nil {
			return err
		}
		d.Line = "sendback " + r.Task + noteIn(v.Note)
		return nil
	},
	"fact " + event.StepDispatched: func(r event.Record, d *Described) error {
		var dd dispatchData
		if err := decode(r, &dd); err != nil {
			return err
		}
		d.Lane, d.Agent, d.Level, d.Types, d.Type = dd.Lane, dd.Agent, dd.Level, dd.Types, dd.Type
		if dd.Drive {
			d.Line = "drive " + r.Task + laneIn(dd.Lane)
		} else {
			d.Line = "dispatched " + r.Task + laneIn(dd.Lane)
		}
		return nil
	},
	"fact " + event.StepSubmitted: func(r event.Record, d *Described) error {
		var s submitData
		if err := decode(r, &s); err != nil {
			return err
		}
		d.Lane, d.Agent, d.Report = s.Lane, s.Agent, s.Report
		d.Line = "submitted " + r.Task
		return nil
	},
	"fact " + event.StepSendback: func(r event.Record, d *Described) error {
		var n noteData
		if err := decode(r, &n); err != nil {
			return err
		}
		d.Line = "sendback " + r.Task + noteIn(n.Note)
		return nil
	},
	"fact " + event.StepNeedsYou: func(r event.Record, d *Described) error {
		var f event.FactData
		if err := decode(r, &f); err != nil {
			return err
		}
		d.Lane, d.Agent = f.Lane, f.Agent
		if f.Cause == event.CauseHeld { // the hold's reason is "held: " + its note (verdict.go hold)
			d.Line, d.Bad = "held "+r.Task+noteIn(strings.TrimPrefix(f.Reason, "held: ")), true
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
		return nil
	},
	"fact " + event.StepReviewStarted: func(r event.Record, d *Described) error {
		var rd ReviewData
		if err := decode(r, &rd); err != nil {
			return err
		}
		d.Line = "review started " + rd.ID
		return nil
	},
	"fact " + event.StepReviewRm: func(r event.Record, d *Described) error {
		d.Line = "review request ended"
		return nil
	},
	"intent " + event.StepPromote: func(r event.Record, d *Described) error {
		var in promoteIn
		if err := decode(r, &in); err != nil {
			return err
		}
		d.Kind, d.Line = KindPromotion, "promotion started: full suite on "+short(in.SHA)
		if len(in.Merges) > 0 {
			d.Line += " (" + strings.Join(in.Merges, ", ") + ")"
		}
		return nil
	},
	"result " + event.StepPromote: func(r event.Record, d *Described) error {
		var out PromoteOut
		if err := decode(r, &out); err != nil {
			return err
		}
		if out.Verdict == "" { // the effect's error: a counted failure (fact failed), not a verdict
			return nil
		}
		d.Kind, d.Outcome, d.Text = KindPromotion, out.Verdict, out.Line
		d.Line, d.Bad = "promotion "+out.Verdict+" "+short(out.SHA)+": "+out.Line, out.Verdict != "GREEN"
		return nil
	},
	"fact " + event.StepPromoted: func(r event.Record, d *Described) error {
		var pd promotedData
		if err := decode(r, &pd); err != nil {
			return err
		}
		d.Kind, d.Line = KindPromotion, "promoted "+cmp.Or(pd.Branch, "the promoted branch")+" → "+short(pd.SHA)
		return nil
	},
	"fact " + event.StepMessage: func(r event.Record, d *Described) error {
		var m event.Message
		if err := decode(r, &m); err != nil {
			return err
		}
		d.To, d.From, d.Text = m.To, m.From, m.Line
		d.Line = "line to " + m.To + ": " + m.Line
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
		return Described{Line: r.Step + " " + r.Task + ": unreadable record " + err.Error(), Bad: true}
	}
	return d
}

// describedKey is r's key in worded: "<kind> <step>", a result's step without its .N, a verdict's its method (the
// step is <method>.<call id>).
func describedKey(r event.Record) string {
	if promoteSeq(r.Step, event.StepPromote) > 0 { // the promotion's promote.<seq>, intent and result (T-977)
		return r.Kind + " " + event.StepPromote
	}
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
