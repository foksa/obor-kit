package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/view"
)

// status is `obor status [--facts] [--json] [--short]`. Serve down is a line, never a non-zero exit: the lanes are read
// from the store alone. --facts adds the facts in force; --short prints only the serve line, the running line and
// needs-you (T-657). --all went with T-864 (T-522: a serve holds one project, this one).
func status(args []string) int {
	asJSON, facts, short := false, false, false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--facts":
			facts = true
		case "--short":
			short = true
		default:
			errf("%s", help.Usage("status"))
			return 2
		}
	}
	cfg, warn, rc := loadReader()
	if rc == 3 {
		errf("obor status: not an obor project")
		return 3
	}
	if rc != 0 {
		return rc
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ss, err := wire.Ask(ctx, wire.SocketPath(cfg.Root))
	db := openStore(cfg.Root)
	if db != nil {
		defer db.Close()
	}
	st := view.Build(view.Input{Status: ss, Err: err, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: time.Now(),
		Events: statusEvents, Notes: view.Notes(cfg.Root, warn),
		Background: view.ScanBackground(ctx, cfg.Root, ss, err, project.OSEnv)})
	st.Serve.PID, st.Serve.Launchd = serveProcess(ctx, cfg, st.Serve.Up)
	if !asJSON {
		switch {
		case short:
			writeStatusShort(out, st, time.Now())
		default:
			writeStatus(out, st, time.Now())
			if facts {
				writeFacts(out, "", st.Facts)
			}
		}
		return 0
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		errf("obor status: %v", err)
		return 1
	}
	return 0
}

// statusEvents is how many worded events obor status --json carries (the text prints none).
const statusEvents = 20

// openStore is the project's store at root, read-only (nil: none yet, or it cannot be opened: the view reads no
// specs and no events then).
func openStore(root string) *sql.DB {
	db, _ := store.OpenAt(root)
	return db
}

// serveProcess is the serve that answered (up: its pid) and the project's launchd agent (view.Serve.Launchd).
func serveProcess(ctx context.Context, cfg *config.Config, up bool) (pid int, launchd string) {
	if up {
		pid = servePID(cfg.Root)
	}
	s := serviceOf(cfg, project.OSEnv)
	if !s.Supported() {
		return pid, ""
	}
	switch lpid, loaded := s.PID(ctx); {
	case !loaded:
		return pid, "none"
	case pid != 0 && lpid == pid:
		return pid, "running"
	}
	return pid, "loaded"
}

// writeStatus is the text of st at now: serve's line, the running line (what serve does without an agent), one line per
// lane, serve's open tasks on no lane (not dropped), the agents on no lane, each needs-you with its answer, the queue
// and the full check.
func writeStatus(w io.Writer, st view.Project, now time.Time) {
	for _, s := range statusSections(st, now) {
		writeLines(w, s)
	}
}

// statusSections is writeStatus' text as sections, in its order: serve's line and the running line, the lanes (with
// serve's tasks on no lane and the agents on no lane), needs-you, the queue, then the full check.
func statusSections(st view.Project, now time.Time) [][]string {
	return [][]string{serveSection(st, now), laneSection(st, now), needsYouSection(st), queueSection(st, now),
		fullCheckSection(st, now)}
}

// serveSection is the project's notes, then serve's line with the running line and the pending line under it.
func serveSection(st view.Project, now time.Time) []string {
	var b strings.Builder
	for _, n := range st.Notes { // the project's own lines first (T-709): an unknown config key, a stopped project
		fmt.Fprintln(&b, n)
	}
	writeServe(&b, st.Serve, now)
	writeRunning(&b, st, now)
	writePending(&b, st, now)
	return textLines(b.String())
}

