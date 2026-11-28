//go:build conformance

// The pi live tier (docs/research/pi-probes.md P1–P9): one throwaway start in the lane, through the kind's own start
// and the repository's own obor (realKit), so the extension's registration, hooks and guard are the shipped ones. The
// start's one tiny prompt is on the cheapest Anthropic model: it calls obor's MCP job_next, runs a git commit the
// worker guard denies, and ends. The rows: the extension's session_start reached obor serve (PI-L1), the session lists
// mcp__obor__job_next and its reply (PI-L2), the guard's line is the tool's result (PI-L3), the Stop hook's block is
// the turn's continuation (PI-L4), /quit exits 0 (PI-L5), and the user's ~/.pi is unchanged beyond the session dir the
// start names (PI-L6). With TURN=1, a line posted to the session's inbox by obor's poster starts a turn (PI-L7).
// The tier runs when pi's own auth check says it can authenticate the model from the environment the pane will get:
// the agent directory's auth.json, the provider's environment key, or its OAuth or auth token; the variables the pane
// needs are carried into the throwaway session.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/wire"
	"obor/internal/inbox"
	"obor/internal/project"
	"obor/internal/store"
)

// piLiveModel is the cheapest Anthropic model the tier runs (pi --list-models; pi-probes P1 ran claude-haiku-4-5).
const piLiveModel = "anthropic/claude-haiku-4-5"

// piLivePrompt is the start's one prompt: tagged as obor's prompts are (an untagged one gets the wrong-tab question),
// one obor MCP call, and one command the worker guard denies.
const piLivePrompt = "[obor conf] Call the obor job_next tool once, then run the bash command: git commit -m conf. Then reply DONE."

// piLiveInbox is the line the tier posts to the session's inbox (TURN=1), tagged as obor's lines are, and the marker
// its turn's reply must carry.
const (
	piLiveInbox      = "[obor conf] Reply with exactly INBOXOK."
	piLiveInboxReply = "INBOXOK"
)

// piLiveStop is the Stop hook's reason for the lane's task (guard/stop.go: a worker's no task_done yet), and
// piLiveDeny the worker guard's line for a git commit.
const (
	piLiveStop = "no task_done for T-1 yet"
	piLiveDeny = "workers don't run git commit"
)

