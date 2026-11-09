// worktext.go: the work job's wake: its text (the worker prompt and the role texts) and the send of it to the worker
// (sendWork, workText, roleTexts, workerPrompt), and the watch on its delivery.
package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
)

// sendWork sends the worker its work job's wake (row 5) under step (work, work.N), job <task>/<attempt>/<step>: a job
// its start did not carry (startJob). A prompt that fails stays pending and is sent again under its key.
func (a *App) sendWork(ctx context.Context, p *proj, fx *Loop, mt mtask, step string) bool {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: step}
	at := a.opt.Now()
	if !a.sendWorkJob(ctx, p, fx, mt, key, key, fmt.Sprintf("work job %s not sent to %s", JobID(key), mt.agent)) {
		return false
	}
	a.jobSent(p, mt.task, JobID(key), at)
	return true
}

// sendWorkJob types mt's work job's (job) wake under key (the job's own, or its retry).
func (a *App) sendWorkJob(ctx context.Context, p *proj, fx *Loop, mt mtask, key, job event.Key, what string) bool {
	return a.send(ctx, p, fx, mt, key, JobID(job), promptData{Agent: mt.agent}, wakeText, what)
}

// workText is mt's worker prompt, from the project's config and the obor kit at kit as they are now: the prompt
// template (its prompt_extra the dispatch's model's kind's), then the worker's role texts (T-603: one read of them instead of two turns).
func workText(p *proj, mt mtask, kit string) (string, error) {
	cfg, err := config.Load(p.root)
	if err != nil {
		return "", err
	}
	typ, _ := config.ParseModelRef(mt.disp.Type) // T-721, T-738
	lt, _ := cfg.Model(typ)
	text, err := workerPrompt(cfg, kit, mt.task, lt)
	if err != nil {
		return "", err
	}
	return withRoleTexts(text, cfg, kit, "worker")
}

// withRoleTexts is text with the role's role texts after it (T-603, T-941): a too-large one named for a read, the
// others inlined under their path lines.
func withRoleTexts(text string, cfg *config.Config, kit, role string) (string, error) {
	roles, read, err := roleTexts(cfg, kit, role)
	if err != nil {
		return "", err
	}
	if len(read) > 0 {
		text += " Read your role text before you act: " + strings.Join(read, ", ") + " (too large to carry here)."
	}
	if roles != "" {
		text += "\n\n" + roles
	}
	return text, nil
}

// roleTextMax is the largest role text the work job inlines: a bigger one is named for the worker to read, which
// costs fewer tokens than carrying it in the job.
const roleTextMax = 12 << 10

// roleFile is one role text of the work job: the line naming it and its path.
type roleFile struct{ title, path string }

// roleTexts is a job's role texts as they are now (role: worker or integrator): the obor kit's
// templates/agents/<role>.md and the project's doc (<paths.agents>/<role>.md) when the main checkout has one. Each comes under a line naming its path; one
// larger than roleTextMax is not inlined, its path is returned in read instead. An empty "## This project" section
// (the template's fill-in note and nothing else) is not sent.
func roleTexts(cfg *config.Config, kit, role string) (text string, read []string, err error) {
	files := []roleFile{{"Your role text", filepath.Join(kit, "templates", "agents", role+".md")}}
	doc := filepath.Join(cfg.Root, cfg.Data.Paths["agents"], role+".md")
	if _, err := os.Stat(doc); err == nil {
		files = append(files, roleFile{"This project's " + role + " rules", doc})
	}
	var parts []string
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			return "", nil, fmt.Errorf("cannot read the role text %s", f.path)
		}
		body := dropEmptyProjectSection(string(b))
		if len(body) > roleTextMax {
			read = append(read, f.path)
			continue
		}
		parts = append(parts, f.title+" ("+f.path+"):\n"+body)
	}
	return strings.Join(parts, "\n\n"), read, nil
}

// dropEmptyProjectSection removes a trailing "## This project" section that holds only blank lines and HTML comments
// (the template's fill-in note for a new project): an empty section is not sent in the job.
func dropEmptyProjectSection(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "## This project" {
			continue
		}
		comment := false
		for _, rest := range lines[i+1:] {
			t := strings.TrimSpace(rest)
			switch {
			case comment:
				comment = !strings.Contains(t, "-->")
			case strings.HasPrefix(t, "<!--"):
				comment = !strings.Contains(t, "-->")
			case t != "":
				return text
			}
		}
		return strings.TrimRight(strings.Join(lines[:i], "\n"), " \t\n")
	}
	return text
}

// watchWork is the work job's delivery timer (rows 6–8, under mu): delivered once the worker's job_next fetched it
// (fact delivered: the task is working); undelivered for deliveryWait: retry.work, then the owner. The timer is not
// recorded: a replay starts it afresh.
func (a *App) watchWork(p *proj, mt mtask) step {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}
	t := a.timerOf(p, key)
	return a.undelivered(p, mt, t, key, mt.agent, func(ctx context.Context, fx *Loop, retry event.Key) bool {
		return a.sendWorkJob(ctx, p, fx, mt, retry, key, fmt.Sprintf("work job %s not sent again to %s", JobID(key),
			mt.agent))
	})
}

// workerPrompt is the worker prompt, the work job's text job_next hands out: templates/worker-prompt.txt rendered (its
// lines joined by spaces), the model's kind's prompt_extra (kinds.<kind>.prompt_extra); no backticks (herdr gotcha). It carries no sender tag of its
// own. The worker's role texts follow it (workText). {{base}} is the target branch (branches.main). Placeholders:
//
//	{{task}} {{base}}
//	{{done_sections}}  done_file_sections, comma-separated: the sections task_done's report must have
func workerPrompt(cfg *config.Config, kit, task string, lt config.Model) (string, error) {
	file := filepath.Join(kit, "templates", "worker-prompt.txt")
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("cannot read the worker prompt %s", file)
	}
	fill := strings.NewReplacer("{{task}}", task, "{{base}}", cfg.Data.Branches.Main,
		"{{done_sections}}", strings.Join(cfg.Data.DoneFileSections, ", "))
	var parts []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts = append(parts, fill.Replace(line))
	}
	if lt.PromptExtra != "" {
		parts = append(parts, lt.PromptExtra)
	}
	return strings.ReplaceAll(strings.Join(parts, " "), "`", "'"), nil
}

// pluginNote warns, in dispatch's reply, that a Claude lane's worktree has no enabled obor plugin (T-705): its worker
// would run without obor's hooks and MCP tools (no job_next).
func pluginNote(cfg *config.Config, l port.Lane, typ string) string {
	kind := project.RunningKind(cfg, config.AgentInfo{Type: typ})
	if k, known := agents.Lookup(kind); !known || !k.Caps().ProjectPlugin || config.PluginEnabledIn(l.Dir) {
		return ""
	}
	return fmt.Sprintf("\nWARN lane %s has no enabled obor plugin (%s/.claude/settings.json): obor setup lane or obor init links it", l.Name, l.Dir)
}