// laneSection is one line per lane, serve's open tasks on no lane (not dropped, not waiting: the queue lists those)
// and the agents on no lane (a role agent stopped, by serve or by the lead: "agent <name>: stopped").
func laneSection(st view.Project, now time.Time) []string {
	var b strings.Builder
	shown := map[string]bool{}
	for _, l := range st.Lanes {
		t := st.TaskOn(l)
		fmt.Fprintln(&b, laneLine(l, t))
		writeBackground(&b, l.Background)
		if t.TaskState != "" {
			shown[t.Task] = true
		}
	}
	for _, id := range st.TaskIDs() {
		t := st.Tasks[id]
		// serve's open tasks alone; a waiting one is the queue's
		if !shown[id] && t.Open && t.TaskState != "waiting" {
			state := "" // serve's state when it says more than the phase (T-788)
			if t.TaskState != t.Phase {
				state = t.TaskState + ", "
			}
			fmt.Fprintf(&b, "task %s (%s): %s (%sattempt %d)%s\n", id, app.Dash(t.Lane), t.Phase, state, t.Attempt,
				running(t.Running))
		}
	}
	for _, a := range st.Agents {
		if a.Lane == "" {
			fmt.Fprintf(&b, "agent %s: %s %s\n", app.Dash(a.Name), a.Status, app.Age(time.Duration(a.Age)*time.Second))
			writeBackground(&b, st.AgentBackground(a.Name))
		}
	}
	for _, r := range st.Roles {
		if r.Status == view.RoleStopped { // serve or the lead stopped it: its next use starts it fresh (T-672, T-677)
			fmt.Fprintf(&b, "agent %s: stopped\n", r.Agent)
		}
	}
	return textLines(b.String())
}

// writeBackground is one line per long-running process of an agent, under its line (T-714, T-727): `  bg:
// <description or command> · 15m` for a recorded background call, `  running <command> · 15m` for another.
func writeBackground(w io.Writer, bs []view.Background) {
	for _, bg := range bs {
		fmt.Fprintf(w, "  %s\n", oneLine(bg.Line(app.Age(time.Duration(bg.Age)*time.Second))))
	}
}

// laneLine is l's line: its lane, the model its dispatch runs ("-": none), branch, the state column (T-657), the task's
// age and id, then serve's state of t (the task serve has on l, Project.TaskOn), the effort its dispatch named (T-695),
// a dirty or warned worktree and the agent.
func laneLine(l view.Lane, t view.Task) string {
	ago := "-"
	if l.DoneAge != nil {
		ago = app.Age(time.Duration(*l.DoneAge) * time.Second)
	}
	model := l.Type
	if model == "" {
		model = "-"
	}
	line := fmt.Sprintf("%-6s  %-7s  %-14s  %-10s  %4s  (%s)", l.Name, model, cmp.Or(l.Branch, "-"),
		l.Column, ago, cmp.Or(l.Task, "-"))
	if t.TaskState != "" {
		line += fmt.Sprintf("  serve: %s (attempt %d)%s", t.TaskState, t.Attempt, running(t.Running))
	}
	if t.Effort != "" {
		line += "  effort: " + t.Effort
	}
	if l.Dirty && l.State != port.LaneDirtyFree {
		line += "  dirty"
	}
	if l.Reason != "" {
		line += "  reason: " + l.Reason
	}
	if l.Warning != "" {
		line += "  warning: " + l.Warning
	}
	if l.Agent != "" {
		line += "  agent: " + l.Agent
		if l.AgentStatus != "" {
			line += " (" + l.AgentStatus + ")"
		}
	}
	return line
}

// needsYouSection is one line per needs-you with the commands that answer it.
func needsYouSection(st view.Project) []string {
	var b strings.Builder
	writeNeedsYou(&b, st)
	return textLines(b.String())
}

// fullCheckSection is the last full check and its age, or that there is none yet; with a promotion (T-974) the line
// "promoted <sha7> (<n> merges since)" instead, and why a GREEN promotion waits.
func fullCheckSection(st view.Project, now time.Time) []string {
	if pr := st.Promote; pr != nil {
		line := fmt.Sprintf("promoted %s (%d merges since)", cmp.Or(pr.SHA[:min(7, len(pr.SHA))], "-"), pr.Merges)
		if pr.Wait != "" {
			line += "; promotion " + pr.Wait
		}
		return []string{line}
	}
	fc := st.FullCheck
	if fc == nil {
		return []string{"full check: none yet"}
	}
	return []string{fmt.Sprintf("full check: %s %s, %s ago (%s)", fc.Verdict, fc.SHA[:min(7, len(fc.SHA))],
		app.Age(max(now.Sub(time.Unix(fc.At, 0)), 0)), fc.Summary)}
}

