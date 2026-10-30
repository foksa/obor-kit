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

// UserPromptSubmit has two jobs, by role. Lead or integrator: the handoff reminder (handoffReminder) on every prompt
// while the session's context is above its limit. Worker: the wrong-tab guard (design "Wrong-tab guard"): a prompt a
// person typed (not tagged `[obor `, nor a slash command, nor one of Claude Code's own envelopes or
// auto-continuations) gets context telling the agent to ask "I am worker <agent> on <task>. Did you mean to type
// here?" first. Anything else → no output.
func UserPromptSubmit(input []byte, env project.Env, out io.Writer) {
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
	case "lead", "integrator":
		if ctx := handoffReminder(cfg, env, role.Role, project.SelfAgent(cfg, role, env), ev.SessionID, ev.TranscriptPath); ctx != "" {
			promptContext(out, ctx)
		}
		return
	case "worker":
	default:
		return
	}
	// Turns Claude Code starts by itself are no person typing (T-147): an origin field naming an auto-continuation.
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
	agent, task, lane := st.Agent, st.Task, role.Lane
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

// promptContext writes ctx as the UserPromptSubmit hook's additionalContext.
func promptContext(out io.Writer, ctx string) {
	b := config.Compact(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit",
		"additionalContext": ctx}})
	fmt.Fprintln(out, b)
}

// handoffReminder (T-175, integrator T-215, reminder T-232, T-465): the lead's or integrator's session context (sid,
// else the transcript file's name; the transcript at path, else project.TranscriptOf) above its handoff_tokens → a
// reminder to hand off at the next logical break: the Handoff section of BACKLOG.md (the lead), then a fresh restart
// of agent ("<you>" when unknown), which obor restart schedules from the agent's own pane until it is idle. Nothing
// forces the handoff: the agent picks the break. "" = no reminder.
func handoffReminder(cfg *config.Config, env project.Env, role, agent, sid, path string) string {
	limit := handoffLimit(cfg, role)
	if sid == "" && path != "" {
		sid = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if limit <= 0 || sid == "" {
		return ""
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		path = project.TranscriptOf(cfg.Root, sid, env)
	}
	ctx, _, _, ok := project.ContextTokens(path)
	if !ok || int64(ctx) <= limit {
		return ""
	}
	who := role
	restart := "`obor restart " + cmp.Or(agent, "<you>") + " --fresh` (scheduled: it waits until you are idle)"
	when := "when the current task or discussion has reached its conclusion (nothing half-done, no question to the " +
		"person open), write the Handoff section at the top of BACKLOG.md (state, in-flight tasks, open decisions, next " +
		"steps), commit it, then run " + restart
	if role == "integrator" {
		when = "when the merge in hand is finished or reported (nothing half-done), run " + restart
	}
	return fmt.Sprintf("obor (%s): context is %dk (> %dk). Hand off at the next logical break, not in the middle of work: %s. "+
		"If the person is talking with you, ask them first.", who, (ctx+500)/1000, limit/1000, when)
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
// `<cross-session-message from=…>` (T-502, testdata/prompt-*.json). A raw post to the inbox socket (obor's own) comes
// bare: it passes by its `[obor ` tag (or a `[herd ` one recorded before the rename, until v0.17).
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
	case has(t, "[obor "), has(t, "[herd "), has(t, "/"), has(t, autoContinue): // [herd: until v0.17 (P7-0a fallback)
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
	case opens(t, "agent-message"): // a subagent's hand-back (T-502)
		return !ends(t, "</agent-message>")
	case opens(t, "cross-session-message"): // another Claude session's SendMessage (T-502)
		return !ends(t, "</cross-session-message>")
	}
	return true
}

// opens: t starts with the opening tag <name>, with or without attributes.
func opens(t, name string) bool {
	return strings.HasPrefix(t, "<"+name+">") || strings.HasPrefix(t, "<"+name+" ")
}
