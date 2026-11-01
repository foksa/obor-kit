package main

// The monitor's look (T-668): the view as sections — a one-line header, NEEDS YOU (absent when empty), LANES, ROLES,
// QUEUE, EVENTS — each a bold title with a dim rule, a colored state dot per lane and role, dim ages, aligned columns.
// Plain ANSI, one palette (palette below), glyphs of single display width only (box drawing, ●, braille). Pure: text
// in, lines out; monitor.go draws them.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"obor/internal/app"
	"obor/internal/view"
)

// The escapes the monitor writes: colors in palette, the rest by the screen (monitor.go).
const (
	escReset = "\033[0m"
	dot      = "●"
	rule     = "─"
)

// spinner is the braille frames of the header's running mark.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// palette is the monitor's colors in one place: on false (NO_COLOR, a pipe) paints nothing, so a plain frame has no
// escape in it. A painted text is flat (never nested): it ends with a reset.
type palette struct{ on bool }

func (p palette) paint(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + escReset
}

func (p palette) bold(s string) string   { return p.paint("1", s) }
func (p palette) dim(s string) string    { return p.paint("2", s) }
func (p palette) red(s string) string    { return p.paint("31", s) }
func (p palette) green(s string) string  { return p.paint("32", s) }
func (p palette) yellow(s string) string { return p.paint("33", s) }
func (p palette) alert(s string) string  { return p.paint("1;7;31", s) } // the NEEDS YOU title: bold, inverse, red

// state paints s in the color of a lane's state column or an agent's status: green working, yellow gating, merging,
// reviewing, waiting, red held or failed, dim free and what is not known.
func (p palette) state(col, s string) string {
	switch col {
	case "working":
		return p.green(s)
	case "gating", "merging", "reviewing", "gate pending", "publish pending", "waiting", "blocked", "done":
		return p.yellow(s)
	case "held", "failed", "missing":
		return p.red(s)
	}
	return p.dim(s)
}

// renderOpts is what the look depends on besides the view.
type renderOpts struct {
	name       string // the project's name (the header)
	now        time.Time
	restarting bool // serve stopped answering a moment ago
	color      bool
	cols       int            // the title rules' width
	loc        *time.Location // the events' clock (nil: local)
}

// fitFrame is st as the rows a terminal of rows by cols shows: the sections fitted to the height (the later ones are
// dropped first), every row cut to the width less one column — a row that fills the last column leaves the cursor
// pending a wrap, and the clear-to-end-of-line after it would erase that column's glyph.
func fitFrame(st view.Project, o renderOpts, rows, cols int) []string {
	o.cols = max(cols-1, 1)
	lines := fitSections(renderSections(st, o), rows)
	for i := range lines {
		lines[i] = clip(lines[i], o.cols)
	}
	return lines
}

// zone is the events' time zone.
func (o renderOpts) zone() *time.Location {
	if o.loc == nil {
		return time.Local
	}
	return o.loc
}

// renderSections is st as sections, top to bottom (each: its lines, the first one its title, the header alone).
func renderSections(st view.Project, o renderOpts) [][]string {
	p := palette{o.color}
	secs := [][]string{{headerLine(st, o, p)}}
	if s := needsSection(st, o, p); s != nil {
		secs = append(secs, s)
	}
	return append(secs, laneRows(st, o, p), roleRows(st, o, p), queueRows(st, o, p), eventRows(st, o, p))
}

// headerLine: project · serve state · merge loop · herdr, then the steps running without an agent behind a spinner.
func headerLine(st view.Project, o renderOpts, p palette) string {
	s := st.Serve
	parts := []string{p.bold(o.name)}
	switch {
	case o.restarting:
		parts = append(parts, p.yellow("serve restarting…"))
	case !s.Up:
		parts = append(parts, p.red("serve down"))
	default:
		up := "serve up"
		if s.PID != 0 && s.Launchd == "running" {
			up = fmt.Sprintf("serve launchd %d", s.PID)
		} else if s.PID != 0 {
			up = fmt.Sprintf("serve pane %d", s.PID)
		}
		if !s.SinceTime.IsZero() {
			up += " " + app.Age(o.now.Sub(s.SinceTime))
		}
		parts = append(parts, p.green(up))
		switch {
		case !s.Project:
			parts = append(parts, p.red("project not loaded"))
		case s.Merging:
			parts = append(parts, "merging")
		default:
			parts = append(parts, p.yellow("merge loop off"))
		}
		if s.Herdr {
			parts = append(parts, p.dim("herdr ok"))
		} else {
			parts = append(parts, p.red("herdr off"))
		}
		if s.LogError != "" {
			parts = append(parts, p.red("event log broken"))
		}
	}
	line := strings.Join(parts, p.dim(" · "))
	if len(st.Running) > 0 {
		items := make([]string, 0, len(st.Running))
		for _, r := range st.Running {
			item := runningName(r.Step)
			if r.Task != "" {
				item += " " + r.Task
			}
			if r.Step != app.StepServeRestart {
				item += " " + p.dim(elapsed(o.now.Sub(time.Unix(r.Since, 0))))
			}
			items = append(items, item)
		}
		line += "   " + p.green(spinner[int(o.now.UnixMilli()/100%int64(len(spinner)))]) + " " +
			strings.Join(items, p.dim(" · "))
	}
	return line
}