// queueSection is what waits (T-664), one line per item, in the order of urgency: a merge open in serve's worktree
// (T-669: a gate or publish there is on the running line), the merge loop's tasks in its order with their stage and
// what each waits for, where serve's scheduler knows of one (T-673: it names no task as next), the dispatches waiting
// and on what (--after tasks, a lane of their level, a spec never dispatched), the review request (or "review due"),
// the lead's watches, the lines not typed yet and the briefs in progress, the pause and the next nightly check (or
// that it is off).
func queueSection(st view.Project, now time.Time) []string {
	var out []string
	q := st.Queue
	if i := st.Idle; i != nil { // the pipeline is empty (T-803): acknowledged, or still waiting for the lead's word
		if i.Acked {
			out = append(out, "idle: "+i.Why)
		} else {
			out = append(out, "idle, not acknowledged since "+time.Unix(i.Since, 0).Local().Format("Jan 2 15:04"))
		}
	}
	if s := q.Scratch; s != nil && s.Why != wire.ScratchGating && s.Why != wire.ScratchPublishing {
		out = append(out, "scratch "+s.Task+": "+s.Why)
	}
	for _, m := range q.Merge {
		out = append(out, mergeLine(m, st.Tasks[m.Task]))
	}
	for _, w := range q.Waiting {
		out = append(out, "queued "+w.Task+": "+waitLine(w, st.Tasks[w.Task], now))
	}
	if st.Review != "" {
		out = append(out, st.Review)
	}
	for _, w := range st.Watches {
		line := "watch " + w.Task + " since " + app.Age(now.Sub(time.Unix(w.Since, 0)))
		if w.By != "" {
			line += " (by " + w.By + ")"
		}
		if w.Note != "" {
			line += " — " + w.Note
		}
		out = append(out, line)
	}
	for _, o := range q.Outbox {
		age := app.Age(max(now.Sub(time.Unix(o.Since, 0)), 0))
		if o.Kind == view.OutLine {
			wait := ""
			if o.Held != "" { // why serve holds it (T-746; C7: "held" is the integrator's verdict alone)
				wait = "; undelivered: " + o.Held
			}
			out = append(out, fmt.Sprintf("undelivered to %s (from %s, %s%s): %s", o.To, app.Dash(o.From), age, wait,
				o.Line))
			continue
		}
		state := "not delivered"
		switch {
		case o.Worked:
			state = "delivered, worked on"
		case o.Delivered:
			state = "delivered"
		}
		out = append(out, fmt.Sprintf("brief to %s: %s, since %s", o.To, state, age))
	}
	if q.Pause != nil {
		out = append(out, strings.TrimSuffix(pauseLine(q.Pause), "\n"))
	}
	if n := q.Nightly; n != nil {
		out = append(out, fmt.Sprintf("nightly check at %s (in %s)", n.At, app.Age(time.Unix(n.Next, 0).Sub(now))))
	} else {
		out = append(out, "nightly: off (obor nightly on HH:MM)") // merge.nightly_at unset (T-781)
	}
	return out
}

// mergeLine is a merge-loop task's queue line (t: its task): its lane, its phase (T-788: the lane line's; serve's
// state when it says more), what it waits for and why (serve's scheduler, T-669; no task is named next, T-673), an
// integrator job out (its review stage's) and whether it was fetched.
func mergeLine(m view.MergeItem, t view.Task) string {
	line := "merge " + m.Task
	if t.Lane != "" {
		line += " (" + t.Lane + ")"
	}
	line += ": " + t.Phase
	if t.TaskState != t.Phase {
		line += " (" + t.TaskState + ")"
	}
	if m.WaitsOn != "" {
		line += ", waits for " + m.WaitsOn + " (" + m.Why + ")"
	}
	if j := t.Job; j != nil && m.Stage == wire.StageReview {
		fetched := "not fetched"
		if j.Delivered {
			fetched = "fetched"
		}
		line += ", job " + j.ID + " " + fetched
	}
	return line
}