func piLive(l *lab, turn bool) {
	where := at("pi/obor.js", "export function create") + ", " + at("kinds/pi/kind/kind.go", "func start(") + ", pi-probes P1–P9"
	home, err := os.UserHomeDir()
	if err != nil {
		skipAll("pi", "no home dir: "+err.Error())
		return
	}
	piHome := filepath.Join(home, ".pi")
	auth, err := piAuthOf(piAgentDir(home, os.Getenv), piLiveModel, cleanEnv(), os.Getenv, piAuthCheckRun)
	if err != nil {
		skipAll("pi", err.Error())
		return
	}
	real, err := l.realKit()
	if err != nil {
		report("FAIL", "PI-L1", "the repository's own obor is not built: "+err.Error(), where)
		return
	}
	if err := l.startStubServe(); err != nil {
		report("FAIL", "PI-L1", err.Error(), where)
		return
	}
	sessions := filepath.Join(l.root, "pi-sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		report("FAIL", "PI-L1", err.Error(), where)
		return
	}
	// The lane's row: a task that awaits its task_done, so the Stop hook blocks once and the block is the transcript's
	// evidence that it ran (guard/stop.go reads it read-only; serve is not running here).
	var rowErr error
	db, err := store.OpenRW(store.File(l.main))
	if err != nil {
		rowErr = fmt.Errorf("the lane's row: %v", err)
	} else {
		if err := store.WriteLanes(db, []store.LaneRow{{Lane: "L-01", Task: "T-1", Branch: "task/T-1"}}); err != nil {
			rowErr = fmt.Errorf("the lane's row: %v", err)
		}
		db.Close()
	}
	k := l.kindOf("pi")
	start, err := agents.StartOf(k, agents.Start{Kit: real, Prompt: piLivePrompt,
		Args: []string{"--offline", "--session-dir", sessions, "--model", piLiveModel, "--thinking", "low"}})
	if err != nil {
		report("FAIL", "PI-L1", "the start: "+err.Error(), where)
		return
	}
	pane, err := l.workspace("conf", l.lane)
	if err != nil {
		report("FAIL", "PI-L1", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane, "OBOR_CONF_PANE_ONLY": "1"}
	if st, err := l.shell(pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "PI-L1", fmt.Sprintf("the env line failed (status %d, %v)", st, err), at("internal/launch/launch.go", "func envLine"))
		return
	}
	before := piTree(piHome)
	t0 := time.Now()
	s := l.start("conf-pi", "pi", pane, start.Args, 30*time.Second)
	ready := l.readyAfter(s, t0, "idle", "working")
	reportStart("pi", s, ready)
	if l.atShell(pane) {
		report("FAIL", "PI-L1", "pi did not start: "+answer(s)+"; "+tail(l.screen(pane), 6), where)
		for _, id := range []string{"PI-L2", "PI-L3", "PI-L4", "PI-L5", "PI-L6", "PI-L7"} {
			report("SKIP", id, "pi did not start", where)
		}
		return
	}

	// PI-L1: the extension's session_start hook reached obor serve with the session's id.
	sid := ""
	if poll(60*time.Second, func() bool {
		if ids := l.app.recorded(); len(ids) > 0 {
			sid = ids[len(ids)-1]
			return true
		}
		return false
	}) {
		report("PASS", "PI-L1", "the extension loaded: its session_start hook reported session "+sid+" to obor serve (the start ran "+piLiveModel+
			", auth resolved from "+auth.How+")", where)
	} else {
		report("FAIL", "PI-L1", "the extension's session_start hook reached obor serve in 60s: none ("+at("pi/obor.js", `"session_start"`)+")", where)
	}
	// PI-L2..PI-L4: one turn's results, read from the session file under the dir the start names.
	text, _ := waitTranscript(180*time.Second, func() string { return piSessions(sessions) }, mcpJobHead, mcpMarker, piLiveDeny, piLiveStop)
	mcp := strings.Contains(text, mcpJobHead) && strings.Contains(text, mcpMarker) &&
		strings.Contains(text, "mcp__obor__job_next") && slices.Contains(l.app.asked(), wire.MethodJobNext)
	if mcp {
		report("PASS", "PI-L2", "obor's MCP tools are listed: the session calls mcp__obor__job_next and its reply reached it"+
			" (the real `obor mcp` was asked job_next)", where)
	} else {
		report("FAIL", "PI-L2", "job_next's reply did not reach the session (obor serve was asked "+
			orNone(strings.Join(l.app.asked(), " "))+"); transcript end: "+tail(text, 3), where)
	}
	if strings.Contains(text, piLiveDeny) {
		report("PASS", "PI-L3", "the guard blocked git commit with obor's reason: "+piLiveDeny, where)
	} else {
		report("FAIL", "PI-L3", "the session does not carry obor's guard reason "+fmt.Sprintf("%q", piLiveDeny)+"; transcript end: "+tail(text, 3), where)
	}
	if strings.Contains(text, piLiveStop) {
		report("PASS", "PI-L4", "the Stop hook fired at the turn's end: its block reason ("+piLiveStop+") is the session's continuation", where)
	} else if rowErr != nil {
		report("FAIL", "PI-L4", rowErr.Error()+"; the session carries no Stop block", where)
	} else {
		report("FAIL", "PI-L4", "the Stop hook's block reason did not reach the session; transcript end: "+tail(text, 3), where)
	}

	piInbox(l, pane, sessions, sid, turn)
	poll(120*time.Second, func() bool { a, err := l.agent(pane); return err == nil && a.Status == "idle" })
	st := k.Stop()
	if code, err := l.quit(pane, st); err != nil || code != 0 {
		report("FAIL", "PI-L5", fmt.Sprintf("%s: exit status %d (%v)", recipeFact(st), code, orOK(err)), where)
		l.stopAgent(pane)
	} else {
		report("PASS", "PI-L5", recipeFact(st)+" exits with status 0", where)
	}
	// PI-L6: pi wrote nothing under the user's ~/.pi; its sessions are the dir the start named.
	if changed := piTreeChanges(before, piTree(piHome)); len(changed) > 0 {
		more := ""
		if len(changed) > 5 {
			more = fmt.Sprintf(" and %d more", len(changed)-5)
			changed = changed[:5]
		}
		report("FAIL", "PI-L6", "the start wrote under "+piHome+" outside its --session-dir ("+sessions+"): "+
			strings.Join(changed, ", ")+more, where)
	} else {
		report("PASS", "PI-L6", "no file under "+piHome+" changed; the session files are under the start's --session-dir "+sessions, where)
	}
}