// title is a section's first line: its name in bold (or by paint) and a dim rule to the right edge.
func title(name string, o renderOpts, p palette, paint func(string) string) string {
	n := max(o.cols-textWidth(name)-1, 0)
	if paint == nil {
		paint = p.bold
	}
	return paint(name) + " " + p.dim(strings.Repeat(rule, n))
}

// needsSection is NEEDS YOU, highlighted: nil when nothing needs the owner. Each item is its task, lane, age and
// reason in bold, the commands that answer it under it.
func needsSection(st view.Project, o renderOpts, p palette) []string {
	if len(st.NeedsYou) == 0 {
		return nil
	}
	out := []string{title("NEEDS YOU", o, p, p.alert)}
	for _, n := range st.NeedsYou {
		head := n.Task
		if n.Lane != "" {
			head += " (" + n.Lane + ")"
		}
		age := ""
		if n.Since != 0 {
			age = " " + p.dim(app.Age(max(o.now.Sub(time.Unix(n.Since, 0)), 0)))
		}
		out = append(out, "  "+p.red(dot)+" "+p.bold(head)+age+"  "+oneLine(n.Reason))
		if n.Answer != "" {
			out = append(out, "    "+p.dim("→ "+oneLine(n.Answer)))
		}
	}
	return out
}