// waitLine is a waiting dispatch's queue line after "queued <T-xx>: ": its --after tasks and its last refusal, the lane
// it waits for (its level and models; the reason when it is not a free lane: "opus paused: …", T-765), or its spec
// stored and never dispatched (t: its task, with the spec's rev and title).
func waitLine(w view.WaitItem, t view.Task, now time.Time) string {
	switch w.WaitsOn {
	case view.WaitsAfter:
		return "waiting (after " + strings.Join(w.After, ", ") + ")" + why(w.Why)
	case view.WaitsNever:
		line := fmt.Sprintf("spec r%d stored", t.SpecRev)
		if w.Since != 0 {
			line += " " + app.Age(max(now.Sub(time.Unix(w.Since, 0)), 0)) + " ago"
		}
		line += ", never dispatched"
		if t.Title != "" {
			line += ": " + t.Title
		}
		return line
	}
	if w.Level == "" {
		return "waiting for a lane" + why(w.Why)
	}
	level := fmt.Sprintf(" (%s: %s)", w.Level, strings.Join(w.Types, ", "))
	if w.Why == "" || w.Why == app.LaneWaitFree {
		return app.LaneWaitFree + level
	}
	return "waiting" + level + why(w.Why)
}

// pauseLine is the pause in force as the queue section words it.
func pauseLine(p *view.Pause) string {
	until := "cleared"
	if p.Until != nil {
		until = time.Unix(*p.Until, 0).Format("15:04")
	}
	return fmt.Sprintf("paused: %s, until %s (obor pause --clear ends it; no dispatch meanwhile)\n", p.Reason, until)
}

// oneLine is s on one line: control characters (newlines, tabs, escapes) become spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
}

// textLines is text split into its lines (a trailing newline ends the last, it does not add one).
func textLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// writeLines writes each line of s with its newline.
func writeLines(w io.Writer, s []string) {
	for _, l := range s {
		fmt.Fprintln(w, l)
	}
}

// writeStatusShort is `obor status --short` (T-657): serve's line, the running line (and the pending line under it,
// T-662) and each needs-you, nothing else (a status bar or a quick glance). It is the head of writeStatus' text, so
// both read the same.
func writeStatusShort(w io.Writer, st view.Project, now time.Time) {
	writeServe(w, st.Serve, now)
	writeRunning(w, st, now)
	writePending(w, st, now)
	writeNeedsYou(w, st)
}

// writeServe is serve's line at now: up (launchd, a pane, since when), down and why, and a broken event log under it.
func writeServe(w io.Writer, s view.Serve, now time.Time) {
	switch {
	case !s.Up && s.Launchd == "none":
		fmt.Fprintln(w, "serve: down (obor serve install)")
	case !s.Up:
		fmt.Fprintf(w, "serve: down (%s)\n", s.Error)
	default:
		line := "serve: up"
		switch {
		case s.PID != 0 && s.Launchd == "running":
			line = fmt.Sprintf("serve: launchd pid %d", s.PID)
		case s.PID != 0:
			line = fmt.Sprintf("serve: pane pid %d", s.PID)
		}
		if !s.SinceTime.IsZero() {
			line += fmt.Sprintf(" since %s (%s)", s.SinceTime.Local().Format("Jan 2 15:04"),
				app.Age(now.Sub(s.SinceTime)))
		}
		switch {
		case !s.Project:
			line += "; this project is not loaded (obor serve's output says why)"
		case !s.Merging:
			line += "; merge loop off (no integrator role)"
		}
		if !s.Herdr {
			line += "; herdr: not connected"
		} else if s.HerdrNote != "" {
			line += "; " + s.HerdrNote
		}
		fmt.Fprintln(w, line)
		if s.LogError != "" {
			fmt.Fprintf(w, "serve: event log broken: %s\n", s.LogError)
		}
	}
}

