package guard

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
)

// UserPromptSubmit has two jobs, by role. Lead: the lines queued for it when its kind has no inbox (hookLines, asked
// through lines), then the handoff reminder (handoffReminder) on every prompt while the session's context is above
// handoff_tokens, both in one context. Worker: the wrong-tab guard (design "Wrong-tab guard"): a prompt a
// person typed (not tagged `[obor `, nor a slash command, nor one of Claude Code's own envelopes or
// auto-continuations) gets context telling the agent to ask "I am worker <agent> on <task>. Did you mean to type
// here?" first. Anything else → no output.
func UserPromptSubmit(input []byte, env project.Env, out io.Writer, lines LeadLines) {
	var ev struct {
		Cwd            string `json:"cwd"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Prompt         string `json:"prompt"`
		Origin         any    `json:"origin"` // "auto-continuation" or {"kind": "auto-continuation"}
		TurnOrigin     any    `json:"turn_origin"`
		TurnOrigin2    any    `json:"turnOrigin"`
		PromptSource   any    `json:"prompt_source"`
		PromptSrc2     any    `json:"promptSource"`
	}
	if json.Unmarshal(input, &ev) != nil {
		return
	}
	if ev.Cwd == "" {
		ev.Cwd, _ = os.Getwd()
	}
	repo, cfg, _, err := loadAt(ev.Cwd) // a config that does not load: the default one (cfg), the guard still asks
	if repo == nil || errors.Is(err, config.ErrNoConfig) {
		return
	}
	role := project.DetectRole(repo, cfg, env)
	switch role.Role {
	case "lead":
		agent := project.SelfAgent(cfg, role)
		var parts []string
		if l := hookLines(cfg, agent, ev.Cwd, lines); l != "" {
			parts = append(parts, "obor (lead): lines for you that arrived since your last turn (your session has no "+
				"inbox: they come with your next prompt or at a turn's end), one per row:\n"+l)
		}
		if ctx := handoffReminder(cfg, env, agent, ev.SessionID, ev.TranscriptPath); ctx != "" {
			parts = append(parts, ctx)
		}
		if len(parts) > 0 {
			promptContext(out, strings.Join(parts, "\n\n"))
		}
		return
	case "worker":
	default:
		return
	}
	// Turns Claude Code starts by itself are no person typing: an origin field naming an auto-continuation.
	origin := ev.Origin
	if o, ok := origin.(map[string]any); ok {
		origin = o["kind"]
	}
	for _, o := range []any{origin, ev.TurnOrigin, ev.TurnOrigin2, ev.PromptSource, ev.PromptSrc2} {
		switch o {
		case "auto-continuation", "auto_continuation", "system":
			return
		}
	}
	if !personTyped(ev.Prompt) {
		return
	}
	st, _, err := store.ReadLaneAt(cfg.Root, role.Lane) // unreadable: the guard still asks, without the task
	// The worker's name is derived from its lane (no row stores it), the task is the row's.
	agent, task, lane := cfg.AgentName("worker", role.Lane), st.Task, role.Lane
	if agent == "" {
		agent = env.Get("OBOR_LANE")
	}
	if agent == "" {
		agent = cmp.Or(role.Lane, "(unknown)")
	}
	task, lane = cmp.Or(task, "(no task)"), cmp.Or(lane, "?")
	ctx := "obor wrong-tab guard: this session is obor worker " + agent + " on " + task + " (lane " + lane + "). " +
		"Prompts sent by obor start with [obor ; the prompt just submitted does not, so a person typed it directly into " +
		"this worker's pane, possibly in the wrong tab. Do not act on it yet and do not run any tool. Reply with exactly " +
		"this one line and then stop:\nI am worker " + agent + " on " + task + ". Did you mean to type here?\n" +
		"Only when the user confirms in their next message, carry out the request. If this prompt itself is the user's " +
		"answer to that question (you asked it in your previous reply), follow the answer instead of asking again."
	if err != nil {
		ctx += " (obor could not read this lane's row: " + err.Error() + ")"
	}
	promptContext(out, ctx)
}

// LeadLines asks the serve of the project at root for the lines queued for its lead agent, which serve records as
// delivered (cmd/obor: the lead_lines request): their text, one line per row ("": none). nil: nothing is asked.
type LeadLines func(root, agent string) (string, error)

// hookLines is the text of the lines the lead's hook hands over ("": none): asked through lines only when the kind
// agent runs as (sessionKind) is known, has no inbox (a kind with one gets its lines posted there) and its transport
// acts on the hooks' answers (agents.Caps.HookAnswers: else an answer would be lost, so nothing is asked and the
// lines wait); a failed ask hands nothing (the lines wait, obor status lists them).
func hookLines(cfg *config.Config, agent, cwd string, lines LeadLines) string {
	if lines == nil || agent == "" {
		return ""
	}
	if k, ok := cfg.Kind(sessionKind(cfg, agent, cwd)); !ok || k.Caps().Inbox || !k.Caps().HookAnswers {
		return ""
	}
	text, err := lines(cfg.Root, agent)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// promptContext writes ctx as the UserPromptSubmit hook's additionalContext.
func promptContext(out io.Writer, ctx string) {
	b := config.Compact(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit",
		"additionalContext": ctx}})
	fmt.Fprintln(out, b)
}

// handoffReminder (reminder): the lead's session context (sid, else the transcript file's name;
// the transcript at path, else project.TranscriptOf) above handoff_tokens → a reminder to hand off at the next logical
// break: the Handoff section of BACKLOG.md, then the handoff restart `obor restart <you> --handoff` of agent ("<you>"
// when unknown), which obor restart schedules from the agent's own pane until it is idle. Only the lead is reminded:
// a role agent restarts with a plain --fresh. Nothing forces the handoff: the agent picks the break. "" = no reminder.
func handoffReminder(cfg *config.Config, env project.Env, agent, sid, path string) string {
	limit := cfg.HandoffTokens()
	if sid == "" && path != "" {
		sid = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if limit <= 0 || sid == "" {
		return ""
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		path = project.TranscriptOf(cfg.Root, sid, env, cfg.Kinds())
	}
	c, ok := project.ContextOf(cfg.Kinds(), path)
	if !ok || int64(c.Tokens) <= limit {
		return ""
	}
	restart := "`obor restart " + cmp.Or(agent, "<you>") + " --handoff` (scheduled: it waits until you are idle)"
	when := "when the current task or discussion has reached its conclusion (nothing half-done, no question to the " +
		"person open), write the Handoff section at the top of BACKLOG.md (state, in-flight tasks, open decisions, next " +
		"steps), commit it, then run " + restart
	return fmt.Sprintf("obor (lead): context is %dk (> %dk). Hand off at the next logical break, not in the middle of work: %s. "+
		"If the person is talking with you, ask them first.", (c.Tokens+500)/1000, limit/1000, when)
}

var (
	pastedOpen  = regexp.MustCompile(`^<pasted_content id="[A-Za-z0-9_]+">[\t\n\v\f\r ]*`)
	pastedClose = regexp.MustCompile(`</pasted_content id="[A-Za-z0-9_]+">[\t\n\v\f\r ]*$`)
)

const autoContinue = "Your claude.ai usage limit has reset. Continue the task you were working on when the limit was reached; do not repeat work that is already complete."

// personTyped: the prompt, trimmed and unwrapped from one leading <pasted_content> envelope, is neither obor's
// (`[obor `), a slash command, the auto-continuation sentence, nor one of Claude Code's machine envelopes (exact tag at
// the start and its closing tag at the very end). Claude Code 2.1.312 hands the hook no origin field for a subagent's
// hand-back or another session's message, only the prompt: `<agent-message from=…>` resp.
// `<cross-session-message from=…>` (testdata/prompt-*.json). A raw post to the inbox socket (obor's own) comes
// bare: it passes by its `[obor ` tag.
func personTyped(prompt string) bool {
	const blanks = " \t\n\v\f\r"
	t := strings.TrimLeft(prompt, blanks)
	if m := pastedOpen.FindString(t); m != "" {
		t = t[len(m):]
		if loc := pastedClose.FindStringIndex(t); loc != nil {
			t = t[:loc[0]]
		}
	}
	t = strings.TrimRight(t, blanks)
	has, ends := strings.HasPrefix, strings.HasSuffix
	switch {
	case has(t, "[obor "), has(t, "/"), has(t, autoContinue):
		return false
	case has(t, "<task-notification>"):
		return !ends(t, "</task-notification>")
	case has(t, "<system-reminder>"):
		return !ends(t, "</system-reminder>")
	case has(t, "<command-name>"), has(t, "<command-message>"):
		return !ends(t, "</command-message>") && !ends(t, "</command-args>")
	case has(t, "<local-command-caveat>"):
		return !ends(t, "</local-command-caveat>")
	case has(t, "<local-command-stdout>"):
		return !ends(t, "</local-command-stdout>")
	case opens(t, "agent-message"): // a subagent's hand-back
		return !ends(t, "</agent-message>")
	case opens(t, "cross-session-message"): // another Claude session's SendMessage
		return !ends(t, "</cross-session-message>")
	}
	return true
}

// opens: t starts with the opening tag <name>, with or without attributes.
func opens(t, name string) bool {
	return strings.HasPrefix(t, "<"+name+">") || strings.HasPrefix(t, "<"+name+" ")
}