// piInbox is PI-L7: the tier's other rows prove hooks and MCP; this one probes the session's inbox, the transport
// obor's lines reach pi by: while the session is idle, a line posted to its inbox by obor's own poster (inbox.Poster:
// the project's inbox record for the session's slot, the kind's deliver of it, its socket) is the session's user
// message, and its turn's reply is in the session record. TURN=1: the row's turn is a model call past the start's
// prompt.
func piInbox(l *lab, pane, sessions, sid string, turn bool) {
	where := at("pi/obor.js", "const line = (ctx)") + ", " + at("internal/inbox/inbox.go", "func (p Poster) Post") + ", pi-probes P6"
	if !turn {
		report("SKIP", "PI-L7", "needs TURN=1 (the inbox's turn is a model call past the start's prompt)", where)
		return
	}
	if sid == "" {
		report("FAIL", "PI-L7", "the session reported no id: no inbox record to read", where)
		return
	}
	if err := piSettled(l, pane, sessions, 3*time.Second, 120*time.Second); err != nil {
		report("FAIL", "PI-L7", "the session is not settled to take a line: "+err.Error(), where)
		return
	}
	cfg := l.config()
	if cfg == nil {
		report("FAIL", "PI-L7", "the scratch project's config does not load", where)
		return
	}
	s, ok := l.app.session(sid)
	if !ok {
		report("FAIL", "PI-L7", "the session fact names no agent: no slot to read the inbox record from", where)
		return
	}
	slot := project.AgentSlot(cfg, s.Agent)
	if slot == "" {
		report("FAIL", "PI-L7", "the session's agent "+s.Agent+" holds no slot of the project", where)
		return
	}
	if _, live := project.InboxLive(cfg, s.Agent); !live {
		report("FAIL", "PI-L7", "the extension recorded no live inbox for "+s.Agent+" (slot "+slot+")", where)
		return
	}
	f, err := project.InboxAt(cfg, slot)
	if err != nil {
		report("FAIL", "PI-L7", "no inbox record file for slot "+slot+": "+err.Error(), where)
		return
	}
	if began, err := (inbox.Poster{Root: cfg.Root, Kinds: cfg.Kinds()}).Post(context.Background(), "pi", s.Agent, piLiveInbox); !began || err != nil {
		report("FAIL", "PI-L7", fmt.Sprintf("obor's poster posted nothing to %s's inbox (%s): %v", s.Agent, f, err), where)
		return
	}
	reply := `"text":"` + piLiveInboxReply + `"` // the assistant message's text, which the posted line's text does not carry
	text, _ := waitTranscript(180*time.Second, func() string { return piSessions(sessions) }, reply)
	switch {
	case !hasLine(text, `"role":"user"`, piLiveInbox):
		report("FAIL", "PI-L7", "the posted line is no user message of the session; transcript end: "+tail(text, 3), where)
	case !hasLine(text, `"role":"assistant"`, reply):
		report("FAIL", "PI-L7", "the line started no turn: no assistant message "+fmt.Sprintf("%q", piLiveInboxReply)+"; transcript end: "+tail(text, 3), where)
	default:
		report("PASS", "PI-L7", "a line posted to "+s.Agent+"'s inbox by obor's poster (the project's record for slot "+slot+" at "+f+
			", the kind's deliver, its socket) while the session was idle is its user message, and its turn's reply is "+fmt.Sprintf("%q", piLiveInboxReply), where)
	}
}

// hasLine: one line of text carries every needle.
func hasLine(text string, needles ...string) bool {
	for _, line := range strings.Split(text, "\n") {
		ok := true
		for _, n := range needles {
			ok = ok && strings.Contains(line, n)
		}
		if ok {
			return true
		}
	}
	return false
}

// piSettled waits until the session in pane reads idle and its record has not changed for quiet: a line posted while
// pi is between prompt transitions can be settled without a turn (the extension's steer into a run that is ending), so
// the row waits for the record to stop changing and then probes acceptance, not that race.
func piSettled(l *lab, pane, sessions string, quiet, d time.Duration) error {
	deadline := time.Now().Add(d)
	last, since := piSessions(sessions), time.Now()
	var status string
	for time.Now().Before(deadline) {
		a, err := l.agent(pane)
		if err != nil {
			return err
		}
		status = a.Status
		if now := piSessions(sessions); now != last {
			last, since = now, time.Now()
		}
		if status == "idle" && time.Since(since) >= quiet {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("status %s, record changed %s ago after %.0fs", orNone(status), time.Since(since).Round(time.Second), d.Seconds())
}

// piSessions is every session file (.jsonl) under dir, concatenated: --session-dir puts a session's file directly
// there (pi-probes P5), and a directory pi makes for the cwd would still be read.
func piSessions(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if c, err := os.ReadFile(p); err == nil {
				b.Write(c)
				b.WriteString("\n")
			}
		}
		return nil
	})
	return b.String()
}

// piTree is every entry under dir as path → size and modification time (a dir: "dir"), for a before/after write check.
func piTree(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		if fi, err := d.Info(); err == nil {
			out[p] = fmt.Sprintf("%d@%d", fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return out
}

// piTreeChanges is the entries of after that before lacks or differs from, and those before has and after lost, sorted.
func piTreeChanges(before, after map[string]string) []string {
	var changed []string
	for p, v := range after {
		if before[p] != v {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			changed = append(changed, p+" (removed)")
		}
	}
	slices.Sort(changed)
	return changed
}