// writeRunning is the running line right under serve's (T-657): what serve does without an agent, each step with the
// command it runs and its elapsed time (`running: gate T-656 (make -s check, 2m14s) · after_merge T-655 (make build,
// 12s)`), or `running: nothing`. A pending self-restart reads "restarting serve" (T-972: it goes at the first idle
// pass, waiting however long); a step without a command (publish, a gate of git alone) its elapsed time only.
func writeRunning(w io.Writer, st view.Project, now time.Time) {
	if len(st.Running) == 0 {
		fmt.Fprintln(w, "running: nothing")
		return
	}
	items := make([]string, 0, len(st.Running))
	for _, r := range st.Running {
		item := runningName(r.Step)
		if r.Task != "" {
			item += " " + r.Task
		}
		switch {
		case r.Step == event.StepServeRestart: // "restarting serve" says what it is: no command, no elapsed time
		case r.Command != "":
			item += fmt.Sprintf(" (%s, %s)", r.Command, elapsed(now.Sub(time.Unix(r.Since, 0))))
		default:
			item += fmt.Sprintf(" (%s)", elapsed(now.Sub(time.Unix(r.Since, 0))))
		}
		items = append(items, item)
	}
	fmt.Fprintln(w, "running: "+strings.Join(items, " · "))
}

// writePending is the pending line under the running one (T-662): the merge loop's command steps whose intent has no
// result and whose effect does not run now (`pending: after_merge T-655 (failed 3m ago: <err>) · gate T-656 (queued)`),
// nothing when none waits. A step that failed names its last failure's age and reason (its first line, short: the
// needs-you or the log has the rest); one that never failed reads "queued": the next pass or a replay runs it.
func writePending(w io.Writer, st view.Project, now time.Time) {
	if len(st.Pending) == 0 {
		return
	}
	items := make([]string, 0, len(st.Pending))
	for _, p := range st.Pending {
		item := runningName(p.Step)
		if p.Task != "" {
			item += " " + p.Task
		}
		if p.FailedAt != 0 {
			item += fmt.Sprintf(" (failed %s ago: %s)", app.Age(now.Sub(time.Unix(p.FailedAt, 0))), shortErr(p.Error))
		} else {
			item += " (queued)"
		}
		items = append(items, item)
	}
	fmt.Fprintln(w, "pending: "+strings.Join(items, " · "))
}

// shortErr is a failed step's reason in the pending line: its first line, cut at pendingErrMax characters.
func shortErr(s string) string {
	s = project.FirstLine(s)
	if r := []rune(s); len(r) > pendingErrMax {
		s = string(r[:pendingErrMax]) + "…"
	}
	return s
}

// pendingErrMax is how much of a failed step's reason the pending line keeps.
const pendingErrMax = 80

// runningName is a running step's name in the line: the log's own step, but the nightly run (a day's key) and the
// self-restart, which say what they are.
func runningName(step string) string {
	switch step {
	case event.StepNightly:
		return "nightly check"
	case event.StepPromote:
		return "promotion"
	case event.StepServeRestart:
		return "restarting serve"
	}
	return step
}

// writeNeedsYou is one line per needs-you of st, its task's lane and the commands that answer it.
func writeNeedsYou(w io.Writer, st view.Project) {
	for _, n := range st.NeedsYou {
		line := "needs-you " + n.Task
		if l := st.Tasks[n.Task].Lane; l != "" {
			line += " (" + l + ")"
		}
		line += ": " + n.Reason
		if n.Answer != "" {
			line += " → " + n.Answer
		}
		fmt.Fprintln(w, line)
	}
}

// elapsed is a running step's age as the running line shows it: 12s, 2m14s, 1h02m, 3d04h.
func elapsed(d time.Duration) string {
	s := max(int64(d/time.Second), 0)
	switch {
	case s < 60:
		return strconv.FormatInt(s, 10) + "s"
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd%02dh", s/86400, s%86400/3600)
}

// orDash is *s, "-" for nil.
// why is a waiting task's last refused dispatch as the queue appends it ("": none).
func why(line string) string {
	if line == "" {
		return ""
	}
	return ": " + line
}

// running is a task's running step as its status line ends ("": none).
func running(step string) string {
	if step == "" {
		return ""
	}
	return ", running " + step
}

// writeFacts is one line per fact in force: its seq, time, task, step{reason} and lane.
func writeFacts(w io.Writer, indent string, recs []event.Record) {
	for _, f := range recs {
		fmt.Fprintf(w, "%sfact #%d %s %s %s (%s)\n", indent, f.Seq, f.TS, f.Task, f, f.Fact().Lane)
	}
}