// laneRows is LANES: one row per lane, columns aligned — state dot and column, lane, task, time in that state, agent
// and its status, the spec's rev and the open job.
func laneRows(st view.Project, o renderOpts, p palette) []string {
	out := []string{title("LANES", o, p, nil)}
	if len(st.Lanes) == 0 {
		return append(out, "  "+p.dim("no lanes"))
	}
	lw, cw, tw, sw, aw := 0, 0, 0, 0, 0
	type row struct{ lane, col, task, since, agent, rev, job string }
	rows := make([]row, 0, len(st.Lanes))
	for _, l := range st.Lanes {
		r := row{lane: l.Lane, col: l.Column, task: "-", since: "", agent: "-", rev: "", job: ""}
		if l.Task != nil {
			r.task = *l.Task
		}
		if l.Since != nil {
			r.since = app.Age(max(o.now.Sub(time.Unix(*l.Since, 0)), 0))
		}
		if l.Agent != "" {
			r.agent = strings.TrimSpace(l.Agent + " " + l.AgentStatus)
		}
		if l.SpecRev > 0 {
			r.rev = fmt.Sprintf("r%d", l.SpecRev)
		}
		if j := l.Job; j != nil {
			r.job = jobStep(j.ID) + " (" + map[bool]string{true: "fetched", false: "not fetched"}[j.Delivered] + ")"
		}
		rows = append(rows, r)
		lw, cw, tw = max(lw, textWidth(r.lane)), max(cw, textWidth(r.col)), max(tw, textWidth(r.task))
		sw, aw = max(sw, textWidth(r.since)), max(aw, textWidth(r.agent))
	}
	for i, r := range rows {
		l := st.Lanes[i]
		line := "  " + p.state(l.Column, dot) + " " + padRight(r.lane, lw) + "  " +
			p.state(l.Column, padRight(r.col, cw)) + "  " + padRight(r.task, tw) + "  " +
			p.dim(padLeft(r.since, sw)) + "  " + p.dim(padRight(r.agent, aw))
		if r.rev != "" || r.job != "" {
			line += "  " + p.dim(padRight(r.rev, 3)) + " " + p.dim(r.job)
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// jobStep is a job id's step: T-659/1/work → work.
func jobStep(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// roleRows is ROLES: one row per role agent on no lane — dot, name, status and for how long, the brief it is on.
func roleRows(st view.Project, o renderOpts, p palette) []string {
	if len(st.Roles) == 0 {
		return nil
	}
	out := []string{title("ROLES", o, p, nil)}
	nw, sw := 0, 0
	for _, r := range st.Roles {
		nw, sw = max(nw, textWidth(r.Agent)), max(sw, textWidth(roleStatus(r)))
	}
	for _, r := range st.Roles {
		status := roleStatus(r)
		line := "  " + p.state(r.Status, dot) + " " + padRight(r.Agent, nw) + "  " + p.state(r.Status, padRight(status, sw))
		if r.Status != "" && r.Status != view.RoleStopped {
			line += "  " + p.dim(app.Age(time.Duration(r.Age)*time.Second))
		}
		if b := r.Brief; b != nil {
			line += "  " + p.dim("briefed "+app.Age(max(o.now.Sub(time.Unix(b.Since, 0)), 0))+" ago")
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// roleStatus is a role's pane status, "no pane" when serve sees none ("stopped" when serve stopped it, T-672).
func roleStatus(r view.Role) string {
	if r.Status == "" {
		return "no pane"
	}
	return r.Status
}

// queueRows is QUEUE: what waits (queueSection's lines, each behind its dim kind word) and the steps serve could not
// run (pending, red when one failed).
func queueRows(st view.Project, o renderOpts, p palette) []string {
	out := []string{title("QUEUE", o, p, nil)}
	var pend strings.Builder
	writePending(&pend, st, o.now)
	n := len(out)
	for _, l := range textLines(pend.String()) {
		if strings.Contains(l, "failed") {
			out = append(out, "  "+p.red(oneLine(l)))
		} else {
			out = append(out, "  "+p.yellow(oneLine(l)))
		}
	}
	for _, l := range queueSection(st, o.now) {
		l = oneLine(l)
		kind, rest, _ := strings.Cut(l, " ")
		if kind == "paused:" {
			out = append(out, "  "+p.yellow(l))
			continue
		}
		out = append(out, "  "+p.dim(kind)+" "+rest)
	}
	if len(out) == n {
		out = append(out, "  "+p.dim("nothing waiting"))
	}
	return out
}

// eventRows is EVENTS: the recent events, newest first, the local time dim, a failure or a hold in red.
func eventRows(st view.Project, o renderOpts, p palette) []string {
	out := []string{title("EVENTS", o, p, nil)}
	for _, e := range st.Events {
		line := oneLine(e.Line)
		if strings.Contains(line, " fail") || strings.HasPrefix(line, "held ") {
			line = p.red(line)
		}
		out = append(out, "  "+p.dim(e.At.In(o.zone()).Format("15:04:05"))+"  "+line)
	}
	if len(out) == 1 {
		out = append(out, "  "+p.dim("no events yet"))
	}
	return out
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

// runeWidth is a rune's display columns: 0 for combining and format characters, 2 for the East Asian wide blocks and
// emoji, else 1. The monitor draws only single-width glyphs; this keeps text from the events honest.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE6F, r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6, r >= 0x1F300 && r <= 0x1FAFF, r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// textWidth is the display width of s with no escapes.
func textWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// visibleWidth is the display width of line, its escape sequences not counted.
func visibleWidth(line string) int {
	n := 0
	for i := 0; i < len(line); {
		if k := escLen(line[i:]); k > 0 {
			i += k
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		n += runeWidth(r)
		i += size
	}
	return n
}

// escLen is the length of the CSI escape sequence at the start of s (0: none).
func escLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
	}
	return len(s)
}

// clip is line cut to width display columns with an ellipsis at the cut — a line longer than the terminal is cut, never
// wrapped. Escapes are not counted; a cut inside a painted text closes it.
func clip(line string, width int) string {
	if width <= 0 || visibleWidth(line) <= width {
		return line
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used, open := 0, false
	for i := 0; i < len(line); {
		if k := escLen(line[i:]); k > 0 {
			b.WriteString(line[i : i+k])
			open = line[i:i+k] != escReset
			i += k
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if used+runeWidth(r) > width-1 {
			break
		}
		used += runeWidth(r)
		b.WriteString(line[i : i+size])
		i += size
	}
	if open {
		b.WriteString(escReset)
	}
	b.WriteString("…")
	return b.String()
}

// padRight is s (no escapes) padded with spaces to width columns.
func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-textWidth(s), 0))
}

// padLeft is s (no escapes) right-aligned in width columns.
func padLeft(s string, width int) string {
	return strings.Repeat(" ", max(width-textWidth(s), 0)) + s
}

// projectName is the name the header shows: the project's directory.
func projectName(root string) string { return filepath.Base(root) }
